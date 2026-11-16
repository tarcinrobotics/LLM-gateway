package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testOrigin = "http://localhost:5173"

func TestCORSAllowedOrigin(t *testing.T) {
	handler := CORSMiddleware([]string{testOrigin})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Accept-Encoding")
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", testOrigin)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("expected allowed origin %q, got %q", testOrigin, got)
	}

	varyValues := strings.Join(rec.Header().Values("Vary"), ",")
	if !strings.Contains(varyValues, "Origin") {
		t.Error("expected Vary header to contain Origin")
	}
	if !strings.Contains(varyValues, "Accept-Encoding") {
		t.Error("expected existing Vary header Accept-Encoding to be preserved")
	}

	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials must not be enabled")
	}
}

func TestCORSDisallowedAndSimilarOrigins(t *testing.T) {
	tests := []string{
		"http://evil.com",
		"http://localhost:51730",
		"https://localhost:5173",
		"http://localhost.evil.com:5173",
	}

	for _, origin := range tests {
		t.Run(origin, func(t *testing.T) {
			called := false

			handler := CORSMiddleware([]string{testOrigin})(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					w.WriteHeader(http.StatusOK)
				}),
			)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			req.Header.Set("Origin", origin)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Error("disallowed origin received CORS permission")
			}

			if !called {
				t.Error("actual request should continue to the application")
			}
		})
	}
}

func TestCORSAllowedPreflight(t *testing.T) {
	handler := CORSMiddleware([]string{testOrigin})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("valid preflight should not reach the application")
		}),
	)

	req := httptest.NewRequest(http.MethodOptions, "/chat", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, X-API-Key")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Error("expected allowed origin header")
	}

	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected allowed methods header")
	}

	if rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("expected allowed headers header")
	}
}

func TestCORSRejectsInvalidPreflight(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		method string
		header string
	}{
		{
			name:   "disallowed origin",
			origin: "http://evil.com",
			method: "POST",
		},
		{
			name:   "unsupported method",
			origin: testOrigin,
			method: "PUT",
		},
		{
			name:   "unsupported header",
			origin: testOrigin,
			method: "POST",
			header: "X-Unknown-Header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CORSMiddleware([]string{testOrigin})(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("invalid preflight should not reach the application")
				}),
			)

			req := httptest.NewRequest(http.MethodOptions, "/chat", nil)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", tt.method)

			if tt.header != "" {
				req.Header.Set("Access-Control-Request-Headers", tt.header)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("expected status 403, got %d", rec.Code)
			}

			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Error("invalid preflight received CORS permission")
			}
		})
	}
}

func TestCORSNoOrigin(t *testing.T) {
	called := false

	handler := CORSMiddleware([]string{})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("request without Origin should reach the application")
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("request without Origin should not receive CORS headers")
	}
}

func TestCORSEmptyAllowlist(t *testing.T) {
	handler := CORSMiddleware([]string{})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", testOrigin)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("empty allowlist must not grant CORS permission")
	}
}

func TestCORSDoesNotBypassAPIKeyAuthentication(t *testing.T) {
	_, plainKey, db := createMiddlewareTestDB(t)

	protectedHandler := AuthMiddleware(db)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	handler := CORSMiddleware([]string{testOrigin})(protectedHandler)

	t.Run("allowed origin without API key is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
		req.Header.Set("Origin", testOrigin)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
		}

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
			t.Errorf("expected CORS permission for %q, got %q", testOrigin, got)
		}
	})

	t.Run("allowed origin with valid API key succeeds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Authorization", "Bearer "+plainKey)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", rec.Code)
		}
	})
}
