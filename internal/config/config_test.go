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
