package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"llm_gateway/internal/ollama"
)

// ScheduleJob submits a chat request and waits for a worker to claim it.
// Queue timeout applies only until worker pickup. Inference timeout starts
// inside the worker after the job has been claimed.
var ErrQueueTimeout = errors.New("queue wait timeout")

func ScheduleJob(
	ctx context.Context,
	wp *WorkerPool,
	client *ollama.Client,
	req *ollama.ChatRequest,
	queueTimeout time.Duration,
	inferenceTimeout time.Duration,
) (JobResult, error) {
	queueCtx, cancelQueue := context.WithTimeout(ctx, queueTimeout)
	defer cancelQueue()

	resultChan := make(chan JobResult, 1)
	pickedUp := make(chan struct{})

	claimMu := &sync.Mutex{}
	claimed := false

	job := Job{
		Ctx:              ctx,
		QueueCtx:         queueCtx,
		InferenceTimeout: inferenceTimeout,
		Req:              req,
		OllamaClient:     client,
		ResultChan:       resultChan,
		PickedUp:         pickedUp,
		ClaimMu:          claimMu,
		Claimed:          &claimed,
	}

	if err := wp.Submit(job); err != nil {
		return JobResult{}, fmt.Errorf("scheduler queue full: %w", err)
	}

	// Wait until a worker claims the job or the queue deadline expires.
	select {
	case <-pickedUp:
		// The worker has claimed the job. Queue timeout no longer applies.
	case <-queueCtx.Done():
		if ctx.Err() != nil {
			return JobResult{}, ctx.Err()
		}

		claimMu.Lock()
		wasClaimed := claimed
		claimMu.Unlock()

		if !wasClaimed {
			return JobResult{}, fmt.Errorf("%w: %v", ErrQueueTimeout, queueCtx.Err())
		}
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}

	// For streaming, return the channels as soon as the worker creates them.
	// For non-streaming, wait for the completed inference result.
	select {
	case res := <-resultChan:
		return res, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}
