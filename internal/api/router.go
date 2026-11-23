package api

import (
	"database/sql"
	"io/fs"
	"net/http"
	"time"

	"llm_gateway/internal/api/handlers"
	"llm_gateway/internal/api/middleware"
	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
	"llm_gateway/ui"
)

// NewRouter configures the HTTP serve multiplexer and wraps endpoints with middleware.
func NewRouter(
	db *sql.DB,
	wp *queue.WorkerPool,
	ollamaClient *ollama.Client,
	discovery *ollama.ModelDiscovery,
	queueTimeout time.Duration,
	inferenceTimeout time.Duration,
	allowedOrigins []string,
) http.Handler {
	mux := http.NewServeMux()

	// 1. Public Endpoints
	mux.HandleFunc("GET /health", handlers.HealthHandler(db, wp))
	mux.HandleFunc("GET /ready", handlers.ReadinessHandler(db, wp, ollamaClient))

	// Initialize middleware config
	rateLimiter := middleware.NewRateLimiter()
	authMW := middleware.AuthMiddleware(db)
	limitMW := middleware.RateLimitMiddleware(db, rateLimiter)

	// Helper to wrap secure routes.
	secureRoute := func(h http.Handler) http.Handler {
		return authMW(limitMW(h))
	}

	// 2. Gateway Proxy / Consumer Endpoints (Secure)
	mux.Handle(
		"POST /api/v1/chat",
		secureRoute(handlers.ChatHandler(db, wp, ollamaClient, queueTimeout, inferenceTimeout)),
	)
	mux.Handle(
		"GET /api/v1/models",
		secureRoute(handlers.ModelsHandler(discovery)),
	)

	// Admin route helper.
	adminRoute := func(h http.Handler) http.Handler {
		return middleware.AdminBasicAuthMiddleware(h)
	}

	// 3. Admin / Maintenance Endpoints (Secure via .env Basic Auth)
	mux.Handle("GET /admin/metrics", adminRoute(handlers.AdminMetricsHandler(db, wp)))
	mux.Handle("GET /admin/keys", adminRoute(handlers.ListAPIKeysHandler(db)))
	mux.Handle("POST /admin/keys", adminRoute(handlers.CreateAPIKeyHandler(db)))
	mux.Handle("DELETE /admin/keys/{id}", adminRoute(handlers.DeleteAPIKeyHandler(db)))
	mux.Handle("GET /admin/logs", adminRoute(handlers.RequestLogsHandler(db)))
	mux.Handle("GET /admin/audit-events", adminRoute(handlers.AuditEventsHandler(db)))
	// 4. Embedded React Frontend
	distFS, err := fs.Sub(ui.FS, "dist")
	if err != nil {
		panic("Failed to load embedded frontend: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(distFS)))

	// 5. Global Middleware Wrapper
	var handler http.Handler = mux
	handler = middleware.RecoveryMiddleware(handler)
	handler = middleware.LoggingMiddleware(handler)
	handler = middleware.CORSMiddleware(allowedOrigins)(handler)

	return handler
}
