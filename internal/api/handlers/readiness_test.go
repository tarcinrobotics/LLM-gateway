package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"llm_gateway/internal/database"
	"llm_gateway/internal/ollama"
	"llm_gateway/internal/queue"
)

type readinessTestResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Ollama   string `json:"ollama"`
}

func setupReadinessTest(
	t *testing.T,
	ollamaHandler http.HandlerFunc,
) (*sql.DB, *httptest.Server, *queue.WorkerPool, http.Handler) {
	t.Helper()

	server := httptest.NewServer(ollamaHandler)
	t.Cleanup(server.Close)

	dbPath := filepath.Join(t.TempDir(), "readiness.db")
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize test database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	wp := queue.NewWorkerPool(1, 1)
	client := ollama.NewClient(server.URL, 5*time.Second)

	handler := ReadinessHandler(db, wp, client)

	return db, server, wp, handler
}

func decodeReadinessResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) readinessTestResponse {
	t.Helper()

	var response readinessTestResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode readiness response: %v", err)
	}

	return response
}

func sendReadinessRequest(handler http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestReadinessHandlerSuccess(t *testing.T) {
	_, _, _, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("expected /api/version, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	recorder := sendReadinessRequest(handler)
	response := decodeReadinessResponse(t, recorder)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if response.Status != "ready" ||
		response.Database != "ready" ||
		response.Ollama != "ready" {
		t.Fatalf("unexpected readiness response: %+v", response)
	}
}

func TestReadinessHandlerDatabaseUnavailable(t *testing.T) {
	db, _, _, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close test database: %v", err)
	}

	recorder := sendReadinessRequest(handler)
	response := decodeReadinessResponse(t, recorder)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d", recorder.Code)
	}

	if response.Status != "not_ready" || response.Database != "unavailable" {
		t.Fatalf("expected database to be unavailable, got %+v", response)
	}

	if response.Ollama != "ready" {
		t.Fatalf("expected Ollama to be ready, got %+v", response)
	}
}

func TestReadinessHandlerOllamaHTTPError(t *testing.T) {
	_, _, _, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusInternalServerError)
	})

	recorder := sendReadinessRequest(handler)
	response := decodeReadinessResponse(t, recorder)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d", recorder.Code)
	}

	if response.Status != "not_ready" || response.Ollama != "unavailable" {
		t.Fatalf("expected Ollama to be unavailable, got %+v", response)
	}

	if response.Database != "ready" {
		t.Fatalf("expected database to be ready, got %+v", response)
	}
}

func TestReadinessHandlerOllamaUnreachable(t *testing.T) {
	db, server, wp, _ := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server.Close()

	client := ollama.NewClient(server.URL, time.Second)
	handler := ReadinessHandler(db, wp, client)

	recorder := sendReadinessRequest(handler)
	response := decodeReadinessResponse(t, recorder)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d", recorder.Code)
	}

	if response.Status != "not_ready" || response.Ollama != "unavailable" {
		t.Fatalf("expected unreachable Ollama to be unavailable, got %+v", response)
	}
}

func TestReadinessHandlerOllamaTimeout(t *testing.T) {
	upstreamCancelled := make(chan struct{})

	_, _, _, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(upstreamCancelled)
	})

	recorder := sendReadinessRequest(handler)
	response := decodeReadinessResponse(t, recorder)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d", recorder.Code)
	}

	if response.Status != "not_ready" || response.Ollama != "unavailable" {
		t.Fatalf("expected timed-out Ollama to be unavailable, got %+v", response)
	}

	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("expected readiness timeout to cancel the upstream request")
	}
}

func TestReadinessHandlerRequestCancellation(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCancelled := make(chan struct{})

	_, _, _, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		<-r.Context().Done()
		close(upstreamCancelled)
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/ready", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		handler.ServeHTTP(recorder, req)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("readiness request did not reach Ollama")
	}

	cancel()

	select {
	case <-upstreamCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected incoming request cancellation to cancel Ollama request")
	}

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness handler did not finish after request cancellation")
	}

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 after cancellation, got %d", recorder.Code)
	}
}

func TestReadinessHandlerShutdown(t *testing.T) {
	_, _, wp, handler := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Readiness should be successful before shutdown begins.
	beforeShutdown := sendReadinessRequest(handler)
	beforeResponse := decodeReadinessResponse(t, beforeShutdown)

	if beforeShutdown.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 before shutdown, got %d", beforeShutdown.Code)
	}

	if beforeResponse.Status != "ready" {
		t.Fatalf("expected ready before shutdown, got %+v", beforeResponse)
	}

	// Begin worker pool shutdown.
	wp.BeginShutdown()

	// Readiness should become unavailable immediately.
	afterShutdown := sendReadinessRequest(handler)
	afterResponse := decodeReadinessResponse(t, afterShutdown)

	if afterShutdown.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 during shutdown, got %d", afterShutdown.Code)
	}

	if afterResponse.Status != "not_ready" {
		t.Fatalf("expected not_ready during shutdown, got %+v", afterResponse)
	}

	if afterResponse.Database != "unknown" || afterResponse.Ollama != "unknown" {
		t.Fatalf("expected dependency checks to be skipped during shutdown, got %+v", afterResponse)
	}
}

func TestHealthHandlerCompatibility(t *testing.T) {
	db, _, wp, _ := setupReadinessTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("healthy database", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)

		HealthHandler(db, wp).ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d", recorder.Code)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode health response: %v", err)
		}

		if response["status"] != "UP" || response["database"] != "healthy" {
			t.Fatalf("unexpected health response: %+v", response)
		}

		for _, key := range []string{
			"active_workers",
			"queue_length",
			"queue_capacity",
		} {
			if _, exists := response[key]; !exists {
				t.Errorf("health response is missing field %q", key)
			}
		}
	})

	t.Run("unhealthy database", func(t *testing.T) {
		if err := db.Close(); err != nil {
			t.Fatalf("failed to close test database: %v", err)
		}

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)

		HealthHandler(db, wp).ServeHTTP(recorder, req)

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected HTTP 503, got %d", recorder.Code)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode health response: %v", err)
		}

		if response["status"] != "UP" || response["database"] != "unhealthy" {
			t.Fatalf("existing health response behavior changed: %+v", response)
		}
	})
}
