package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Log is the global logger instance.
var Log *slog.Logger

// Init initializes the structured JSON logger.
func Init(filePath, levelStr string) error {
	// Ensure log directory exists
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	// Open or create log file
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	// Determine log level
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Write to both stdout (for dev/containers) and the file
	multiWriter := io.MultiWriter(os.Stdout, file)

	handler := slog.NewJSONHandler(multiWriter, &slog.HandlerOptions{
		Level: level,
	})

	Log = slog.New(handler)
	slog.SetDefault(Log)

	return nil
}
