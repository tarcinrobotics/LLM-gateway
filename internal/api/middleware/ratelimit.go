package middleware

import (
	"net/http"
	"sync"
	"time"
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

// Allow checks if a token can be consumed from the bucket.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefilled).Seconds()
	tb.lastRefilled = now

	// Refill rate: capacity / 60 tokens per second (to distribute evenly over 1 minute)
	refillRate := tb.capacity / 60.0
	tb.tokens += elapsed * refillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}

	return false
}

// RateLimiter manages TokenBuckets for different API keys.
type RateLimiter struct {
	buckets map[int64]*TokenBucket
	mu      sync.RWMutex
}

// NewRateLimiter initializes a RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		buckets: make(map[int64]*TokenBucket),
	}
}

// GetBucket retrieves or creates a TokenBucket for a specific API Key.
func (rl *RateLimiter) GetBucket(apiKeyID int64, rateLimitRPM int) *TokenBucket {
	rl.mu.RLock()
	bucket, exists := rl.buckets[apiKeyID]
	rl.mu.RUnlock()

	if exists {
		// If capacity has changed (e.g. dynamic admin updates), update it
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

	// Double check under write lock
	bucket, exists = rl.buckets[apiKeyID]
	if !exists {
		bucket = NewTokenBucket(float64(rateLimitRPM))
		rl.buckets[apiKeyID] = bucket
	}

	return bucket
}

// RateLimitMiddleware enforces rate limiting per API Key.
func RateLimitMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAPIKey(r.Context())
			if apiKey == nil {
				// If there's no authenticated API Key (e.g., public endpoints), skip
				next.ServeHTTP(w, r)
				return
			}

			bucket := rl.GetBucket(apiKey.ID, apiKey.RateLimitRPM)
			if !bucket.Allow() {
				w.Header().Set("Retry-After", "1")
				respondWithError(w, http.StatusTooManyRequests, "Rate limit exceeded (Too Many Requests)")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
