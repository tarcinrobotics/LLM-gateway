package middleware

import (
	"net/http"
	"strings"
)

var allowedMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodDelete: true,
}

var allowedHeaders = map[string]bool{
	"authorization": true,
	"content-type":  true,
	"x-api-key":     true,
}

// CORSMiddleware configures CORS for explicitly trusted browser origins.
func CORSMiddleware(origins []string) func(http.Handler) http.Handler {
	allowedOrigins := make(map[string]bool)

	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin != "" && origin != "*" {
			allowedOrigins[origin] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Requests without an Origin header are not subject to CORS.
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Preserve cache behavior for origin-dependent responses.
			addVaryHeader(w.Header(), "Origin")

			// Disallowed origins receive no CORS permission.
			if !allowedOrigins[origin] {
				if r.Method == http.MethodOptions &&
					r.Header.Get("Access-Control-Request-Method") != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				next.ServeHTTP(w, r)
				return
			}

			// Handle browser preflight requests.
			if r.Method == http.MethodOptions {
				requestedMethod := strings.TrimSpace(
					r.Header.Get("Access-Control-Request-Method"),
				)

				// Ordinary OPTIONS requests continue normally.
				if requestedMethod == "" {
					next.ServeHTTP(w, r)
					return
				}

				addVaryHeader(w.Header(), "Access-Control-Request-Method")
				addVaryHeader(w.Header(), "Access-Control-Request-Headers")

				if !allowedMethods[requestedMethod] {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				requestedHeaders := strings.Split(
					r.Header.Get("Access-Control-Request-Headers"), ",",
				)

				for _, header := range requestedHeaders {
					header = strings.ToLower(strings.TrimSpace(header))

					if header != "" && !allowedHeaders[header] {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}

				w.Header().Set("Access-Control-Allow-Origin", origin)

				w.Header().Set(
					"Access-Control-Allow-Methods",
					"GET, POST, DELETE",
				)
				w.Header().Set(
					"Access-Control-Allow-Headers",
					"Authorization, Content-Type, X-API-Key",
				)

				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Add CORS permissions to allowed actual requests.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set(
				"Access-Control-Allow-Methods",
				"GET, POST, DELETE",
			)
			w.Header().Set(
				"Access-Control-Allow-Headers",
				"Authorization, Content-Type, X-API-Key",
			)

			next.ServeHTTP(w, r)
		})
	}
}

// addVaryHeader appends a Vary value without removing existing values.
func addVaryHeader(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, item := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
