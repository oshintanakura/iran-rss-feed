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

	items, err := s.Recent(ctx, "c", 10, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(items) != 1 || items[0].MessageID != 1 {
		t.Fatalf("want only the translated post in the feed, got %+v", items)
	}
}

func TestRecent_IncludesEveryPostWithinTheAgeWindowRegardlessOfCount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// 15 posts, all within the last 10 days — more than a small
	// count-based cap would have allowed through under the old
	// (buggy) semantics, where "safetyCap" was the real limit.
	for i := int64(1); i <= 15; i++ {
		p := source.Post{
			Channel:   "c",
			MessageID: i,
			Text:      "post",
			URL:       "https://t.me/c/1",
			PostedAt:  now.Add(-time.Duration(i) * time.Hour), // spread over the last 15 hours
		}
		if err := s.SaveTranslated(ctx, p, "post (en)"); err != nil {
			t.Fatalf("SaveTranslated(%d): %v", i, err)
		}
	}

	// A post from 20 days ago must NOT appear (outside the 10-day window).
	old := source.Post{Channel: "c", MessageID: 100, Text: "old", URL: "https://t.me/c/100", PostedAt: now.AddDate(0, 0, -20)}
	if err := s.SaveTranslated(ctx, old, "old (en)"); err != nil {
		t.Fatalf("SaveTranslated(old): %v", err)
	}

	// safetyCap (5) is deliberately smaller than the 15 in-window posts,
	// to prove the age window — not the count — is what's enforced.
	items, err := s.Recent(ctx, "c", 10, 5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("safetyCap should still bound the result when it's genuinely smaller than the window's contents, got %d items", len(items))
	}

	// With a safetyCap comfortably above the in-window count (as the
	// real config always sets it), every one of the 15 must come back —
	// none silently dropped for being "past #N" the way a pure count
	// limit would have done.
	items, err = s.Recent(ctx, "c", 10, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(items) != 15 {
		t.Fatalf("want all 15 posts within the 10-day window, got %d", len(items))
	}
	for _, it := range items {
		if it.MessageID == 100 {
			t.Fatalf("post from 20 days ago should be outside the 10-day window, but was included")
		}
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

	items, err := s.RecentAll(ctx, 10, 100)
	if err != nil {
		t.Fatalf("RecentAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want exactly one row after upserting the same (channel, message_id) twice, got %d", len(items))
	}
}
