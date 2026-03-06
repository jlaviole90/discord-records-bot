package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || m.Author.System || m.GuildID == "" {
		return
	}

	if isQuotesChannel(s, m.ChannelID) {
		return
	}

	if err := saveMessage(m.Message); err != nil {
		log.Printf("Error saving message %s: %v", m.ID, err)
	}

	if !isBotMentioned(m.Mentions) {
		return
	}

	lower := strings.ToLower(m.Content)

	if m.MessageReference != nil && m.MessageReference.MessageID != "" &&
		(strings.Contains(lower, "original") || strings.Contains(lower, "unedited")) {
		handleRepostOriginal(s, m)
		return
	}

	if strings.Contains(lower, "help") {
		handleHelp(s, m)
		return
	}

	if hours, ok := parseTLDR(m.Content); ok {
		handleTLDR(s, m, hours)
		return
	}

	if strings.Contains(lower, "leaderboard") || strings.Contains(lower, "cowards") || strings.Contains(lower, "stats") {
		handleLeaderboard(s, m)
		return
	}

	target := findMentionedTarget(m.Mentions)
	if target == nil {
		return
	}

	if strings.Contains(m.Content, "🗑️") || strings.Contains(m.Content, "🗑") ||
		strings.Contains(lower, "deleted") || strings.Contains(lower, "delete") {
		handleRepostDeleted(s, m, target)
	} else {
		handleRepostLatest(s, m, target)
	}
}

func isQuotesChannel(s *discordgo.Session, channelID string) bool {
	channel, err := s.State.Channel(channelID)
	if err != nil {
		channel, err = s.Channel(channelID)
	}
	return err == nil && strings.EqualFold(channel.Name, "quotes")
}

func isBotMentioned(mentions []*discordgo.User) bool {
	for _, u := range mentions {
		if u.ID == botID {
			return true
		}
	}
	return false
}

// findMentionedTarget returns the first non-bot user mentioned alongside the
// bot. Returns nil if no target user was found.
func findMentionedTarget(mentions []*discordgo.User) *discordgo.User {
	for _, u := range mentions {
		if u.ID != botID && !u.Bot {
			return u
		}
	}
	return nil
}

func onMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m.Author == nil || m.Author.Bot {
		return
	}
	if err := updateMessageContent(m.ID, m.Content); err != nil {
		log.Printf("Error updating message %s: %v", m.ID, err)
	}
}

func onMessageDelete(s *discordgo.Session, m *discordgo.MessageDelete) {
	if err := markMessageDeleted(m.ID); err != nil {
		log.Printf("Error marking message %s as deleted: %v", m.ID, err)
	}
}

func onMessageDeleteBulk(s *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	for _, id := range m.Messages {
		if err := markMessageDeleted(id); err != nil {
			log.Printf("Error marking message %s as deleted: %v", id, err)
		}
	}
}

func parseTLDR(content string) (int, bool) {
	lower := strings.ToLower(content)
	idx := strings.Index(lower, "tldr")
	if idx == -1 {
		return 0, false
	}

	rest := strings.TrimSpace(lower[idx+4:])
	if rest == "" {
		return 1, true
	}

	fields := strings.Fields(rest)
	hours, err := strconv.Atoi(fields[0])
	if err != nil || hours < 1 {
		return 1, true
	}
	if hours > 24 {
		hours = 24
	}
	return hours, true
}

func handleTLDR(s *discordgo.Session, m *discordgo.MessageCreate, hours int) {
	if geminiAPIKey == "" {
		s.ChannelMessageSend(m.ChannelID, "The TLDR feature is not configured (missing Gemini API key).")
		return
	}

	denial, err := checkTLDRRateLimit(m.Author.ID, m.ChannelID)
	if err != nil {
		log.Printf("Error checking TLDR rate limit: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong. Try again later.")
		return
	}
	if denial != "" {
		s.ChannelMessageSend(m.ChannelID, denial)
		return
	}

	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	messages, err := getRecentMessages(m.ChannelID, since)
	if err != nil || len(messages) == 0 {
		s.ChannelMessageSend(m.ChannelID,
			fmt.Sprintf("No messages found in the last %d hour(s).", hours))
		return
	}

	s.ChannelTyping(m.ChannelID)

	summary, err := summarizeMessages(geminiAPIKey, messages, hours)
	if err != nil {
		log.Printf("Error summarizing messages: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong generating the summary.")
		return
	}

	if err := recordTLDRUsage(m.Author.ID, m.Author.Username, m.ChannelID, m.GuildID, hours, len(messages)); err != nil {
		log.Printf("Error recording TLDR usage: %v", err)
	}

	if len(summary) > 2000 {
		summary = summary[:1997] + "..."
	}

	s.ChannelMessageSend(m.ChannelID, summary)
}

func handleRepostOriginal(s *discordgo.Session, m *discordgo.MessageCreate) {
	msg, contents, err := getMessageByID(m.MessageReference.MessageID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "I don't have a saved version of that message.")
		return
	}

	if msg.OriginalContent != "" {
		msg.Content = msg.OriginalContent
	}
	repostMessage(s, m.ChannelID, msg, contents)
}

