// Package source fetches raw Telegram posts, without translating or
// storing them. Two backends implement the same interface: a plain HTTP
// scrape of the public web preview, and an authenticated MTProto client.
package source

import (
	"context"
	"time"
)

// Post is one raw Telegram message, before translation.
type Post struct {
	Channel   string    // username, no @
	MessageID int64     // telegram message id, monotonic within a channel
	Text      string    // raw Persian text, no HTML
	HTML      string    // original HTML fragment if available, else ""
	URL       string    // https://t.me/<channel>/<id>
	PostedAt  time.Time // UTC
}

// Source fetches the newest posts for one channel.
type Source interface {
	Fetch(ctx context.Context, channel string) ([]Post, error)
}
