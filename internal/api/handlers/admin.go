package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

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
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request payload"})
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
