package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func onMessageReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.MessageReaction.Emoji.Name != "📸" &&
		r.MessageReaction.Emoji.Name != ":camera_with_flash:" {
		return
	}

	handleQuote(s, r)
}

func handleQuote(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	msg, err := s.ChannelMessage(r.ChannelID, r.MessageID)
	if err != nil {
		log.Printf("Error fetching message for quote %s: %v", r.MessageID, err)
		return
	}

	if msg.Author.Bot && msg.Author.ID != s.State.User.ID {
		s.ChannelMessageSend(r.ChannelID, "Sorry, I don't quote application messages!")
		return
	}

	chns, err := s.GuildChannels(r.GuildID)
	if err != nil {
		log.Printf("Error fetching guild channels for quote: %v", err)
		return
	}

	qchn, err := getQuotesChannel(chns)
	if err != nil {
		s.ChannelMessageSend(r.ChannelID, "Sorry, I couldn't find the quotes channel!")
		return
	}

	if err := s.State.ChannelAdd(qchn); err != nil {
		log.Printf("Warning: could not add quotes channel to state: %v", err)
	}

	for _, m := range qchn.Messages {
		if m.Content == msg.Content {
			return
		}
	}

	dn := msg.Author.GlobalName
	if dn == "" {
		dn = msg.Author.Username
	}

	wh, err := s.WebhookCreate(qchn.ID, msg.Author.Username, msg.Author.AvatarURL(""))
	if err != nil {
		log.Printf("Error creating webhook for quote: %v", err)
		s.ChannelMessageSend(r.ChannelID, "Something went wrong while creating the webhook.")
		return
	}

	params := &discordgo.WebhookParams{
		Content:   msg.Content,
		Username:  fmt.Sprintf("%s | %s", msg.Author.Username, dn),
		AvatarURL: msg.Author.AvatarURL(""),
	}
	if len(msg.Attachments) > 0 {
		params.Embeds = []*discordgo.MessageEmbed{
			{
				Title: msg.Attachments[0].Filename,
				Image: &discordgo.MessageEmbedImage{
					URL: msg.Attachments[0].URL,
				},
			},
		}
	}

	if _, err = s.WebhookExecute(wh.ID, wh.Token, false, params); err != nil {
		log.Printf("Error executing webhook for quote: %v", err)
		s.ChannelMessageSend(r.ChannelID, "Something went wrong while quoting that message.")
	}

	cleanupWebhook(s, r.ChannelID, wh)
}

func getQuotesChannel(chns []*discordgo.Channel) (*discordgo.Channel, error) {
	for _, chn := range chns {
		if strings.EqualFold(chn.Name, "quotes") {
			return chn, nil
		}
	}
	return nil, fmt.Errorf("no quotes channel present")
}
