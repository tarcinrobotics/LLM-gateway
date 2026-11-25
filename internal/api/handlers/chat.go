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

		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip = r.RemoteAddr
		}

		if apiKey == nil {
			writeChatAudit(db, reqID, "authentication_failed", "failure",
				http.StatusUnauthorized, nil, "missing_api_key")
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}

		apiKeyID := apiKey.ID

		// Decode the request body with a maximum size.
		var chatReq ollama.ChatRequest

		if err := decodeLimitedJSON(r, ChatRequestBodyLimit, &chatReq); err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				writeChatAudit(db, reqID, "invalid_request", "failure",
					http.StatusRequestEntityTooLarge, &apiKeyID, "request_body_too_large")
				respondPayloadTooLarge(w)
				return
			}

			writeChatAudit(db, reqID, "invalid_request", "failure",
				http.StatusBadRequest, &apiKeyID, "invalid_request_body")

			respondJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		if chatReq.Model == "" {
			writeChatAudit(db, reqID, "invalid_request", "failure",
				http.StatusBadRequest, &apiKeyID, "missing_model")
			respondJSONError(w, http.StatusBadRequest, "model is a required field")
			return
		}

		// Update context with the requested model name for request logging.
		r = r.WithContext(middleware.SetModel(r.Context(), chatReq.Model))
		if wrapped, ok := w.(*middleware.ResponseWriterWrapper); ok {
			wrapped.Model = chatReq.Model
		}

		// Check RBAC permission.
		allowed, err := auth.IsModelAllowed(db, apiKey.RoleID, chatReq.Model)
		if err != nil {
			slog.Error(
				"Failed to check permissions",
				slog.String("request_id", reqID),
				slog.Any("error", err),
			)

			writeChatAudit(db, reqID, "permission_check_failed", "failure",
				http.StatusInternalServerError, &apiKeyID, "permission_check_error")

			respondJSONError(w, http.StatusInternalServerError, "Failed to verify access permissions")
			return
		}

		if !allowed {
			slog.Warn(
				"Model access forbidden",
				slog.String("request_id", reqID),
				slog.Int64("role_id", apiKey.RoleID),
				slog.String("model", chatReq.Model),
			)

			writeChatAudit(db, reqID, "permission_denied", "failure",
				http.StatusForbidden, &apiKeyID, "model_not_permitted")

			respondJSONError(
				w,
				http.StatusForbidden,
				"Forbidden: Access to model '"+chatReq.Model+"' is not allowed for your role",
			)
			return
		}

		// Use the request context so client disconnection cancels inference.
		requestCtx, cancel := context.WithCancel(r.Context())
		defer cancel()

		jobRes, err := queue.ScheduleJob(
			requestCtx,
			wp,
			client,
			&chatReq,
			queueTimeout,
			inferenceTimeout,
		)

		if err != nil {
			if errors.Is(err, context.Canceled) {
				slog.Info("Client cancelled request", slog.String("request_id", reqID))

				writeChatAudit(db, reqID, "client_cancelled", "cancelled",
					499, &apiKeyID, "client_cancelled")
				return
			}

			if errors.Is(err, queue.ErrQueueTimeout) {
				slog.Error("Queue wait timeout", slog.String("request_id", reqID))

				writeChatAudit(db, reqID, "queue_timeout", "failure",
					http.StatusGatewayTimeout, &apiKeyID, "queue_timeout")

				respondJSONError(
					w,
					http.StatusGatewayTimeout,
					"Gateway Timeout: Request spent too long waiting in the queue",
				)
				return
			}

			if errors.Is(err, queue.ErrQueueFull) {
				slog.Warn("Worker queue is full", slog.String("request_id", reqID))

				writeChatAudit(db, reqID, "queue_rejected", "failure",
					http.StatusServiceUnavailable, &apiKeyID, "queue_full")

				respondJSONError(
					w,
					http.StatusServiceUnavailable,
					"Service Unavailable: Worker queue is full. Please try again later",
				)
				return
			}

			if errors.Is(err, queue.ErrWorkerPoolStopped) {
				slog.Warn("Worker pool is shutting down", slog.String("request_id", reqID))

				writeChatAudit(db, reqID, "queue_rejected", "failure",
					http.StatusServiceUnavailable, &apiKeyID, "worker_pool_stopped")

				respondJSONError(
					w,
					http.StatusServiceUnavailable,
					"Service Unavailable: Server is shutting down and cannot accept new requests",
				)
				return
			}

			slog.Error("Queue submission failed",
				slog.String("request_id", reqID),
				slog.Any("error", err),
			)

			writeChatAudit(db, reqID, "queue_failed", "failure",
				http.StatusInternalServerError, &apiKeyID, "queue_submission_failed")

			respondJSONError(
				w,
				http.StatusInternalServerError,
				"Internal Server Error: Failed to process request",
			)
			return
		}

		// Handle streaming responses.
		if chatReq.Stream {
			if jobRes.Err != nil {
				metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

				statusCode := http.StatusBadGateway
				errorCode := "upstream_error"
				message := "Bad Gateway: Ollama streaming request failed"

				if errors.Is(jobRes.Err, context.DeadlineExceeded) {
					statusCode = http.StatusGatewayTimeout
					errorCode = "inference_timeout"
					message = "Gateway Timeout: Ollama request timed out"
				}

				slog.Error(
					"Streaming failed before headers",
					slog.String("request_id", reqID),
					slog.Any("error", jobRes.Err),
				)

				respondJSONError(w, statusCode, message)

				latency := time.Since(startTime).Milliseconds()
				writeChatAudit(db, reqID, "inference_failed", "failure",
					statusCode, &apiKeyID, errorCode)

				writeLegacyRequestLog(db, &database.RequestLog{
					RequestID:    reqID,
					APIKeyID:     apiKey.ID,
					Model:        chatReq.Model,
					LatencyMS:    latency,
					StatusCode:   statusCode,
					IPAddress:    ip,
					ErrorMessage: errorCode,
				})
				return
			}

			if jobRes.ChunkChan == nil {
				metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

				writeChatAudit(db, reqID, "stream_failed", "failure",
					http.StatusInternalServerError, &apiKeyID, "missing_stream_channel")

				respondJSONError(
					w,
					http.StatusInternalServerError,
					"Internal Server Error: Missing stream channel",
				)
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

					slog.Warn(
						"Client disconnected or SSE write failed",
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
			if finalErr != nil &&
				!clientDisconnected &&
				r.Context().Err() == nil &&
				!errors.Is(finalErr, context.Canceled) {

				errorMessage := "Generation failed"
				if errors.Is(finalErr, context.DeadlineExceeded) {
					errorMessage = "Generation timed out"
				}

				if err := ollama.WriteSSEErrorEvent(w, errorMessage); err != nil {
					slog.Warn(
						"Failed to write SSE error event",
						slog.String("request_id", reqID),
						slog.Any("error", err),
					)
				}
			}

			if errors.Is(finalErr, context.DeadlineExceeded) {
				slog.Error(
					"Inference timeout during streaming",
					slog.String("request_id", reqID),
					slog.Duration("timeout", inferenceTimeout),
				)
			} else if finalErr != nil {
				slog.Error(
					"Streaming inference failed",
					slog.String("request_id", reqID),
					slog.Any("error", finalErr),
				)
			}

			latency := time.Since(startTime).Milliseconds()
			success := finalErr == nil && !clientDisconnected

			metrics.RecordRequest(success, latency)

			statusCode := http.StatusOK
			outcome := "success"
			eventType := "chat_succeeded"
			errorCode := ""

			if clientDisconnected || errors.Is(finalErr, context.Canceled) ||
				r.Context().Err() != nil {
				outcome = "cancelled"
				eventType = "client_cancelled"
				errorCode = "client_cancelled"
			} else if finalErr != nil {
				outcome = "failure"
				eventType = "stream_failed"
				errorCode = "stream_error"

				if errors.Is(finalErr, context.DeadlineExceeded) {
					errorCode = "inference_timeout"
				} else if !completed {
					errorCode = "incomplete_stream"
				}
			}

			// HTTP status remains 200 after SSE headers are sent.
			writeChatAudit(db, reqID, eventType, outcome,
				statusCode, &apiKeyID, errorCode)

			writeLegacyRequestLog(db, &database.RequestLog{
				RequestID:        reqID,
				APIKeyID:         apiKey.ID,
				Model:            chatReq.Model,
				LatencyMS:        latency,
				StatusCode:       statusCode,
				IPAddress:        ip,
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				ErrorMessage:     errorCode,
			})
			return
		}

		// Handle non-streaming responses.
		if jobRes.Err != nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

			statusCode := http.StatusInternalServerError
			errorCode := "upstream_error"

			if errors.Is(jobRes.Err, context.DeadlineExceeded) {
				statusCode = http.StatusGatewayTimeout
				errorCode = "inference_timeout"

				slog.Error(
					"Inference timeout",
					slog.String("request_id", reqID),
					slog.Duration("timeout", inferenceTimeout),
				)

				respondJSONError(w, statusCode, "Gateway Timeout: Ollama request timed out")
			} else {
				slog.Error(
					"Ollama processing failed",
					slog.String("request_id", reqID),
					slog.Any("error", jobRes.Err),
				)

				respondJSONError(
					w,
					statusCode,
					"Ollama Execution Failed: "+jobRes.Err.Error(),
				)
			}

			latency := time.Since(startTime).Milliseconds()

			writeChatAudit(db, reqID, "inference_failed", "failure",
				statusCode, &apiKeyID, errorCode)

			writeLegacyRequestLog(db, &database.RequestLog{
				RequestID:    reqID,
				APIKeyID:     apiKey.ID,
				Model:        chatReq.Model,
				LatencyMS:    latency,
				StatusCode:   statusCode,
				IPAddress:    ip,
				ErrorMessage: errorCode,
			})
			return
		}

		if jobRes.Response == nil {
			metrics.RecordRequest(false, time.Since(startTime).Milliseconds())

			writeChatAudit(db, reqID, "inference_failed", "failure",
				http.StatusInternalServerError, &apiKeyID, "empty_upstream_response")

			respondJSONError(
				w,
				http.StatusInternalServerError,
				"Ollama returned empty response",
			)
			return
		}

		// Write successful response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(jobRes.Response)

		latency := time.Since(startTime).Milliseconds()
		metrics.RecordRequest(true, latency)

		writeChatAudit(db, reqID, "chat_succeeded", "success",
			http.StatusOK, &apiKeyID, "")

		writeLegacyRequestLog(db, &database.RequestLog{
			RequestID:        reqID,
			APIKeyID:         apiKey.ID,
			Model:            chatReq.Model,
			LatencyMS:        latency,
			StatusCode:       http.StatusOK,
			IPAddress:        ip,
			PromptTokens:     jobRes.Response.PromptEvalCount,
			CompletionTokens: jobRes.Response.EvalCount,
		})
	}
}

