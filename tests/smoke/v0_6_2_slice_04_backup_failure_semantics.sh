#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$ROOT_DIR"

go test ./internal/nodeagent -run 'TestFlushWatchedRootBackups(MarksTransportLimitAsFailed|DoesNotBlockSmallFileAfterPermanentFailure|KeepsTransientUploadErrorRetryable)'
printf '[ok] watched-root backup permanent and retryable failure semantics\n'

go test ./internal/nodeagent/runtime -run TestBuildLocalSummaryIncludesWatchedRootBackupCounts
printf '[ok] runtime summary exposes watched-root backup queue categories\n'

printf '[smoke] completed 2 checks\n'
