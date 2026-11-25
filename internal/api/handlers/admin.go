package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"llm_gateway/internal/api/middleware"
	"llm_gateway/internal/database"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/queue"
)

// AdminMetricsHandler exposes runtime server metrics.
func AdminMetricsHandler(db *sql.DB, wp *queue.WorkerPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		m := metrics.GetMetrics()

		var avgLatency float64
		if m.TotalRequests > 0 {
			avgLatency = float64(m.LatencySumMs) / float64(m.TotalRequests)
		}

		stats := map[string]interface{}{
			"total_requests":      m.TotalRequests,
			"successful_requests": m.SuccessfulRequests,
			"failed_requests":     m.FailedRequests,
			"average_latency_ms":  avgLatency,
			"active_workers":      wp.ActiveWorkers(),
			"queue_length":        wp.QueueLength(),
			"timestamp":           time.Now(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(stats)
	}
}

// ListAPIKeysHandler lists non-sensitive metadata for all API keys.
func ListAPIKeysHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		rows, err := db.Query(`
			SELECT id, prefix, user_id, role_id, rate_limit_rpm, is_active, created_at, expires_at
			FROM api_keys
			ORDER BY id DESC
		`)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Failed to query keys: " + err.Error()})
			return
		}
		defer rows.Close()

		var keys []database.APIKey
		for rows.Next() {
			var k database.APIKey
			var expiresAt sql.NullTime
			if err := rows.Scan(&k.ID, &k.Prefix, &k.UserID, &k.RoleID, &k.RateLimitRPM, &k.IsActive, &k.CreatedAt, &expiresAt); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "Failed to scan key: " + err.Error()})
				return
			}
			if expiresAt.Valid {
				k.ExpiresAt = &expiresAt.Time
			}
			keys = append(keys, k)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(keys)
	}
}

type CreateKeyPayload struct {
	UserID       int64 `json:"user_id"`
	RoleID       int64 `json:"role_id"`
	RateLimitRPM int   `json:"rate_limit_rpm"`
}

// CreateAPIKeyHandler generates, hashes, and stores a new API key.
func CreateAPIKeyHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		var payload CreateKeyPayload

		if err := decodeLimitedJSON(r, CreateAPIKeyRequestBodyLimit, &payload); err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				respondPayloadTooLarge(w)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)

			json.NewEncoder(w).Encode(
				map[string]string{"error": "Invalid request payload"},
			)
			return
		}

		if payload.UserID <= 0 || payload.RoleID <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "user_id and role_id are required"})
			return
		}

		if payload.RateLimitRPM <= 0 {
			payload.RateLimitRPM = 60 // default
		}

		// Generate random secure key
		plainKey, err := database.GenerateRandomKey()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Key generation failed"})
			return
		}

		dbKey, err := database.CreateAPIKey(db, plainKey, payload.UserID, payload.RoleID, payload.RateLimitRPM)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save API key: " + err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":        "API key created successfully. Save it now! It will not be shown again.",
			"plain_api_key":  plainKey,
			"key_id":         dbKey.ID,
			"prefix":         dbKey.Prefix,
			"rate_limit_rpm": dbKey.RateLimitRPM,
		})
	}
}

// RequestLogsHandler lists the database request logs.
func RequestLogsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
			limit = val
		}

		rows, err := db.Query(`
			SELECT id, request_id, api_key_id, model, latency_ms, status_code, ip_address, prompt_tokens, completion_tokens, error_message, created_at
			FROM request_logs
			ORDER BY id DESC
			LIMIT ?
		`, limit)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Failed to query logs: " + err.Error()})
			return
		}
		defer rows.Close()

		var logs []database.RequestLog
		for rows.Next() {
			var l database.RequestLog
			var errStr sql.NullString
			if err := rows.Scan(
				&l.ID,
				&l.RequestID,
				&l.APIKeyID,
				&l.Model,
				&l.LatencyMS,
				&l.StatusCode,
				&l.IPAddress,
				&l.PromptTokens,
				&l.CompletionTokens,
				&errStr,
				&l.CreatedAt,
			); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "Failed to scan log: " + err.Error()})
				return
			}
			if errStr.Valid {
				l.ErrorMessage = errStr.String
			}
			logs = append(logs, l)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(logs)
	}
}

