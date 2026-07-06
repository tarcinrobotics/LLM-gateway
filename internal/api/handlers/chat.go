package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"llm_gateway/internal/api/middleware"
	"llm_gateway/internal/auth"
	"llm_gateway/internal/database"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
)

// ChatHandler processes incoming LLM chat requests.
func ChatHandler(db *sql.DB, wp *queue.WorkerPool, client *ollama.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		reqID := middleware.GetRequestID(r.Context())
		apiKey := middleware.GetAPIKey(r.Context())

		// Extract client IP
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip = r.RemoteAddr
		}

		if apiKey == nil {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}

		// Decode the request body
		var chatReq ollama.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
			respondJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		if chatReq.Model == "" {
			respondJSONError(w, http.StatusBadRequest, "model is a required field")
			return
		}

		// Update context with the requested model name so logging middleware can output it
		r = r.WithContext(middleware.SetModel(r.Context(), chatReq.Model))
		if wrapped, ok := w.(*middleware.ResponseWriterWrapper); ok {
			wrapped.Model = chatReq.Model
		}

		// Check RBAC permission (Model allow-list)
		allowed, err := auth.IsModelAllowed(db, apiKey.RoleID, chatReq.Model)
		if err != nil {
			slog.Error("Failed to check permissions", slog.String("request_id", reqID), slog.Any("error", err))
			respondJSONError(w, http.StatusInternalServerError, "Failed to verify access permissions")
			return
		}

		if !allowed {
			slog.Warn("Model access forbidden", slog.String("request_id", reqID), slog.Int64("role_id", apiKey.RoleID), slog.String("model", chatReq.Model))
			respondJSONError(w, http.StatusForbidden, "Forbidden: Access to model '"+chatReq.Model+"' is not allowed for your role")
			return
		}

		// Set context timeout (default 30 seconds)
		execCtx, cancel := queue.WithRequestTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Schedule job into the worker pool
		jobRes, err := queue.ScheduleJob(execCtx, wp, client, &chatReq)
		if err != nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())
			
			// Check if queue full or timeout
			if errors.Is(err, context.DeadlineExceeded) {
				slog.Error("Queue wait timeout (30s exceeded)", slog.String("request_id", reqID))
				respondJSONError(w, http.StatusGatewayTimeout, "Gateway Timeout: Request spent too long in queue")
				return
			}
			
			slog.Error("Queue submission rejected", slog.String("request_id", reqID), slog.Any("error", err))
			respondJSONError(w, http.StatusServiceUnavailable, "Service Unavailable: Worker queue is full")
			return
		}

		// Handle streaming response
		if chatReq.Stream {
			if jobRes.ChunkChan == nil {
				metrics.RecordRequest(false, time.Since(startTime).Milliseconds())
				respondJSONError(w, http.StatusInternalServerError, "Internal Server Error: Missing stream channel")
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("Transfer-Encoding", "chunked")
			w.WriteHeader(http.StatusOK)

			var promptTokens, completionTokens int
			var finalErr error

			for chunk := range jobRes.ChunkChan {
				if chunk.Done {
					promptTokens = chunk.PromptEvalCount
					completionTokens = chunk.EvalCount
				}
				
				// Write SSE event chunk
				if err := ollama.WriteSSEChunk(w, chunk); err != nil {
					finalErr = err
					slog.Warn("Client disconnected or SSE write failed", slog.String("request_id", reqID), slog.Any("error", err))
					break
				}
			}

			latency := time.Since(startTime).Milliseconds()
			success := finalErr == nil

			// Log to metrics and SQLite DB
			metrics.RecordRequest(success, latency)
			
			var errMsg string
			var statusCode = http.StatusOK
			if !success {
				errMsg = finalErr.Error()
				statusCode = http.StatusInternalServerError
			}

			// Async audit log to database to keep request fast
			go func() {
				auditLog := &database.RequestLog{
					RequestID:        reqID,
					APIKeyID:         apiKey.ID,
					Model:            chatReq.Model,
					LatencyMS:        latency,
					StatusCode:       statusCode,
					IPAddress:        ip,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
					ErrorMessage:     errMsg,
				}
				if err := database.WriteRequestLog(db, auditLog); err != nil {
					slog.Error("Failed to write request audit log to database", slog.Any("error", err))
				}
			}()

			return
		}

		// Handle non-streaming response
		if jobRes.Err != nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())
			slog.Error("Ollama processing failed", slog.String("request_id", reqID), slog.Any("error", jobRes.Err))

			statusCode := http.StatusInternalServerError
			if errors.Is(jobRes.Err, context.DeadlineExceeded) {
				statusCode = http.StatusGatewayTimeout
				respondJSONError(w, statusCode, "Gateway Timeout: Ollama request timed out")
			} else {
				respondJSONError(w, statusCode, "Ollama Execution Failed: "+jobRes.Err.Error())
			}

			// Write error to database
			latency := time.Since(startTime).Milliseconds()
			go func() {
				auditLog := &database.RequestLog{
					RequestID:    reqID,
					APIKeyID:     apiKey.ID,
					Model:        chatReq.Model,
					LatencyMS:    latency,
					StatusCode:   statusCode,
					IPAddress:    ip,
					ErrorMessage: jobRes.Err.Error(),
				}
				database.WriteRequestLog(db, auditLog)
			}()
			return
		}

		if jobRes.Response == nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())
			respondJSONError(w, http.StatusInternalServerError, "Ollama returned empty response")
			return
		}

		// Write successful response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(jobRes.Response)

		latency := time.Since(startTime).Milliseconds()
		metrics.RecordRequest(true, latency)

		// Async audit log
		go func() {
			auditLog := &database.RequestLog{
				RequestID:        reqID,
				APIKeyID:         apiKey.ID,
				Model:            chatReq.Model,
				LatencyMS:        latency,
				StatusCode:       http.StatusOK,
				IPAddress:        ip,
				PromptTokens:     jobRes.Response.PromptEvalCount,
				CompletionTokens: jobRes.Response.EvalCount,
			}
			if err := database.WriteRequestLog(db, auditLog); err != nil {
				slog.Error("Failed to write request audit log to database", slog.Any("error", err))
			}
		}()
	}
}

func respondJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
