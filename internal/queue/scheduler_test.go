package queue

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm_gateway/internal/ollama"
)

func TestExpiredQueuedJobDoesNotStartInference(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		server.CloseClientConnections()
		server.Close()
	}()

	client := ollama.NewClient(server.URL, time.Second)
	wp := NewWorkerPool(1, 1)

	// Do not start workers yet: the job must remain queued.
	_, err := ScheduleJob(
		context.Background(),
		wp,
		client,
		&ollama.ChatRequest{Model: "test-model"},
		20*time.Millisecond,
		time.Second,
	)

	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("expected queue timeout, got %v", err)
	}

	// Process the expired job to verify that Ollama is never called.
	job := <-wp.queue
	wp.process(job)

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("expected zero Ollama requests, got %d", got)
	}
}

func TestInferenceTimeoutStartsWhenWorkerClaimsJob(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseServer := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseServer
	}))
	defer server.Close()

	client := ollama.NewClient(server.URL, time.Second)
	wp := NewWorkerPool(1, 1)

	queueCtx := context.Background()
	resultChan := make(chan JobResult, 1)
	pickedUp := make(chan struct{})
	claimMu := &sync.Mutex{}
	claimed := false

	job := Job{
		Ctx:              context.Background(),
		QueueCtx:         queueCtx,
		InferenceTimeout: 50 * time.Millisecond,
		Req:              &ollama.ChatRequest{Model: "test-model"},
		OllamaClient:     client,
		ResultChan:       resultChan,
		PickedUp:         pickedUp,
		ClaimMu:          claimMu,
		Claimed:          &claimed,
	}

	wp.process(job)
	close(releaseServer)

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("Ollama request never started")
	}

	result := <-resultChan

	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("expected inference deadline exceeded, got %v", result.Err)
	}

	select {
	case <-pickedUp:
	default:
		t.Fatal("expected worker to claim the job")
	}
}
