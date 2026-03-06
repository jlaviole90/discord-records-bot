package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const timestampFmt = "Jan 2, 3:04 PM"
const noMessagesYet = "*No messages recorded yet.*\n"

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

	if isBotMentioned(m.Mentions) {
		dispatchCommand(s, m)
	}
}

func dispatchCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
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

	if strings.Contains(lower, "backfill") {
		handleBackfill(s, m)
		return
	}

	if strings.Contains(lower, "leaderboard") || strings.Contains(lower, "cowards") || strings.Contains(lower, "stats") {
		dispatchLeaderboard(s, m, lower)
		return
	}

	target := findMentionedTarget(m.Mentions)
	if target == nil {
		return
	}

	if strings.Contains(m.Content, "🗑️") || strings.Contains(m.Content, "🗑") ||
		strings.Contains(lower, "deleted") || strings.Contains(lower, "delete") {
		count := parseDeleteCount(m.Content)
		handleRepostDeleted(s, m, target, count)
	} else {
		handleRepostLatest(s, m, target)
	}
}

func dispatchLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate, lower string) {
	since, label := parseLeaderboardTime(lower)

	if strings.Contains(lower, "channels") {
		handleTopChannels(s, m, since, label)
		return
	}

	if chID := parseChannelMention(m.Content); chID != "" {
		handleChannelLeaderboard(s, m, chID, since, label)
		return
	}

	if target := findMentionedTarget(m.Mentions); target != nil {
		handleUserLeaderboard(s, m, target, since, label)
		return
	}

	handleLeaderboard(s, m, since, label)
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

	edits, _ := getEditHistory(msg.ID)
	if len(edits) == 0 {
		repostMessage(s, m.ChannelID, msg, contents)
		return
	}

	dn := msg.DisplayName
	if dn == "" {
		dn = msg.Username
	}

	reply := fmt.Sprintf("✏️ **Edit History for %s**\n\n", dn)
	for i, v := range edits {
		if i == 0 {
			reply += fmt.Sprintf("**Original** (%s):\n%s\n\n",
				v.VersionAt.Format(timestampFmt), blockquote(v.Content))
		} else {
			reply += fmt.Sprintf("**Edit %d** (%s):\n%s\n\n",
				i, v.VersionAt.Format(timestampFmt), blockquote(v.Content))
		}
	}
	reply += fmt.Sprintf("**Current** (%s):\n%s",
		msg.EditedAt.Format(timestampFmt), blockquote(msg.Content))

	if len(reply) > 2000 {
		reply = reply[:1997] + "..."
	}

	s.ChannelMessageSend(m.ChannelID, reply)
}

func blockquote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
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

