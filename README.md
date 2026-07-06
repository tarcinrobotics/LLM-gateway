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

For detailed API documentation, check the `docs/` folder!
