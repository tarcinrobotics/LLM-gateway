package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"llm_gateway/internal/database"
)

func newRateLimitTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "rate_limit_test.db")
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize test database: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func newRateLimitTestRequest(apiKey *database.APIKey, requestID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)

	ctx := context.WithValue(req.Context(), RequestIDContextKey, requestID)
	if apiKey != nil {
		ctx = context.WithValue(ctx, APIKeyContextKey, apiKey)
	}

	return req.WithContext(ctx)
}

func TestRateLimitMiddlewareAuditsRejectedRequest(t *testing.T) {
	db := newRateLimitTestDB(t)

	apiKey := &database.APIKey{
		ID:           123,
		RateLimitRPM: 1,
	}

	limiter := NewRateLimiter()
	middleware := RateLimitMiddleware(db, limiter)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := middleware(nextHandler)

	// The first request should be allowed.
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		firstResponse,
		newRateLimitTestRequest(apiKey, "request-allowed"),
	)

	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf(
			"expected first request status %d, got %d",
			http.StatusNoContent,
			firstResponse.Code,
		)
	}

	// The second request should exceed the one-request bucket capacity.
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		secondResponse,
		newRateLimitTestRequest(apiKey, "request-rejected"),
	)

	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf(
			"expected second request status %d, got %d",
			http.StatusTooManyRequests,
			secondResponse.Code,
		)
	}

	if got := secondResponse.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After header to be 1, got %q", got)
	}

	var (
		requestID  string
		eventType  string
		outcome    string
		statusCode int
		apiKeyID   sql.NullInt64
		errorCode  string
	)

	err := db.QueryRow(`
		SELECT request_id, event_type, outcome, status_code, api_key_id, error_code
		FROM audit_events
		WHERE request_id = ?
	`, "request-rejected").Scan(
		&requestID,
		&eventType,
		&outcome,
		&statusCode,
		&apiKeyID,
		&errorCode,
	)
	if err != nil {
		t.Fatalf("failed to retrieve rate-limit audit event: %v", err)
	}

	if requestID != "request-rejected" {
		t.Errorf("unexpected request ID: %q", requestID)
	}
	if eventType != "rate_limit_exceeded" {
		t.Errorf("unexpected event type: %q", eventType)
	}
	if outcome != "failure" {
		t.Errorf("unexpected outcome: %q", outcome)
	}
	if statusCode != http.StatusTooManyRequests {
		t.Errorf("unexpected status code: %d", statusCode)
	}
	if !apiKeyID.Valid || apiKeyID.Int64 != apiKey.ID {
		t.Errorf("unexpected API key ID: %+v", apiKeyID)
	}
	if errorCode != "rate_limit_exceeded" {
		t.Errorf("unexpected error code: %q", errorCode)
	}
}

func TestRateLimitMiddlewareAllowsRequestWithoutAPIKey(t *testing.T) {
	db := newRateLimitTestDB(t)

	limiter := NewRateLimiter()
	middleware := RateLimitMiddleware(db, limiter)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(
		response,
		newRateLimitTestRequest(nil, "anonymous-request"),
	)

	if !nextCalled {
		t.Fatal("expected request without API key to reach the next handler")
	}

	if response.Code != http.StatusNoContent {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNoContent,
			response.Code,
		)
	}

	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM audit_events WHERE request_id = ?",
		"anonymous-request",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count audit events: %v", err)
	}

	if count != 0 {
		t.Errorf("expected no rate-limit audit event, got %d", count)
	}
}

func TestRateLimitMiddlewareReturns429WhenAuditWriteFails(t *testing.T) {
	db := newRateLimitTestDB(t)

	apiKey := &database.APIKey{
		ID:           123,
		RateLimitRPM: 1,
	}

	limiter := NewRateLimiter()
	middleware := RateLimitMiddleware(db, limiter)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := middleware(nextHandler)

	// Consume the only available token.
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		firstResponse,
		newRateLimitTestRequest(apiKey, "request-first"),
	)

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close test database: %v", err)
	}

	// The audit write will fail because the database is closed.
	// The client must still receive the normal rate-limit response.
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		secondResponse,
		newRateLimitTestRequest(apiKey, "request-second"),
	)

	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf(
			"expected status %d despite audit failure, got %d",
			http.StatusTooManyRequests,
			secondResponse.Code,
		)
	}
}
