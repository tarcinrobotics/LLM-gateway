package middleware

import (
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"llm_gateway/internal/database"
)

// TokenBucket represents a single token bucket for rate limiting.
type TokenBucket struct {
	tokens       float64
	capacity     float64
	lastRefilled time.Time
	mu           sync.Mutex
}

// NewTokenBucket creates a new TokenBucket with the given capacity.
func NewTokenBucket(capacity float64) *TokenBucket {
	return &TokenBucket{
		tokens:       capacity,
		capacity:     capacity,
		lastRefilled: time.Now(),
	}
}

// Allow checks whether a request can consume one token.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefilled).Seconds()
	tb.lastRefilled = now

	refillRate := tb.capacity / 60.0
	tb.tokens += elapsed * refillRate

	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens--
		return true
	}

	return false
}

type RateLimiter struct {
	buckets map[int64]*TokenBucket
	mu      sync.RWMutex
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		buckets: make(map[int64]*TokenBucket),
	}
}

func (rl *RateLimiter) GetBucket(apiKeyID int64, rateLimitRPM int) *TokenBucket {
	rl.mu.RLock()
	bucket, exists := rl.buckets[apiKeyID]
	rl.mu.RUnlock()

	if exists {
		bucket.mu.Lock()
		if bucket.capacity != float64(rateLimitRPM) {
			bucket.capacity = float64(rateLimitRPM)
			if bucket.tokens > bucket.capacity {
				bucket.tokens = bucket.capacity
			}
		}
		bucket.mu.Unlock()
		return bucket
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists = rl.buckets[apiKeyID]
	if !exists {
		bucket = NewTokenBucket(float64(rateLimitRPM))
		rl.buckets[apiKeyID] = bucket
	}

	return bucket
}

// RateLimitMiddleware applies per-API-key rate limiting and audits rejected requests.
func RateLimitMiddleware(db *sql.DB, rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAPIKey(r.Context())
			if apiKey == nil {
				next.ServeHTTP(w, r)
				return
			}

			bucket := rl.GetBucket(apiKey.ID, apiKey.RateLimitRPM)
			if !bucket.Allow() {
				requestID := GetRequestID(r.Context())

				if requestID != "" {
					event := &database.AuditEvent{
						RequestID:  requestID,
						EventType:  "rate_limit_exceeded",
						Outcome:    "failure",
						StatusCode: http.StatusTooManyRequests,
						APIKeyID:   &apiKey.ID,
						ErrorCode:  "rate_limit_exceeded",
					}

					if err := database.WriteAuditEvent(db, event); err != nil {
						slog.Error(
							"Failed to write rate limit audit event",
							slog.String("request_id", requestID),
							slog.String("error", err.Error()),
						)
					}
				} else {
					slog.Warn(
						"Cannot audit rate-limited request because request ID is missing",
						slog.Int64("api_key_id", apiKey.ID),
					)
				}

				w.Header().Set("Retry-After", "1")
				respondWithError(
					w,
					http.StatusTooManyRequests,
					"Rate limit exceeded (Too Many Requests)",
				)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
