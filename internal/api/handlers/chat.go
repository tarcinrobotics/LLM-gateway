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
func ChatHandler(
	db *sql.DB,
	wp *queue.WorkerPool,
	client *ollama.Client,
	queueTimeout time.Duration,
	inferenceTimeout time.Duration,
) http.HandlerFunc {
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

		// Use the request context so client disconnection cancels inference.
		requestCtx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Schedule the job with separate queue and inference timeouts.
		jobRes, err := queue.ScheduleJob(
			requestCtx,
			wp,
			client,
			&chatReq,
			queueTimeout,
			inferenceTimeout,
		)
		if err != nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

			if errors.Is(err, context.Canceled) {
				slog.Info("Chat request cancelled", slog.String("request_id", reqID))
				return
			}

			if errors.Is(err, queue.ErrQueueTimeout) {
				slog.Error("Queue wait timeout",
					slog.String("request_id", reqID),
					slog.Duration("timeout", queueTimeout),
				)
				respondJSONError(w, http.StatusGatewayTimeout, "Gateway Timeout: Request spent too long waiting in the queue")
				return
			}

			slog.Error("Queue submission failed",
				slog.String("request_id", reqID),
				slog.Any("error", err),
			)
			respondJSONError(w, http.StatusServiceUnavailable, "Service Unavailable: Worker queue is full")
			return
		}

		// Handle streaming response
		if chatReq.Stream {
			// Handle errors that occur before streaming starts.
			if jobRes.Err != nil {
				metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

				statusCode := http.StatusBadGateway
				message := "Bad Gateway: Ollama streaming request failed"

				if errors.Is(jobRes.Err, context.DeadlineExceeded) {
					statusCode = http.StatusGatewayTimeout
					message = "Gateway Timeout: Ollama request timed out"
				}

				slog.Error("Streaming failed before headers",
					slog.String("request_id", reqID),
					slog.Any("error", jobRes.Err),
				)

				respondJSONError(w, statusCode, message)

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

					if err := database.WriteRequestLog(db, auditLog); err != nil {
						slog.Error("Failed to write request audit log", slog.Any("error", err))
					}
				}()

				return
			}
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
			var completed bool
			var clientDisconnected bool

			for chunk := range jobRes.ChunkChan {
				if chunk.Done {
					completed = true
					promptTokens = chunk.PromptEvalCount
					completionTokens = chunk.EvalCount
				}

				if err := ollama.WriteSSEChunk(w, chunk); err != nil {
					finalErr = err
					clientDisconnected = true

					slog.Warn("Client disconnected or SSE write failed",
						slog.String("request_id", reqID),
						slog.Any("error", err),
					)

					cancel()
					break
				}
			}
			// Read the worker's final inference outcome.
			if !clientDisconnected && jobRes.ErrChan != nil {
				if streamErr, ok := <-jobRes.ErrChan; ok && streamErr != nil {
					finalErr = streamErr
				}
			}

			// A stream without a completion chunk is never successful.
			if finalErr == nil && !completed {
				finalErr = errors.New("stream ended without completion signal")
			}

			// Send an error event only while the client is still connected.
			if finalErr != nil && !clientDisconnected &&
				r.Context().Err() == nil &&
				!errors.Is(finalErr, context.Canceled) {
				errorMessage := "Generation failed"
				if errors.Is(finalErr, context.DeadlineExceeded) {
					errorMessage = "Generation timed out"
				}

				if err := ollama.WriteSSEErrorEvent(w, errorMessage); err != nil {
					slog.Warn("Failed to write SSE error event",
						slog.String("request_id", reqID),
						slog.Any("error", err),
					)
				}
			}

			if errors.Is(finalErr, context.DeadlineExceeded) {
				slog.Error("Inference timeout during streaming",
					slog.String("request_id", reqID),
					slog.Duration("timeout", inferenceTimeout),
				)
			} else if finalErr != nil {
				slog.Error("Streaming inference failed",
					slog.String("request_id", reqID),
					slog.Any("error", finalErr),
				)
			}

			latency := time.Since(startTime).Milliseconds()
			success := finalErr == nil

			metrics.RecordRequest(success, latency)

			var errMsg string
			statusCode := http.StatusOK
			if !success {
				errMsg = finalErr.Error()
			}

			// HTTP status remains 200 after SSE headers are sent.
			// ErrorMessage and metrics record the generation outcome separately.
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
			if errors.Is(jobRes.Err, context.DeadlineExceeded) {
				slog.Error("Inference timeout",
					slog.String("request_id", reqID),
					slog.Duration("timeout", inferenceTimeout),
				)
			} else {
				slog.Error("Ollama processing failed",
					slog.String("request_id", reqID),
					slog.Any("error", jobRes.Err),
				)
			}
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
