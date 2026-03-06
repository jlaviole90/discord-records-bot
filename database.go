package main

import (
	"database/sql"
	_ "embed"
	"fmt"
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
		INSERT INTO messages (id, guild_id, channel_id, user_id, username, display_name, avatar_url, content, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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

type EditVersion struct {
	Content   string
	VersionAt time.Time
}

func updateMessageContent(messageID, content string) error {
	_, err := db.Exec(`
		INSERT INTO message_edits (message_id, content, version_at)
		SELECT $1, content, COALESCE(edited_at, sent_at)
		FROM messages
		WHERE id = $1 AND content != $2`,
		messageID, content,
	)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		UPDATE messages
		SET original_content = CASE WHEN original_content = '' THEN content ELSE original_content END,
		    content = $1,
		    edited_at = NOW()
		WHERE id = $2`,
		content, messageID,
	)
	return err
}

func getEditHistory(messageID string) ([]EditVersion, error) {
	rows, err := db.Query(`
		SELECT content, version_at
		FROM message_edits
		WHERE message_id = $1
		ORDER BY version_at ASC`,
		messageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []EditVersion
	for rows.Next() {
		var v EditVersion
		if err := rows.Scan(&v.Content, &v.VersionAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
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

func getDeletedMessages(channelID, userID string, limit int) ([]StoredMessage, error) {
	rows, err := db.Query(`
		SELECT id, guild_id, channel_id, user_id, username, display_name,
		       avatar_url, content, original_content, sent_at, edited_at, is_deleted, deleted_at
		FROM messages
		WHERE channel_id = $1 AND user_id = $2 AND is_deleted = TRUE
		ORDER BY deleted_at DESC
		LIMIT $3`,
		channelID, userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(
			&m.ID, &m.GuildID, &m.ChannelID, &m.UserID,
			&m.Username, &m.DisplayName, &m.AvatarURL,
			&m.Content, &m.OriginalContent, &m.SentAt, &m.EditedAt, &m.IsDeleted, &m.DeletedAt,
		); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func getRecentMessages(channelID string, since time.Time) ([]ChatLine, error) {
	rows, err := db.Query(`
		SELECT username, display_name, content, sent_at
		FROM messages
		WHERE channel_id = $1 AND sent_at >= $2
		ORDER BY sent_at ASC`,
		channelID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []ChatLine
	for rows.Next() {
		var l ChatLine
		if err := rows.Scan(&l.Username, &l.DisplayName, &l.Content, &l.SentAt); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

func checkTLDRRateLimit(userID, channelID string) (string, error) {
	oneHourAgo := time.Now().Add(-1 * time.Hour)

	var channelCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM tldr_usages
		WHERE user_id = $1 AND channel_id = $2 AND used_at >= $3`,
		userID, channelID, oneHourAgo,
	).Scan(&channelCount)
	if err != nil {
		return "", err
	}
	if channelCount >= tldrChannelLimitPerHr {
		return fmt.Sprintf("You can only use TLDR %d time(s) per hour in this channel.", tldrChannelLimitPerHr), nil
	}

	var globalCount int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM tldr_usages
		WHERE user_id = $1 AND used_at >= $2`,
		userID, oneHourAgo,
	).Scan(&globalCount)
	if err != nil {
		return "", err
	}
	if globalCount >= tldrGlobalLimitPerHr {
		return fmt.Sprintf("You've reached the maximum of %d TLDR uses per hour. Try again later.", tldrGlobalLimitPerHr), nil
	}

	return "", nil
}

func recordTLDRUsage(userID, username, channelID, guildID string, hoursRequested, messageCount int) error {
	_, err := db.Exec(`
		INSERT INTO tldr_usages (user_id, username, channel_id, guild_id, hours_requested, message_count)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, username, channelID, guildID, hoursRequested, messageCount,
	)
	return err
}

type LeaderboardEntry struct {
	Username    string
	DisplayName string
	Count       int
	AvgSeconds  float64
}

type Leaderboards struct {
	Active  []LeaderboardEntry
	Deletes []LeaderboardEntry
	Edits   []LeaderboardEntry
}

func getLeaderboards(guildID string, since *time.Time) (*Leaderboards, error) {
	lb := &Leaderboards{}
	var err error

	timeFilter := ""
	args := []any{guildID}
	if since != nil {
		timeFilter = " AND sent_at >= $2"
		args = append(args, *since)
	}

	lb.Active, err = queryLeaderboard(
		`SELECT MAX(username), MAX(display_name), COUNT(*) as total, NULL
		FROM messages WHERE guild_id = $1`+timeFilter+`
		GROUP BY user_id ORDER BY total DESC LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}

	lb.Deletes, err = queryLeaderboard(
		`SELECT MAX(username), MAX(display_name), COUNT(*) as total,
		        AVG(EXTRACT(EPOCH FROM (deleted_at - sent_at)))
		FROM messages WHERE guild_id = $1 AND is_deleted = TRUE AND deleted_at IS NOT NULL`+timeFilter+`
		GROUP BY user_id ORDER BY total DESC LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}

	lb.Edits, err = queryLeaderboard(
		`SELECT MAX(username), MAX(display_name), COUNT(*) as total,
		        AVG(EXTRACT(EPOCH FROM (edited_at - sent_at)))
		FROM messages WHERE guild_id = $1 AND edited_at IS NOT NULL`+timeFilter+`
		GROUP BY user_id ORDER BY total DESC LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}

	return lb, nil
}

func queryLeaderboard(query string, args ...any) ([]LeaderboardEntry, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LeaderboardEntry
	for rows.Next() {
		var e LeaderboardEntry
		var avgSecs sql.NullFloat64
		if err := rows.Scan(&e.Username, &e.DisplayName, &e.Count, &avgSecs); err != nil {
			return nil, err
		}
		if avgSecs.Valid {
			e.AvgSeconds = avgSecs.Float64
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
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
