# LLM Security Gateway

A high-performance, secure Go-based reverse proxy and gateway for Ollama LLMs. It features Rate Limiting, Role-Based Access Control (RBAC), Queue Management, and TLS 1.3 encryption.

## Features

- 🚀 **Asynchronous Queueing:** Uses FIFO worker pools to handle concurrent LLM requests without overwhelming the backend.
- 🔒 **Security:** HTTP Basic Auth for admin routes, API Keys for consumers.
- 🛑 **Rate Limiting:** In-memory Token Bucket per API Key.
- 📜 **Audit Logging:** Records request outcomes, HTTP status codes, timestamps, request IDs, API key IDs, and safe error codes in SQLite without storing raw prompts, responses, API keys, or credentials.
- 🎨 **Developer Portal:** Embedded React dashboard for managing API Keys and chatting directly with models.

## Audit Logging

The gateway maintains structured audit records in SQLite to track request outcomes and support security monitoring and troubleshooting.

### Audit Events

Audit events are stored separately in the `audit_events` table.

Each event can contain:

| Field         | Description                                                                           |
| ------------- | ------------------------------------------------------------------------------------- |
| `request_id`  | Unique identifier associated with the request.                                        |
| `event_type`  | Type of event, such as authentication failure, permission denial, or queue rejection. |
| `outcome`     | Request outcome: `success`, `failure`, or `cancelled`.                                |
| `status_code` | HTTP status code associated with the event.                                           |
| `api_key_id`  | API key identifier, when available.                                                   |
| `error_code`  | Safe error classification, when applicable.                                           |
| `created_at`  | Timestamp when the event was recorded.                                                |

### Audited Request Outcomes

The gateway records relevant events across the request lifecycle, including:

- Successful chat requests.
- Invalid request payloads and missing model information.
- Authentication failures, including missing, invalid, inactive, or expired API keys.
- Permission-check failures and model access denials.
- Rate-limit violations.
- Queue rejections, queue timeouts, and worker shutdown failures.
- Upstream inference errors and inference timeouts.
- Streaming failures and incomplete streams.
- Client cancellations.

### Privacy and Data Protection

- Raw prompts and model responses are not stored in `audit_events`.
- API keys and authentication credentials are never stored in audit records.
- Safe error codes are used instead of raw upstream error messages.
- Audit database write failures do not replace or change the original request outcome.

### Legacy Request Logs

The existing `request_logs` table is preserved for compatibility with existing functionality. It stores legacy request metadata, such as model information, client IP, token counts, and latency.

The separate `audit_events` table adds structured security and request-outcome tracking without changing the legacy table schema.

## Getting Started

1. Set up your admin credentials in `.env` (see `docs/security.md`).
2. Run `./setup.sh` to compile the React UI and the Go server into a single binary.
3. Run `./start.sh` to launch the Gateway on `http://localhost:8080`.

### CORS Configuration for Local Development

If your frontend runs on a different origin from the gateway, configure its origin in `config.yaml`:

```yaml
cors:
  allowed_origins:
    - "http://localhost:5173"
```

The example above assumes a local frontend development server running on port `5173`.

By default, `cors.allowed_origins` is an empty list, so cross-origin browser access is not permitted until trusted origins are configured.

For production deployments, replace the development origin with the exact trusted frontend origin. See `docs/security.md` for CORS security rules and `docs/api_endpoints.md` for preflight behavior.

## Timeout Configuration

The gateway uses separate timeouts for queue waiting and Ollama inference.

Default settings in `config.yaml`:

| Setting                                                 |     Default |
| ------------------------------------------------------- | ----------: |
| Queue timeout (`queue.timeout_seconds`)                 |  30 seconds |
| Inference timeout (`ollama.inference_timeout_seconds`)  | 120 seconds |
| Ollama client timeout (`ollama.client_timeout_seconds`) |  30 seconds |

Queue timeout controls how long a request waits for an available worker. Inference timeout starts when a worker begins processing the request and controls the time allowed for Ollama inference.

The Ollama client timeout must not impose a shorter limit on chat inference.

For detailed API documentation, check `docs/api_endpoints.md`.

## Health and Readiness Checks

