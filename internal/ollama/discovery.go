package ollama

import (
	"context"
	"sync"
	"time"
)

// ModelDiscovery manages a TTL-cached list of available Ollama models.
type ModelDiscovery struct {
	client      *Client
	cachedList  []ModelInfo
	lastFetched time.Time
	cacheTTL    time.Duration
	mu          sync.RWMutex
}

// NewModelDiscovery initializes ModelDiscovery.
func NewModelDiscovery(client *Client, cacheTTL time.Duration) *ModelDiscovery {
	return &ModelDiscovery{
		client:   client,
		cacheTTL: cacheTTL,
	}
}

// GetModels retrieves the list of models, utilizing the cache if valid.
func (md *ModelDiscovery) GetModels(ctx context.Context) ([]ModelInfo, error) {
	md.mu.RLock()
	if time.Since(md.lastFetched) < md.cacheTTL && md.cachedList != nil {
		defer md.mu.RUnlock()
		// Return a copy to avoid race conditions on slices
		copied := make([]ModelInfo, len(md.cachedList))
		copy(copied, md.cachedList)
		return copied, nil
	}
	md.mu.RUnlock()

	md.mu.Lock()
	defer md.mu.Unlock()

	// Double-check cache under write lock
	if time.Since(md.lastFetched) < md.cacheTTL && md.cachedList != nil {
		copied := make([]ModelInfo, len(md.cachedList))
		copy(copied, md.cachedList)
		return copied, nil
	}

	models, err := md.client.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	md.cachedList = models
	md.lastFetched = time.Now()

	copied := make([]ModelInfo, len(models))
	copy(copied, models)
	return copied, nil
}

// HasModel checks if the model list has the specified model.
func (md *ModelDiscovery) HasModel(ctx context.Context, modelName string) (bool, error) {
	models, err := md.GetModels(ctx)
	if err != nil {
		return false, err
	}

	// Normalise matching
	for _, m := range models {
		if m.Name == modelName {
			return true, nil
		}
		// Match short names (e.g. "llama3" matches "llama3:latest")
		if m.Name == modelName+":latest" {
			return true, nil
		}
		if modelName == m.Name+":latest" {
			return true, nil
		}
	}
	return false, nil
}
