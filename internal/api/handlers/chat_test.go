package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()

	return setupChatTestWithWorkerPool(t, ollamaHandler, wp, true)
}

func setupChatTestWithWorkerPool(
	t *testing.T,
	ollamaHandler http.HandlerFunc,
	wp *queue.WorkerPool,
	expectAudit bool,
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
		if expectAudit {
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
					t.Errorf(
						"timed out waiting for audit log: count=%d, error=%v",
						count,
						err,
					)
					break
				}

				time.Sleep(10 * time.Millisecond)
			}
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

func sendChatRequest(handler http.Handler, body, requestID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(body),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("X-Request-ID", requestID)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

func sendStreamingRequest(handler http.Handler) *httptest.ResponseRecorder {
	body := `{
		"model": "test-model",
		"stream": true,
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	return sendChatRequest(handler, body, "chat-stream-test")
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

func TestChatQueueFullReturnsServiceUnavailable(t *testing.T) {
	var requestCount atomic.Int32

	// Zero-capacity queue with no worker started:
	// submission must be rejected immediately.
	wp := queue.NewWorkerPool(1, 0)

	handler := setupChatTestWithWorkerPool(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
	}, wp, false)

	recorder := sendStreamingRequest(handler)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"expected HTTP 503, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	if !strings.Contains(recorder.Body.String(), "Worker queue is full") {
		t.Fatalf("expected queue-full message, got: %s", recorder.Body.String())
	}

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("expected no Ollama requests, got %d", got)
	}
}

func TestChatWorkerPoolStoppedReturnsServiceUnavailable(t *testing.T) {
	var requestCount atomic.Int32

	wp := queue.NewWorkerPool(1, 10)
	wp.BeginShutdown()

	handler := setupChatTestWithWorkerPool(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
	}, wp, false)

	recorder := sendStreamingRequest(handler)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"expected HTTP 503, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	if !strings.Contains(recorder.Body.String(), "Server is shutting down") {
		t.Fatalf("expected shutdown message, got: %s", recorder.Body.String())
	}

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("expected no Ollama requests, got %d", got)
	}
}

func TestChatQueueTimeoutReturnsGatewayTimeout(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})

	wp := queue.NewWorkerPool(1, 1)
	wp.Start()

	handler := setupChatTestWithWorkerPool(t, func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)

		// Keep the first inference active until the test releases it.
		<-releaseUpstream

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}, wp, false)

	// Start one request and keep its inference active.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	firstBody := `{
		"model": "test-model",
		"stream": false,
		"messages": [
			{"role": "user", "content": "First request"}
		]
	}`

	firstReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(firstBody),
	).WithContext(firstCtx)

	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Authorization", "Bearer "+testAPIKey)

	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)
		handler.ServeHTTP(httptest.NewRecorder(), firstReq)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach fake Ollama server")
	}

	// The worker is occupied by the first request.
	// The second request enters the queue and must time out.
	recorder := sendStreamingRequest(handler)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf(
			"expected HTTP 504, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	if !strings.Contains(recorder.Body.String(), "too long waiting in the queue") {
		t.Fatalf("expected queue-timeout message, got: %s", recorder.Body.String())
	}

	// Cancel the first request and release the fake server handler.
	cancelFirst()
	close(releaseUpstream)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first handler did not finish after cancellation")
	}
}

func setupChatAuditTest(
	t *testing.T,
	ollamaHandler http.HandlerFunc,
	wp *queue.WorkerPool,
	queueTimeout time.Duration,
) (http.Handler, *sql.DB) {
	t.Helper()
	return setupChatAuditTestWithTrustedProxies(
		t, ollamaHandler, wp, queueTimeout, nil,
	)
}

func setupChatAuditTestWithTrustedProxies(
	t *testing.T,
	ollamaHandler http.HandlerFunc,
	wp *queue.WorkerPool,
	queueTimeout time.Duration,
	trustedProxies []netip.Prefix,
) (http.Handler, *sql.DB) {
	t.Helper()

	server := httptest.NewServer(ollamaHandler)
	t.Cleanup(server.Close)

	dbPath := filepath.Join(t.TempDir(), "audit_test.db")
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	if err := database.SeedData(db); err != nil {
		t.Fatalf("failed to seed test database: %v", err)
	}

	if _, err := database.CreateAPIKey(db, testAPIKey, 1, 1, 100); err != nil {
		t.Fatalf("failed to create test API key: %v", err)
	}

	client := ollama.NewClient(server.URL, time.Second)

	handler := ChatHandler(
		db,
		wp,
		client,
		queueTimeout,
		2*time.Second,
	)

	wrappedHandler := middleware.LoggingMiddlewareWithTrustedProxies(
		middleware.AuthMiddleware(db)(handler),
		trustedProxies,
	)

	return wrappedHandler, db
}

func assertChatAuditEvent(
	t *testing.T,
	db *sql.DB,
	requestID string,
	expectedEventType string,
	expectedOutcome string,
	expectedStatus int,
	expectedErrorCode string,
) {
	t.Helper()

	var (
		eventType  string
		outcome    string
		statusCode int
		errorCode  sql.NullString
	)

	err := db.QueryRow(`
		SELECT event_type, outcome, status_code, error_code
		FROM audit_events
		WHERE request_id = ?
	`, requestID).Scan(
		&eventType,
		&outcome,
		&statusCode,
		&errorCode,
	)
	if err != nil {
		t.Fatalf("failed to retrieve audit event for %q: %v", requestID, err)
	}

	if eventType != expectedEventType {
		t.Errorf("expected event type %q, got %q", expectedEventType, eventType)
	}

	if outcome != expectedOutcome {
		t.Errorf("expected outcome %q, got %q", expectedOutcome, outcome)
	}

	if statusCode != expectedStatus {
		t.Errorf("expected status %d, got %d", expectedStatus, statusCode)
	}

	actualErrorCode := ""
	if errorCode.Valid {
		actualErrorCode = errorCode.String
	}

	if actualErrorCode != expectedErrorCode {
		t.Errorf("expected error code %q, got %q", expectedErrorCode, actualErrorCode)
	}
}

func TestChatInvalidJSONIsAudited(t *testing.T) {
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama should not be called for invalid JSON")
	}, wp, time.Second)

	requestID := "invalid-json-test"
	recorder := sendChatRequest(handler, "{invalid-json", requestID)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", recorder.Code, recorder.Body.String())
	}

	assertChatAuditEvent(
		t, db, requestID,
		"invalid_request", "failure",
		http.StatusBadRequest, "invalid_request_body",
	)
}

func TestChatMissingModelIsAudited(t *testing.T) {
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama should not be called when model is missing")
	}, wp, time.Second)

	requestID := "missing-model-test"
	body := `{"stream":false,"messages":[{"role":"user","content":"Hello"}]}`

	recorder := sendChatRequest(handler, body, requestID)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", recorder.Code, recorder.Body.String())
	}

	assertChatAuditEvent(
		t, db, requestID,
		"invalid_request", "failure",
		http.StatusBadRequest, "missing_model",
	)
}

func TestChatSuccessfulStreamingIsAudited(t *testing.T) {
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true}` + "\n",
		))
	}, wp, time.Second)

	requestID := "successful-stream-audit-test"
	body := `{
		"model":"test-model",
		"stream":true,
		"messages":[{"role":"user","content":"Private prompt content"}]
	}`

	recorder := sendChatRequest(handler, body, requestID)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	assertChatAuditEvent(
		t, db, requestID,
		"chat_succeeded", "success",
		http.StatusOK, "",
	)

	// Verify that the audit event does not contain prompt content.
	var eventData string
	err := db.QueryRow(`
		SELECT event_type || ' ' || outcome || ' ' || COALESCE(error_code, '')
		FROM audit_events
		WHERE request_id = ?
	`, requestID).Scan(&eventData)
	if err != nil {
		t.Fatalf("failed to read audit event: %v", err)
	}

	if strings.Contains(eventData, "Private prompt content") {
		t.Fatal("audit event must not contain prompt content")
	}
}

