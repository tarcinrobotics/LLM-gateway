package handlers

import (
	"encoding/json"
	"net/http"

	"llm_gateway/internal/ollama"
)

// ModelsHandler exposes available models from the cached discovery service.
func ModelsHandler(discovery *ollama.ModelDiscovery) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := discovery.GetModels(r.Context())
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Failed to fetch model discovery: " + err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": models,
		})
	}
}
