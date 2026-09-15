#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

go test ./internal/nodeagent ./internal/watchedroots ./internal/filetransfer ./internal/httpapi
