package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var activeBackfills sync.Map

func handleBackfill(s *discordgo.Session, m *discordgo.MessageCreate) {
	if _, loaded := activeBackfills.LoadOrStore(m.GuildID, true); loaded {
		s.ChannelMessageSend(m.ChannelID, "⏳ A backfill is already running for this server.")
		return
	}

	s.ChannelMessageSend(m.ChannelID,
		"📥 **Starting message backfill...** This may take a while depending on server size.\n"+
			"Progress updates will be posted here. You can continue using the bot normally.")

	go runBackfill(s, m.GuildID, m.ChannelID)
}

func runBackfill(s *discordgo.Session, guildID, reportChannelID string) {
	defer activeBackfills.Delete(guildID)

	channels, err := s.GuildChannels(guildID)
	if err != nil {
		log.Printf("Backfill: failed to get channels for guild %s: %v", guildID, err)
		s.ChannelMessageSend(reportChannelID, "❌ Failed to fetch server channels.")
		return
	}

	textChannels := filterBackfillChannels(s, channels)

	var totalMessages, totalChannels int
	skipped := len(channels) - len(textChannels)
	start := time.Now()

	for _, ch := range textChannels {
		count, chErr := backfillChannel(s, guildID, ch.ID, reportChannelID, ch.Name)
		if chErr != nil {
			log.Printf("Backfill: error on channel #%s (%s): %v", ch.Name, ch.ID, chErr)
			s.ChannelMessageSend(reportChannelID,
				fmt.Sprintf("⚠️ **#%s** — stopped early due to an error (%d messages saved)", ch.Name, count))
		}
		if count > 0 {
			totalMessages += count
			totalChannels++
		}
	}

	elapsed := time.Since(start)
	summary := fmt.Sprintf("✅ **Backfill complete!**\n"+
		"📊 **%d** messages across **%d** channels in %s\n"+
		"Duplicates were automatically skipped.",
		totalMessages, totalChannels, formatDuration(elapsed.Seconds()))
	if skipped > 0 {
		summary += fmt.Sprintf("\n⚠️ %d channel(s) skipped (non-text, quotes, or missing permissions).", skipped)
	}
	s.ChannelMessageSend(reportChannelID, summary)
}

func filterBackfillChannels(s *discordgo.Session, channels []*discordgo.Channel) []*discordgo.Channel {
	var result []*discordgo.Channel
	for _, ch := range channels {
		if ch.Type != discordgo.ChannelTypeGuildText && ch.Type != discordgo.ChannelTypeGuildNews {
			continue
		}
		if strings.EqualFold(ch.Name, "quotes") {
			continue
		}
		botPerms, err := s.UserChannelPermissions(botID, ch.ID)
		if err != nil ||
			botPerms&discordgo.PermissionViewChannel == 0 ||
			botPerms&discordgo.PermissionReadMessageHistory == 0 {
			continue
		}
		result = append(result, ch)
	}
	return result
}

const backfillProgressInterval = 5000

func backfillChannel(s *discordgo.Session, guildID, channelID, reportChannelID, channelName string) (int, error) {
	var count int
	beforeID := ""
	lastReport := 0

	for {
		msgs, err := s.ChannelMessages(channelID, 100, beforeID, "", "")
		if err != nil {
			return count, err
		}
		if len(msgs) == 0 {
			break
		}

		count += saveBatch(msgs, guildID)

		if count-lastReport >= backfillProgressInterval {
			s.ChannelMessageSend(reportChannelID,
				fmt.Sprintf("📥 **#%s** — %d messages processed so far...", channelName, count))
			lastReport = count
		}

		beforeID = msgs[len(msgs)-1].ID
		time.Sleep(250 * time.Millisecond)
	}

	if count > 0 {
		s.ChannelMessageSend(reportChannelID,
			fmt.Sprintf("📥 **#%s** — done (%d messages)", channelName, count))
	}

	return count, nil
}

func saveBatch(msgs []*discordgo.Message, guildID string) int {
	saved := 0
	for _, msg := range msgs {
		if msg.Author == nil || msg.Author.Bot || msg.Author.System {
			continue
		}
		if msg.GuildID == "" {
			msg.GuildID = guildID
		}
		if err := saveMessage(msg); err != nil {
			log.Printf("Backfill: error saving message %s: %v", msg.ID, err)
			continue
		}
		saved++
	}
	return saved
}
