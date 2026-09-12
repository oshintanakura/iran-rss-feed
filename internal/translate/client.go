// Package translate calls an OpenAI-compatible chat completions API to
// translate Telegram posts from Persian to English, one post at a time,
// with an optional refinement pass.
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const systemPrompt = `Translate Persian Telegram posts to natural English.

Rules:
- Translate the full meaning, not word-for-word.
- Never translate proper nouns: personal names, place names, organization
  names, and titles. Transliterate them into their common English spellings
  instead. For example: "مسعود پزشکیان" -> "Masoud Pezeshkian", "آیت‌الله خامنه‌ای" -> "Ayatollah Khamenei".
  This applies the same way to Arabic names and words that appear in Persian text.
- Use the same English spelling for the same name every time it appears in a post.
- Preserve all URLs, @mentions, #hashtags, and numbers exactly as they appear.
- Preserve paragraph breaks.
- Do not summarize. Do not add commentary. Do not omit content.
- If a post is already in English, return it unchanged.

Output the translation only — no commentary, no explanations, no markdown.`

const refinePrompt = `You are improving a Persian-to-English translation of a Telegram post.

Improve the translation below so that:
- No information, nuance, or tone from the original is lost.
- It reads as natural English, not translated English.
- The proper-noun rules from the first pass are kept (names transliterated,
  not translated; URLs, @mentions, #hashtags, and numbers untouched).

Return ONLY the improved translation — no commentary, no explanations, no markdown.`

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	HTTPClient  *http.Client
	Logger      *slog.Logger

	// Calls counts actual HTTP requests made to the API (including
	// retries), for the run summary log line. Sequential use only.
	Calls int
}

func NewClient(baseURL, apiKey, model string, temperature float64, timeout time.Duration, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		Model:       model,
		Temperature: temperature,
		HTTPClient:  &http.Client{Timeout: timeout},
		Logger:      logger,
	}
}

// Translate translates one post. An empty translation is returned as an
// error so callers never store a blank string.
func (c *Client) Translate(ctx context.Context, text string) (string, error) {
	content, err := c.call(ctx, systemPrompt, text)
	if err != nil {
		return "", err
	}
	content = cleanReply(content)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("model returned an empty translation")
	}
	return content, nil
}

// Refine asks the model to improve a first-pass translation against the
// original. It is best-effort: callers keep the first attempt when it
// fails.
func (c *Client) Refine(ctx context.Context, original, translation string) (string, error) {
	user := fmt.Sprintf("Original Persian post:\n---\n%s\n---\n\nCurrent English translation:\n---\n%s\n---", original, translation)
	content, err := c.call(ctx, refinePrompt, user)
	if err != nil {
		return "", err
	}
	content = cleanReply(content)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("model returned an empty refinement")
	}
	return content, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call performs one chat-completions round-trip with bounded retries on
// 429/5xx, and returns the raw assistant message content.
func (c *Client) call(ctx context.Context, system, user string) (string, error) {
	reqBody := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: c.Temperature,
		MaxTokens:   maxTokensFor(len(user)),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encoding request: %w", err)
	}

	var lastErr error
	delays := []time.Duration{2 * time.Second, 8 * time.Second, 32 * time.Second}
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delays[attempt-1]):
			}
		}

		content, retryable, err := c.doRequest(ctx, body)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
		c.Logger.Warn("chat API request failed, retrying", "attempt", attempt+1, "error", err)
	}
	return "", fmt.Errorf("giving up after retries: %w", lastErr)
}

// doRequest returns (content, retryable, err). retryable is true for
// HTTP 429/5xx, meaning the caller may back off and try again.
func (c *Client) doRequest(ctx context.Context, body []byte) (string, bool, error) {
	c.Calls++
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("calling chat API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("reading chat API response: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", true, fmt.Errorf("chat API HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("chat API HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, fmt.Errorf("decoding chat API response: %w", err)
	}
	if parsed.Error != nil {
		return "", false, fmt.Errorf("chat API error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("chat API response had no choices")
	}
	return parsed.Choices[0].Message.Content, false, nil
}

// cleanReply strips markdown code fences and surrounding whitespace.
func cleanReply(content string) string {
	cleaned := strings.TrimSpace(content)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	return strings.TrimSpace(cleaned)
}

// maxTokensFor sizes the output budget to roughly 4 * input chars / 3, a
// generous estimate that English translations rarely exceed.
func maxTokensFor(inputChars int) int {
	const minTokens = 256
	tokens := inputChars * 4 / 3
	if tokens < minTokens {
		tokens = minTokens
	}
	return tokens
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
