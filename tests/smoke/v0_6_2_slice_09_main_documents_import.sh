#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

go test ./internal/mainstorage ./internal/loomcli ./internal/workers/runtimes ./internal/storageexport ./internal/storageview \
  -run 'TestRunOnceAcceptsStableFileAndRegistersCatalogEntry|TestRunOnceDelaysFreshChangingFile|TestRunOnceSkipsMacOSPlatformMetadata|TestFileStillMatchesSnapshotDetectsPartialCopyMovement|TestStorageMainDocumentsStatusCommand|TestMainDocumentsImportRuntimeRunsScannerAndWritesCheckpoint|TestRebuild|TestBuildTreeCreatesGeneratedRootsAndPermissions'

printf '[ok] main/Documents importer waits for stable files, ignores platform sidecars, and reports import status\n'
printf '[smoke] completed 1 check\n'