The gateway provides two endpoints for monitoring its health and readiness.

### `GET /health`

Reports the gateway's existing health information, including database status and worker pool statistics.

| HTTP Status               | Description                     |
| ------------------------- | ------------------------------- |
| `200 OK`                  | SQLite database is reachable.   |
| `503 Service Unavailable` | SQLite database is unavailable. |

The response includes the gateway status, database health, active workers, queue length, and queue capacity.

### `GET /ready`

Checks whether the gateway is ready to serve requests by verifying SQLite database connectivity and actual Ollama service reachability through the lightweight `/api/version` endpoint.

| HTTP Status               | Description                                                                     |
| ------------------------- | ------------------------------------------------------------------------------- |
| `200 OK`                  | Both SQLite and Ollama are reachable, and the worker pool is not shutting down. |
| `503 Service Unavailable` | Either dependency is unavailable, a check times out, or shutdown has started.   |

Example successful response:

```json
{
  "status": "ready",
  "database": "ready",
  "ollama": "ready"
}
```

Example response when a dependency is unavailable:

```json
{
  "status": "not_ready",
  "database": "ready",
  "ollama": "unavailable"
}
```

The readiness checks use a combined timeout of 2 seconds and respect incoming request cancellation. Readiness is also disabled as soon as worker pool shutdown begins.

The `/health` endpoint retains its existing response format and behavior, while `/ready` provides dependency-aware readiness information for deployment and monitoring systems.

## Queue Management

The gateway uses a bounded in-memory FIFO queue and a fixed-size worker pool to manage Ollama requests.

When all workers are busy and the queue reaches its configured capacity, additional requests are rejected immediately instead of being accepted indefinitely.

### Queue-related HTTP responses

| Condition                    | HTTP Status               | Description                                                                              |
| ---------------------------- | ------------------------- | ---------------------------------------------------------------------------------------- |
| Worker queue is full         | `503 Service Unavailable` | The gateway cannot accept additional requests because the queue is full.                 |
| Queue wait timeout           | `504 Gateway Timeout`     | The request exceeded the configured queue waiting time before a worker became available. |
| Worker pool is shutting down | `503 Service Unavailable` | The gateway is shutting down and cannot accept new requests.                             |
| Client cancellation          | No guaranteed response    | Processing is cancelled when the client disconnects or cancels the request.              |

Queue saturation, queue wait timeout, and client cancellation are handled as separate conditions.

Queue-full rejection happens immediately when the queue has no available capacity, whereas queue wait timeout occurs when a request remains queued longer than the configured queue timeout.

Rejected requests are not forwarded to Ollama.

## Graceful Shutdown

When the gateway receives an operating-system shutdown signal, it initiates a coordinated shutdown of the HTTP server and worker pool.

The shutdown process follows these rules:

1. **Stop accepting new jobs:** The worker pool stops accepting submissions as soon as shutdown begins. New requests are rejected.
2. **Process queued jobs:** Jobs already waiting in the queue are allowed to drain during graceful shutdown.
3. **Allow active requests to finish:** Requests already being processed are given an opportunity to complete within the configured server shutdown timeout.
4. **Force cancellation after the deadline:** If the shutdown deadline expires, active inference requests are cancelled through their request contexts. Jobs that have not started processing are skipped or rejected.
5. **Repeated shutdown calls:** Shutdown operations are safe to invoke repeatedly and do not close the queue multiple times.

### Shutdown timeout configuration

The graceful-shutdown deadline is controlled by `server.shutdown_timeout_seconds` in `config.yaml`.

Default configuration:

```yaml
server:
  shutdown_timeout_seconds: 10
```

The HTTP server and worker pool share this shutdown deadline. If the deadline expires before shutdown completes, the HTTP server is force-closed and active inference requests are cancelled. Queued jobs that have not started are skipped.

You can increase or decrease this value depending on how much time your deployment should allow for in-flight requests to finish.

### Shutdown limitations

Graceful shutdown is best-effort and limited by the configured shutdown deadline. Requests that cannot finish within this period may be cancelled or abandoned. Completion is not guaranteed if the upstream service does not cooperate with cancellation or the process terminates unexpectedly.
