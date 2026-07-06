package metrics

import (
	"sync/atomic"
)

// Metrics holds global server statistics.
type Metrics struct {
	TotalRequests      uint64 `json:"total_requests"`
	SuccessfulRequests uint64 `json:"successful_requests"`
	FailedRequests     uint64 `json:"failed_requests"`
	LatencySumMs       uint64 `json:"latency_sum_ms"`
}

// GlobalMetrics is the singleton metrics instance.
var GlobalMetrics Metrics

// RecordRequest tracks a completed request's success and latency.
func RecordRequest(success bool, latencyMs int64) {
	atomic.AddUint64(&GlobalMetrics.TotalRequests, 1)
	if success {
		atomic.AddUint64(&GlobalMetrics.SuccessfulRequests, 1)
	} else {
		atomic.AddUint64(&GlobalMetrics.FailedRequests, 1)
	}
	if latencyMs > 0 {
		atomic.AddUint64(&GlobalMetrics.LatencySumMs, uint64(latencyMs))
	}
}

// GetMetrics returns a snapshot of current metrics.
func GetMetrics() Metrics {
	return Metrics{
		TotalRequests:      atomic.LoadUint64(&GlobalMetrics.TotalRequests),
		SuccessfulRequests: atomic.LoadUint64(&GlobalMetrics.SuccessfulRequests),
		FailedRequests:     atomic.LoadUint64(&GlobalMetrics.FailedRequests),
		LatencySumMs:       atomic.LoadUint64(&GlobalMetrics.LatencySumMs),
	}
}
