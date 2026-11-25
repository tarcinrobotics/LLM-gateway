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

## Request ID Policy

The gateway accepts an optional `X-Request-ID` header for request correlation.

The request boundary accepts exactly one value containing 1–128 ASCII characters
from this set: `A-Z`, `a-z`, `0-9`, hyphen (`-`) and underscore (`_`).

- A single valid value is preserved.
- A missing, empty, malformed, oversized or repeated header is replaced with a
  cryptographically generated server-side ID.
- The selected ID is used in the response `X-Request-ID` header, request context,
  structured application logs and audit events.
- Rejected input is not propagated as the request ID.
- The admin audit-event `request_id` filter uses the same format and length rules.
- Historical audit rows containing IDs outside this format remain stored but
  cannot be queried through this exact-match filter.

Clients must never place credentials, tokens, personal information or other
sensitive data in request IDs.

Format validation only checks syntax and length. It cannot identify a secret
or personal datum that happens to look like a valid identifier.

Deployments requiring stronger confidentiality should generate IDs server-side
and disregard client-provided values entirely.

### Audit metadata review

| Metadata                               | Source                              | Current handling                                                       | Follow-up                                                             |
| -------------------------------------- | ----------------------------------- | ---------------------------------------------------------------------- | --------------------------------------------------------------------- |
| `request_id`                           | Client header or server generator   | Validated and bounded at request boundary                              | Keep filter validation aligned                                        |
| `event_type`, `outcome`, `status_code` | Gateway code paths                  | Selected by code; outcome has database constraint; status is integer   | Use fixed internal values                                             |
| `api_key_id`                           | Authenticated API-key record        | Internal database identifier, not raw key                              | No change proposed                                                    |
| `error_code`                           | Gateway error handling              | Fixed error codes; raw upstream errors and credentials are not written | Continue using safe codes                                             |
| Legacy request-log `model`             | Client chat request                 | Stored as requested model name                                         | Consider a bounded model-name policy separately                       |
| Legacy request-log `ip_address`        | `X-Forwarded-For` or remote address | Forwarding header is currently trusted when present                    | Configure trusted proxies and validate forwarded addresses separately |
| `created_at`                           | Database                            | Database-generated timestamp                                           | No change proposed                                                    |

The legacy request-log fields are outside this request-ID change.
Unrelated metadata should be reviewed with the maintainer before
modifying its behavior.

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

## Cross-Origin Resource Sharing (CORS)

The gateway uses an explicit CORS allowlist to control which browser-based frontends may access the API from a different origin.

CORS is configured in `config.yaml`:

```yaml
cors:
  allowed_origins: []
```

An empty list is the default and grants no cross-origin browser access.

- `X-Request-ID`

The `X-Request-ID` response header is exposed to allowed browser origins.

### Local Development

If your frontend development server runs at `http://localhost:5173`, add this origin to the allowlist:

```yaml
cors:
  allowed_origins:
    - "http://localhost:5173"
```

This is a development example only. Production deployments must configure their actual trusted frontend origins.

### Origin Matching

- Origins must match exactly, including scheme, hostname, and port.
- `http://localhost:5173` and `https://localhost:5173` are different origins.
- Wildcard (`*`) origins are not supported.
- Do not include URL paths or trailing slashes in configured origins.

### Preflight Requests

For an allowed origin, the gateway supports CORS preflight requests for these methods:

- `GET`
- `POST`
- `DELETE`

The following request headers are permitted:

- `Authorization`
- `Content-Type`
- `X-API-Key`

Preflight requests with a disallowed origin, unsupported method, or unsupported requested header are rejected.

The gateway does not enable credentialed CORS.

### Authentication Is Still Required

CORS is not an authentication mechanism.

- Consumer endpoints still require valid API keys.
- Admin endpoints still require HTTP Basic Authentication.
- Allowing a frontend origin does not grant access to protected endpoints.
- Requests without an `Origin` header continue through the application normally.

## Request body limits

Incoming JSON bodies are bounded before decoding to prevent
unbounded request body reads:

- `POST /api/v1/chat`: 1 MiB (1,048,576 bytes).
- `POST /admin/keys`: 16 KiB (16,384 bytes).

Bodies larger than the endpoint limit receive
`413 Payload Too Large` with a clear JSON error.

Requests exactly at the limit are accepted.

The limits also apply when `Content-Length` is absent.

Malformed JSON that is within the limit continues to receive
`400 Bad Request`.

The chat limit applies to the incoming request only, not the
outgoing streaming response.

The limits are compile-time documented constants and are not
configurable at runtime.

Clients with chat prompts or conversation histories above 1 MiB
must trim or split those requests. API-key creation clients should
send only the small JSON fields required by the endpoint.
