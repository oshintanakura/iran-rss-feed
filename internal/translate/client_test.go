package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-key", "test-model", 0.2, 5*time.Second, nil)
}

func writeChoice(w http.ResponseWriter, content string) {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": content}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func decodeMessages(r *http.Request) []string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	var contents []string
	for _, m := range req.Messages {
		contents = append(contents, m.Content)
	}
	return contents
}

func TestTranslate(t *testing.T) {
	var userContent string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		msgs := decodeMessages(r)
		userContent = msgs[len(msgs)-1]
		writeChoice(w, "Masoud Pezeshkian said hello")
	})

	got, err := client.Translate(context.Background(), "مسعود پزشکیان گفت سلام")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "Masoud Pezeshkian said hello" {
		t.Fatalf("unexpected result: %q", got)
	}
	if userContent != "مسعود پزشکیان گفت سلام" {
		t.Fatalf("user message should be the raw post, got %q", userContent)
	}
	if client.Calls != 1 {
		t.Fatalf("want 1 HTTP call, got %d", client.Calls)
	}
}

func TestTranslate_StripsMarkdownFences(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, "```\nHello world\n```")
	})

	got, err := client.Translate(context.Background(), "سلام دنیا")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "Hello world" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestTranslate_EmptyReplyIsAnError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, "   ")
	})

	if _, err := client.Translate(context.Background(), "سلام"); err == nil {
		t.Fatal("want an error for an empty translation")
	}
}

func TestRefine_SendsOriginalAndTranslation(t *testing.T) {
	var userContent string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		msgs := decodeMessages(r)
		userContent = msgs[len(msgs)-1]
		writeChoice(w, "Masoud Pezeshkian said he will travel tomorrow.")
	})

	got, err := client.Refine(context.Background(), "مسعود پزشکیان گفت فردا سفر می‌کند", "Masoud said he goes tomorrow")
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got != "Masoud Pezeshkian said he will travel tomorrow." {
		t.Fatalf("unexpected result: %q", got)
	}
	if !strings.Contains(userContent, "مسعود پزشکیان گفت فردا سفر می‌کند") || !strings.Contains(userContent, "Masoud said he goes tomorrow") {
		t.Fatalf("user message should contain both the original and the translation, got %q", userContent)
	}
}

func TestTranslate_FailsFastOnHTTP400(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	if _, err := client.Translate(context.Background(), "سلام"); err == nil {
		t.Fatal("want an error for HTTP 400")
	}
	if client.Calls != 1 {
		t.Fatalf("non-retryable status must not be retried, got %d calls", client.Calls)
	}
}

func TestTranslate_RetriesOnHTTP500(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeChoice(w, "Hello")
	})
	client.HTTPClient.Timeout = 2 * time.Second

	// The real backoff is 2s/8s/32s; this test only waits out the first
	// 2-second delay.
	got, err := client.Translate(context.Background(), "سلام")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "Hello" {
		t.Fatalf("unexpected result: %q", got)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls (500 then success), got %d", calls)
	}
}
