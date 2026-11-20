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

// createMiddlewareTestDB creates an isolated temporary database
// and inserts a randomly generated API key.
func createMiddlewareTestDB(t *testing.T) (*database.APIKey, string, *sql.DB) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "auth_test.db")

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

	plainKey, err := database.GenerateRandomKey()
	if err != nil {
		t.Fatalf("failed to generate test API key: %v", err)
	}

	apiKey, err := database.CreateAPIKey(db, plainKey, 1, 2, 60)
	if err != nil {
		t.Fatalf("failed to create test API key: %v", err)
	}

	return apiKey, plainKey, db
}

func TestAuthMiddlewareRejectsQueryOnlyAPIKey(t *testing.T) {
	_, plainKey, db := createMiddlewareTestDB(t)

	request := httptest.NewRequest(
		http.MethodGet,
		"/test?api_key="+plainKey,
		nil,
	)
	recorder := httptest.NewRecorder()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	AuthMiddleware(db)(next).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", recorder.Code)
	}

	if nextCalled {
		t.Error("next handler should not be called for query-only API key")
	}
}

func TestAuthMiddlewareAcceptsValidHeaderAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		setHeader func(*http.Request, string)
	}{
		{
			name: "Bearer Authorization",
			setHeader: func(r *http.Request, key string) {
				r.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name: "Direct Authorization",
			setHeader: func(r *http.Request, key string) {
				r.Header.Set("Authorization", key)
			},
		},
		{
			name: "X-API-Key",
			setHeader: func(r *http.Request, key string) {
				r.Header.Set("X-API-Key", key)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, plainKey, db := createMiddlewareTestDB(t)

			request := httptest.NewRequest(
				http.MethodGet,
				"/test?api_key=ignored-query-key",
				nil,
			)
			tt.setHeader(request, plainKey)

			recorder := httptest.NewRecorder()
			nextCalled := false

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true

				apiKey := GetAPIKey(r.Context())
				if apiKey == nil {
					t.Error("expected API key in request context")
				}
			})

			AuthMiddleware(db)(next).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", recorder.Code)
			}

			if !nextCalled {
				t.Error("next handler was not called for valid header API key")
			}
		})
	}
}

func assertAuthenticationAuditEvent(
	t *testing.T,
	db *sql.DB,
	requestID string,
	wantStatus int,
	wantErrorCode string,
	wantAPIKeyID int64,
) {
	t.Helper()

	var eventType, outcome, errorCode string
	var statusCode int
	var apiKeyID sql.NullInt64

	err := db.QueryRow(`
		SELECT event_type, outcome, status_code, api_key_id, error_code
		FROM audit_events
		WHERE request_id = ?
	`, requestID).Scan(
		&eventType,
		&outcome,
		&statusCode,
		&apiKeyID,
		&errorCode,
	)
	if err != nil {
		t.Fatalf("failed to read authentication audit event: %v", err)
	}

	if eventType != "authentication_failed" {
		t.Errorf("expected event type authentication_failed, got %q", eventType)
	}
	if outcome != "failure" {
		t.Errorf("expected outcome failure, got %q", outcome)
	}
	if statusCode != wantStatus {
		t.Errorf("expected status %d, got %d", wantStatus, statusCode)
	}
	if errorCode != wantErrorCode {
		t.Errorf("expected error code %q, got %q", wantErrorCode, errorCode)
	}

	if wantAPIKeyID == 0 {
		if apiKeyID.Valid {
			t.Errorf("expected no API key ID, got %d", apiKeyID.Int64)
		}
	} else if !apiKeyID.Valid || apiKeyID.Int64 != wantAPIKeyID {
		t.Errorf("expected API key ID %d, got %+v", wantAPIKeyID, apiKeyID)
	}
}

func TestAuthMiddlewareAuditsMissingAndInvalidKeys(t *testing.T) {
	tests := []struct {
		name          string
		apiKey        string
		wantErrorCode string
	}{
		{
			name:          "missing API key",
			apiKey:        "",
			wantErrorCode: "missing_api_key",
		},
		{
			name:          "invalid API key",
			apiKey:        "invalid-test-key",
			wantErrorCode: "invalid_api_key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, db := createMiddlewareTestDB(t)

			const requestID = "auth-failure-test"

			request := httptest.NewRequest(http.MethodGet, "/test", nil)
			request = request.WithContext(context.WithValue(
				request.Context(),
				RequestIDContextKey,
				requestID,
			))

			if tt.apiKey != "" {
				request.Header.Set("Authorization", "Bearer "+tt.apiKey)
			}

			recorder := httptest.NewRecorder()
			nextCalled := false

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
			})

			AuthMiddleware(db)(next).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d", recorder.Code)
			}
			if nextCalled {
				t.Fatal("next handler should not be called")
			}

			assertAuthenticationAuditEvent(
				t,
				db,
				requestID,
				http.StatusUnauthorized,
				tt.wantErrorCode,
				0,
			)
		})
	}
}

func TestAuthMiddlewareAuditsInactiveKey(t *testing.T) {
	apiKey, plainKey, db := createMiddlewareTestDB(t)

	_, err := db.Exec(
		"UPDATE api_keys SET is_active = 0 WHERE id = ?",
		apiKey.ID,
	)
	if err != nil {
		t.Fatalf("failed to deactivate test API key: %v", err)
	}

	const requestID = "inactive-key-audit-test"

	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	request.Header.Set("Authorization", "Bearer "+plainKey)
	request = request.WithContext(context.WithValue(
		request.Context(),
		RequestIDContextKey,
		requestID,
	))

	recorder := httptest.NewRecorder()
	AuthMiddleware(db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for an inactive key")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", recorder.Code)
	}

	assertAuthenticationAuditEvent(
		t,
		db,
		requestID,
		http.StatusForbidden,
		"inactive_api_key",
		apiKey.ID,
	)
}

func TestAuthMiddlewareAuditsExpiredKey(t *testing.T) {
	apiKey, plainKey, db := createMiddlewareTestDB(t)

	_, err := db.Exec(
		"UPDATE api_keys SET expires_at = datetime('now', '-1 hour') WHERE id = ?",
		apiKey.ID,
	)
	if err != nil {
		t.Fatalf("failed to expire test API key: %v", err)
	}

	const requestID = "expired-key-audit-test"

	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	request.Header.Set("Authorization", "Bearer "+plainKey)
	request = request.WithContext(context.WithValue(
		request.Context(),
		RequestIDContextKey,
		requestID,
	))

	recorder := httptest.NewRecorder()
	AuthMiddleware(db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for an expired key")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", recorder.Code)
	}

	assertAuthenticationAuditEvent(
		t,
		db,
		requestID,
		http.StatusForbidden,
		"expired_api_key",
		apiKey.ID,
	)
}
