package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || m.GuildID == "" {
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

	if m.MessageReference != nil && m.MessageReference.MessageID != "" {
		handleRepostOriginal(s, m)
		return
	}

	target := findMentionedTarget(m.Mentions)
	if target == nil {
		return
	}

	if strings.Contains(m.Content, "🗑️") || strings.Contains(m.Content, "🗑") {
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