func parseDeleteCount(content string) int {
	for _, field := range strings.Fields(content) {
		if n, err := strconv.Atoi(field); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func handleRepostDeleted(s *discordgo.Session, m *discordgo.MessageCreate, target *discordgo.User, count int) {
	msgs, err := getDeletedMessages(m.ChannelID, target.ID, count)
	if err != nil || len(msgs) == 0 {
		s.ChannelMessageSend(m.ChannelID,
			fmt.Sprintf("No deleted messages found for **%s** in this channel.", target.Username))
		return
	}

	// Reverse to chronological order (query returns newest first)
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	first := msgs[0]
	wh, err := s.WebhookCreate(m.ChannelID, first.Username, first.AvatarURL)
	if err != nil {
		log.Printf("Error creating webhook: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong while creating the webhook.")
		return
	}

	for _, msg := range msgs {
		contents, err := getMessageContents(msg.ID)
		if err != nil {
			log.Printf("Error fetching contents for message %s: %v", msg.ID, err)
			continue
		}

		params := buildWebhookParams(&msg, contents)
		if params == nil {
			continue
		}

		if _, err := s.WebhookExecute(wh.ID, wh.Token, false, params); err != nil {
			log.Printf("Error executing webhook for message %s: %v", msg.ID, err)
		}
	}

	cleanupWebhook(s, m.ChannelID, wh)
}

func buildWebhookParams(msg *StoredMessage, contents []StoredContent) *discordgo.WebhookParams {
	dn := msg.DisplayName
	if dn == "" {
		dn = msg.Username
	}

	params := &discordgo.WebhookParams{
		Content:   msg.Content,
		Username:  fmt.Sprintf("%s | %s", msg.Username, dn),
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
		return nil
	}
	return params
}

func repostMessage(s *discordgo.Session, channelID string, msg *StoredMessage, contents []StoredContent) {
	wh, err := s.WebhookCreate(channelID, msg.Username, msg.AvatarURL)
	if err != nil {
		log.Printf("Error creating webhook: %v", err)
		s.ChannelMessageSend(channelID, "Something went wrong while creating the webhook.")
		return
	}

	params := buildWebhookParams(msg, contents)
	if params == nil {
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
		"🗑️ `@bot @user deleted [count]` or `@bot @user 🗑️ [count]` — Repost their last N deleted messages (default: 1)\n" +
		"✏️ `Reply to a message + @bot original` — Repost the original pre-edit version\n\n" +
		"**Summaries**\n" +
		"📜 `@bot tldr [hours]` — AI summary of the last N hours (default: 1, max: 24)\n\n" +
		"**Leaderboards**\n" +
		"📊 `@bot leaderboard [time]` — Server-wide leaderboards\n" +
		"📊 `@bot leaderboard #channel [time]` — Leaderboards for a specific channel\n" +
		"📊 `@bot leaderboard @user [time]` — A user's most active channels\n" +
		"📊 `@bot channels leaderboard [time]` — Top channels by message count\n" +
		"  Time options: `all` (default), `3 hours`, `7 days`, `2 months`\n" +
		"  Shortcuts: `h`, `d`, `m` — e.g. `@bot leaderboard 24 h`\n\n" +
		"**Meta**\n" +
		"❓ `@bot help` — Show this message"
	s.ChannelMessageSend(m.ChannelID, help)
}

// parseLeaderboardTime extracts an optional time window from the message.
// Returns nil for "all time" and a human-readable label for the header.
func parseLeaderboardTime(lower string) (*time.Time, string) {
	if strings.Contains(lower, "all") {
		return nil, "All Time"
	}

	// Find a number followed by a unit keyword
	fields := strings.Fields(lower)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 {
			continue
		}

		unit := ""
		if i+1 < len(fields) {
			unit = fields[i+1]
		}

		switch {
		case strings.HasPrefix(unit, "h"):
			t := time.Now().Add(-time.Duration(n) * time.Hour)
			return &t, fmt.Sprintf("Last %d hour(s)", n)
		case strings.HasPrefix(unit, "d"):
			t := time.Now().Add(-time.Duration(n) * 24 * time.Hour)
			return &t, fmt.Sprintf("Last %d day(s)", n)
		case strings.HasPrefix(unit, "m"):
			t := time.Now().AddDate(0, -n, 0)
			return &t, fmt.Sprintf("Last %d month(s)", n)
		}
	}

	return nil, "All Time"
}

func parseChannelMention(content string) string {
	idx := strings.Index(content, "<#")
	if idx == -1 {
		return ""
	}
	end := strings.Index(content[idx:], ">")
	if end == -1 {
		return ""
	}
	id := content[idx+2 : idx+end]
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return id
}

func handleLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate, since *time.Time, label string) {
	total, _ := getMessageCount(m.GuildID, "", since)
	lb, err := getLeaderboards(m.GuildID, "", since)
	if err != nil {
		log.Printf("Error fetching leaderboards: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching the leaderboards.")
		return
	}

	msg := fmt.Sprintf("📊 **Server Leaderboards — %s** (%d messages)\n", label, total)
	msg += formatLeaderboardBody(lb)
	s.ChannelMessageSend(m.ChannelID, msg)
}

func handleTopChannels(s *discordgo.Session, m *discordgo.MessageCreate, since *time.Time, label string) {
	total, _ := getMessageCount(m.GuildID, "", since)
	channels, err := getTopChannels(m.GuildID, since)
	if err != nil {
		log.Printf("Error fetching top channels: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching channel stats.")
		return
	}

	msg := fmt.Sprintf("📊 **Top Channels — %s** (%d messages)\n\n", label, total)

	if len(channels) == 0 {
		msg += noMessagesYet
	} else {
		for i, ch := range channels {
			msg += fmt.Sprintf("%s <#%s> — %d messages\n", formatRank(i+1), ch.ChannelID, ch.Count)
		}
	}

	s.ChannelMessageSend(m.ChannelID, msg)
}

func handleChannelLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate, channelID string, since *time.Time, label string) {
	total, _ := getMessageCount(m.GuildID, channelID, since)
	lb, err := getLeaderboards(m.GuildID, channelID, since)
	if err != nil {
		log.Printf("Error fetching channel leaderboards: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching the leaderboards.")
		return
	}

	msg := fmt.Sprintf("📊 **<#%s> Leaderboards — %s** (%d messages)\n", channelID, label, total)
	msg += formatLeaderboardBody(lb)
	s.ChannelMessageSend(m.ChannelID, msg)
}

func handleUserLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate, target *discordgo.User, since *time.Time, label string) {
	total, deletes, edits, err := getUserStats(m.GuildID, target.ID, since)
	if err != nil {
		log.Printf("Error fetching user stats: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching user stats.")
		return
	}

	dn := target.GlobalName
	if dn == "" {
		dn = target.Username
	}

	msg := fmt.Sprintf("📊 **%s — %s** (%d messages, %d deletes, %d edits)\n\n", dn, label, total, deletes, edits)

	channels, err := getUserChannelActivity(m.GuildID, target.ID, since)
	if err != nil {
		log.Printf("Error fetching user channel activity: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Something went wrong fetching channel activity.")
		return
	}

	if len(channels) == 0 {
		msg += noMessagesYet
	} else {
		msg += "📍 **Most Active Channels**\n"
		for i, ch := range channels {
			msg += fmt.Sprintf("%s <#%s> — %d messages\n", formatRank(i+1), ch.ChannelID, ch.Count)
		}
	}

	s.ChannelMessageSend(m.ChannelID, msg)
}

func formatLeaderboardBody(lb *Leaderboards) string {
	var msg string

	msg += "\n💬 **Most Active**\n"
	if len(lb.Active) == 0 {
		msg += noMessagesYet
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

	return msg
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
