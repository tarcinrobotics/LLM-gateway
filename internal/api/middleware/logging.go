package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"llm_gateway/internal/database"
)

const ModelContextKey contextKey = "model"

// GetModel retrieves the requested model name from the context.
func GetModel(ctx context.Context) string {
	if val := ctx.Value(ModelContextKey); val != nil {
		if model, ok := val.(string); ok {
			return model
		}
	}
	return "unknown"
}

// SetModel sets the requested model name in the context.
func SetModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, ModelContextKey, model)
}

// GenerateRequestID creates a unique request ID.
func GenerateRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if random fails
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405")))
	}
	return hex.EncodeToString(b)
}

// ResponseWriterWrapper wraps http.ResponseWriter to capture status code and bytes written.
type ResponseWriterWrapper struct {
	http.ResponseWriter
	StatusCode int
	Written    int64
	APIKey     interface{} // holds *database.APIKey
	Model      string
}

// WriteHeader captures the status code.
func (rw *ResponseWriterWrapper) WriteHeader(code int) {
	rw.StatusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Write captures the number of bytes written.
func (rw *ResponseWriterWrapper) Write(b []byte) (int, error) {
	if rw.StatusCode == 0 {
		rw.StatusCode = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.Written += int64(n)
	return n, err
}

// Flush implements http.Flusher to support streaming (SSE).
func (rw *ResponseWriterWrapper) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LoggingMiddleware logs each request with structured data.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		// Generate or extract request ID
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = GenerateRequestID()
		}

		// Set header on response
		w.Header().Set("X-Request-ID", reqID)

		// Get client IP address
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip = r.RemoteAddr
		}

		// Inject request ID into context
		ctx := context.WithValue(r.Context(), RequestIDContextKey, reqID)
		r = r.WithContext(ctx)

		// Wrap ResponseWriter
		wrapped := &ResponseWriterWrapper{
			ResponseWriter: w,
			StatusCode:     http.StatusOK, // Default to 200
			Model:          "unknown",
		}

		// Serve the request
		next.ServeHTTP(wrapped, r)

		// Calculate latency
		latency := time.Since(startTime)

		// Retrieve API key ID
		var apiKeyID int64 = 0
		var keyPrefix string = "anonymous"
		
		if wrapped.APIKey != nil {
			if apiKey, ok := wrapped.APIKey.(*database.APIKey); ok {
				apiKeyID = apiKey.ID
				keyPrefix = apiKey.Prefix
			}
		}

		// Log structured message
		slog.Info("HTTP Request",
			slog.String("request_id", reqID),
			slog.String("ip", ip),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", wrapped.StatusCode),
			slog.Int64("latency_ms", latency.Milliseconds()),
			slog.Int64("api_key_id", apiKeyID),
			slog.String("api_key_prefix", keyPrefix),
			slog.String("model", wrapped.Model),
			slog.Int64("bytes_written", wrapped.Written),
		)
	})
}

