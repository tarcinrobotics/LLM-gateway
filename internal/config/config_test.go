package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsInferenceTimeout(t *testing.T) {
	configContent := `
ollama:
  url: "http://localhost:11434"
  client_timeout_seconds: 30
queue:
  workers: 5
  max_size: 50
  timeout_seconds: 30
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	if cfg.Ollama.InferenceTimeoutSeconds != 120 {
		t.Errorf("expected inference timeout 120, got %d",
			cfg.Ollama.InferenceTimeoutSeconds)
	}
}

func TestLoadConfigPreservesInferenceTimeout(t *testing.T) {
	configContent := `
ollama:
  inference_timeout_seconds: 180
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	if cfg.Ollama.InferenceTimeoutSeconds != 180 {
		t.Errorf("expected inference timeout 180, got %d",
			cfg.Ollama.InferenceTimeoutSeconds)
	}
}

func TestLoadConfigDefaultsCORSAllowedOriginsToEmpty(t *testing.T) {
	configContent := `
ollama:
  url: "http://localhost:11434"
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	if len(cfg.CORS.AllowedOrigins) != 0 {
		t.Errorf("expected empty CORS allowed origins, got %v",
			cfg.CORS.AllowedOrigins)
	}
}

func TestLoadConfigPreservesCORSAllowedOrigins(t *testing.T) {
	configContent := `
cors:
  allowed_origins:
    - "http://localhost:5173"
    - "https://example.com"
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	expected := []string{
		"http://localhost:5173",
		"https://example.com",
	}

	if len(cfg.CORS.AllowedOrigins) != len(expected) {
		t.Fatalf("expected %d origins, got %d",
			len(expected), len(cfg.CORS.AllowedOrigins))
	}

	for i, origin := range expected {
		if cfg.CORS.AllowedOrigins[i] != origin {
			t.Errorf("expected origin %q at index %d, got %q",
				origin, i, cfg.CORS.AllowedOrigins[i])
		}
	}
}

func TestLoadConfigPreservesTrustedProxyCIDRs(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")

	configContent := `
server:
  trusted_proxy_cidrs:
    - "10.0.0.0/8"
    - "192.0.2.10/32"
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	want := []string{"10.0.0.0/8", "192.0.2.10/32"}

	if len(cfg.Server.TrustedProxyCIDRs) != len(want) {
		t.Fatalf(
			"expected trusted proxies %v, got %v",
			want,
			cfg.Server.TrustedProxyCIDRs,
		)
	}

	for i := range want {
		if cfg.Server.TrustedProxyCIDRs[i] != want[i] {
			t.Errorf(
				"trusted proxy %d = %q, want %q",
				i,
				cfg.Server.TrustedProxyCIDRs[i],
				want[i],
			)
		}
	}
}

func TestLoadConfigTrustedProxyCIDRsEnvironmentOverride(t *testing.T) {
	t.Setenv(
		"TRUSTED_PROXY_CIDRS",
		"10.0.0.0/8, 192.0.2.10/32",
	)

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(
		configPath,
		[]byte("server:\n  trusted_proxy_cidrs: [\"172.16.0.0/12\"]\n"),
		0600,
	); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}

	want := []string{"10.0.0.0/8", "192.0.2.10/32"}

	if len(cfg.Server.TrustedProxyCIDRs) != len(want) {
		t.Fatalf(
			"expected environment override %v, got %v",
			want,
			cfg.Server.TrustedProxyCIDRs,
		)
	}

	for i := range want {
		if cfg.Server.TrustedProxyCIDRs[i] != want[i] {
			t.Errorf(
				"trusted proxy %d = %q, want %q",
				i,
				cfg.Server.TrustedProxyCIDRs[i],
				want[i],
			)
		}
	}
}
