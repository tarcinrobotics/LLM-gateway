package auth

import (
	"net/http"
	"strings"
)

// ExtractAPIKey extracts the API key from the Authorization header or X-API-Key header.
func ExtractAPIKey(r *http.Request) string {
	// Check Authorization header first
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return parts[1]
		}
		// Allow simple Authorization: <key>
		return authHeader
	}

	// Fallback to X-API-Key header
	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "" {
		return apiKey
	}

	return ""
}