func handleRepostLatest(s *discordgo.Session, m *discordgo.MessageCreate, target *discordgo.User) {
	msg, contents, err := getLatestMessage(m.ChannelID, target.ID, m.ID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID,
			fmt.Sprintf("No messages found for **%s** in this channel.", target.Username))
		return
	}
	repostMessage(s, m.ChannelID, msg, contents)
}

func handleRepostDeleted(s *discordgo.Session, m *discordgo.MessageCreate, target *discordgo.User) {
	msg, contents, err := getLatestDeletedMessage(m.ChannelID, target.ID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID,
			fmt.Sprintf("No deleted messages found for **%s** in this channel.", target.Username))
		return
	}
	repostMessage(s, m.ChannelID, msg, contents)
}

func repostMessage(s *discordgo.Session, channelID string, msg *StoredMessage, contents []StoredContent) {
	wh, err := s.WebhookCreate(channelID, msg.Username, msg.AvatarURL)
	if err != nil {
		log.Printf("Error creating webhook: %v", err)
		s.ChannelMessageSend(channelID, "Something went wrong while creating the webhook.")
		return
	}

	displayName := msg.DisplayName
	if displayName == "" {
		displayName = msg.Username
	}

	params := &discordgo.WebhookParams{
		Content:   msg.Content,
		Username:  fmt.Sprintf("%s | %s", msg.Username, displayName),
		AvatarURL: msg.AvatarURL,
	}

	for _, c := range contents {
		if c.ContentType == "attachment" && c.URL != "" {
			params.Embeds = append(params.Embeds, &discordgo.MessageEmbed{
				Title: c.Filename,
				Image: &discordgo.MessageEmbedImage{
					URL: c.URL,
				},
			})
		}
	}

	if params.Content == "" && len(params.Embeds) == 0 {
		s.ChannelMessageSend(channelID, "That message had no retrievable content.")
		cleanupWebhook(s, channelID, wh)
		return
	}

	_, err = s.WebhookExecute(wh.ID, wh.Token, false, params)
	if err != nil {
		log.Printf("Error executing webhook: %v", err)
		s.ChannelMessageSend(channelID, "Something went wrong while reposting the message.")
	}

	cleanupWebhook(s, channelID, wh)
}

func cleanupWebhook(s *discordgo.Session, channelID string, wh *discordgo.Webhook) {
	if err := s.WebhookDelete(wh.ID); err != nil {
		log.Printf("Error deleting webhook %s: %v", wh.Name, err)
		s.ChannelMessageSend(channelID,
			fmt.Sprintf("Could not delete webhook **%s**. You may want to delete it manually.", wh.Name))
	}
}

func handleHelp(s *discordgo.Session, m *discordgo.MessageCreate) {
	help := "📋 **Records Bot — Commands**\n\n" +
		"**Message Reposting**\n" +
		"💬 `@bot @user` — Repost their latest message in this channel\n" +
		"🗑️ `@bot @user deleted` or `@bot @user 🗑️` — Repost their latest deleted message\n" +
		"✏️ `Reply to a message + @bot original` — Repost the original pre-edit version\n\n" +
		"**Summaries**\n" +
		"📜 `@bot tldr [hours]` — AI summary of the last N hours (default: 1, max: 24)\n\n" +
		"**Leaderboards**\n" +
		"📊 `@bot leaderboard` — Most active, most deletes, most edits (with avg reaction times)\n\n" +
		"**Meta**\n" +
		"❓ `@bot help` — Show this message"
	s.ChannelMessageSend(m.ChannelID, help)
}

func handleLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate) {
	lb, err := getLeaderboards(m.GuildID)
	if err != nil {
		log.Printf("Error fetching leaderboards: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching the leaderboards.")
		return
	}

	msg := "📊 **Server Leaderboards**\n"

	msg += "\n💬 **Most Active**\n"
	if len(lb.Active) == 0 {
		msg += "*No messages recorded yet.*\n"
	}
	for i, e := range lb.Active {
		msg += fmt.Sprintf("%s **%s** — %d messages\n", formatRank(i+1), displayName(e), e.Count)
	}

	msg += "\n🗑️ **Most Regretful** (deletes)\n"
	if len(lb.Deletes) == 0 {
		msg += "*No deleted messages yet. Everyone's clean... for now.*\n"
	}
	for i, e := range lb.Deletes {
		msg += fmt.Sprintf("%s **%s** — %d deletes (avg. %s)\n", formatRank(i+1), displayName(e), e.Count, formatDuration(e.AvgSeconds))
	}

	msg += "\n✏️ **Second Thoughts** (edits)\n"
	if len(lb.Edits) == 0 {
		msg += "*No edited messages yet. Everyone says what they mean, apparently.*\n"
	}
	for i, e := range lb.Edits {
		msg += fmt.Sprintf("%s **%s** — %d edits (avg. %s)\n", formatRank(i+1), displayName(e), e.Count, formatDuration(e.AvgSeconds))
	}

	s.ChannelMessageSend(m.ChannelID, msg)
}

func displayName(e LeaderboardEntry) string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Username
}

func formatRank(n int) string {
	switch n {
	case 1:
		return "🥇"
	case 2:
		return "🥈"
	case 3:
		return "🥉"
	default:
		return fmt.Sprintf("`%d.`", n)
	}
}

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds * float64(time.Second))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