func TestChatQueueFullIsAudited(t *testing.T) {
	wp := queue.NewWorkerPool(1, 0)

	handler, db := setupChatAuditTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama should not be called when the queue is full")
	}, wp, time.Second)

	requestID := "queue-full-audit-test"
	body := `{
		"model":"test-model",
		"stream":false,
		"messages":[{"role":"user","content":"Hello"}]
	}`

	recorder := sendChatRequest(handler, body, requestID)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d: %s", recorder.Code, recorder.Body.String())
	}

	assertChatAuditEvent(
		t, db, requestID,
		"queue_rejected", "failure",
		http.StatusServiceUnavailable, "queue_full",
	)
}

func TestChatPermissionDeniedIsAudited(t *testing.T) {
	ollamaCalled := atomic.Bool{}

	ollamaHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ollamaCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(t, ollamaHandler, wp, time.Second)

	// Create a role with no model permissions.
	result, err := db.Exec(
		`INSERT INTO roles (name) VALUES (?)`,
		"RestrictedTestRole",
	)
	if err != nil {
		t.Fatalf("failed to create restricted role: %v", err)
	}

	roleID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("failed to get restricted role ID: %v", err)
	}

	// Assign the test API key to the restricted role.
	_, err = db.Exec(
		`UPDATE api_keys SET role_id = ? WHERE hash = ?`,
		roleID,
		database.HashKey(testAPIKey),
	)
	if err != nil {
		t.Fatalf("failed to assign restricted role: %v", err)
	}

	body := `{"model":"forbidden-test-model","messages":[{"role":"user","content":"hello"}]}`

	rr := sendChatRequest(handler, body, "permission-denied-test")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403, got %d: %s", rr.Code, rr.Body.String())
	}

	if ollamaCalled.Load() {
		t.Fatal("Ollama should not be called when permission is denied")
	}

	assertChatAuditEvent(
		t,
		db,
		"permission-denied-test",
		"permission_denied",
		"failure",
		http.StatusForbidden,
		"model_not_permitted",
	)
}

