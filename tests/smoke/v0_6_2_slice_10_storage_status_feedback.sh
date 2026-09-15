#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

go test ./internal/filetransfer ./internal/httpapi ./internal/localclient ./internal/storageexport ./internal/storageview ./internal/loomcli ./internal/loomcli/portal \
  -run 'TestFileTransferHTTPListFiltersTransfers|TestStorageClientPaths|TestRebuildCreatesExportRootAndSystemFiles|TestBuildTreeCreatesGeneratedRootsAndPermissions|TestStorageStatusAndVerifyCommands|TestStorageFailuresAndTransfersCommands|TestStoragePortalScreenRendersTreeStatusAndHidesInternalPaths|TestStoragePortalSelectionAndActions|TestStoragePortalActionsExecuteThroughClient'

printf '[ok] storage status, verification, transfer listing, failure summary, and portal storage feedback\n'
printf '[smoke] completed 1 check\n'
