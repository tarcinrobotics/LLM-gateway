package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"os"

	"llm_gateway/internal/auth"
	"llm_gateway/internal/database"
)

// AdminBasicAuthMiddleware protects admin routes using HTTP Basic Auth against .env credentials.
func AdminBasicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		expectedUser := os.Getenv("ADMIN_USER")
		expectedPass := os.Getenv("ADMIN_PASS")

		if expectedUser == "" || expectedPass == "" {
			http.Error(w, "Admin portal credentials not configured on server", http.StatusInternalServerError)
			return
		}

		if !ok || user != expectedUser || pass != expectedPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
			http.Error(w, "Unauthorized access to admin portal", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type contextKey string

const (
	APIKeyContextKey   contextKey = "api_key"
	RequestIDContextKey contextKey = "request_id"
)

// GetAPIKey retrieves the authenticated APIKey from the context.
func GetAPIKey(ctx context.Context) *database.APIKey {
	if val := ctx.Value(APIKeyContextKey); val != nil {
		if key, ok := val.(*database.APIKey); ok {
			return key
		}
	}
	return nil
}

// GetRequestID retrieves the request ID from the context.
func GetRequestID(ctx context.Context) string {
	if val := ctx.Value(RequestIDContextKey); val != nil {
		if id, ok := val.(string); ok {
			return id
		}
	}
	return ""
}

// AuthMiddleware creates a middleware that authenticates the request using the SQLite database.
func AuthMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plainKey := auth.ExtractAPIKey(r)
			if plainKey == "" {
				respondWithError(w, http.StatusUnauthorized, "Missing API Key")
				return
			}

			// Hash and lookup
			hash := database.HashKey(plainKey)
			apiKey, err := database.GetAPIKeyByHash(db, hash)
			if err != nil {
				respondWithError(w, http.StatusInternalServerError, "Database error during authentication")
				return
			}

			if apiKey == nil {
				respondWithError(w, http.StatusUnauthorized, "Invalid API Key")
				return
			}

			if !apiKey.IsActive {
				respondWithError(w, http.StatusForbidden, "API Key has been deactivated")
				return
			}

			if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
				respondWithError(w, http.StatusForbidden, "API Key has expired")
				return
			}

			// Store API key in context
			ctx := context.WithValue(r.Context(), APIKeyContextKey, apiKey)

			// Store in ResponseWriterWrapper for logging middleware
			if wrapped, ok := w.(*ResponseWriterWrapper); ok {
				wrapped.APIKey = apiKey
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func respondWithError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
