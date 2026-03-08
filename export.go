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
	customEmojiRe  = regexp.MustCompile(`<a?:\w+:\d+>`)
	colonEmojiRe   = regexp.MustCompile(`:\w[\w+-]*:`)
	userMentionRe  = regexp.MustCompile(`<@!?(\d+)>`)
	channelMentionRe = regexp.MustCompile(`<#\d+>`)
	roleMentionRe  = regexp.MustCompile(`<@&\d+>`)
	urlRe          = regexp.MustCompile(`https?://\S+`)
	multiSpaceRe   = regexp.MustCompile(`\s{2,}`)
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

const defaultSystemPrompt = "You are a casual member of a Discord server. " +
	"Respond naturally and concisely like a real person would in a Discord chat. " +
	"Never say you are an AI, language model, or chatbot. " +
	"You have opinions, preferences, and a personality."

func runExport() {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	guildID := fs.String("guild-id", "", "Discord guild ID (required)")
	output := fs.String("output", "training_data.jsonl", "Output JSONL file path")
	sinceStr := fs.String("since", "", "Only export messages after this timestamp (RFC3339)")
	windowMin := fs.Int("window", 5, "Conversation window gap in minutes")
	minTurns := fs.Int("min-turns", 3, "Minimum conversation turns to include")
	fs.Parse(os.Args[2:])

	if *guildID == "" {
		fmt.Fprintln(os.Stderr, "Usage: discord-records-bot export --guild-id=ID [--output=file.jsonl] [--since=RFC3339] [--window=5] [--min-turns=3]")
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

	conversations := buildConversations(messages, defaultSystemPrompt, *windowMin, *minTurns)
	log.Printf("Built %d conversations", len(conversations))

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
	content = customEmojiRe.ReplaceAllString(content, "")
	content = colonEmojiRe.ReplaceAllString(content, "")

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
	content = multiSpaceRe.ReplaceAllString(content, " ")

	return strings.TrimSpace(content)
}

func hasExcessiveRepetition(content string) bool {
	words := strings.Fields(strings.ToLower(content))
	if len(words) < 4 {
		return false
	}
	freq := make(map[string]int)
	for _, w := range words {
		freq[w]++
	}
	for _, count := range freq {
		if count > 3 && float64(count)/float64(len(words)) > 0.3 {
			return true
		}
	}
	return false
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

func buildConversations(messages []exportMsg, systemPrompt string, windowMin, minTurns int) []ShareGPTConversation {
	var result []ShareGPTConversation
	windows := splitIntoWindows(messages, time.Duration(windowMin)*time.Minute)

	for _, window := range windows {
		if conv, ok := formatConversation(window, systemPrompt, minTurns); ok {
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

func buildTurns(window []exportMsg, systemPrompt string) []ShareGPTMessage {
	msgs := []ShareGPTMessage{{From: "system", Value: systemPrompt}}

	var lastUserID string
	currentRole := "human"

	for _, m := range window {
		content := strings.TrimSpace(m.Content)
		if len(content) < 3 || hasExcessiveRepetition(content) {
			continue
		}

		if m.UserID != lastUserID && lastUserID != "" {
			if currentRole == "human" {
				currentRole = "gpt"
			} else {
				currentRole = "human"
			}
		}

		if len(msgs) > 1 && msgs[len(msgs)-1].From == currentRole {
			msgs[len(msgs)-1].Value += "\n" + content
		} else {
			msgs = append(msgs, ShareGPTMessage{From: currentRole, Value: content})
		}
		lastUserID = m.UserID
	}

	return msgs
}

func formatConversation(window []exportMsg, systemPrompt string, minTurns int) (ShareGPTConversation, bool) {
	msgs := buildTurns(window, systemPrompt)

	if len(msgs) > 1 && msgs[len(msgs)-1].From != "gpt" {
		msgs = msgs[:len(msgs)-1]
	}

	turnCount := 0
	for _, m := range msgs {
		if m.From != "system" {
			turnCount++
		}
	}

	if turnCount < minTurns {
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
