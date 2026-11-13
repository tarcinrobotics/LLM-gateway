# Security & Configuration Guide

This document outlines how the LLM Gateway secures internal LLM access for deployments.

## Environment Variables (`.env`)

The Developer Portal and all `/admin/*` backend routes are secured via HTTP Basic Authentication. To configure this, create a `.env` file at the root of the project:

```env
ADMIN_USER=admin
ADMIN_PASS=your_strong_password_here
```

- **ADMIN_USER:** The username to access the Developer Portal.
- **ADMIN_PASS:** The password to access the portal.

> **Security Note:** Do not commit your `.env` file to version control. It is already excluded in our `.gitignore`. The dashboard securely converts these credentials into a base64 encoded Basic Auth header to manage your gateway.

## Database & API Keys

- Consumer endpoints (`/api/v1/chat` and `/api/v1/models`) DO NOT use `.env` credentials. Instead, they require **API Keys**.
- API Keys are generated from the Developer Portal. When generated, a cryptographically secure 16-byte random token is generated.
- Only the **SHA-256 hash** of the API Key is ever saved in the SQLite database (`gateway.db`).
- **If you lose the API key string, it cannot be recovered.** You must revoke it and generate a new one.

### Legacy Seeded API Key Cleanup

Earlier versions of the gateway automatically created a publicly known test API key during database initialization:

`gw_86e95dbb09274255564ca7d9e63dd13b`

New installations no longer create this key automatically.

**Important:** Removing the automatic seeding code does not remove this key from databases initialized by earlier versions. Existing installations must revoke it manually.

To identify and revoke the legacy key:

1. Start the gateway and authenticate using the configured admin credentials.
2. Open `GET /admin/keys` to list the API keys registered in the database.
3. Look for the legacy key using its prefix: `gw_86e95dbb`.
4. Note the corresponding key ID.
5. Revoke the key using `DELETE /admin/keys/{id}`, replacing `{id}` with the identified key ID.

The admin API requires HTTP Basic Authentication.

For example:

```bash
curl -k -u "ADMIN_USER:ADMIN_PASS" \
  https://localhost:8443/admin/keys
```

To revoke the identified key:

```bash
curl -k -u "ADMIN_USER:ADMIN_PASS" \
  -X DELETE https://localhost:8443/admin/keys/KEY_ID
```

Replace the placeholders with your configured credentials and the actual key ID.

Do not revoke other API keys unintentionally. The legacy key is not automatically removed from existing databases.

### API Key Authentication

API keys must be sent through HTTP request headers. The gateway supports:

- `Authorization: Bearer <API_KEY>`
- `Authorization: <API_KEY>`
- `X-API-Key: <API_KEY>`

**Security Note:** API keys passed through the `api_key` URL query parameter are no longer accepted. URL query parameters can appear in browser history, server logs, and other request records.

**Migration:** Clients previously using `?api_key=<API_KEY>` must move their API key to one of the supported request headers. Requests containing only the query parameter receive `401 Unauthorized`.

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
