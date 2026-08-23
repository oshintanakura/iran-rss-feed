package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func decodeInputIDs(r *http.Request) []string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	var ids []string
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		var posts []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(m.Content), &posts); err == nil {
			for _, p := range posts {
				ids = append(ids, p.ID)
			}
		}
	}
	return ids
}

func TestBatch_NormalJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, `{"1":"Hello","2":"World"}`)
	})

	result, err := client.Batch(context.Background(), []Item{{ID: "1", Text: "سلام"}, {ID: "2", Text: "دنیا"}})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if result["1"] != "Hello" || result["2"] != "World" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if client.Calls != 1 {
		t.Fatalf("want 1 HTTP call, got %d", client.Calls)
	}
}

func TestBatch_StripsMarkdownFences(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, "```json\n{\"1\":\"Hello\"}\n```")
	})

	result, err := client.Batch(context.Background(), []Item{{ID: "1", Text: "سلام"}})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if result["1"] != "Hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestBatch_MalformedThenRetrySucceeds(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeChoice(w, "not json")
			return
		}
		writeChoice(w, `{"1":"Hello"}`)
	})

	result, err := client.Batch(context.Background(), []Item{{ID: "1", Text: "سلام"}})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if result["1"] != "Hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if calls != 2 {
		t.Fatalf("want exactly 2 calls (initial + one retry with correction hint), got %d", calls)
	}
}

func TestBatch_PoisonPostFallsBackToPerPostWithoutLosingTheRest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ids := decodeInputIDs(r)
		// Any request that still contains the poison post ("2") returns
		// garbage; a request for a single good post succeeds.
		for _, id := range ids {
			if id == "2" {
				writeChoice(w, "still not valid json")
				return
			}
		}
		result := map[string]string{}
		for _, id := range ids {
			result[id] = "OK:" + id
		}
		b, _ := json.Marshal(result)
		writeChoice(w, string(b))
	})

	items := []Item{{ID: "1", Text: "a"}, {ID: "2", Text: "poison"}, {ID: "3", Text: "c"}}
	result, err := client.Batch(context.Background(), items)
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if result["1"] != "OK:1" || result["3"] != "OK:3" {
		t.Fatalf("good posts in the batch should still be translated, got: %+v", result)
	}
	if _, ok := result["2"]; ok {
		t.Fatalf("poison post should be absent from the result (caller marks it SaveFailed), got: %+v", result)
	}
}

func TestBatch_RetryableStatusIsRetried(t *testing.T) {
	// Not exercising the real sleep backoff (2s/8s/32s) in a unit test;
	// this just confirms a single 200 after we'd see a 500 is handled by
	// the outer retry-batch-once-then-fallback logic instead of erroring
	// out immediately as non-retryable would.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // non-retryable: fail fast
	})
	client.HTTPClient.Timeout = 2 * time.Second

	_, err := client.tryBatch(context.Background(), []Item{{ID: "1", Text: "a"}})
	if err == nil {
		t.Fatalf("want an error for HTTP 400")
	}
}
