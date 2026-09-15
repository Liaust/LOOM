#!/usr/bin/env bash
set -euo pipefail

go test ./internal/filetransfer ./internal/httpapi ./internal/nodeagent
printf '[ok] file transfer HTTP receiver, catalog registration, and node-agent client helpers\n'

printf '[smoke] completed 1 check\n'
