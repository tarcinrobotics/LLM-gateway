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
Consumer endpoints require a valid generated API Key. The key must be passed in the `Authorization` header as a Bearer token.
`Authorization: Bearer <API_KEY>`

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
  "messages": [{"role": "user", "content": "Explain TLS."}],
  "stream": false
}
```
If `stream: true`, the gateway proxies Ollama's Server-Sent Events (SSE). Otherwise, it waits for the full response and returns JSON.

---

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
