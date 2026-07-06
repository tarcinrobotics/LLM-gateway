#!/bin/bash

# Port and Key Configuration
PORT=8443
API_KEY="gw_86e95dbb09274255564ca7d9e63dd13b"

echo "--------------------------------------------------------"
echo "Starting LLM Gateway Server..."
echo "--------------------------------------------------------"
./bin/server > test_run.log 2>&1 &
SERVER_PID=$!

# Wait for server to start
sleep 2

# Helper function to print headers
print_header() {
    echo ""
    echo "========================================================"
    echo "TEST: $1"
    echo "========================================================"
}

# 1. Test Health Endpoint (Public)
print_header "Health Endpoint (Public)"
curl -k -s -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/health

# 2. Test Model Discovery without API Key (Should fail)
print_header "Model Discovery without API Key (Should fail)"
curl -k -s -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/api/v1/models

# 3. Test Model Discovery with API Key (Should succeed)
print_header "Model Discovery with API Key"
curl -k -s -H "Authorization: Bearer $API_KEY" -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/api/v1/models

# 4. Test Admin API Keys Listing
print_header "Admin: List API Keys"
curl -k -s -H "Authorization: Bearer $API_KEY" -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/admin/keys

# 5. Test Admin API Key Creation
print_header "Admin: Create New API Key (Developer role, 45 RPM)"
curl -k -s -X POST -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"user_id": 1, "role_id": 2, "rate_limit_rpm": 45}' \
     -w "\nHTTP Status: %{http_code}\n" \
     https://localhost:$PORT/admin/keys

# 6. Test Admin Metrics Listing
print_header "Admin: View Performance Metrics"
curl -k -s -H "Authorization: Bearer $API_KEY" -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/admin/metrics

# 7. Test Admin Audit Logs Listing
print_header "Admin: View Audit logs"
curl -k -s -H "Authorization: Bearer $API_KEY" -w "\nHTTP Status: %{http_code}\n" https://localhost:$PORT/admin/logs

# 8. Test Chat Completion (Non-Streaming)
print_header "Chat Completion (Non-Streaming)"
curl -k -s -X POST -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"model": "qwen2.5:3b-instruct", "messages": [{"role": "user", "content": "Hi! Say only '\''Hello!'\''"}], "stream": false}' \
     -w "\nHTTP Status: %{http_code}\n" \
     https://localhost:$PORT/api/v1/chat

# 9. Test Chat Completion (Streaming)
print_header "Chat Completion (Streaming)"
curl -k -s -X POST -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"model": "qwen2.5:3b-instruct", "messages": [{"role": "user", "content": "Hi! Say only '\''Hello!'\''"}], "stream": true}' \
     -w "\nHTTP Status: %{http_code}\n" \
     https://localhost:$PORT/api/v1/chat

# Clean up
echo ""
echo "--------------------------------------------------------"
echo "Stopping LLM Gateway Server (PID: $SERVER_PID)..."
echo "--------------------------------------------------------"
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null
echo "Done!"
