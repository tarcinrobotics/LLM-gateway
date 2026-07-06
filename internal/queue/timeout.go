package queue

import (
	"context"
	"time"
)

// WithRequestTimeout derives a context with a timeout for worker execution.
func WithRequestTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}
