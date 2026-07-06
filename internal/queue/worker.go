package queue

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"llm_gateway/internal/ollama"
)

// Job represents a chat completion request to be processed by a worker.
type Job struct {
	Ctx          context.Context
	Req          *ollama.ChatRequest
	OllamaClient *ollama.Client
	ResultChan   chan JobResult
}

// JobResult represents the output of processing a Job.
type JobResult struct {
	Response  *ollama.ChatResponse
	ChunkChan chan ollama.ChatResponse
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
	// Check if context has timed out or cancelled while waiting in queue
	if err := job.Ctx.Err(); err != nil {
		job.ResultChan <- JobResult{Err: err}
		return
	}

	if !job.Req.Stream {
		resp, err := job.OllamaClient.Chat(job.Ctx, job.Req, nil)
		job.ResultChan <- JobResult{Response: resp, Err: err}
		return
	}

	// For streaming, create a chunk channel
	chunkChan := make(chan ollama.ChatResponse, 50)

	// Send chunk channel back to handler immediately
	job.ResultChan <- JobResult{ChunkChan: chunkChan}

	// Fetch stream from Ollama and forward chunks to channel
	_, err := job.OllamaClient.Chat(job.Ctx, job.Req, func(chunk ollama.ChatResponse) error {
		select {
		case <-job.Ctx.Done():
			return job.Ctx.Err()
		case chunkChan <- chunk:
			return nil
		}
	})

	if err != nil {
		// Log but let closing the channel signal completion/failure to reader
		slog.Error("Ollama stream read error", slog.Any("error", err))
	}
	close(chunkChan)
}
