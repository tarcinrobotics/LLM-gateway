package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"llm_gateway/internal/database"
)

const (
	ModelContextKey    contextKey = "model"
	RequestIDHeader               = "X-Request-ID"
	MaxRequestIDLength            = 128
)

var fallbackRequestIDCounter atomic.Uint64

// IsValidRequestID checks whether an ID follows the allowed format.
// Allowed: ASCII letters, digits, hyphens and underscores.
// Maximum length: 128 characters.
func IsValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > MaxRequestIDLength {
		return false
	}

	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_') {
			return false
		}
	}

	return true
}

// GetModel retrieves the requested model name from context.
func GetModel(ctx context.Context) string {
	if val := ctx.Value(ModelContextKey); val != nil {
		if model, ok := val.(string); ok {
			return model
		}
	}
	return "unknown"
}

// SetModel sets the requested model name in context.
func SetModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, ModelContextKey, model)
}

// GenerateRequestID creates a unique request ID.
func GenerateRequestID() string {
	b := make([]byte, 12)

	if _, err := rand.Read(b); err != nil {
		// Keep the fallback unique across concurrent calls.
		counter := fallbackRequestIDCounter.Add(1)
		return fmt.Sprintf(
			"fallback-%x-%x",
			time.Now().UnixNano(),
			counter,
		)
	}

	return hex.EncodeToString(b)
}

// ResponseWriterWrapper captures status code and bytes written.
type ResponseWriterWrapper struct {
	http.ResponseWriter
	StatusCode    int
	Written       int64
	APIKey        interface{}
	Model         string
	headerWritten bool
}

func (rw *ResponseWriterWrapper) WriteHeader(code int) {
	if rw.headerWritten {
		return
	}

	rw.headerWritten = true
	rw.StatusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *ResponseWriterWrapper) Write(b []byte) (int, error) {
	if !rw.headerWritten {
		rw.headerWritten = true
		rw.StatusCode = http.StatusOK
	}

	n, err := rw.ResponseWriter.Write(b)
	rw.Written += int64(n)
	return n, err
}

// Flush supports streaming responses (SSE).
func (rw *ResponseWriterWrapper) Flush() {
	if !rw.headerWritten {
		rw.headerWritten = true
		rw.StatusCode = http.StatusOK
	}

	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LoggingMiddleware validates or generates a request ID before
// passing the request to downstream middleware and handlers.
func LoggingMiddleware(next http.Handler) http.Handler {
	return LoggingMiddlewareWithTrustedProxies(next, nil)
}

// LoggingMiddlewareWithTrustedProxies resolves the client address once at the
// request boundary so application and downstream request logs share one value.
func LoggingMiddlewareWithTrustedProxies(
	next http.Handler,
	trusted []netip.Prefix,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		// Accept exactly one safe client-provided value.
		// Rejected values are never propagated.
		values := r.Header.Values(RequestIDHeader)

		var reqID string

		if len(values) == 1 && IsValidRequestID(values[0]) {
			reqID = values[0]
		} else {
			reqID = GenerateRequestID()
		}

		// Return the selected ID to the client.
		w.Header().Set(RequestIDHeader, reqID)

		ip := ResolveClientIP(
			r.RemoteAddr,
			r.Header.Values("X-Forwarded-For"),
			trusted,
		)
		r = withClientIP(r, ip)

		// Store the same selected ID in request context.
		ctx := context.WithValue(
			r.Context(),
			RequestIDContextKey,
			reqID,
		)
		r = r.WithContext(ctx)

		wrapped := &ResponseWriterWrapper{
			ResponseWriter: w,
			StatusCode:     http.StatusOK,
			Model:          "unknown",
		}

		next.ServeHTTP(wrapped, r)

		latency := time.Since(startTime)

		var apiKeyID int64
		keyPrefix := "anonymous"

		if wrapped.APIKey != nil {
			if apiKey, ok := wrapped.APIKey.(*database.APIKey); ok {
				apiKeyID = apiKey.ID
				keyPrefix = apiKey.Prefix
			}
		}

		// Log only the validated or generated request ID.
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
