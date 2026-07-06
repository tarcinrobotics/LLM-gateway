package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Ollama   OllamaConfig   `yaml:"ollama"`
	Queue    QueueConfig    `yaml:"queue"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type ServerConfig struct {
	Port                   int    `yaml:"port"`
	TLSEnabled             bool   `yaml:"tls_enabled"`
	CertFile               string `yaml:"cert_file"`
	KeyFile                string `yaml:"key_file"`
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type OllamaConfig struct {
	URL                  string `yaml:"url"`
	ClientTimeoutSeconds int    `yaml:"client_timeout_seconds"`
}

type QueueConfig struct {
	Workers        int `yaml:"workers"`
	MaxSize        int `yaml:"max_size"`
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

type LoggingConfig struct {
	FilePath string `yaml:"file_path"`
	Level    string `yaml:"level"`
}

// LoadConfig reads and parses the configuration from the given path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Set sane defaults if not defined
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8443
	}
	if cfg.Server.ShutdownTimeoutSeconds == 0 {
		cfg.Server.ShutdownTimeoutSeconds = 10
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "database/gateway.db"
	}
	if cfg.Ollama.URL == "" {
		cfg.Ollama.URL = "http://localhost:11434"
	}
	if cfg.Ollama.ClientTimeoutSeconds == 0 {
		cfg.Ollama.ClientTimeoutSeconds = 30
	}
	if cfg.Queue.Workers == 0 {
		cfg.Queue.Workers = 5
	}
	if cfg.Queue.MaxSize == 0 {
		cfg.Queue.MaxSize = 50
	}
	if cfg.Queue.TimeoutSeconds == 0 {
		cfg.Queue.TimeoutSeconds = 30
	}
	if cfg.Logging.FilePath == "" {
		cfg.Logging.FilePath = "logs/gateway.log"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}

	return &cfg, nil
}
