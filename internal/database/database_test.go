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