func TestChatAuditWriteFailureDoesNotChangeResponse(t *testing.T) {
	wp := queue.NewWorkerPool(1, 10)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama should not be called for invalid JSON")
	}, wp, time.Second)

	// Force audit inserts to fail while leaving the rest of the database usable.
	_, err := db.Exec(`
		CREATE TRIGGER reject_audit_insert
		BEFORE INSERT ON audit_events
		BEGIN
			SELECT RAISE(FAIL, 'simulated audit write failure');
		END;
	`)
	if err != nil {
		t.Fatalf("failed to create audit failure trigger: %v", err)
	}

	requestID := "audit-write-failure-test"
	recorder := sendChatRequest(handler, "{invalid-json", requestID)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf(
			"expected HTTP 400 even when audit write fails, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM audit_events WHERE request_id = ?",
		requestID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query audit events: %v", err)
	}

	if count != 0 {
		t.Fatalf("expected audit insert to fail, but found %d events", count)
	}
}

func TestInvalidRequestIDIsReplacedAcrossResponseLogAndAudit(t *testing.T) {
	var logOutput strings.Builder

	previousLogger := slog.Default()
	slog.SetDefault(
		slog.New(slog.NewTextHandler(&logOutput, nil)),
	)

	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	wp := queue.NewWorkerPool(1, 2)
	wp.Start()
	t.Cleanup(wp.Stop)

	handler, db := setupChatAuditTest(
		t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(
				`{"message":{"role":"assistant","content":"ok"},"done":true}`,
			))
		},
		wp,
		time.Second,
	)

	const rejectedID = "invalid request id SYNTHETIC_REJECTED_MARKER"

	body := `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/chat",
		strings.NewReader(body),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("X-Request-ID", rejectedID)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected HTTP 200, got %d: %s",
			response.Code,
			response.Body.String(),
		)
	}

	// Verify the response contains a newly generated valid ID.
	selectedID := response.Header().Get("X-Request-ID")

	if !middleware.IsValidRequestID(selectedID) ||
		selectedID == rejectedID {
		t.Fatalf(
			"expected a generated valid request ID, got %q",
			selectedID,
		)
	}

	if strings.Contains(response.Body.String(), rejectedID) {
		t.Fatal("rejected request ID leaked into response body")
	}

	// Verify the audit event uses the same generated ID.
	var auditID string

	if err := db.QueryRow(
		`SELECT request_id FROM audit_events ORDER BY id DESC LIMIT 1`,
	).Scan(&auditID); err != nil {
		t.Fatalf("read audit event: %v", err)
	}

	if auditID != selectedID {
		t.Fatalf(
			"audit request ID %q does not match response ID %q",
			auditID,
			selectedID,
		)
	}

	// Verify the application log uses the generated ID.
	logs := logOutput.String()

	if !strings.Contains(logs, "request_id="+selectedID) {
		t.Fatalf(
			"application log does not contain selected request ID %q: %s",
			selectedID,
			logs,
		)
	}

	if strings.Contains(logs, rejectedID) ||
		strings.Contains(auditID, rejectedID) {
		t.Fatal(
			"rejected request ID leaked into application log or audit event",
		)
	}
}

func TestChatRequestBodyLimit(t *testing.T) {
	handler := setupChatTest(
		t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(
				`{"message":{"role":"assistant","content":"ok"},"done":true}`,
			))
		},
	)

	base := `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`

	exact := base + strings.Repeat(
		" ",
		int(ChatRequestBodyLimit)-len(base),
	)

	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/chat",
			strings.NewReader(body),
		)

		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testAPIKey)
		req.Header.Set("X-Request-ID", "chat-body-limit-test")

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		return recorder
	}

	if got := request(exact); got.Code != http.StatusOK {
		t.Fatalf(
			"exact-limit chat body should succeed, got %d: %s",
			got.Code,
			got.Body.String(),
		)
	}

	if got := request(exact + " "); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"over-limit chat body should return 413, got %d: %s",
			got.Code,
			got.Body.String(),
		)
	}
}

func TestTrustedProxyClientIPMatchesApplicationAndSQLiteLogs(t *testing.T) {
	trustedProxies, err := middleware.ParseTrustedProxyCIDRs([]string{"10.20.0.0/16"})
	if err != nil {
		t.Fatalf("parse trusted proxy CIDRs: %v", err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		wantIP     string
	}{
		{
			name:       "trusted proxy resolves forwarded client",
			remoteAddr: "10.20.0.15:443",
			forwarded:  "203.0.113.42",
			wantIP:     "203.0.113.42",
		},
		{
			name:       "untrusted peer cannot forge forwarded client",
			remoteAddr: "198.51.100.17:443",
			forwarded:  "203.0.113.42",
			wantIP:     "198.51.100.17",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logOutput strings.Builder
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logOutput, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			wp := queue.NewWorkerPool(1, 2)
			wp.Start()
			t.Cleanup(wp.Stop)

			handler, db := setupChatAuditTestWithTrustedProxies(
				t,
				func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(
						`{"message":{"role":"assistant","content":"ok"},"done":true}`,
					))
				},
				wp,
				time.Second,
				trustedProxies,
			)

			const requestID = "trusted-proxy-ip-integration"
			body := `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+testAPIKey)
			req.Header.Set("X-Request-ID", requestID)
			req.Header.Set("X-Forwarded-For", tt.forwarded)

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("expected HTTP 200, got %d: %s", response.Code, response.Body.String())
			}

			logs := logOutput.String()
			if !strings.Contains(logs, "ip="+tt.wantIP) {
				t.Fatalf("application log does not contain resolved IP %q: %s", tt.wantIP, logs)
			}

			var loggedIP string
			if err := db.QueryRow(
				"SELECT ip_address FROM request_logs WHERE request_id = ?",
				requestID,
			).Scan(&loggedIP); err != nil {
				t.Fatalf("read SQLite request log: %v", err)
			}
			if loggedIP != tt.wantIP {
				t.Fatalf("SQLite request log IP = %q, want %q", loggedIP, tt.wantIP)
			}
		})
	}
}
