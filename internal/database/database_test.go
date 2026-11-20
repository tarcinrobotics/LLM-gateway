package database

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// createTestDB creates an isolated temporary SQLite database.
func createTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	return db
}

// TestSeedDataCreatesRolesAndPermissions verifies that initial seeding
// creates the expected roles, admin user, and permissions without API keys.
func TestSeedDataCreatesRolesAndPermissions(t *testing.T) {
	t.Setenv("ALLOWED_MODELS", "test-model-a,test-model-b")

	db := createTestDB(t)

	if err := SeedData(db); err != nil {
		t.Fatalf("SeedData failed: %v", err)
	}

	var roleCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount); err != nil {
		t.Fatalf("failed to count roles: %v", err)
	}
	if roleCount != 3 {
		t.Errorf("expected 3 roles, got %d", roleCount)
	}

	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("expected 1 default admin user, got %d", userCount)
	}

	var permissionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM permissions").Scan(&permissionCount); err != nil {
		t.Fatalf("failed to count permissions: %v", err)
	}
	if permissionCount != 4 {
		t.Errorf("expected 4 permissions, got %d", permissionCount)
	}

	var apiKeyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&apiKeyCount); err != nil {
		t.Fatalf("failed to count API keys: %v", err)
	}
	if apiKeyCount != 0 {
		t.Errorf("expected no API keys after fresh seeding, got %d", apiKeyCount)
	}
}

// TestSeedDataRepeatedlyDoesNotCreateTestKey verifies that repeated
// seeding never creates the old publicly known test API key.
func TestSeedDataRepeatedlyDoesNotCreateTestKey(t *testing.T) {
	db := createTestDB(t)

	for i := 0; i < 3; i++ {
		if err := SeedData(db); err != nil {
			t.Fatalf("SeedData failed on attempt %d: %v", i+1, err)
		}
	}

	knownTestKey := "gw_86e95dbb09274255564ca7d9e63dd13b"
	knownTestKeyHash := HashKey(knownTestKey)

	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM api_keys WHERE hash = ?",
		knownTestKeyHash,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to check for known test key: %v", err)
	}

	if count != 0 {
		t.Errorf("expected known test key to be absent, found %d", count)
	}

	var totalKeys int
	if err := db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&totalKeys); err != nil {
		t.Fatalf("failed to count API keys: %v", err)
	}
	if totalKeys != 0 {
		t.Errorf("expected no API keys after repeated seeding, got %d", totalKeys)
	}
}

