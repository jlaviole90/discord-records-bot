package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	geminiModel     = "gemini-2.0-flash"
	geminiEndpoint  = "https://generativelanguage.googleapis.com/v1beta/models/" + geminiModel + ":generateContent"
	maxPayloadChars = 16_000
)

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type ChatLine struct {
	Username    string
	DisplayName string
	Content     string
	SentAt      time.Time
}

func summarizeMessages(apiKey string, messages []ChatLine, hours int) (string, error) {
	transcript := buildTranscript(messages)

	prompt := fmt.Sprintf(
		"Summarize the following Discord conversation from the last %d hour(s). "+
			"Be concise. Highlight key topics, decisions, and important points. "+
			"Format for Discord using markdown. Keep the summary under 1800 characters.\n\n%s",
		hours, transcript,
	)

	reqBody := geminiRequest{
		Contents: []geminiContent{{
			Parts: []geminiPart{{Text: prompt}},
		}},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s?key=%s", geminiEndpoint, apiKey)
	client := &http.Client{Timeout: 60 * time.Second}

	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini returned %d: %s", resp.StatusCode, body)
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(body, &gemResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(gemResp.Candidates) == 0 || len(gemResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from Gemini")
	}

	return gemResp.Candidates[0].Content.Parts[0].Text, nil
}

// buildTranscript formats messages into a timestamped chat log, stopping at
// maxPayloadChars to cap the size of the API request.
func buildTranscript(messages []ChatLine) string {
	var buf bytes.Buffer
	for _, m := range messages {
		name := m.DisplayName
		if name == "" {
			name = m.Username
		}
		line := fmt.Sprintf("[%s] %s: %s\n", m.SentAt.Format("3:04 PM"), name, m.Content)

		if buf.Len()+len(line) > maxPayloadChars {
			buf.WriteString("... (older messages truncated) ...\n")
			break
		}
		buf.WriteString(line)
	}
	return buf.String()
}
