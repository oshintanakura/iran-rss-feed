// Package translate batches posts to an OpenAI-compatible chat
// completions API and maps replies back onto the posts they came from.
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

const systemPrompt = `You are a professional Persian-to-English translator working on Telegram channel posts.

Rules:
- Translate the meaning, not word-for-word. Produce natural English.
- Preserve all URLs, @mentions, #hashtags, and numbers exactly as they appear.
- Preserve paragraph breaks as \n.
- Do not summarize. Do not add commentary. Do not omit content.
- Transliterate Persian proper nouns consistently using common English spellings.
- If a post is already in English, return it unchanged.

Output format:
Return ONLY a JSON object. Keys are the "id" values from the input. Values are the
translated English strings. No markdown fences, no explanation, no extra keys.

Example input:  [{"id":"101","text":"سلام دنیا"},{"id":"102","text":"خبر فوری"}]
Example output: {"101":"Hello world","102":"Breaking news"}`

// Item is one post to translate, keyed by an opaque caller-chosen id
// (the caller uses the store's (channel, message_id) to build it).
type Item struct {
	ID   string
	Text string
}

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

// Batch translates one batch (already sized to batch_size by the caller).
// Every input id is guaranteed to appear as a key in the result unless it
// truly could not be translated even individually — in which case it is
// simply absent, and the caller must treat a missing id as failed.
func (c *Client) Batch(ctx context.Context, items []Item) (map[string]string, error) {
	if len(items) == 0 {
		return map[string]string{}, nil
	}

	result, err := c.tryBatch(ctx, items)
	if err == nil {
		return result, nil
	}
	c.Logger.Warn("batch translation failed, retrying once with a correction prompt", "error", err, "count", len(items))

	result, err = c.tryBatchWithRetryHint(ctx, items)
	if err == nil {
		return result, nil
	}
	c.Logger.Warn("batch translation still invalid after retry, falling back to per-post translation", "error", err, "count", len(items))

	// One poison post must not lose the rest of the batch: fall back to
	// translating each post individually.
	merged := make(map[string]string, len(items))
	for _, item := range items {
		single, err := c.tryBatch(ctx, []Item{item})
		if err != nil {
			c.Logger.Warn("individual translation failed, will retry next run", "id", item.ID, "error", err)
			continue
		}
		if text, ok := single[item.ID]; ok {
			merged[item.ID] = text
		}
	}
	return merged, nil
}

func (c *Client) tryBatch(ctx context.Context, items []Item) (map[string]string, error) {
	content, err := c.call(ctx, items, nil)
	if err != nil {
		return nil, err
	}
	return parseReply(content)
}

func (c *Client) tryBatchWithRetryHint(ctx context.Context, items []Item) (map[string]string, error) {
	content, err := c.call(ctx, items, []string{"Your previous reply was not valid JSON. Return only the JSON object."})
	if err != nil {
		return nil, err
	}
	return parseReply(content)
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

type inputPost struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// call performs the HTTP round-trip with bounded retries on 429/5xx, and
// returns the raw assistant message content. extraUserMessages are
// appended after the main user message (used for the malformed-JSON
// correction turn).
func (c *Client) call(ctx context.Context, items []Item, extraUserMessages []string) (string, error) {
	inputs := make([]inputPost, len(items))
	totalChars := 0
	for i, item := range items {
		inputs[i] = inputPost{ID: item.ID, Text: item.Text}
		totalChars += len(item.Text)
	}
	userJSON, err := json.Marshal(inputs)
	if err != nil {
		return "", fmt.Errorf("encoding batch: %w", err)
	}

	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(userJSON)},
	}
	for _, extra := range extraUserMessages {
		messages = append(messages, chatMessage{Role: "user", Content: extra})
	}

	reqBody := chatRequest{
		Model:          c.Model,
		Messages:       messages,
		Temperature:    c.Temperature,
		MaxTokens:      maxTokensFor(totalChars),
		ResponseFormat: &responseFormat{Type: "json_object"},
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

// parseReply strips optional markdown code fences (models add these even
// when told not to) and decodes the {"id": "translation"} object.
func parseReply(content string) (map[string]string, error) {
	cleaned := strings.TrimSpace(content)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var result map[string]string
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("reply was not a valid JSON object: %w", err)
	}
	return result, nil
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
