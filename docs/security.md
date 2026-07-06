# Security & Configuration Guide

This document outlines how the LLM Gateway secures internal LLM access for deployments.

## Environment Variables (`.env`)

The Developer Portal and all `/admin/*` backend routes are secured via HTTP Basic Authentication. To configure this, create a `.env` file at the root of the project:

```env
ADMIN_USER=admin
ADMIN_PASS=SuperSecretPassword123!
```
- **ADMIN_USER:** The username to access the Developer Portal.
- **ADMIN_PASS:** The password to access the portal.

> **Security Note:** Do not commit your `.env` file to version control. It is already excluded in our `.gitignore`. The dashboard securely converts these credentials into a base64 encoded Basic Auth header to manage your gateway.

## Database & API Keys

- Consumer endpoints (`/api/v1/chat` and `/api/v1/models`) DO NOT use `.env` credentials. Instead, they require **API Keys**.
- API Keys are generated from the Developer Portal. When generated, a cryptographically secure 16-byte random token is generated.
- Only the **SHA-256 hash** of the API Key is ever saved in the SQLite database (`gateway.db`). 
- **If you lose the API key string, it cannot be recovered.** You must revoke it and generate a new one.

## TLS Encryption (HTTPS)

The server supports TLS 1.3 encryption for deploying over public or untrusted networks.

By default, the server is running in HTTP mode (`tls_enabled: false` in `config.yaml`). To enable encryption:
1. Open `config.yaml`.
2. Set `tls_enabled: true`.
3. Start the server.

### Automatic Certificate Generation
If you enable TLS, the `internal/tls/` package will look for `server.crt` and `server.key` inside the `certs/` folder. 
If these files do not exist, the gateway will **automatically generate self-signed RSA certificates** on boot.

For production deployments, replace the contents of the `certs/` folder with valid SSL certificates (e.g., from Let's Encrypt or your internal Corporate CA) to avoid browser security warnings.
