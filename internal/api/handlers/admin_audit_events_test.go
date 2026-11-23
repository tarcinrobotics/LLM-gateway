package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm_gateway/internal/api/middleware"
	"llm_gateway/internal/database"
)

func createAuditTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.InitDB(t.TempDir() + "/audit.db")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		db.Close()
	})

	return db
}

func insertTestAudit(
	t *testing.T,
	db *sql.DB,
	requestID, eventType, outcome, createdAt string,
) {
	t.Helper()

	_, err := db.Exec(`
		INSERT INTO audit_events
		(request_id, event_type, outcome, status_code, error_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, requestID, eventType, outcome, 200, "safe_code", createdAt)

	if err != nil {
		t.Fatal(err)
	}
}

func callAuditHandler(handler http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	return rec
}

func TestAuditEventsPaginationAndOrdering(t *testing.T) {
	db := createAuditTestDB(t)

	insertTestAudit(t, db, "old", "chat", "success", "2026-01-01 10:00:00")
	insertTestAudit(t, db, "same-a", "chat", "failure", "2026-01-02 10:00:00")
	insertTestAudit(t, db, "same-b", "chat", "success", "2026-01-02 10:00:00")

	handler := AuditEventsHandler(db)

	rec := callAuditHandler(handler, "/admin/audit-events")

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var events []database.AuditEvent

	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Same timestamp: newer auto-increment ID comes first.
	if events[0].RequestID != "same-b" ||
		events[1].RequestID != "same-a" {
		t.Fatal("incorrect event ordering")
	}

	// Test second page.
	rec = callAuditHandler(handler, "/admin/audit-events?page=2&limit=1")

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	events = nil
	json.Unmarshal(rec.Body.Bytes(), &events)

	if len(events) != 1 || events[0].RequestID != "same-a" {
		t.Fatal("pagination failed")
	}
}

func TestAuditEventsDefaultLimit(t *testing.T) {
	db := createAuditTestDB(t)

	for i := 0; i < 55; i++ {
		insertTestAudit(
			t,
			db,
			"req-"+time.Unix(int64(i), 0).Format("150405"),
			"chat",
			"success",
			time.Unix(int64(i), 0).UTC().Format("2006-01-02 15:04:05"),
		)
	}

	rec := callAuditHandler(
		AuditEventsHandler(db),
		"/admin/audit-events",
	)

	var events []database.AuditEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}

	if len(events) != 50 {
		t.Fatalf("expected default 50, got %d", len(events))
	}
}

func TestAuditEventsFilters(t *testing.T) {
	db := createAuditTestDB(t)

	insertTestAudit(t, db, "req-1", "chat", "success", "2026-01-01 10:00:00")
	insertTestAudit(t, db, "req-2", "authentication_failed", "failure", "2026-01-02 10:00:00")

	handler := AuditEventsHandler(db)

	tests := []struct {
		query string
		want  int
	}{
		{"?event_type=chat", 1},
		{"?outcome=failure", 1},
		{"?request_id=req-1", 1},
		{"?event_type=chat&outcome=success&request_id=req-1", 1},
		{"?request_id=missing", 0},
	}

	for _, tc := range tests {
		rec := callAuditHandler(handler, "/admin/audit-events"+tc.query)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: unexpected status %d", tc.query, rec.Code)
			continue
		}

		var events []database.AuditEvent
		if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
			t.Fatal(err)
		}

		if len(events) != tc.want {
			t.Errorf("%s: got %d, want %d", tc.query, len(events), tc.want)
		}
	}
}

func TestAuditEventsInvalidParameters(t *testing.T) {
	db := createAuditTestDB(t)
	handler := AuditEventsHandler(db)

	tests := []string{
		"?page=0",
		"?page=abc",
		"?page=1000001",
		"?limit=0",
		"?limit=101",
		"?limit=abc",
		"?outcome=unknown",
		"?event_type=chat%20OR%201%3D1",
		"?request_id=a%0Ab",
		"?limit=1&limit=2",
	}

	for _, query := range tests {
		rec := callAuditHandler(handler, "/admin/audit-events"+query)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", query, rec.Code)
		}
	}
}

func TestAuditEventsAuthentication(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin-test")
	t.Setenv("ADMIN_PASS", "pass-test")

	db := createAuditTestDB(t)

	handler := middleware.AdminBasicAuthMiddleware(
		AuditEventsHandler(db),
	)

	// Missing credentials.
	rec := callAuditHandler(handler, "/admin/audit-events")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Invalid credentials.
	req := httptest.NewRequest(http.MethodGet, "/admin/audit-events", nil)
	req.SetBasicAuth("admin-test", "wrong")

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Valid credentials.
	req = httptest.NewRequest(http.MethodGet, "/admin/audit-events", nil)
	req.SetBasicAuth("admin-test", "pass-test")

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuditEventsDatabaseError(t *testing.T) {
	db := createAuditTestDB(t)

	_, err := db.Exec("DROP TABLE audit_events")
	if err != nil {
		t.Fatal(err)
	}

	rec := callAuditHandler(
		AuditEventsHandler(db),
		"/admin/audit-events",
	)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatal("database error leaked")
	}
}

func TestAuditEventsPrivacy(t *testing.T) {
	db := createAuditTestDB(t)

	insertTestAudit(
		t, db, "safe-req", "chat", "success",
		"2026-01-01 10:00:00",
	)

	rec := callAuditHandler(
		AuditEventsHandler(db),
		"/admin/audit-events",
	)

	var events []map[string]interface{}

	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	allowed := map[string]bool{
		"id":          true,
		"request_id":  true,
		"event_type":  true,
		"outcome":     true,
		"status_code": true,
		"api_key_id":  true,
		"error_code":  true,
		"created_at":  true,
	}

	for field := range events[0] {
		if !allowed[field] {
			t.Errorf("unexpected field in response: %s", field)
		}
	}
}

func TestLegacyLogsStillWork(t *testing.T) {
	db := createAuditTestDB(t)

	_, err := db.Exec(`
		INSERT INTO request_logs (
			request_id, api_key_id, model, latency_ms,
			status_code, ip_address, prompt_tokens, completion_tokens
		)
		VALUES ('legacy-1', 1, 'model-x', 20, 200, '127.0.0.1', 2, 3)
	`)
	if err != nil {
		t.Fatal(err)
	}

	rec := callAuditHandler(
		RequestLogsHandler(db),
		"/admin/logs",
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("legacy endpoint returned %d", rec.Code)
	}

	var logs []database.RequestLog

	if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil {
		t.Fatal(err)
	}

	if len(logs) != 1 || logs[0].RequestID != "legacy-1" {
		t.Fatal("legacy logs contract changed")
	}
}

func TestExistingDatabaseGetsAuditTable(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL,
			api_key_id INTEGER NOT NULL,
			model TEXT NOT NULL,
			latency_ms INTEGER NOT NULL,
			status_code INTEGER NOT NULL,
			ip_address TEXT NOT NULL,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			error_message TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		INSERT INTO request_logs
		(request_id, api_key_id, model, latency_ms, status_code, ip_address)
		VALUES ('existing-123', 1, 'model-x', 20, 200, '127.0.0.1')
	`)
	if err != nil {
		t.Fatal(err)
	}

	db.Close()

	// Run normal application initialization.
	db, err = database.InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int

	err = db.QueryRow(`
		SELECT COUNT(*) FROM request_logs
		WHERE request_id = 'existing-123'
	`).Scan(&count)

	if err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Fatal("existing request logs were not preserved")
	}

	err = db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'audit_events'
	`).Scan(&count)

	if err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Fatal("audit_events table was not initialized")
	}
}

func TestAuditEventsMaximumLimit(t *testing.T) {
	db := createAuditTestDB(t)

	for i := 0; i < 110; i++ {
		insertTestAudit(
			t,
			db,
			fmt.Sprintf("max-req-%d", i),
			"chat",
			"success",
			time.Unix(int64(i), 0).UTC().Format("2006-01-02 15:04:05"),
		)
	}

	rec := callAuditHandler(
		AuditEventsHandler(db),
		"/admin/audit-events?limit=100",
	)

	var events []database.AuditEvent

	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if len(events) != 100 {
		t.Fatalf("expected 100 events, got %d", len(events))
	}
}
