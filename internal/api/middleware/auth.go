package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"llm_gateway/internal/auth"
	"llm_gateway/internal/database"
)

// AdminBasicAuthMiddleware protects admin routes using HTTP Basic Auth
// against .env credentials.
func AdminBasicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		expectedUser := os.Getenv("ADMIN_USER")
		expectedPass := os.Getenv("ADMIN_PASS")

		if expectedUser == "" || expectedPass == "" {
			http.Error(
				w,
				"Admin portal credentials not configured on server",
				http.StatusInternalServerError,
			)
			return
		}

		if !ok || user != expectedUser || pass != expectedPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
			http.Error(
				w,
				"Unauthorized access to admin portal",
				http.StatusUnauthorized,
			)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type contextKey string

const (
	APIKeyContextKey    contextKey = "api_key"
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

// AuthMiddleware creates a middleware that authenticates requests using SQLite.
func AuthMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plainKey := auth.ExtractAPIKey(r)

			if plainKey == "" {
				writeAuthenticationAudit(
					db,
					r,
					"authentication_failed",
					http.StatusUnauthorized,
					nil,
					"missing_api_key",
				)

				respondWithError(w, http.StatusUnauthorized, "Missing API Key")
				return
			}

			// Hash the supplied key before looking it up.
			hash := database.HashKey(plainKey)
			apiKey, err := database.GetAPIKeyByHash(db, hash)

			if err != nil {
				writeAuthenticationAudit(
					db,
					r,
					"authentication_check_failed",
					http.StatusInternalServerError,
					nil,
					"authentication_database_error",
				)

				respondWithError(
					w,
					http.StatusInternalServerError,
					"Database error during authentication",
				)
				return
			}

			if apiKey == nil {
				writeAuthenticationAudit(
					db,
					r,
					"authentication_failed",
					http.StatusUnauthorized,
					nil,
					"invalid_api_key",
				)

				respondWithError(w, http.StatusUnauthorized, "Invalid API Key")
				return
			}

			// The key exists, so its ID is known even if it is inactive or expired.
			// Preserve this metadata for the HTTP logging middleware.
			if wrapped, ok := w.(*ResponseWriterWrapper); ok {
				wrapped.APIKey = apiKey
			}

			if !apiKey.IsActive {
				writeAuthenticationAudit(
					db,
					r,
					"authentication_failed",
					http.StatusForbidden,
					&apiKey.ID,
					"inactive_api_key",
				)

				respondWithError(
					w,
					http.StatusForbidden,
					"API Key has been deactivated",
				)
				return
			}

			if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
				writeAuthenticationAudit(
					db,
					r,
					"authentication_failed",
					http.StatusForbidden,
					&apiKey.ID,
					"expired_api_key",
				)

				respondWithError(
					w,
					http.StatusForbidden,
					"API Key has expired",
				)
				return
			}

			// Store the authenticated API key in the request context.
			ctx := context.WithValue(r.Context(), APIKeyContextKey, apiKey)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthenticationAudit records an authentication outcome.
// It never stores the supplied API key or any raw credential.
func writeAuthenticationAudit(
	db *sql.DB,
	r *http.Request,
	eventType string,
	statusCode int,
	apiKeyID *int64,
	errorCode string,
) {
	requestID := GetRequestID(r.Context())

	if requestID == "" {
		slog.Warn("Authentication audit skipped: request ID unavailable")
		return
	}

	event := &database.AuditEvent{
		RequestID:  requestID,
		EventType:  eventType,
		Outcome:    "failure",
		StatusCode: statusCode,
		APIKeyID:   apiKeyID,
		ErrorCode:  errorCode,
	}

	if err := database.WriteAuditEvent(db, event); err != nil {
		// Do not expose audit storage errors or credentials to the client.
		slog.Error(
			"Failed to write authentication audit event",
			slog.String("request_id", requestID),
			slog.String("event_type", eventType),
			slog.String("error", err.Error()),
		)
	}
}

func respondWithError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
