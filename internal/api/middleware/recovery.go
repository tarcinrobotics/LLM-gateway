package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoveryMiddleware recovers from panics and returns a 500 Internal Server Error.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				reqID := GetRequestID(r.Context())
				slog.Error("Panic recovered",
					slog.Any("error", err),
					slog.String("request_id", reqID),
					slog.String("stack", string(debug.Stack())),
				)

				respondWithError(w, http.StatusInternalServerError, "Internal Server Error (Panic Recovered)")
			}
		}()

		next.ServeHTTP(w, r)
	})
}
