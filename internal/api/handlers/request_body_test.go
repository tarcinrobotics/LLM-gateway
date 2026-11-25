package handlers

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeLimitedJSONBoundaries(t *testing.T) {
	const limit = int64(32)
	valid := `{"ok":true}`

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name: "below limit",
			body: valid,
		},
		{
			name: "exact limit",
			body: valid + strings.Repeat(" ", int(limit)-len(valid)),
		},
		{
			name:    "above limit",
			body:    valid + strings.Repeat(" ", int(limit)-len(valid)+1),
			wantErr: errRequestBodyTooLarge,
		},
		{
			name:    "malformed within limit",
			body:    `{"ok":`,
			wantErr: errors.New("malformed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				"POST",
				"/",
				strings.NewReader(tt.body),
			)

			// Exercise requests without a declared Content-Length.
			req.ContentLength = -1

			var decoded struct {
				OK bool `json:"ok"`
			}

			err := decodeLimitedJSON(req, limit, &decoded)

			if tt.wantErr == errRequestBodyTooLarge &&
				!errors.Is(err, errRequestBodyTooLarge) {
				t.Fatalf("expected oversized-body error, got %v", err)
			}

			if tt.wantErr != nil &&
				tt.wantErr != errRequestBodyTooLarge &&
				err == nil {
				t.Fatal("expected malformed JSON error")
			}

			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantErr == nil && !decoded.OK {
				t.Fatal("expected decoded JSON value")
			}
		})
	}
}

func TestRespondPayloadTooLarge(t *testing.T) {
	recorder := httptest.NewRecorder()

	respondPayloadTooLarge(recorder)

	if recorder.Code != 413 {
		t.Fatalf("expected 413, got %d", recorder.Code)
	}

	if !strings.Contains(
		recorder.Body.String(),
		"Request body too large",
	) {
		t.Fatalf(
			"expected clear error message, got %q",
			recorder.Body.String(),
		)
	}
}
