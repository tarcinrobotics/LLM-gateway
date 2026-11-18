
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		apiKey     string
		query      string
		want       string
	}{
		{
			name:       "Bearer Authorization",
			authHeader: "Bearer test-bearer-key",
			want:       "test-bearer-key",
		},
		{
			name:       "Direct Authorization",
			authHeader: "test-direct-key",
			want:       "test-direct-key",
		},
		{
			name:   "X-API-Key header",
			apiKey: "test-x-api-key",
			want:   "test-x-api-key",
		},
		{
			name:       "Authorization takes precedence over X-API-Key",
			authHeader: "Bearer authorization-key",
			apiKey:     "x-api-key",
			want:       "authorization-key",
		},
		{
			name:  "Query-only key is rejected",
			query: "test-query-key",
			want:  "",
		},
		{
			name:       "Header takes precedence over query parameter",
			authHeader: "Bearer header-key",
			query:      "query-key",
			want:       "header-key",
		},
		{
			name:   "X-API-Key takes precedence over query parameter",
			apiKey: "header-key",
			query:  "query-key",
			want:   "header-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/test",
				nil,
			)

			if tt.query != "" {
				query := req.URL.Query()
				query.Set("api_key", tt.query)
				req.URL.RawQuery = query.Encode()
			}

			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}

			got := ExtractAPIKey(req)

			if got != tt.want {
				t.Errorf("ExtractAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
