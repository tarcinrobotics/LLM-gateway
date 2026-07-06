package queue

import (
	"context"
	"fmt"

	"llm_gateway/internal/ollama"
)

// ScheduleJob schedules a chat completion job on the worker pool.
// It returns a JobResult which contains either the final response (non-stream)
// or a channel of streaming chunks.
func ScheduleJob(ctx context.Context, wp *WorkerPool, client *ollama.Client, req *ollama.ChatRequest) (JobResult, error) {
	resultChan := make(chan JobResult, 1)
	job := Job{
		Ctx:          ctx,
		Req:          req,
		OllamaClient: client,
		ResultChan:   resultChan,
	}

	// Try to submit to the queue
	if err := wp.Submit(job); err != nil {
		return JobResult{}, fmt.Errorf("scheduler queue full: %w", err)
	}

	// Wait for worker pickup or context expiration
	select {
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	case res := <-resultChan:
		return res, nil
	}
}
