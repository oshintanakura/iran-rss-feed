// Package store persists fetched posts in sqlite so a post is never
// translated twice and feeds can be rebuilt from what's already known.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"iran-rss-feed/internal/source"
)

const schema = `
CREATE TABLE IF NOT EXISTS posts (
    channel      TEXT    NOT NULL,
    message_id   INTEGER NOT NULL,
    content_hash TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    posted_at    INTEGER NOT NULL,
    original     TEXT    NOT NULL,
    translated   TEXT,
    translated_at INTEGER,
    PRIMARY KEY (channel, message_id)
);

CREATE INDEX IF NOT EXISTS idx_posts_recent ON posts(channel, posted_at DESC);
`

// SkippedTooLong is the sentinel stored in `translated` for posts that
// exceeded max_chars_per_post. It is never returned by Recent/RecentAll,
// so such posts are excluded from feeds without being retried forever.
const SkippedTooLong = "SKIPPED_TOO_LONG"

// Item is a stored post ready to render into a feed.
type Item struct {
	Channel    string
	MessageID  int64
	URL        string
	PostedAt   time.Time
	Original   string
	Translated string
}

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the sqlite database at path and applies
// the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening store %s: %w", path, err)
	}
	// sqlite handles one writer at a time; a single connection avoids
	// "database is locked" errors without needing a connection pool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema to %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// ContentHash returns sha256(text) hex-encoded.
func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// FilterUnseen keeps only posts that need (re)translation: new posts,
// posts whose stored content_hash no longer matches the freshly fetched
// text (i.e. the post was edited), and posts that were fetched before but
// never successfully translated (translated IS NULL — a prior run's
// translation failed and must be retried). A post with a matching hash
// and a non-NULL translated value is done — either really translated, or
// deliberately parked as SkippedTooLong — and is never revisited.
func (s *Store) FilterUnseen(ctx context.Context, posts []source.Post) ([]source.Post, error) {
	var unseen []source.Post
	for _, p := range posts {
		hash := ContentHash(p.Text)
		var storedHash string
		var translated sql.NullString
		err := s.db.QueryRowContext(ctx,
			`SELECT content_hash, translated FROM posts WHERE channel = ? AND message_id = ?`,
			p.Channel, p.MessageID,
		).Scan(&storedHash, &translated)
		switch {
		case err == sql.ErrNoRows:
			unseen = append(unseen, p)
		case err != nil:
			return nil, fmt.Errorf("checking seen state for %s/%d: %w", p.Channel, p.MessageID, err)
		case storedHash != hash:
			unseen = append(unseen, p) // edited since last seen
		case !translated.Valid:
			unseen = append(unseen, p) // previously failed to translate; retry
		}
	}
	return unseen, nil
}

// SaveTranslated upserts a post together with its English translation.
func (s *Store) SaveTranslated(ctx context.Context, p source.Post, englishText string) error {
	return s.save(ctx, p, sql.NullString{String: englishText, Valid: true}, true)
}

// SaveSkippedTooLong upserts a post marked with the "too long" sentinel so
// it is excluded from feeds and never retried.
func (s *Store) SaveSkippedTooLong(ctx context.Context, p source.Post) error {
	return s.save(ctx, p, sql.NullString{String: SkippedTooLong, Valid: true}, true)
}

// SaveFailed upserts a post with translated = NULL, so it is retried on
// the next run.
func (s *Store) SaveFailed(ctx context.Context, p source.Post) error {
	return s.save(ctx, p, sql.NullString{}, false)
}

func (s *Store) save(ctx context.Context, p source.Post, translated sql.NullString, setTranslatedAt bool) error {
	hash := ContentHash(p.Text)
	var translatedAt sql.NullInt64
	if setTranslatedAt {
		translatedAt = sql.NullInt64{Int64: time.Now().UTC().Unix(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO posts (channel, message_id, content_hash, url, posted_at, original, translated, translated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel, message_id) DO UPDATE SET
			content_hash = excluded.content_hash,
			url = excluded.url,
			posted_at = excluded.posted_at,
			original = excluded.original,
			translated = excluded.translated,
			translated_at = excluded.translated_at
	`, p.Channel, p.MessageID, hash, p.URL, p.PostedAt.UTC().Unix(), p.Text, translated, translatedAt)
	if err != nil {
		return fmt.Errorf("saving %s/%d: %w", p.Channel, p.MessageID, err)
	}
	return nil
}

// Recent returns every translated item for one channel posted within the
// last maxAgeDays, newest first — never fewer than that, regardless of
// count. safetyCap is a hard ceiling on rows returned, purely to bound a
// pathological runaway (a channel somehow producing thousands of posts
// in the window); at any realistic volume it never actually binds, so
// the age window is the real constraint, not the count. Rows with
// translated IS NULL or the "too long" sentinel are excluded so a feed
// never carries untranslated Persian.
func (s *Store) Recent(ctx context.Context, channel string, maxAgeDays, safetyCap int) ([]Item, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays).Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, message_id, url, posted_at, original, translated
		FROM posts
		WHERE channel = ? AND translated IS NOT NULL AND translated != ? AND posted_at >= ?
		ORDER BY posted_at DESC
		LIMIT ?
	`, channel, SkippedTooLong, cutoff, safetyCap)
	if err != nil {
		return nil, fmt.Errorf("querying recent posts for %s: %w", channel, err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// RecentAll is Recent, across every channel.
func (s *Store) RecentAll(ctx context.Context, maxAgeDays, safetyCap int) ([]Item, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays).Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, message_id, url, posted_at, original, translated
		FROM posts
		WHERE translated IS NOT NULL AND translated != ? AND posted_at >= ?
		ORDER BY posted_at DESC
		LIMIT ?
	`, SkippedTooLong, cutoff, safetyCap)
	if err != nil {
		return nil, fmt.Errorf("querying recent posts: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// AllTranslated returns every translated item ever stored, across every
// channel, with no limit — used to render one permanent HTML page per
// post. Pages are never pruned, so this intentionally ignores the
// max_items_per_feed window that Recent/RecentAll apply.
func (s *Store) AllTranslated(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, message_id, url, posted_at, original, translated
		FROM posts
		WHERE translated IS NOT NULL AND translated != ?
		ORDER BY posted_at DESC
	`, SkippedTooLong)
	if err != nil {
		return nil, fmt.Errorf("querying all translated posts: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func scanItems(rows *sql.Rows) ([]Item, error) {
	var items []Item
	for rows.Next() {
		var it Item
		var postedAtUnix int64
		if err := rows.Scan(&it.Channel, &it.MessageID, &it.URL, &postedAtUnix, &it.Original, &it.Translated); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		it.PostedAt = time.Unix(postedAtUnix, 0).UTC()
		items = append(items, it)
	}
	return items, rows.Err()
}
