package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"llm_gateway/internal/api"
	"llm_gateway/internal/config"
	"llm_gateway/internal/database"
	"llm_gateway/internal/logger"
	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
	"llm_gateway/internal/tls"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found or failed to load it. Will rely on system environment variables.")
	}

	// 1. Load config
	cfgPath := "config.yaml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		cfgPath = envPath
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize logger
	if err := logger.Init(cfg.Logging.FilePath, cfg.Logging.Level); err != nil {
		fmt.Printf("Error initializing logger: %v\n", err)
		os.Exit(1)
	}

	slog.Info("Starting LLM Gateway...")

	// 3. Initialize SQLite DB
	db, err := database.InitDB(cfg.Database.Path)
	if err != nil {
		slog.Error("Failed to initialize database", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	// 4. Seed initial database data (default user, roles, permissions, API Key)
	if err := database.SeedData(db); err != nil {
		slog.Error("Database seeding failed", slog.Any("error", err))
		os.Exit(1)
	}

	// 5. Initialize Ollama Client & Cache Discovery
	ollamaClient := ollama.NewClient(cfg.Ollama.URL, time.Duration(cfg.Ollama.ClientTimeoutSeconds)*time.Second)
	// Cache models for 1 minute
	discovery := ollama.NewModelDiscovery(ollamaClient, 1*time.Minute)

	// 6. Start Worker Pool
	wp := queue.NewWorkerPool(cfg.Queue.Workers, cfg.Queue.MaxSize)
	wp.Start()
	defer wp.Stop()

	// 7. Configure HTTP router
	router := api.NewRouter(db, wp, ollamaClient, discovery)

	// 8. Configure HTTPS/TLS Server
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	if cfg.Server.TLSEnabled {
		// Verify if certificate and key files exist; if not, generate them automatically
		_, certErr := os.Stat(cfg.Server.CertFile)
		_, keyErr := os.Stat(cfg.Server.KeyFile)

		if os.IsNotExist(certErr) || os.IsNotExist(keyErr) {
			slog.Warn("TLS certificates not found. Generating self-signed certificates for localhost...",
				slog.String("cert_file", cfg.Server.CertFile),
				slog.String("key_file", cfg.Server.KeyFile),
			)
			if err := tls.GenerateSelfSignedCert(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil {
				slog.Error("Failed to generate self-signed certificates", slog.Any("error", err))
				os.Exit(1)
			}
			slog.Info("Self-signed certificates generated successfully")
		}

		// Attach TLS 1.3 configuration
		server.TLSConfig = tls.GetTLSConfig()

		slog.Info(fmt.Sprintf("LLM Gateway running securely over HTTPS at https://localhost%s", addr))
		go func() {
			if err := server.ListenAndServeTLS(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTPS server closed with error", slog.Any("error", err))
				os.Exit(1)
			}
		}()
	} else {
		slog.Warn("TLS is disabled! Server is starting in HTTP mode.")
		slog.Info(fmt.Sprintf("LLM Gateway running over HTTP at http://localhost%s", addr))
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTP server closed with error", slog.Any("error", err))
				os.Exit(1)
			}
		}()
	}

	// 9. Graceful Shutdown listener
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	<-quit
	slog.Info("Shutdown signal received. Initiating graceful shutdown...")

	// Context for server shutdown timeout
	shutdownTimeout := time.Duration(cfg.Server.ShutdownTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Shutdown HTTP Server
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("HTTP server shutdown failed, forcing close", slog.Any("error", err))
	} else {
		slog.Info("HTTP server stopped gracefully")
	}

	slog.Info("Server execution completed")
}
