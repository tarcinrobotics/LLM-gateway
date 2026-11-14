package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm_gateway/internal/api/middleware"
	"llm_gateway/internal/database"
	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
)

const testAPIKey = "test-api-key"

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) WriteHeader(statusCode int) {}

func (w *failingResponseWriter) Write(p []byte) (int, error) {
	return 0, errors.New("simulated client disconnect")
}

func (w *failingResponseWriter) Flush() {}

func setupChatTest(
	t *testing.T,
	ollamaHandler http.HandlerFunc,
) http.Handler {
	t.Helper()

	// Start a fake Ollama HTTP server.
	server := httptest.NewServer(ollamaHandler)
	t.Cleanup(server.Close)

	// Create an isolated SQLite database.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize test database: %v", err)
	}
	t.Cleanup(func() {
		// Wait for the asynchronous audit log to be written.
		deadline := time.Now().Add(2 * time.Second)

		for {
			var count int
			err := db.QueryRow(
				"SELECT COUNT(*) FROM request_logs",
			).Scan(&count)

			if err == nil && count > 0 {
				break
			}

			if time.Now().After(deadline) {
				t.Errorf("timed out waiting for audit log: count=%d, error=%v", count, err)
				break
			}

			time.Sleep(10 * time.Millisecond)
		}

		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	// Seed roles, users, and permissions.
	if err := database.SeedData(db); err != nil {
		t.Fatalf("failed to seed test database: %v", err)
	}

	// SeedData creates the initial admin user and role.
	_, err = database.CreateAPIKey(db, testAPIKey, 1, 1, 100)
	if err != nil {
		t.Fatalf("failed to create test API key: %v", err)
	}

	// Start one worker for the test.
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	client := ollama.NewClient(server.URL, time.Second)

	handler := ChatHandler(
		db,
		wp,
		client,
		time.Second,
		2*time.Second,
	)

	// Apply the same authentication and request logging middleware
	// used by the application.
	return middleware.LoggingMiddleware(
		middleware.AuthMiddleware(db)(handler),
	)
}

func sendStreamingRequest(handler http.Handler) *httptest.ResponseRecorder {
	body := `{
		"model": "test-model",
		"stream": true,
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(body),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

func TestChatStreamingSuccess(t *testing.T) {
	handler := setupChatTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":5,"eval_count":2}` + "\n",
		))
	})

	recorder := sendStreamingRequest(handler)
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", recorder.Code, body)
	}

	if !strings.Contains(body, `"content":"Hello"`) {
		t.Fatal("expected assistant content in stream")
	}

	if !strings.Contains(body, `"prompt_eval_count":5`) {
		t.Fatal("expected prompt token count 5")
	}

	if !strings.Contains(body, `"eval_count":2`) {
		t.Fatal("expected completion token count 2")
	}

	if strings.Contains(body, "event: error") {
		t.Fatal("successful stream should not contain an error event")
	}
}

func TestChatStreamingFailureBeforeFirstChunk(t *testing.T) {
	handler := setupChatTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		// Malformed JSON causes Ollama streaming to fail
		// before the first valid chunk is received.
		_, _ = w.Write([]byte("not-valid-json\n"))
	})

	recorder := sendStreamingRequest(handler)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf(
			"expected HTTP 502, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatal("SSE error event should not be sent before streaming starts")
	}
}

func TestChatStreamingFailureAfterFirstChunk(t *testing.T) {
	handler := setupChatTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n",
		))

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// The next chunk is malformed.
		_, _ = w.Write([]byte("not-valid-json\n"))
	})

	recorder := sendStreamingRequest(handler)
	body := recorder.Body.String()

	// HTTP status cannot change after SSE headers are sent.
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", recorder.Code)
	}

	if !strings.Contains(body, `"content":"Hello"`) {
		t.Fatal("expected the first valid chunk")
	}

	if !strings.Contains(body, "event: error") {
		t.Fatal("expected an SSE error event")
	}

	if !strings.Contains(body, "Generation failed") {
		t.Fatal("expected a safe generation failure message")
	}
}

func TestChatStreamingWithoutCompletionSignal(t *testing.T) {
	handler := setupChatTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		// A valid chunk, but no final Done:true chunk.
		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n",
		))
	})

	recorder := sendStreamingRequest(handler)
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", recorder.Code)
	}

	if !strings.Contains(body, `"content":"Hello"`) {
		t.Fatal("expected the valid partial chunk")
	}

	if !strings.Contains(body, "event: error") {
		t.Fatal("expected an SSE error event for incomplete stream")
	}
}
func TestChatStreamingClientDisconnectCancelsInference(t *testing.T) {
	cancelled := make(chan struct{})

	handler := setupChatTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n",
		))

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// Wait until the gateway cancels the upstream request.
		<-r.Context().Done()
		close(cancelled)
	})

	body := `{
		"model": "test-model",
		"stream": true,
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(body),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)

	writer := &failingResponseWriter{
		header: make(http.Header),
	}

	handler.ServeHTTP(writer, req)

	select {
	case <-cancelled:
		// Upstream inference was successfully cancelled.
	case <-time.After(2 * time.Second):
		t.Fatal("expected client disconnect to cancel upstream inference")
	}
}
