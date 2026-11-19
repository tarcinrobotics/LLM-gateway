package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
)

const readinessTimeout = 2 * time.Second

type readinessResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Ollama   string `json:"ollama"`
}

// ReadinessHandler reports whether the gateway can serve requests.
// It checks SQLite, Ollama, and whether the worker pool is shutting down.
func ReadinessHandler(
	db interface {
		PingContext(context.Context) error
	},
	wp *queue.WorkerPool,
	ollamaClient *ollama.Client,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := readinessResponse{
			Status:   "not_ready",
			Database: "unknown",
			Ollama:   "unknown",
		}

		if wp.IsStopping() {
			writeReadinessResponse(w, http.StatusServiceUnavailable, result)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := db.PingContext(ctx); err == nil {
			result.Database = "ready"
		} else {
			result.Database = "unavailable"
		}

		if ctx.Err() == nil {
			if err := ollamaClient.CheckHealth(ctx); err == nil {
				result.Ollama = "ready"
			} else {
				result.Ollama = "unavailable"
			}
		} else {
			result.Ollama = "unavailable"
		}

		if wp.IsStopping() {
			writeReadinessResponse(w, http.StatusServiceUnavailable, result)
			return
		}

		if result.Database == "ready" && result.Ollama == "ready" {
			result.Status = "ready"
			writeReadinessResponse(w, http.StatusOK, result)
			return
		}

		writeReadinessResponse(w, http.StatusServiceUnavailable, result)
	}
}

func writeReadinessResponse(
	w http.ResponseWriter,
	statusCode int,
	result readinessResponse,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(result)
}
