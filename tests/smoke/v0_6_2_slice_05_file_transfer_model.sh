#!/usr/bin/env bash
set -euo pipefail

go test ./internal/filetransfer
printf '[ok] shared file transfer model and local state\n'

go test ./internal/migrations -run TestV062Slice05FileTransferMigrationContract
printf '[ok] file transfer migration contract\n'

printf '[smoke] completed 2 checks\n'
