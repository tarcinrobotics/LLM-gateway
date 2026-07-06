package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"llm_gateway/internal/queue"
)

// HealthHandler returns database status and worker pool statistics.
func HealthHandler(db *sql.DB, wp *queue.WorkerPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbStatus := "healthy"
		if err := db.Ping(); err != nil {
			dbStatus = "unhealthy"
		}

		w.Header().Set("Content-Type", "application/json")
		
		status := http.StatusOK
		if dbStatus == "unhealthy" {
			status = http.StatusServiceUnavailable
		}
		
		w.WriteHeader(status)

		stats := map[string]interface{}{
			"status":             "UP",
			"database":           dbStatus,
			"active_workers":     wp.ActiveWorkers(),
			"queue_length":       wp.QueueLength(),
			"queue_capacity":     wp.QueueCapacity(),
		}

		json.NewEncoder(w).Encode(stats)
	}
}
