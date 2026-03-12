#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_ROOT/frontend"

echo "Installing dependencies..."
npm install

echo "Building frontend..."
npm run build

echo "Frontend build complete: frontend/build/"