// DeleteAPIKeyHandler deletes an API key by ID (permanently revoking it).
func DeleteAPIKeyHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or missing key ID"})
			return
		}

		res, err := db.Exec("DELETE FROM api_keys WHERE id = ?", id)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Failed to delete API key: " + err.Error()})
			return
		}

		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "API key not found"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "API key deleted successfully"})
	}
}

// AuditEventsHandler returns privacy-safe audit metadata with bounded pagination.
func AuditEventsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// Pagination defaults.
		page := 1
		limit := 50
		var err error

		// Validate page.
		if values, exists := q["page"]; exists {
			if len(values) != 1 || values[0] == "" {
				writeAuditError(w, http.StatusBadRequest, "Invalid page")
				return
			}

			page, err = strconv.Atoi(values[0])
			if err != nil || page < 1 || page > 1000000 {
				writeAuditError(w, http.StatusBadRequest, "page must be between 1 and 1000000")
				return
			}
		}

		// Validate limit.
		if values, exists := q["limit"]; exists {
			if len(values) != 1 || values[0] == "" {
				writeAuditError(w, http.StatusBadRequest, "Invalid limit")
				return
			}

			limit, err = strconv.Atoi(values[0])
			if err != nil || limit < 1 || limit > 100 {
				writeAuditError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
		}

		// Validate exact-match filters.
		eventType, ok := getAuditFilter(q, "event_type")
		if !ok || (eventType != "" && !isValidAuditFilter(eventType, 64)) {
			writeAuditError(w, http.StatusBadRequest, "Invalid event_type")
			return
		}

		outcome, ok := getAuditFilter(q, "outcome")
		if !ok || (outcome != "" &&
			outcome != "success" &&
			outcome != "failure" &&
			outcome != "cancelled") {
			writeAuditError(w, http.StatusBadRequest, "Invalid outcome")
			return
		}

		requestID, ok := getAuditFilter(q, "request_id")
		if !ok || (requestID != "" && !middleware.IsValidRequestID(requestID)) {
			writeAuditError(w, http.StatusBadRequest, "Invalid request_id")
			return
		}

		// Build a parameterized query.
		query := `
			SELECT id, request_id, event_type, outcome,
			       status_code, api_key_id, error_code, created_at
			FROM audit_events
			WHERE 1 = 1
		`

		args := make([]interface{}, 0, 5)

		if eventType != "" {
			query += " AND event_type = ?"
			args = append(args, eventType)
		}

		if outcome != "" {
			query += " AND outcome = ?"
			args = append(args, outcome)
		}

		if requestID != "" {
			query += " AND request_id = ?"
			args = append(args, requestID)
		}

		offset := (page - 1) * limit

		// ID provides deterministic ordering for identical timestamps.
		query += `
			ORDER BY created_at DESC, id DESC
			LIMIT ? OFFSET ?
		`

		args = append(args, limit, offset)

		rows, err := db.Query(query, args...)
		if err != nil {
			writeAuditError(w, http.StatusInternalServerError,
				"Unable to retrieve audit events")
			return
		}
		defer rows.Close()

		events := make([]database.AuditEvent, 0)

		for rows.Next() {
			var event database.AuditEvent
			var apiKeyID sql.NullInt64
			var errorCode sql.NullString

			err := rows.Scan(
				&event.ID,
				&event.RequestID,
				&event.EventType,
				&event.Outcome,
				&event.StatusCode,
				&apiKeyID,
				&errorCode,
				&event.CreatedAt,
			)

			if err != nil {
				writeAuditError(w, http.StatusInternalServerError,
					"Unable to retrieve audit events")
				return
			}

			if apiKeyID.Valid {
				id := apiKeyID.Int64
				event.APIKeyID = &id
			}

			if errorCode.Valid {
				event.ErrorCode = errorCode.String
			}

			events = append(events, event)
		}

		if err := rows.Err(); err != nil {
			writeAuditError(w, http.StatusInternalServerError,
				"Unable to retrieve audit events")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(events)
	}
}

// Gets a single filter value and rejects repeated or empty parameters.
func getAuditFilter(q url.Values, key string) (string, bool) {
	values, exists := q[key]

	if !exists {
		return "", true
	}

	if len(values) != 1 || values[0] == "" {
		return "", false
	}

	return values[0], true
}

// Allows only safe identifier characters for event_type.
func isValidAuditFilter(value string, maxLength int) bool {
	if len(value) == 0 || len(value) > maxLength {
		return false
	}

	for _, r := range value {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			continue
		}

		return false
	}

	return true
}

// Returns a generic JSON error without exposing database details.
func writeAuditError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
