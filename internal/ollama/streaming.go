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

	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	if err != nil {
		return fmt.Errorf("failed to write SSE data: %w", err)
	}

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	return nil
}

// WriteSSEErrorEvent sends a safe error event to an active SSE client.
func WriteSSEErrorEvent(w http.ResponseWriter, message string) error {
	payload, err := json.Marshal(map[string]string{
		"error": message,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal SSE error: %w", err)
	}

	_, err = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	if err != nil {
		return fmt.Errorf("failed to write SSE error event: %w", err)
	}

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	return nil
}