// TestSeedDataPreservesExistingAPIKey verifies that seeding does not
// delete or modify an API key that already exists in the database.
func TestSeedDataPreservesExistingAPIKey(t *testing.T) {
	db := createTestDB(t)

	if err := SeedData(db); err != nil {
		t.Fatalf("initial SeedData failed: %v", err)
	}

	plainKey := "gw_existing_test_key_123456"

	createdKey, err := CreateAPIKey(db, plainKey, 1, 2, 45)
	if err != nil {
		t.Fatalf("failed to create existing API key: %v", err)
	}

	// Give the existing key distinctive values to verify they remain unchanged.
	_, err = db.Exec(
		"UPDATE api_keys SET is_active = 0, rate_limit_rpm = 45 WHERE id = ?",
		createdKey.ID,
	)
	if err != nil {
		t.Fatalf("failed to update existing API key: %v", err)
	}

	before, err := GetAPIKeyByHash(db, HashKey(plainKey))
	if err != nil {
		t.Fatalf("failed to retrieve API key before seeding: %v", err)
	}
	if before == nil {
		t.Fatal("expected existing API key before seeding")
	}

	if err := SeedData(db); err != nil {
		t.Fatalf("repeated SeedData failed: %v", err)
	}

	after, err := GetAPIKeyByHash(db, HashKey(plainKey))
	if err != nil {
		t.Fatalf("failed to retrieve API key after seeding: %v", err)
	}
	if after == nil {
		t.Fatal("existing API key was deleted by SeedData")
	}

	if after.ID != before.ID ||
		after.Prefix != before.Prefix ||
		after.Hash != before.Hash ||
		after.UserID != before.UserID ||
		after.RoleID != before.RoleID ||
		after.RateLimitRPM != before.RateLimitRPM ||
		after.IsActive != before.IsActive ||
		!after.CreatedAt.Equal(before.CreatedAt) ||
		!sameExpiry(after.ExpiresAt, before.ExpiresAt) {
		t.Errorf("existing API key was modified by SeedData\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func sameExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func TestInitDBCreatesAuditEventsTable(t *testing.T) {
	db := createTestDB(t)

	var tableName string
	err := db.QueryRow(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name = 'audit_events'
	`).Scan(&tableName)

	if err != nil {
		t.Fatalf("audit_events table was not created: %v", err)
	}

	if tableName != "audit_events" {
		t.Fatalf("unexpected table name: %s", tableName)
	}
}

func TestWriteAuditEventWithAPIKey(t *testing.T) {
	db := createTestDB(t)

	if err := SeedData(db); err != nil {
		t.Fatalf("SeedData failed: %v", err)
	}

	apiKey, err := CreateAPIKey(
		db,
		"gw_audit_test_key_123",
		1,
		2,
		60,
	)
	if err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	event := &AuditEvent{
		RequestID:  "request-success-001",
		EventType:  "request_success",
		Outcome:    "success",
		StatusCode: 200,
		APIKeyID:   &apiKey.ID,
	}

	if err := WriteAuditEvent(db, event); err != nil {
		t.Fatalf("WriteAuditEvent failed: %v", err)
	}

	var storedRequestID string
	var storedEventType string
	var storedOutcome string
	var storedStatus int
	var storedAPIKeyID int64

	err = db.QueryRow(`
		SELECT request_id, event_type, outcome, status_code, api_key_id
		FROM audit_events
		WHERE request_id = ?
	`, event.RequestID).Scan(
		&storedRequestID,
		&storedEventType,
		&storedOutcome,
		&storedStatus,
		&storedAPIKeyID,
	)
	if err != nil {
		t.Fatalf("failed to retrieve audit event: %v", err)
	}

	if storedRequestID != event.RequestID {
		t.Errorf("expected request ID %q, got %q", event.RequestID, storedRequestID)
	}

	if storedEventType != "request_success" {
		t.Errorf("expected event type request_success, got %q", storedEventType)
	}

	if storedOutcome != "success" {
		t.Errorf("expected outcome success, got %q", storedOutcome)
	}

	if storedStatus != 200 {
		t.Errorf("expected status 200, got %d", storedStatus)
	}

	if storedAPIKeyID != apiKey.ID {
		t.Errorf("expected API key ID %d, got %d", apiKey.ID, storedAPIKeyID)
	}
}

func TestWriteAuditEventWithoutAPIKey(t *testing.T) {
	db := createTestDB(t)

	event := &AuditEvent{
		RequestID:  "request-auth-failure-001",
		EventType:  "authentication_failed",
		Outcome:    "failure",
		StatusCode: 401,
		APIKeyID:   nil,
		ErrorCode:  "invalid_api_key",
	}

	if err := WriteAuditEvent(db, event); err != nil {
		t.Fatalf("WriteAuditEvent failed: %v", err)
	}

	var apiKeyID sql.NullInt64
	var errorCode sql.NullString

	err := db.QueryRow(`
		SELECT api_key_id, error_code
		FROM audit_events
		WHERE request_id = ?
	`, event.RequestID).Scan(&apiKeyID, &errorCode)
	if err != nil {
		t.Fatalf("failed to retrieve audit event: %v", err)
	}

	if apiKeyID.Valid {
		t.Errorf("expected NULL API key ID, got %d", apiKeyID.Int64)
	}

	if !errorCode.Valid || errorCode.String != "invalid_api_key" {
		t.Errorf("expected invalid_api_key error code, got %+v", errorCode)
	}
}

func TestWriteAuditEventRejectsInvalidValues(t *testing.T) {
	db := createTestDB(t)

	tests := []struct {
		name  string
		event AuditEvent
	}{
		{
			name: "invalid outcome",
			event: AuditEvent{
				RequestID:  "request-001",
				EventType:  "request_failed",
				Outcome:    "unknown",
				StatusCode: 500,
			},
		},
		{
			name: "raw error message",
			event: AuditEvent{
				RequestID:  "request-002",
				EventType:  "upstream_error",
				Outcome:    "failure",
				StatusCode: 502,
				ErrorCode:  "connection failed: sensitive upstream details",
			},
		},
		{
			name: "invalid status code",
			event: AuditEvent{
				RequestID:  "request-003",
				EventType:  "request_failed",
				Outcome:    "failure",
				StatusCode: 999,
			},
		},
		{
			name: "empty request ID",
			event: AuditEvent{
				RequestID:  "",
				EventType:  "request_failed",
				Outcome:    "failure",
				StatusCode: 500,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteAuditEvent(db, &tt.event)
			if err == nil {
				t.Fatal("expected invalid audit event to be rejected")
			}
		})
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&count); err != nil {
		t.Fatalf("failed to count audit events: %v", err)
	}

	if count != 0 {
		t.Errorf("expected no invalid audit events to be inserted, got %d", count)
	}
}

func TestInitDBPreservesAuditEvents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "preserve.db")

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("initial InitDB failed: %v", err)
	}

	event := &AuditEvent{
		RequestID:  "persistent-request-001",
		EventType:  "queue_rejected",
		Outcome:    "failure",
		StatusCode: 503,
		ErrorCode:  "queue_full",
	}

	if err := WriteAuditEvent(db, event); err != nil {
		t.Fatalf("WriteAuditEvent failed: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close first database connection: %v", err)
	}

	db, err = InitDB(dbPath)
	if err != nil {
		t.Fatalf("second InitDB failed: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM audit_events
		WHERE request_id = ?
	`, event.RequestID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to retrieve preserved audit event: %v", err)
	}

	if count != 1 {
		t.Errorf("expected audit event to survive reinitialization, got count %d", count)
	}
}
