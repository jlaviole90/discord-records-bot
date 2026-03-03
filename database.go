package main

import (
	"database/sql"
	_ "embed"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schemaSQL string

type StoredMessage struct {
	ID              string
	GuildID         string
	ChannelID       string
	UserID          string
	Username        string
	DisplayName     string
	AvatarURL       string
	Content         string
	OriginalContent string
	SentAt          time.Time
	EditedAt        *time.Time
	IsDeleted       bool
	DeletedAt       *time.Time
}

type StoredContent struct {
	ID          int
	MessageID   string
	ContentType string
	Content     string
	Filename    string
	URL         string
}

func initDB(databaseURL string) (*sql.DB, error) {
	conn, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}

	if err := conn.Ping(); err != nil {
		return nil, err
	}

	if _, err := conn.Exec(schemaSQL); err != nil {
		return nil, err
	}

	return conn, nil
}

func saveMessage(m *discordgo.Message) error {
	sentAt := m.Timestamp
	if sentAt.IsZero() {
		sentAt = time.Now()
	}

	displayName := ""
	avatarURL := ""
	if m.Author != nil {
		displayName = m.Author.GlobalName
		avatarURL = m.Author.AvatarURL("")
	}

	_, err := db.Exec(`
		INSERT INTO messages (id, guild_id, channel_id, user_id, username, display_name, avatar_url, content, original_content, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9)
		ON CONFLICT (id) DO NOTHING`,
		m.ID, m.GuildID, m.ChannelID, m.Author.ID,
		m.Author.Username, displayName, avatarURL,
		m.Content, sentAt,
	)
	if err != nil {
		return err
	}

	for _, a := range m.Attachments {
		_, err := db.Exec(`
			INSERT INTO message_contents (message_id, content_type, content, filename, url)
			VALUES ($1, $2, $3, $4, $5)`,
			m.ID, "attachment", "", a.Filename, a.URL,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func updateMessageContent(messageID, content string) error {
	_, err := db.Exec(
		`UPDATE messages SET content = $1, edited_at = NOW() WHERE id = $2`,
		content, messageID,
	)
	return err
}

func markMessageDeleted(messageID string) error {
	_, err := db.Exec(
		`UPDATE messages SET is_deleted = TRUE, deleted_at = NOW() WHERE id = $1`,
		messageID,
	)
	return err
}

func getLatestMessage(channelID, userID, excludeMessageID string) (*StoredMessage, []StoredContent, error) {
	msg := &StoredMessage{}
	err := db.QueryRow(`
		SELECT id, guild_id, channel_id, user_id, username, display_name,
		       avatar_url, content, original_content, sent_at, edited_at, is_deleted, deleted_at
		FROM messages
		WHERE channel_id = $1 AND user_id = $2 AND id != $3
		ORDER BY sent_at DESC
		LIMIT 1`,
		channelID, userID, excludeMessageID,
	).Scan(
		&msg.ID, &msg.GuildID, &msg.ChannelID, &msg.UserID,
		&msg.Username, &msg.DisplayName, &msg.AvatarURL,
		&msg.Content, &msg.OriginalContent, &msg.SentAt, &msg.EditedAt, &msg.IsDeleted, &msg.DeletedAt,
	)
	if err != nil {
		return nil, nil, err
	}

	contents, err := getMessageContents(msg.ID)
	return msg, contents, err
}

func getLatestDeletedMessage(channelID, userID string) (*StoredMessage, []StoredContent, error) {
	msg := &StoredMessage{}
	err := db.QueryRow(`
		SELECT id, guild_id, channel_id, user_id, username, display_name,
		       avatar_url, content, original_content, sent_at, edited_at, is_deleted, deleted_at
		FROM messages
		WHERE channel_id = $1 AND user_id = $2 AND is_deleted = TRUE
		ORDER BY deleted_at DESC
		LIMIT 1`,
		channelID, userID,
	).Scan(
		&msg.ID, &msg.GuildID, &msg.ChannelID, &msg.UserID,
		&msg.Username, &msg.DisplayName, &msg.AvatarURL,
		&msg.Content, &msg.OriginalContent, &msg.SentAt, &msg.EditedAt, &msg.IsDeleted, &msg.DeletedAt,
	)
	if err != nil {
		return nil, nil, err
	}

	contents, err := getMessageContents(msg.ID)
	return msg, contents, err
}

func getMessageByID(messageID string) (*StoredMessage, []StoredContent, error) {
	msg := &StoredMessage{}
	err := db.QueryRow(`
		SELECT id, guild_id, channel_id, user_id, username, display_name,
		       avatar_url, content, original_content, sent_at, edited_at, is_deleted, deleted_at
		FROM messages
		WHERE id = $1`,
		messageID,
	).Scan(
		&msg.ID, &msg.GuildID, &msg.ChannelID, &msg.UserID,
		&msg.Username, &msg.DisplayName, &msg.AvatarURL,
		&msg.Content, &msg.OriginalContent, &msg.SentAt, &msg.EditedAt, &msg.IsDeleted, &msg.DeletedAt,
	)
	if err != nil {
		return nil, nil, err
	}

	contents, err := getMessageContents(msg.ID)
	return msg, contents, err
}

func getMessageContents(messageID string) ([]StoredContent, error) {
	rows, err := db.Query(`
		SELECT id, message_id, content_type, content,
		       COALESCE(filename, ''), COALESCE(url, '')
		FROM message_contents
		WHERE message_id = $1`,
		messageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contents []StoredContent
	for rows.Next() {
		var c StoredContent
		if err := rows.Scan(&c.ID, &c.MessageID, &c.ContentType, &c.Content, &c.Filename, &c.URL); err != nil {
			return nil, err
		}
		contents = append(contents, c)
	}
	return contents, rows.Err()
}
