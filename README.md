# LLM Security Gateway

A high-performance, secure Go-based reverse proxy and gateway for Ollama LLMs. It features Rate Limiting, Role-Based Access Control (RBAC), Queue Management, and TLS 1.3 encryption.

## Features

- 🚀 **Asynchronous Queueing:** Uses FIFO worker pools to handle concurrent LLM requests without overwhelming the backend.
- 🔒 **Security:** HTTP Basic Auth for admin routes, API Keys for consumers.
- 🛑 **Rate Limiting:** In-memory Token Bucket per API Key.
- 📜 **Audit Logging:** Logs all prompts, tokens, latencies, and metadata directly to an internal SQLite database.
- 🎨 **Developer Portal:** Embedded React dashboard for managing API Keys and chatting directly with models.

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
