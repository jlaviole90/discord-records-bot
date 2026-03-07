package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	customEmojiRe  = regexp.MustCompile(`<a?:(\w+):\d+>`)
	userMentionRe  = regexp.MustCompile(`<@!?(\d+)>`)
	channelMentionRe = regexp.MustCompile(`<#\d+>`)
	roleMentionRe  = regexp.MustCompile(`<@&\d+>`)
	urlRe          = regexp.MustCompile(`https?://\S+`)
)

type ShareGPTMessage struct {
	From  string `json:"from"`
	Value string `json:"value"`
}

type ShareGPTConversation struct {
	Conversations []ShareGPTMessage `json:"conversations"`
}

type exportMsg struct {
	UserID      string
	Username    string
	DisplayName string
	Content     string
	SentAt      time.Time
	ChannelID   string
}

const defaultSystemPromptTpl = "You are %s. You speak exactly as %s does in Discord — " +
	"same vocabulary, slang, humor, opinions, and personality. You ARE %s. " +
	"Respond naturally and concisely as they would in a casual Discord chat."

func runExport() {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	userID := fs.String("user-id", "", "Discord user ID to export training data for (required)")
	guildID := fs.String("guild-id", "", "Discord guild ID (required)")
	output := fs.String("output", "training_data.jsonl", "Output JSONL file path")
	sinceStr := fs.String("since", "", "Only export messages after this timestamp (RFC3339)")
	windowMin := fs.Int("window", 5, "Conversation window gap in minutes")
	minTurns := fs.Int("min-turns", 2, "Minimum conversation turns to include")
	fs.Parse(os.Args[2:])

	if *userID == "" || *guildID == "" {
		fmt.Fprintln(os.Stderr, "Usage: discord-records-bot export --user-id=ID --guild-id=ID [--output=file.jsonl] [--since=RFC3339] [--window=5] [--min-turns=2]")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	conn, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer conn.Close()

	if err := conn.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	var since time.Time
	if *sinceStr != "" {
		since, err = time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			log.Fatalf("Invalid --since timestamp: %v", err)
		}
	}

	displayName := resolveDisplayName(conn, *userID)
	systemPrompt := fmt.Sprintf(defaultSystemPromptTpl, displayName, displayName, displayName)

	userNames, err := buildUserNameMap(conn, *guildID)
	if err != nil {
		log.Fatalf("Failed to build user name map: %v", err)
	}

	messages, err := queryExportMessages(conn, *guildID, since)
	if err != nil {
		log.Fatalf("Failed to query messages: %v", err)
	}
	log.Printf("Loaded %d messages from database", len(messages))

	for i := range messages {
		messages[i].Content = sanitizeContent(messages[i].Content, userNames)
	}

	conversations := buildConversations(messages, *userID, systemPrompt, *windowMin, *minTurns)
	log.Printf("Built %d conversations containing target user", len(conversations))

	if err := writeJSONL(*output, conversations); err != nil {
		log.Fatalf("Failed to write output: %v", err)
	}

	log.Printf("Wrote %d conversations to %s", len(conversations), *output)
}

func buildUserNameMap(conn *sql.DB, guildID string) (map[string]string, error) {
	rows, err := conn.Query(
		`SELECT DISTINCT ON (user_id) user_id, COALESCE(NULLIF(display_name, ''), username)
		 FROM messages WHERE guild_id = $1 ORDER BY user_id, sent_at DESC`,
		guildID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var uid, name string
		if err := rows.Scan(&uid, &name); err != nil {
			return nil, err
		}
		m[uid] = name
	}
	return m, rows.Err()
}

func sanitizeContent(content string, userNames map[string]string) string {
	content = customEmojiRe.ReplaceAllString(content, ":$1:")

	content = userMentionRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := userMentionRe.FindStringSubmatch(match)
		if len(sub) > 1 {
			if name, ok := userNames[sub[1]]; ok {
				return "@" + name
			}
		}
		return "@someone"
	})

	content = channelMentionRe.ReplaceAllString(content, "#channel")
	content = roleMentionRe.ReplaceAllString(content, "@role")
	content = urlRe.ReplaceAllString(content, "")

	return strings.TrimSpace(content)
}

