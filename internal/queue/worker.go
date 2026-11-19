package queue

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"llm_gateway/internal/ollama"
)

// Job represents a chat completion request waiting for a worker.
type Job struct {
	Ctx              context.Context
	QueueCtx         context.Context
	InferenceTimeout time.Duration
	Req              *ollama.ChatRequest
	OllamaClient     *ollama.Client
	ResultChan       chan JobResult
	PickedUp         chan struct{}
	ClaimMu          *sync.Mutex
	Claimed          *bool
}

// JobResult represents the output of processing a Job.
type JobResult struct {
	Response  *ollama.ChatResponse
	ChunkChan chan ollama.ChatResponse
	ErrChan   chan error
	Err       error
}

// WorkerPool manages the queue and active workers.
type WorkerPool struct {
	maxWorkers int
	maxQueue   int
	queue      chan Job

	wg     sync.WaitGroup
	active int32

	mu             sync.Mutex
	stopping       bool
	startOnce      sync.Once
	stopOnce       sync.Once
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	shutdownDone   chan struct{}
}

// NewWorkerPool instantiates a WorkerPool.
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	return &WorkerPool{
		maxWorkers:     workers,
		maxQueue:       queueSize,
		queue:          make(chan Job, queueSize),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
		shutdownDone:   make(chan struct{}),
	}
}

// Start spawns the worker goroutines.
func (wp *WorkerPool) Start() {
	started := false

	wp.startOnce.Do(func() {
		wp.mu.Lock()
		defer wp.mu.Unlock()

		if wp.stopping {
			return
		}

		for i := 0; i < wp.maxWorkers; i++ {
			wp.wg.Add(1)
			go wp.worker(i)
		}

		started = true
	})

	if started {
		slog.Info(
			"Worker pool started",
			slog.Int("workers", wp.maxWorkers),
			slog.Int("queue_size", wp.maxQueue),
		)
	}
}

// Submit pushes a job to the queue.
// It rejects submissions during shutdown and when the queue is full.
func (wp *WorkerPool) Submit(job Job) error {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.stopping {
		return ErrWorkerPoolStopped
	}

	select {
	case wp.queue <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// BeginShutdown stops accepting new jobs and closes the queue safely.
func (wp *WorkerPool) BeginShutdown() {
	wp.stopOnce.Do(func() {
		wp.mu.Lock()
		wp.stopping = true
		close(wp.queue)
		wp.mu.Unlock()

		go func() {
			wp.wg.Wait()
			close(wp.shutdownDone)
		}()
	})
}

// Wait waits for workers to finish until the provided deadline.
// If the deadline expires, it cancels active inference and returns
// immediately instead of waiting indefinitely for workers to exit.
func (wp *WorkerPool) Wait(ctx context.Context) error {
	wp.BeginShutdown()

	// Prefer successful completion if workers have already stopped,
	// even if the context deadline expires at approximately the same time.
	select {
	case <-wp.shutdownDone:
		slog.Info("Worker pool stopped gracefully")
		return nil
	default:
	}

	select {
	case <-wp.shutdownDone:
		slog.Info("Worker pool stopped gracefully")
		return nil

	case <-ctx.Done():
		// Cancel active inference requests.
		// Workers will finish their cleanup independently.
		wp.shutdownCancel()

		slog.Warn(
			"Worker pool shutdown deadline reached; active inference cancelled",
			slog.Any("error", ctx.Err()),
		)

		return ctx.Err()
	}
}

// Stop preserves the existing cleanup API.
func (wp *WorkerPool) Stop() {
	wp.BeginShutdown()

	if err := wp.Wait(context.Background()); err != nil {
		slog.Error("Worker pool shutdown failed", slog.Any("error", err))
	}
}

// ActiveWorkers returns the current number of workers executing jobs.
func (wp *WorkerPool) ActiveWorkers() int {
	return int(atomic.LoadInt32(&wp.active))
}

// QueueLength returns the current size of pending requests in the queue.
func (wp *WorkerPool) QueueLength() int {
	return len(wp.queue)
}

// QueueCapacity returns the configured maximum capacity of the queue.
func (wp *WorkerPool) QueueCapacity() int {
	return wp.maxQueue
}

// IsStopping reports whether the worker pool has begun shutting down.
func (wp *WorkerPool) IsStopping() bool {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	return wp.stopping
}

// claimJob atomically checks the queue deadline and marks the job as claimed.
func (job Job) claimJob() bool {
	job.ClaimMu.Lock()
	defer job.ClaimMu.Unlock()

	if job.QueueCtx.Err() != nil {
		return false
	}

	*job.Claimed = true
	close(job.PickedUp)
	return true
}

func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	slog.Debug("Worker started", slog.Int("worker_id", id))

	for job := range wp.queue {
		atomic.AddInt32(&wp.active, 1)
		wp.process(job)
		atomic.AddInt32(&wp.active, -1)
	}

	slog.Debug("Worker stopped", slog.Int("worker_id", id))
}

func (wp *WorkerPool) process(job Job) {
	// Do not start queued jobs after forced shutdown.
	if wp.shutdownCtx.Err() != nil {
		// Wake the scheduler so it can receive the shutdown error.
		if job.claimJob() {
			job.ResultChan <- JobResult{Err: ErrWorkerPoolStopped}
		} else {
			job.ResultChan <- JobResult{Err: job.QueueCtx.Err()}
		}
		return
	}

	if !job.claimJob() {
		job.ResultChan <- JobResult{Err: job.QueueCtx.Err()}
		return
	}

	// Inference gets a fresh deadline only after the job is claimed.
	inferenceCtx, cancel := context.WithTimeout(job.Ctx, job.InferenceTimeout)
	stopShutdown := context.AfterFunc(wp.shutdownCtx, cancel)

	defer cancel()
	defer stopShutdown()

	// Handle shutdown that happened immediately before inference started.
	if wp.shutdownCtx.Err() != nil {
		job.ResultChan <- JobResult{Err: ErrWorkerPoolStopped}
		return
	}

	if !job.Req.Stream {
		resp, err := job.OllamaClient.Chat(inferenceCtx, job.Req, nil)
		job.ResultChan <- JobResult{Response: resp, Err: err}
		return
	}

	chunkChan := make(chan ollama.ChatResponse, 50)
	errChan := make(chan error, 1)

	var started bool

	_, err := job.OllamaClient.Chat(inferenceCtx, job.Req, func(chunk ollama.ChatResponse) error {
		if !started {
			chunkChan <- chunk
			started = true

			job.ResultChan <- JobResult{
				ChunkChan: chunkChan,
				ErrChan:   errChan,
			}

			return nil
		}

		select {
		case <-inferenceCtx.Done():
			return inferenceCtx.Err()
		case chunkChan <- chunk:
			return nil
		}
	})

	if err != nil {
		if !started {
			job.ResultChan <- JobResult{Err: err}
		} else {
			errChan <- err
			slog.Error(
				"Ollama stream processing failed",
				slog.Any("error", err),
			)
		}
	} else if !started {
		job.ResultChan <- JobResult{
			Err: errors.New("ollama stream returned without producing chunks"),
		}
	}

	close(errChan)
	close(chunkChan)
}
