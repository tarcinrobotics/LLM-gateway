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
	baseURL string
	client  *http.Client
}

// NewClient creates a new Ollama API client.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: timeout,
		},
	}
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

	// If streaming, disable default HTTP client timeout and let context control it
	var clientToUse *http.Client
	if chatReq.Stream {
		// Create a separate client without a hard response read timeout to allow long streams
		clientToUse = &http.Client{
			Transport: c.client.Transport,
		}
	} else {
		clientToUse = c.client
	}

	resp, err := clientToUse.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send chat request to Ollama: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Attempt to read error message
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
	for {
		// Read line by line (Ollama streams newline-delimited JSON chunks)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading stream line: %w", err)
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return nil, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
		}

		// Keep track of the last chunk so we can extract final metrics
		lastChunk = chunk

		// Invoke callback
		if onChunk != nil {
			if err := onChunk(chunk); err != nil {
				return nil, fmt.Errorf("callback error (e.g. client disconnect): %w", err)
			}
		}

		if chunk.Done {
			break
		}
	}

	return &lastChunk, nil
}
