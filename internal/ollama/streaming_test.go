package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteSSEErrorEvent(t *testing.T) {
	recorder := httptest.NewRecorder()

	err := WriteSSEErrorEvent(recorder, "Generation failed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := recorder.Body.String()

	if !strings.HasPrefix(body, "event: error\n") {
		t.Fatalf("expected SSE error event, got: %q", body)
	}

	const prefix = "data: "
	start := strings.Index(body, prefix)
	if start == -1 {
		t.Fatalf("missing SSE data field: %q", body)
	}

	payload := strings.TrimSpace(body[start+len(prefix):])

	var result map[string]string
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	if result["error"] != "Generation failed" {
		t.Fatalf("unexpected error message: %q", result["error"])
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", recorder.Code)
	}
}
