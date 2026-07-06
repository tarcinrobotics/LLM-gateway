package ollama

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// WriteSSEChunk formats a data object as JSON and writes it in Server-Sent Events format.
func WriteSSEChunk(w http.ResponseWriter, data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE payload: %w", err)
	}

	// SSE chunk format
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	if err != nil {
		return fmt.Errorf("failed to write SSE data: %w", err)
	}

	// Flush immediately
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	return nil
}
