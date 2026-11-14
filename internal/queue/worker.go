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
	wg         sync.WaitGroup
	active     int32
}

// NewWorkerPool instantiates a WorkerPool.
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	return &WorkerPool{
		maxWorkers: workers,
		maxQueue:   queueSize,
		queue:      make(chan Job, queueSize),
	}
}

// Start spawns the worker goroutines.
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
	slog.Info("Worker pool started", slog.Int("workers", wp.maxWorkers), slog.Int("queue_size", wp.maxQueue))
}

// Submit pushes a job to the queue. Returns an error if the queue is full.
func (wp *WorkerPool) Submit(job Job) error {
	select {
	case wp.queue <- job:
		return nil
	default:
		return errors.New("queue is full")
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

// Stop closes the queue and waits for workers to finish.
func (wp *WorkerPool) Stop() {
	close(wp.queue)
	wp.wg.Wait()
	slog.Info("Worker pool stopped gracefully")
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
	if !job.claimJob() {
		job.ResultChan <- JobResult{Err: job.QueueCtx.Err()}
		return
	}

	// Inference gets a fresh deadline only after the worker claims the job.
	inferenceCtx, cancel := context.WithTimeout(job.Ctx, job.InferenceTimeout)
	defer cancel()

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
			// Buffer the first chunk before notifying the handler.
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
			// The upstream failed before producing its first chunk.
			job.ResultChan <- JobResult{Err: err}
		} else {
			errChan <- err
			slog.Error("Ollama stream processing failed", slog.Any("error", err))
		}
	} else if !started {
		// Defensive: Chat returned without producing any chunks.
		job.ResultChan <- JobResult{
			Err: errors.New("ollama stream returned without producing chunks"),
		}
	}

	close(errChan)
	close(chunkChan)
}