func resolveDisplayName(conn *sql.DB, userID string) string {
	var dn, un string
	err := conn.QueryRow(
		`SELECT COALESCE(MAX(display_name), ''), MAX(username) FROM messages WHERE user_id = $1`,
		userID,
	).Scan(&dn, &un)
	if err != nil || (dn == "" && un == "") {
		return "User"
	}
	if dn != "" {
		return dn
	}
	return un
}

func queryExportMessages(conn *sql.DB, guildID string, since time.Time) ([]exportMsg, error) {
	query := `SELECT user_id, username, display_name, content, sent_at, channel_id
		FROM messages
		WHERE guild_id = $1 AND content != ''`
	args := []any{guildID}

	if !since.IsZero() {
		query += " AND sent_at >= $2"
		args = append(args, since)
	}

	query += " ORDER BY channel_id, sent_at ASC"

	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []exportMsg
	for rows.Next() {
		var m exportMsg
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Content, &m.SentAt, &m.ChannelID); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func buildConversations(messages []exportMsg, targetUserID, systemPrompt string, windowMin, minTurns int) []ShareGPTConversation {
	var result []ShareGPTConversation
	windows := splitIntoWindows(messages, time.Duration(windowMin)*time.Minute)

	for _, window := range windows {
		if conv, ok := formatConversation(window, targetUserID, systemPrompt, minTurns); ok {
			result = append(result, conv)
		}
	}

	return result
}

func splitIntoWindows(messages []exportMsg, gap time.Duration) [][]exportMsg {
	var windows [][]exportMsg
	var current []exportMsg

	for _, m := range messages {
		if len(current) > 0 {
			prev := current[len(current)-1]
			if m.ChannelID != prev.ChannelID || m.SentAt.Sub(prev.SentAt) > gap {
				windows = append(windows, current)
				current = nil
			}
		}
		current = append(current, m)
	}

	if len(current) > 0 {
		windows = append(windows, current)
	}

	return windows
}

func hasTargetUser(window []exportMsg, targetUserID string) bool {
	for _, m := range window {
		if m.UserID == targetUserID {
			return true
		}
	}
	return false
}

func buildTurns(window []exportMsg, targetUserID, systemPrompt string) []ShareGPTMessage {
	msgs := []ShareGPTMessage{{From: "system", Value: systemPrompt}}
	var lastRole string

	for _, m := range window {
		role := "human"
		if m.UserID == targetUserID {
			role = "gpt"
		}

		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}

		if role == "human" {
			name := m.DisplayName
			if name == "" {
				name = m.Username
			}
			content = name + ": " + content
		}

		if role == lastRole && len(msgs) > 1 {
			msgs[len(msgs)-1].Value += "\n" + content
		} else {
			msgs = append(msgs, ShareGPTMessage{From: role, Value: content})
			lastRole = role
		}
	}

	return msgs
}

func formatConversation(window []exportMsg, targetUserID, systemPrompt string, minTurns int) (ShareGPTConversation, bool) {
	if !hasTargetUser(window, targetUserID) {
		return ShareGPTConversation{}, false
	}

	msgs := buildTurns(window, targetUserID, systemPrompt)

	turnCount := 0
	for _, m := range msgs {
		if m.From != "system" {
			turnCount++
		}
	}

	if turnCount < minTurns || msgs[len(msgs)-1].From != "gpt" {
		return ShareGPTConversation{}, false
	}

	return ShareGPTConversation{Conversations: msgs}, true
}

func writeJSONL(path string, conversations []ShareGPTConversation) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, conv := range conversations {
		if err := enc.Encode(conv); err != nil {
			return err
		}
	}
	return nil
}
