package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestDiscovery creates a fake Ollama server and a ModelDiscovery instance.
func newTestDiscovery(
	t *testing.T,
	handler http.HandlerFunc,
	ttl time.Duration,
) (*ModelDiscovery, *int32) {
	t.Helper()

	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}

		handler(w, r)
	}))

	t.Cleanup(server.Close)

	client := NewClient(server.URL, time.Second)
	discovery := NewModelDiscovery(client, ttl)

	return discovery, &requestCount
}

// expireCache forces the cache to expire without waiting for the actual TTL.
func expireCache(md *ModelDiscovery) {
	md.mu.Lock()
	defer md.mu.Unlock()

	md.lastFetched = time.Now().Add(-2 * md.cacheTTL)
}

// TestModelDiscoveryCacheHit verifies that repeated requests use the cache.
func TestModelDiscoveryCacheHit(t *testing.T) {
	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3:latest"}]}`))
		},
		time.Minute,
	)

	first, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	second, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	if len(first) != 1 || first[0].Name != "llama3:latest" {
		t.Fatalf("unexpected first result: %+v", first)
	}

	if len(second) != 1 || second[0].Name != "llama3:latest" {
		t.Fatalf("unexpected second result: %+v", second)
	}

	if got := atomic.LoadInt32(requestCount); got != 1 {
		t.Fatalf("expected 1 Ollama request, got %d", got)
	}
}

// TestModelDiscoveryCacheExpiry verifies that expired cache entries are refreshed.
func TestModelDiscoveryCacheExpiry(t *testing.T) {
	responseBody := `{"models":[{"name":"llama3:latest"}]}`

	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(responseBody))
		},
		time.Minute,
	)

	first, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("initial request failed: %v", err)
	}

	if len(first) != 1 || first[0].Name != "llama3:latest" {
		t.Fatalf("unexpected initial result: %+v", first)
	}

	// Simulate Ollama receiving an updated model list.
	responseBody = `{"models":[{"name":"mistral:latest"}]}`

	// Force the cached result to expire.
	expireCache(discovery)

	refreshed, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("refresh request failed: %v", err)
	}

	if len(refreshed) != 1 || refreshed[0].Name != "mistral:latest" {
		t.Fatalf("expected refreshed model mistral:latest, got %+v", refreshed)
	}

	if got := atomic.LoadInt32(requestCount); got != 2 {
		t.Fatalf("expected 2 Ollama requests, got %d", got)
	}
}

// TestModelDiscoveryConcurrentRequests verifies that concurrent requests
// do not trigger duplicate Ollama requests when the cache is empty.
func TestModelDiscoveryConcurrentRequests(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var upstreamOnce sync.Once

	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			// Hold the first upstream request until all callers are ready.
			upstreamOnce.Do(func() {
				close(upstreamStarted)
			})
			<-releaseUpstream

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3:latest"}]}`))
		},
		time.Minute,
	)

	const workers = 20

	start := make(chan struct{})
	calling := make(chan struct{}, workers)
	ready := sync.WaitGroup{}
	var wg sync.WaitGroup

	ready.Add(workers)
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()

			ready.Done()
			<-start

			calling <- struct{}{}

			models, err := discovery.GetModels(context.Background())
			if err != nil {
				t.Errorf("GetModels failed: %v", err)
				return
			}

			if len(models) != 1 || models[0].Name != "llama3:latest" {
				t.Errorf("unexpected models: %+v", models)
			}
		}()
	}

	// Wait until all goroutines are ready.
	ready.Wait()
	close(start)

	// Wait until all goroutines are about to call GetModels.
	for i := 0; i < workers; i++ {
		<-calling
	}

	// Wait until the first upstream request is blocked.
	<-upstreamStarted
	close(releaseUpstream)

	wg.Wait()

	if got := atomic.LoadInt32(requestCount); got != 1 {
		t.Fatalf("expected 1 Ollama request for concurrent calls, got %d", got)
	}
}

// TestModelDiscoveryErrorRecovery verifies that a failed refresh
// returns an error and a later successful request recovers.
func TestModelDiscoveryErrorRecovery(t *testing.T) {
	statusCode := http.StatusOK
	responseBody := `{"models":[{"name":"llama3:latest"}]}`

	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(responseBody))
		},
		time.Minute,
	)

	// First request successfully populates the cache.
	_, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("initial request failed: %v", err)
	}

	// Expire the cache and simulate an Ollama failure.
	expireCache(discovery)
	statusCode = http.StatusServiceUnavailable

	models, err := discovery.GetModels(context.Background())
	if err == nil {
		t.Fatal("expected an error when Ollama is unavailable")
	}

	if models != nil {
		t.Fatalf("expected nil models on refresh failure, got %+v", models)
	}

	// The implementation should not serve stale results on refresh failure.
	// A later successful request should recover.
	statusCode = http.StatusOK
	responseBody = `{"models":[{"name":"mistral:latest"}]}`

	recovered, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("expected recovery, got error: %v", err)
	}

	if len(recovered) != 1 || recovered[0].Name != "mistral:latest" {
		t.Fatalf("unexpected recovered models: %+v", recovered)
	}

	if got := atomic.LoadInt32(requestCount); got != 3 {
		t.Fatalf("expected 3 Ollama requests, got %d", got)
	}
}

// TestModelDiscoveryReturnedSliceIsIndependent verifies that
// modifying a returned slice does not modify the cached result.
func TestModelDiscoveryReturnedSliceIsIndependent(t *testing.T) {
	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3:latest"}]}`))
		},
		time.Minute,
	)

	first, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// Modify the result returned to the caller.
	first[0].Name = "modified-model"

	second, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	if second[0].Name != "llama3:latest" {
		t.Fatalf("cached model was modified: got %q", second[0].Name)
	}

	if got := atomic.LoadInt32(requestCount); got != 1 {
		t.Fatalf("expected 1 Ollama request, got %d", got)
	}
}

// TestModelDiscoveryEmptyModelList verifies that an empty model list
// is cached and does not trigger repeated upstream requests.
func TestModelDiscoveryEmptyModelList(t *testing.T) {
	discovery, requestCount := newTestDiscovery(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		},
		time.Minute,
	)

	first, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	if len(first) != 0 {
		t.Fatalf("expected empty model list, got %+v", first)
	}

	if first == nil {
		t.Fatal("expected a non-nil empty model list")
	}

	second, err := discovery.GetModels(context.Background())
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	if len(second) != 0 {
		t.Fatalf("expected empty cached model list, got %+v", second)
	}

	if got := atomic.LoadInt32(requestCount); got != 1 {
		t.Fatalf("expected empty result to be cached; got %d requests", got)
	}
}
