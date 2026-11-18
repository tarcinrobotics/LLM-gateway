package middleware

import (
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
