#!/bin/bash
set -e

echo "========================================="
echo "1. Building React Frontend (Glassmorphism)"
echo "========================================="
cd ui
npm install
npm run build
cd ..

echo "========================================="
echo "2. Building Go Backend (with embedded UI)"
echo "========================================="
go mod tidy
go build -o bin/server cmd/server/main.go

echo "========================================="
echo "Setup Complete!"
echo "Run ./start.sh to launch the LLM Gateway."
echo "========================================="
