#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

go test ./internal/dropzone ./internal/filetransfer ./internal/httpapi ./internal/nodeagent \
  -run 'TestWorkerCycle|TestFileTransferHTTPCreateIsIdempotentByManifestKey|TestRemoteDropzoneReceiverUsesFileTransferCustody|TestDropzoneFileTransferSafeToDeleteRequiresCatalogRefs'

printf '[ok] Dropzone custody transfers use the unified file-transfer substrate and preserve safe-to-delete semantics\n'
printf '[smoke] completed 1 check\n'
