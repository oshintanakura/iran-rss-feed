package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// userAgent must look like a real browser. Telegram's preview page blocks
// the default Go http.Client user-agent.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// WebSource scrapes the public https://t.me/s/<username> preview page.
// It requires no Telegram account and no channel join.
type WebSource struct {
	Client *http.Client
}

// NewWebSource builds a WebSource with the given per-request timeout.
func NewWebSource(timeout time.Duration) *WebSource {
	return &WebSource{Client: &http.Client{Timeout: timeout}}
}

func (s *WebSource) Fetch(ctx context.Context, channel string) ([]Post, error) {
	url := "https://t.me/s/" + channel
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", channel, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", channel, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetching %s: HTTP %d: %s", channel, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML for %s: %w", channel, err)
	}

	var posts []Post
	doc.Find("div.tgme_widget_message").Each(func(_ int, sel *goquery.Selection) {
		p, ok := parseMessage(channel, sel)
		if !ok {
			return
		}
		posts = append(posts, p)
	})

	if len(posts) == 0 {
		return nil, fmt.Errorf("selector returned 0 posts for %s — page markup may have changed", channel)
	}

	// Telegram renders oldest-first top-to-bottom; the pipeline wants
	// newest-first.
	reverse(posts)
	return posts, nil
}

func parseMessage(channel string, sel *goquery.Selection) (Post, bool) {
	dataPost, ok := sel.Attr("data-post")
	if !ok {
		return Post{}, false
	}
	parts := strings.SplitN(dataPost, "/", 2)
	if len(parts) != 2 {
		return Post{}, false
	}
	msgID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Post{}, false
	}

	textSel := sel.Find(".tgme_widget_message_text").First()
	text := strings.TrimSpace(textSel.Text())
	if text == "" {
		// Pure media post with no caption; nothing to translate.
		return Post{}, false
	}
	html, _ := textSel.Html()

	postedAt := time.Now().UTC()
	if dt, ok := sel.Find("time[datetime]").First().Attr("datetime"); ok {
		if parsed, err := time.Parse(time.RFC3339, dt); err == nil {
			postedAt = parsed.UTC()
		}
	}

	return Post{
		Channel:   channel,
		MessageID: msgID,
		Text:      text,
		HTML:      html,
		URL:       fmt.Sprintf("https://t.me/%s/%d", channel, msgID),
		PostedAt:  postedAt,
	}, true
}

func reverse(posts []Post) {
	for i, j := 0, len(posts)-1; i < j; i, j = i+1, j-1 {
		posts[i], posts[j] = posts[j], posts[i]
	}
}
