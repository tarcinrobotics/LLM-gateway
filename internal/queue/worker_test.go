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

func TestSubmitReturnsQueueFullWhenCapacityReached(t *testing.T) {
	wp := NewWorkerPool(1, 1)

	// Fill the queue without starting workers.
	err := wp.Submit(Job{})
	if err != nil {
		t.Fatalf("first submission should succeed, got: %v", err)
	}

	// The second submission should be rejected.
	err = wp.Submit(Job{})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got: %v", err)
	}

	if got := wp.QueueLength(); got != 1 {
		t.Fatalf("expected queue length 1, got: %d", got)
	}
}

func TestSubmitRejectedAfterShutdown(t *testing.T) {
	wp := NewWorkerPool(1, 2)

	wp.BeginShutdown()

	err := wp.Submit(Job{})
	if !errors.Is(err, ErrWorkerPoolStopped) {
		t.Fatalf("expected ErrWorkerPoolStopped, got: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("Wait after shutdown failed: %v", err)
	}
}

func TestRepeatedShutdownIsSafe(t *testing.T) {
	wp := NewWorkerPool(1, 2)

	// Repeated calls must not panic.
	wp.BeginShutdown()
	wp.BeginShutdown()
	wp.BeginShutdown()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("Wait after repeated shutdown failed: %v", err)
	}

	// Calling Wait again must also be safe.
	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("second Wait failed: %v", err)
	}
}

func TestConcurrentSubmitAndShutdown(t *testing.T) {
	wp := NewWorkerPool(1, 100)

	const submissions = 100

	var wg sync.WaitGroup
	start := make(chan struct{})

	errs := make(chan error, submissions)

	// Launch concurrent submitters.
	for i := 0; i < submissions; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start
			errs <- wp.Submit(Job{})
		}()
	}

	// Launch shutdown concurrently with submissions.
	wg.Add(1)
	go func() {
		defer wg.Done()

		<-start
		wp.BeginShutdown()
	}()

	close(start)
	wg.Wait()
	close(errs)

	// Only successful submissions, queue-full errors,
	// or shutdown errors are acceptable.
	for err := range errs {
		if err == nil ||
			errors.Is(err, ErrQueueFull) ||
			errors.Is(err, ErrWorkerPoolStopped) {
			continue
		}

		t.Errorf("unexpected submission error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("Wait after concurrent shutdown failed: %v", err)
	}

	// All accepted jobs should remain within queue capacity.
	if got := wp.QueueLength(); got > wp.QueueCapacity() {
		t.Fatalf(
			"queue length %d exceeds capacity %d",
			got,
			wp.QueueCapacity(),
		)
	}
}

func TestStartAfterShutdownDoesNotStartWorkers(t *testing.T) {
	wp := NewWorkerPool(2, 2)

	wp.BeginShutdown()

	// Starting a stopped pool should be a safe no-op.
	wp.Start()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("Wait after Start following shutdown failed: %v", err)
	}

	if got := wp.ActiveWorkers(); got != 0 {
		t.Fatalf("expected 0 active workers, got: %d", got)
	}
}

func TestQueueFullJobNeverReachesOllama(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := ollama.NewClient(server.URL, time.Second)
	wp := NewWorkerPool(1, 1)

	// Fill the queue before starting workers.
	if err := wp.Submit(Job{}); err != nil {
		t.Fatalf("failed to fill queue: %v", err)
	}

	// This request must be rejected because the queue is full.
	_, err := ScheduleJob(
		context.Background(),
		wp,
		client,
		&ollama.ChatRequest{Model: "test-model"},
		time.Second,
		time.Second,
	)

	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got: %v", err)
	}

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("expected zero Ollama requests, got: %d", got)
	}
}

