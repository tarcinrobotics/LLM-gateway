package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatStreamingRequiresCompletionSignal(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    bool
		wantChunks int
		wantDone   bool
	}{
		{
			name: "completed stream",
			body: `{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":5,"eval_count":2}` + "\n",
			wantErr:    false,
			wantChunks: 2,
			wantDone:   true,
		},
		{
			name:       "stream ends without completion signal",
			body:       `{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n",
			wantErr:    true,
			wantChunks: 1,
		},
		{
			name: "malformed chunk after valid chunk",
			body: `{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n" +
				`not-valid-json` + "\n",
			wantErr:    true,
			wantChunks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClient(server.URL, time.Second)
			req := &ChatRequest{
				Model:  "test-model",
				Stream: true,
			}

			var receivedChunks int
			resp, err := client.Chat(context.Background(), req, func(chunk ChatResponse) error {
				receivedChunks++
				return nil
			})

			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if receivedChunks != tt.wantChunks {
				t.Fatalf("expected %d chunks, got %d", tt.wantChunks, receivedChunks)
			}

			if !tt.wantErr {
				if resp == nil {
					t.Fatal("expected a response, got nil")
				}
				if resp.Done != tt.wantDone {
					t.Fatalf("expected Done=%v, got %v", tt.wantDone, resp.Done)
				}
				if resp.PromptEvalCount != 5 || resp.EvalCount != 2 {
					t.Fatalf("unexpected token counts: prompt=%d completion=%d",
						resp.PromptEvalCount, resp.EvalCount)
				}
			}
		})
	}
}

func TestChatStreamingCompletionWithoutTrailingNewline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.TrimSpace(
			`{"message":{"role":"assistant","content":"Hello"},"done":true}`,
		)))
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	req := &ChatRequest{Model: "test-model", Stream: true}

	resp, err := client.Chat(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || !resp.Done {
		t.Fatal("expected a completed response")
	}
}
