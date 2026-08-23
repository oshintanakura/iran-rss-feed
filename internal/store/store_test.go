package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"iran-rss-feed/internal/source"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFilterUnseen_NewPostIsUnseen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}
	unseen, err := s.FilterUnseen(ctx, []source.Post{p})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 1 {
		t.Fatalf("want 1 unseen post, got %d", len(unseen))
	}
}

func TestFilterUnseen_FailedPostIsRetriedOnRerun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}
	if err := s.SaveFailed(ctx, p); err != nil {
		t.Fatalf("SaveFailed: %v", err)
	}

	// Same text, unchanged hash — but translated is still NULL, so this
	// must come back as unseen, not silently skipped forever.
	unseen, err := s.FilterUnseen(ctx, []source.Post{p})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 1 {
		t.Fatalf("want the failed post to be retried (hash unchanged, translated still NULL), got %d unseen", len(unseen))
	}
}

func TestFilterUnseen_TranslatedPostIsSkippedOnRerun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}
	if err := s.SaveTranslated(ctx, p, "hello (en)"); err != nil {
		t.Fatalf("SaveTranslated: %v", err)
	}

	unseen, err := s.FilterUnseen(ctx, []source.Post{p})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Fatalf("want 0 unseen posts (already translated, unchanged), got %d", len(unseen))
	}
}

func TestFilterUnseen_SkippedTooLongIsNeverRetried(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}
	if err := s.SaveSkippedTooLong(ctx, p); err != nil {
		t.Fatalf("SaveSkippedTooLong: %v", err)
	}

	// Unlike a real failure, SKIPPED_TOO_LONG is a deliberate, permanent
	// sentinel (translated is NOT NULL) — it must not come back as unseen.
	unseen, err := s.FilterUnseen(ctx, []source.Post{p})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Fatalf("want a SKIPPED_TOO_LONG post to never be retried, got %d unseen", len(unseen))
	}
}

func TestFilterUnseen_EditedPostReappearsAsUnseen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	original := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}
	if err := s.SaveTranslated(ctx, original, "hello (en)"); err != nil {
		t.Fatalf("SaveTranslated: %v", err)
	}

	edited := original
	edited.Text = "hello, edited"
	unseen, err := s.FilterUnseen(ctx, []source.Post{edited})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 1 {
		t.Fatalf("want the edited post to come back as unseen (hash differs), got %d unseen", len(unseen))
	}
}

func TestRecent_ExcludesFailedAndSkippedTooLong(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()

	ok := source.Post{Channel: "c", MessageID: 1, Text: "a", URL: "https://t.me/c/1", PostedAt: now}
	failed := source.Post{Channel: "c", MessageID: 2, Text: "b", URL: "https://t.me/c/2", PostedAt: now}
	tooLong := source.Post{Channel: "c", MessageID: 3, Text: "c", URL: "https://t.me/c/3", PostedAt: now}

	if err := s.SaveTranslated(ctx, ok, "a (en)"); err != nil {
		t.Fatalf("SaveTranslated: %v", err)
	}
	if err := s.SaveFailed(ctx, failed); err != nil {
		t.Fatalf("SaveFailed: %v", err)
	}
	if err := s.SaveSkippedTooLong(ctx, tooLong); err != nil {
		t.Fatalf("SaveSkippedTooLong: %v", err)
	}

	items, err := s.Recent(ctx, "c", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(items) != 1 || items[0].MessageID != 1 {
		t.Fatalf("want only the translated post in the feed, got %+v", items)
	}
}

func TestSave_IsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := source.Post{Channel: "c", MessageID: 1, Text: "hello", URL: "https://t.me/c/1", PostedAt: time.Now()}

	if err := s.SaveTranslated(ctx, p, "hello (en)"); err != nil {
		t.Fatalf("SaveTranslated (1st): %v", err)
	}
	if err := s.SaveTranslated(ctx, p, "hello (en), updated"); err != nil {
		t.Fatalf("SaveTranslated (2nd): %v", err)
	}

	items, err := s.RecentAll(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want exactly one row after upserting the same (channel, message_id) twice, got %d", len(items))
	}
}
