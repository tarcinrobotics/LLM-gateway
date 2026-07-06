package ollama

import "time"

// Message represents a single message in chat context.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents the payload sent to Ollama's /api/chat.
type ChatRequest struct {
	Model    string                 `json:"model"`
	Messages []Message              `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// ChatResponse represents chunks or full responses returned by Ollama's /api/chat.
type ChatResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Message   Message   `json:"message"`
	Done      bool      `json:"done"`

	// Latency and token stats returned when done = true
	TotalDuration      int64 `json:"total_duration"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalCount    int   `json:"prompt_eval_count"` // prompt tokens
	EvalCount          int   `json:"eval_count"`        // completion tokens
}

// ModelInfo contains detail of a single model retrieved from Ollama.
type ModelInfo struct {
	Name       string    `json:"name"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
}

// ModelListResponse represents the list of models returned by Ollama's /api/tags.
type ModelListResponse struct {
	Models []ModelInfo `json:"models"`
}
