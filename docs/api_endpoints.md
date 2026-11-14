# API Endpoints Documentation

The LLM Gateway is split into **Public**, **Consumer**, and **Admin** endpoints.

## 1. Public Endpoints

Public endpoints do not require any authentication.

### `GET /health`

Returns the status of the gateway, database connectivity, and the worker queue length.

- **Auth:** None
- **Response (200 OK):**

```json
{
  "status": "UP",
  "database": "healthy",
  "active_workers": 0,
  "queue_length": 0,
  "queue_capacity": 50
}
```

---

## 2. Consumer Endpoints

Consumer endpoints require a valid generated API Key. API keys must be provided through an HTTP request header using one of the following formats:

- `Authorization: Bearer <API_KEY>`
- `Authorization: <API_KEY>`
- `X-API-Key: <API_KEY>`

API keys passed through the `api_key` URL query parameter are no longer supported. Requests relying only on this parameter will receive `401 Unauthorized`.

### `GET /api/v1/models`

Retrieves a list of available models from the backend Ollama instance.

- **Auth:** API Key
- **Response (200 OK):**

```json
{
  "models": [
    {
      "name": "qwen2.5:3b-instruct",
      "size": 1929912432
    }
  ]
}
```

### `POST /api/v1/chat`

Sends a prompt to a specific LLM.

- **Auth:** API Key
- **Request Body:**

```json
{
  "model": "qwen2.5:3b-instruct",
  "messages": [{ "role": "user", "content": "Explain TLS." }],
  "stream": false
}
```

If `stream: true`, the gateway proxies Ollama's Server-Sent Events (SSE). Otherwise, it waits for the full response and returns JSON.

#### Streaming Behavior

When streaming is enabled:

- The gateway forwards generated content as SSE `data:` events.
- A stream is considered successful only when Ollama sends its final completion chunk with `done: true`.
- If Ollama fails before the first chunk, the gateway returns an HTTP error before starting the SSE response:
  - `502 Bad Gateway` for upstream streaming failures.
  - `504 Gateway Timeout` when inference exceeds its configured timeout.
- If an error occurs after SSE headers have been sent, the gateway sends an SSE error event instead of changing the HTTP status:

```text
event: error
data: {"error":"Generation failed"}
```

- The HTTP status remains `200 OK` after streaming begins, even if generation subsequently fails. Check the SSE events and request audit logs to determine the generation outcome.
- If the client disconnects, the gateway cancels the associated upstream inference request.
- Internal upstream error details are logged by the gateway but are not exposed in SSE error messages.

#### Timeout Configuration

The gateway uses separate timeouts for queue waiting and Ollama inference.

| Setting               | Configuration Key                  |     Default | Description                                                                             |
| --------------------- | ---------------------------------- | ----------: | --------------------------------------------------------------------------------------- |
| Queue timeout         | `queue.timeout_seconds`            |  30 seconds | Maximum time a request can wait in the worker queue.                                    |
| Inference timeout     | `ollama.inference_timeout_seconds` | 120 seconds | Maximum time allowed for Ollama inference after a worker starts processing the request. |
| Ollama client timeout | `ollama.client_timeout_seconds`    |  30 seconds | HTTP client timeout used for Ollama operations other than chat inference.               |

Example configuration in `config.yaml`:

```yaml
queue:
  timeout_seconds: 30

ollama:
  url: "http://localhost:11434"
  client_timeout_seconds: 30
  inference_timeout_seconds: 120
```

## 3. Admin Endpoints

Admin endpoints require HTTP Basic Auth using the credentials defined in your `.env` file (`ADMIN_USER` and `ADMIN_PASS`).
`Authorization: Basic <base64(user:pass)>`

### `GET /admin/metrics`

Returns runtime latency and performance statistics.

### `GET /admin/keys`

Lists all generated API keys (excluding the raw secret keys).

### `POST /admin/keys`

Generates a new API key.

- **Request Body:**

```json
{
  "user_id": 1,
  "role_id": 1,
  "rate_limit_rpm": 60
}
```

### `DELETE /admin/keys/{id}`

Revokes and permanently deletes an API key.

### `GET /admin/logs`

Retrieves a list of the 50 most recent request logs, detailing the status codes, latencies, IP addresses, and consumed tokens.
