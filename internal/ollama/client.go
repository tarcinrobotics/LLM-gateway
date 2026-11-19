package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the client for interacting with a local or remote Ollama instance.
type Client struct {
	baseURL    string
	client     *http.Client
	chatClient *http.Client
}

// NewClient creates a new Ollama API client.
func NewClient(baseURL string, timeout time.Duration) *Client {
	client := &http.Client{
		Timeout: timeout,
	}

	return &Client{
		baseURL:    baseURL,
		client:     client,
		chatClient: &http.Client{Transport: client.Transport},
	}
}

// CheckHealth checks whether the Ollama service is reachable.
// It uses the lightweight /api/version endpoint without requiring a model
// or performing inference. The supplied context controls cancellation.
func (c *Client) CheckHealth(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/api/version", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create Ollama health request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama health check returned status code %d", resp.StatusCode)
	}

	return nil
}

// ListModels queries the /api/tags endpoint to dynamically fetch all loaded models.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	reqUrl := fmt.Sprintf("%s/api/tags", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", reqUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create list models request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send list models request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama api returned status code %d", resp.StatusCode)
	}

	var listResp ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to decode list models response: %w", err)
	}

	return listResp.Models, nil
}

// Chat handles both standard and streaming chat requests.
// For streaming requests (Stream = true), onChunk will be called for each chunk.
func (c *Client) Chat(ctx context.Context, chatReq *ChatRequest, onChunk func(ChatResponse) error) (*ChatResponse, error) {
	reqUrl := fmt.Sprintf("%s/api/chat", c.baseURL)

	payload, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqUrl, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Chat requests use the request context for their full lifetime.
	// The inference deadline is managed by the worker.
	resp, err := c.chatClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send chat request to Ollama: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, string(body))
	}

	if !chatReq.Stream {
		defer resp.Body.Close()
		var fullResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&fullResp); err != nil {
			return nil, fmt.Errorf("failed to decode non-streaming response: %w", err)
		}
		return &fullResp, nil
	}

	// Stream handling
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	var lastChunk ChatResponse
	completed := false

	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)

		if len(line) > 0 {
			var chunk ChatResponse
			if err := json.Unmarshal(line, &chunk); err != nil {
				return nil, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
			}

			lastChunk = chunk

			if onChunk != nil {
				if err := onChunk(chunk); err != nil {
					return nil, fmt.Errorf("callback error (e.g. client disconnect): %w", err)
				}
			}

			if chunk.Done {
				completed = true
				break
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading stream line: %w", readErr)
		}
	}

	if !completed {
		return nil, fmt.Errorf("ollama stream ended before completion signal")
	}

	return &lastChunk, nil
}