// writeChatAudit records a privacy-safe request outcome in audit_events.
func writeChatAudit(
	db *sql.DB,
	requestID string,
	eventType string,
	outcome string,
	statusCode int,
	apiKeyID *int64,
	errorCode string,
) {
	if requestID == "" {
		slog.Warn(
			"Skipping chat audit event because request ID is missing",
			slog.String("event_type", eventType),
		)
		return
	}

	event := &database.AuditEvent{
		RequestID:  requestID,
		EventType:  eventType,
		Outcome:    outcome,
		StatusCode: statusCode,
		APIKeyID:   apiKeyID,
		ErrorCode:  errorCode,
	}

	if err := database.WriteAuditEvent(db, event); err != nil {
		slog.Error(
			"Failed to write chat audit event",
			slog.String("request_id", requestID),
			slog.String("event_type", eventType),
			slog.Any("error", err),
		)
	}
}

// writeLegacyRequestLog preserves compatibility with the existing request_logs table.
// ErrorMessage contains only a safe error code, never a raw upstream error.
func writeLegacyRequestLog(db *sql.DB, requestLog *database.RequestLog) {
	if err := database.WriteRequestLog(db, requestLog); err != nil {
		slog.Error(
			"Failed to write legacy request log",
			slog.String("request_id", requestLog.RequestID),
			slog.Any("error", err),
		)
	}
}

func respondJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
