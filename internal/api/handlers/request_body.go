package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	// Maximum encoded JSON body accepted by chat.
	ChatRequestBodyLimit int64 = 1 << 20 // 1 MiB

	// Maximum encoded JSON body accepted when creating an API key.
	CreateAPIKeyRequestBodyLimit int64 = 16 << 10 // 16 KiB
)

var errRequestBodyTooLarge = errors.New("request body too large")

// decodeLimitedJSON reads at most limit+1 bytes before decoding.
// The extra byte detects oversized bodies even when Content-Length
// is absent or the JSON value ends before oversized trailing whitespace.
func decodeLimitedJSON(r *http.Request, limit int64, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}

	if int64(len(body)) > limit {
		return errRequestBodyTooLarge
	}

	return json.NewDecoder(bytes.NewReader(body)).Decode(dst)
}

// respondPayloadTooLarge returns a consistent HTTP 413 response.
func respondPayloadTooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)

	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "Request body too large",
	})
}