func TestForcedShutdownCancelsActiveJobAndSkipsQueuedJob(t *testing.T) {
	var requestCount atomic.Int32

	requestStarted := make(chan struct{})
	activeRequestCancelled := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		close(requestStarted)

		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()

		// Wait until the client cancels the HTTP request.
		<-r.Context().Done()
		close(activeRequestCancelled)
	}))
	defer server.Close()

	client := ollama.NewClient(server.URL, 10*time.Second)
	wp := NewWorkerPool(1, 1)
	wp.Start()

	firstResult := make(chan JobResult, 1)
	secondResult := make(chan JobResult, 1)

	go func() {
		result, err := ScheduleJob(
			context.Background(),
			wp,
			client,
			&ollama.ChatRequest{Model: "test-model"},
			5*time.Second,
			10*time.Second,
		)

		if err != nil {
			firstResult <- JobResult{Err: err}
			return
		}

		firstResult <- result
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first Ollama request never started")
	}

	go func() {
		result, err := ScheduleJob(
			context.Background(),
			wp,
			client,
			&ollama.ChatRequest{Model: "test-model"},
			5*time.Second,
			10*time.Second,
		)

		if err != nil {
			secondResult <- JobResult{Err: err}
			return
		}

		secondResult <- result
	}()

	deadline := time.Now().Add(2 * time.Second)

	for wp.QueueLength() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("second job was not queued")
		}
		time.Sleep(time.Millisecond)
	}

	// Give the pool a deadline to finish gracefully.
	// The active inference should be cancelled when this expires.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := wp.Wait(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline exceeded, got: %v", err)
	}

	select {
	case <-activeRequestCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("active Ollama request was not cancelled")
	}

	select {
	case result := <-firstResult:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("expected active job cancellation, got: %v", result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first job did not return")
	}

	select {
	case result := <-secondResult:
		if !errors.Is(result.Err, ErrWorkerPoolStopped) {
			t.Fatalf("expected ErrWorkerPoolStopped for queued job, got: %v", result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second job did not return")
	}

	if got := requestCount.Load(); got != 1 {
		t.Fatalf("expected exactly one Ollama request, got: %d", got)
	}
}

func TestGracefulShutdownProcessesActiveAndQueuedJobs(t *testing.T) {
	var requestCount atomic.Int32

	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		if count == 1 {
			close(firstRequestStarted)
			<-releaseFirstRequest
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	client := ollama.NewClient(server.URL, 5*time.Second)

	wp := NewWorkerPool(1, 1)
	wp.Start()

	firstResult := make(chan error, 1)

	go func() {
		_, err := ScheduleJob(
			context.Background(),
			wp,
			client,
			&ollama.ChatRequest{},
			3*time.Second,
			3*time.Second,
		)
		firstResult <- err
	}()

	// Wait until the first job is actively being processed.
	select {
	case <-firstRequestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first job did not reach the fake Ollama server")
	}

	secondResult := make(chan error, 1)

	go func() {
		_, err := ScheduleJob(
			context.Background(),
			wp,
			client,
			&ollama.ChatRequest{},
			3*time.Second,
			3*time.Second,
		)
		secondResult <- err
	}()

	// Ensure the second job is waiting in the queue.
	deadline := time.Now().Add(2 * time.Second)
	for len(wp.queue) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("second job was not added to the queue")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Begin graceful shutdown: reject new jobs but drain existing jobs.
	wp.BeginShutdown()

	// Allow the active job to finish.
	close(releaseFirstRequest)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := wp.Wait(ctx); err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}

	if err := <-firstResult; err != nil {
		t.Errorf("active job failed during graceful shutdown: %v", err)
	}

	if err := <-secondResult; err != nil {
		t.Errorf("queued job failed during graceful shutdown: %v", err)
	}

	if got := requestCount.Load(); got != 2 {
		t.Errorf("expected 2 Ollama requests, got %d", got)
	}
}

func TestWaitReturnsAtDeadlineWhenWorkerDoesNotExit(t *testing.T) {
	wp := NewWorkerPool(1, 1)

	releaseWorker := make(chan struct{})
	workerExited := make(chan struct{})

	// Simulate a worker that does not respond to shutdown cancellation.
	wp.wg.Add(1)

	go func() {
		defer wp.wg.Done()
		defer close(workerExited)

		<-releaseWorker
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := wp.Wait(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}

	if elapsed > 500*time.Millisecond {
		t.Fatalf("Wait exceeded shutdown deadline: returned after %v", elapsed)
	}

	if wp.shutdownCtx.Err() == nil {
		t.Fatal("expected shutdown context to be cancelled")
	}

	// The simulated worker should still be blocked.
	select {
	case <-workerExited:
		t.Fatal("worker exited before its release signal")
	default:
	}

	// Release the worker so the test can clean up.
	close(releaseWorker)

	select {
	case <-wp.shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("worker pool did not finish cleanup")
	}
}
