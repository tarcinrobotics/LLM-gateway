#!/bin/bash

set -Eeuo pipefail

# Test configuration
PORT=8081
BASE_URL="http://localhost:$PORT"
ADMIN_USER="${ADMIN_USER:-admin}"

if [[ -z "${ADMIN_PASS:-}" ]]; then
    read -rsp "Enter admin password: " ADMIN_PASS
    echo
    export ADMIN_PASS
fi
MODEL_NAME="${MODEL_NAME:-qwen2.5-coder:3b}"

# Isolated test files
TEST_DIR=".test-tmp-$$"
CONFIG_PATH="$TEST_DIR/config.yaml"
SERVER_LOG="$TEST_DIR/server.log"
KEY_RESPONSE_FILE="$TEST_DIR/key-response.json"

SERVER_PID=""
API_KEY=""
API_KEY_ID=""

# Create an isolated test directory
mkdir -p "$TEST_DIR"

# Cleanup runs when the script exits, including on failure.
cleanup() {
    echo ""
    echo "--------------------------------------------------------"
    echo "Cleaning up smoke test..."
    echo "--------------------------------------------------------"

    # Revoke the temporary API key, if it was created.
    if [[ -n "$API_KEY_ID" ]]; then
        echo "Revoking temporary API key..."
        curl -sS -X DELETE \
            -u "$ADMIN_USER:$ADMIN_PASS" \
            "$BASE_URL/admin/keys/$API_KEY_ID" >/dev/null || true
    fi

    # Stop the gateway server.
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "Stopping server (PID: $SERVER_PID)..."
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi

    # Remove only this run's temporary files.
    rm -rf -- "$TEST_DIR"

    echo "Cleanup complete."
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Generate a temporary configuration.
cat > "$CONFIG_PATH" <<EOF
server:
  port: $PORT
  tls_enabled: false
  cert_file: "certs/server.crt"
  key_file: "certs/server.key"
  shutdown_timeout_seconds: 10

database:
  path: "$TEST_DIR/gateway.db"

ollama:
  url: "http://localhost:11434"
  client_timeout_seconds: 30

queue:
  workers: 5
  max_size: 50
  timeout_seconds: 30

logging:
  file_path: "$TEST_DIR/gateway.log"
  level: "info"
EOF

# Helper function to print test headers.
print_header() {
    echo ""
    echo "========================================================"
    echo "TEST: $1"
    echo "========================================================"
}

# Start gateway using the isolated configuration.
echo "--------------------------------------------------------"
echo "Starting isolated LLM Gateway Server..."
echo "--------------------------------------------------------"

CONFIG_PATH="$CONFIG_PATH" PORT="$PORT" TLS_ENABLED=false ./bin/server > "$SERVER_LOG" 2>&1 &
SERVER_PID=$!

# Wait for the health endpoint to become available.
echo "Waiting for server to start..."

SERVER_READY=false

for i in $(seq 1 30); do
    if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
        SERVER_READY=true
        break
    fi
    sleep 1
done

if [[ "$SERVER_READY" != true ]]; then
    echo "Server failed to start. Server log:"
    cat "$SERVER_LOG"
    exit 1
fi

echo "Server is ready."

# 1. Public health endpoint.
print_header "Health Endpoint (Public)"
curl -sS -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/health"

# 2. Model discovery without an API key.
print_header "Model Discovery without API Key (Should fail)"
curl -sS -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/api/v1/models"

# 3. Create a temporary API key through the admin API.
print_header "Admin: Create Temporary API Key"

CREATE_STATUS=$(curl -sS \
    -o "$KEY_RESPONSE_FILE" \
    -w "%{http_code}" \
    -X POST \
    -u "$ADMIN_USER:$ADMIN_PASS" \
    -H "Content-Type: application/json" \
    -d '{"user_id": 1, "role_id": 2, "rate_limit_rpm": 45}' \
    "$BASE_URL/admin/keys")

if [[ "$CREATE_STATUS" != "201" ]]; then
    echo "Temporary API key creation failed."
    echo "HTTP Status: $CREATE_STATUS"
    cat "$KEY_RESPONSE_FILE"
    exit 1
fi

# Extract the one-time key and its database ID.
API_KEY=$(python -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["plain_api_key"])' \
    "$KEY_RESPONSE_FILE")

API_KEY_ID=$(python -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["key_id"])' \
    "$KEY_RESPONSE_FILE")

if [[ -z "$API_KEY" || -z "$API_KEY_ID" ]]; then
    echo "Could not extract temporary API key details."
    exit 1
fi

echo "Temporary API key created successfully."
echo "Key ID: $API_KEY_ID"

# 4. Model discovery using the temporary key.
print_header "Model Discovery with Temporary API Key"
curl -sS \
    -H "Authorization: Bearer $API_KEY" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/api/v1/models"

# 5. List API keys.
print_header "Admin: List API Keys"
curl -sS \
    -u "$ADMIN_USER:$ADMIN_PASS" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/admin/keys"

# 6. Admin metrics.
print_header "Admin: View Performance Metrics"
curl -sS \
    -u "$ADMIN_USER:$ADMIN_PASS" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/admin/metrics"

# 7. Admin audit logs.
print_header "Admin: View Audit Logs"
curl -sS \
    -u "$ADMIN_USER:$ADMIN_PASS" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/admin/logs"

# 8. Non-streaming chat completion.
print_header "Chat Completion (Non-Streaming)"
curl -sS -X POST \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"model\": \"$MODEL_NAME\", \"messages\": [{\"role\": \"user\", \"content\": \"Hi! Say only 'Hello!'\"}], \"stream\": false}" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/api/v1/chat"

# 9. Streaming chat completion.
print_header "Chat Completion (Streaming)"
curl -sS -X POST \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"model\": \"$MODEL_NAME\", \"messages\": [{\"role\": \"user\", \"content\": \"Hi! Say only 'Hello!'\"}], \"stream\": true}" \
    -w "\nHTTP Status: %{http_code}\n" \
    "$BASE_URL/api/v1/chat"

echo ""
echo "Smoke tests completed."
echo "Temporary API key will now be revoked."
