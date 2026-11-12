package database

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// APIKey represents the API Key model.
type APIKey struct {
	ID           int64      `json:"id"`
	Prefix       string     `json:"prefix"`
	Hash         string     `json:"-"`
	UserID       int64      `json:"user_id"`
	RoleID       int64      `json:"role_id"`
	RateLimitRPM int        `json:"rate_limit_rpm"`
	IsActive     bool       `json:"is_active"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

// RequestLog represents the audit log for LLM requests.
type RequestLog struct {
	ID               int64     `json:"id"`
	RequestID        string    `json:"request_id"`
	APIKeyID         int64     `json:"api_key_id"`
	Model            string    `json:"model"`
	LatencyMS        int64     `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	IPAddress        string    `json:"ip_address"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ErrorMessage     string    `json:"error_message"`
	CreatedAt        time.Time `json:"created_at"`
}

// Permission represents a model access permission for a role.
type Permission struct {
	ID        int64  `json:"id"`
	RoleID    int64  `json:"role_id"`
	ModelName string `json:"model_name"`
}

// InitDB initializes the SQLite database, creating directory and tables if they don't exist.
func InitDB(path string) (*sql.DB, error) {
	// Create directory if not exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database connection
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection limits and enable WAL mode for concurrency
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Create tables schema
	schema := `
	CREATE TABLE IF NOT EXISTS roles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL
	);

	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(role_id) REFERENCES roles(id)
	);

	CREATE TABLE IF NOT EXISTS permissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role_id INTEGER NOT NULL,
		model_name TEXT NOT NULL,
		UNIQUE(role_id, model_name),
		FOREIGN KEY(role_id) REFERENCES roles(id)
	);

	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		prefix TEXT NOT NULL,
		hash TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		role_id INTEGER NOT NULL,
		rate_limit_rpm INTEGER DEFAULT 60,
		is_active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		FOREIGN KEY(user_id) REFERENCES users(id),
		FOREIGN KEY(role_id) REFERENCES roles(id)
	);

	CREATE TABLE IF NOT EXISTS request_logs (
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(api_key_id) REFERENCES api_keys(id)
	);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to execute tables schema: %w", err)
	}

	return db, nil
}

// SeedData inserts default roles, users, and permissions.
func SeedData(db *sql.DB) error {
	// Seed roles
	_, err := db.Exec(`
		INSERT OR IGNORE INTO roles (id, name) VALUES
		(1, 'Admin'),
		(2, 'Developer'),
		(3, 'Consumer')
	`)
	if err != nil {
		return fmt.Errorf("failed to seed roles: %w", err)
	}

	// Seed default admin user
	// We use a dummy hash because admin routes authenticate directly against .env variables.
	_, err = db.Exec(`
		INSERT OR IGNORE INTO users (id, username, password_hash, role_id)
		VALUES (1, 'admin', 'dummy_hash_auth_is_in_env', 1)
	`)
	if err != nil {
		return fmt.Errorf("failed to seed default admin user: %w", err)
	}

	// Seed default permissions
	// Admin and Developer roles can access all models ("*")
	_, err = db.Exec(`
		INSERT OR IGNORE INTO permissions (role_id, model_name) VALUES
		(1, '*'),
		(2, '*')
	`)
	if err != nil {
		return fmt.Errorf("failed to seed admin/developer permissions: %w", err)
	}

	// Consumer role permissions are seeded from ALLOWED_MODELS env var
	allowedModelsEnv := os.Getenv("ALLOWED_MODELS")
	if allowedModelsEnv == "" {
		allowedModelsEnv = "qwen2.5:3b-instruct"
	}
	modelsList := strings.Split(allowedModelsEnv, ",")
	for _, model := range modelsList {
		modelTrimmed := strings.TrimSpace(model)
		if modelTrimmed != "" {
			_, err = db.Exec(`
				INSERT OR IGNORE INTO permissions (role_id, model_name)
				VALUES (3, ?)
			`, modelTrimmed)
			if err != nil {
				return fmt.Errorf("failed to seed permission for model %q: %w", modelTrimmed, err)
			}
		}
	}

	return nil
}

// HashKey hashes a plain API key using SHA-256 and returns a hex string.
func HashKey(plainKey string) string {
	sum := sha256.Sum256([]byte(plainKey))
	return hex.EncodeToString(sum[:])
}

// GenerateRandomKey generates a secure random 16-byte key with a "gw_" prefix.
func GenerateRandomKey() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "gw_" + hex.EncodeToString(bytes), nil
}

// GetAPIKeyByHash retrieves an API key from the database by its SHA-256 hash.
func GetAPIKeyByHash(db *sql.DB, hash string) (*APIKey, error) {
	row := db.QueryRow(`
		SELECT id, prefix, hash, user_id, role_id, rate_limit_rpm, is_active, created_at, expires_at
		FROM api_keys
		WHERE hash = ?
	`, hash)

	var k APIKey
	var expiresAt sql.NullTime
	err := row.Scan(&k.ID, &k.Prefix, &k.Hash, &k.UserID, &k.RoleID, &k.RateLimitRPM, &k.IsActive, &k.CreatedAt, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Time
	}

	return &k, nil
}

// CreateAPIKey creates, hashes, and stores a new API key.
func CreateAPIKey(db *sql.DB, plainKey string, userID, roleID int64, rateLimitRPM int) (*APIKey, error) {
	prefix := plainKey
	if len(plainKey) > 11 {
		prefix = plainKey[:11]
	}
	hash := HashKey(plainKey)

	res, err := db.Exec(`
		INSERT INTO api_keys (prefix, hash, user_id, role_id, rate_limit_rpm, is_active)
		VALUES (?, ?, ?, ?, ?, 1)
	`, prefix, hash, userID, roleID, rateLimitRPM)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &APIKey{
		ID:           id,
		Prefix:       prefix,
		Hash:         hash,
		UserID:       userID,
		RoleID:       roleID,
		RateLimitRPM: rateLimitRPM,
		IsActive:     true,
		CreatedAt:    time.Now(),
	}, nil
}

// GetPermissionsByRole retrieves all model permission rules for a specific role ID.
func GetPermissionsByRole(db *sql.DB, roleID int64) ([]Permission, error) {
	rows, err := db.Query(`
		SELECT id, role_id, model_name
		FROM permissions
		WHERE role_id = ?
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []Permission
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.ID, &p.RoleID, &p.ModelName); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}

	return perms, nil
}

// WriteRequestLog inserts a new request audit log entry.
func WriteRequestLog(db *sql.DB, log *RequestLog) error {
	var errMsg sql.NullString
	if log.ErrorMessage != "" {
		errMsg = sql.NullString{String: log.ErrorMessage, Valid: true}
	}

	_, err := db.Exec(`
		INSERT INTO request_logs (request_id, api_key_id, model, latency_ms, status_code, ip_address, prompt_tokens, completion_tokens, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.RequestID, log.APIKeyID, log.Model, log.LatencyMS, log.StatusCode, log.IPAddress, log.PromptTokens, log.CompletionTokens, errMsg)

	return err
}
