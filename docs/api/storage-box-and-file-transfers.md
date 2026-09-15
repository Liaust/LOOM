---
title: "Storage Box And File Transfers API"
description: "Typed HTTP routes for Box policy, file transfer, canonical filesystem status, catalog custody, retention, archive, and compatibility diagnostics."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - storage
status: verified
verified_at: "2026-08-27"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/httpapi/storage.go"
  - "internal/httpapi/file_transfers.go"
  - "internal/localclient/client.go"
related:
  - "[[Storage Box And Lane CLI Reference]]"
  - "[[Box Storage And Lane]]"
aliases:
  - "Storage API"
---
# Storage Box And File Transfers API

LOOM’s local/private HTTP API exposes typed catalog and custody operations. It
does not expose an arbitrary filesystem browser or arbitrary final custody
path.

## Common Contract

Requests use the standard correlation and actor context. Responses use LOOM’s
typed envelope and structured errors. Clients should branch on error code and
typed phase, not on human message text.

Path-bearing inputs are normalized and confined by the owning service. Main
derives Imports, Storage, backup, archive, and runtime staging roots from its
trusted runtime configuration.

## Box And Transfer Routes

| Route | Purpose |
|---|---|
| `POST /v1/box/watch-policy/apply` | Apply desired watched-root policy to main. |
| `POST /v1/box/watch-status` | Read desired/applied Box watch status for a resolved Box plan. |
| `/v1/box/dropzone/upload-sessions` | Create/list Dropzone upload sessions. |
| `/v1/box/dropzone/upload-sessions/{id}` | Inspect or advance one confined session. |
| `/v1/file-transfers` | Create/list resumable transfers. |
| `/v1/file-transfers/{id}` | Upload chunks, inspect, or complete one transfer. |

Watched-root completion records exact accepted custody relative to configured
Storage and keeps the physical reference as the exact accepted absolute path.
Batch/item identity prevents cross-batch evidence replay. Whole-file checksum
verification happens before canonical publication.

## Storage Routes

| Route | Purpose |
|---|---|
| `GET /v1/storage/filesystem/status` | Bounded physical-root and catalog health. |
| `/v1/storage/entries` and `/v1/storage/entries/{id}` | Bounded list and inspect. |
| `GET /v1/storage/resolve` | Resolve one view path through catalog refs. |
| `GET /v1/storage/inspect-path` | Inspect protection/fidelity for one path. |
| `/v1/storage/main-documents/*` | Status, reconcile, retention backfill, protection, and safe-delete. |
| `POST /v1/storage/lane/accept` | Promote trusted main staging to configured Imports custody. |
| `POST /v1/storage/archive` | Plan or commit archive custody. |
| `/v1/storage/retention/status` | Read retention health. |
| `/v1/storage/safe-to-delete` | Compute source safe-delete evidence. |
| `/v1/storage/fetch` and `/v1/storage/restore` | Copy verified content to an explicit destination. |
| `/v1/storage/tombstones` | Record deliberate lifecycle state. |
| `POST /v1/storage/fidelity/backfill` | Plan/apply bounded observations. |

`GET /v1/storage/tree` is retired; callers should use bounded entry/catalog
queries. Archive source selection paginates bounded prefix queries instead of
building a full generated tree.

## Export Compatibility

`GET /v1/storage/export/status` returns a deprecated read-only compatibility
diagnostic. Historical refresh/rebuild routes return `410 Gone` with
`storage.export_retired`; no route creates, refreshes, repairs, links, or
deletes a generated export.

## Failure Semantics

Lane acceptance reports promotion, catalog, and source-cleanup phases so the
client can advertise an executable idempotent repair. Filesystem/catalog
absence fails status closed. JSON findings and HTTP success/failure semantics
must agree.

## Related Docs

- [[Storage Box And Lane CLI Reference]]
- [[Box Storage And Lane]]
- [[Storage Retention And Safe Delete]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Scope

This page documents the route groups that back Box watch state, Dropzone upload
sessions, file transfers, and main storage operations.

The recommended user-facing interface is the CLI and portal. Raw HTTP routes
are documented here so developers can understand the system boundary and local
client behavior.

### Source Of Truth

Routes are registered in `internal/httpapi/server.go`. The Go local client wraps
these routes in `internal/localclient/client.go`.

Slice 7 verified route presence from source and checked behavior through safe
CLI calls backed by the local client.

### Response Envelope

Most backend route handlers return the LOOM response envelope:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_...",
    "source": "main-authoritative",
    "freshness": "live"
  }
}
```

Some CLI commands render direct local reports after combining backend and local
state. Do not infer raw route shape from every CLI JSON output. Use the local
client method and endpoint source for exact route behavior.

### Box Watch Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/box/watch-policy/apply` | `POST` | dry-run or mutating by request body | `ApplyBoxWatchPolicy` |
| `/v1/box/watch-status` | `POST` | read-only | `GetBoxWatchStatus` |

`watch-policy/apply` accepts the compiled Box watch plan. When the input has
dry-run semantics, it computes the desired backend state without recording
changes. Without dry-run, it records Box watched-root registration state on
main.

`watch-status` compares desired plan, backend registrations, and node-agent
reported state.

### Dropzone Upload Session Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/box/dropzone/upload-sessions` | `GET` | read-only | `ListDropzoneUploadSessions` |
| `/v1/box/dropzone/upload-sessions` | `POST` | mutating | `CreateDropzoneUploadSession` |
| `/v1/box/dropzone/upload-sessions/{id}` | `GET` | read-only | `GetDropzoneUploadSession` |
| `/v1/box/dropzone/upload-sessions/{id}/chunks` | `POST` | mutating | `UploadDropzoneChunk` |
| `/v1/box/dropzone/upload-sessions/{id}/complete` | `POST` | mutating | `CompleteDropzoneUpload` |
| `/v1/box/dropzone/upload-sessions/{id}/abort` | `POST` | mutating | `AbortDropzoneUpload` |

These routes model Dropzone as custody transfer state, not as a writable
storage view.

### File Transfer Routes

| Route | Method | Safety | Purpose |
|---|---|---|---|
| `/v1/file-transfers` | `GET` | read-only | List file transfer status. |
| `/v1/file-transfers` | `POST` | mutating | Create or accept file transfer manifest state. |
| `/v1/file-transfers/{id}` | `GET` | read-only | Inspect one transfer. |
| `/v1/file-transfers/{id}/chunks/{index}` | `PUT` | mutating | Upload one checksum-attested bounded chunk. |
| `/v1/file-transfers/{id}/complete` | `POST` | mutating | Verify and atomically publish accepted custody. |
| `/v1/file-transfers/{id}/abort` | `POST` | mutating | Abort a retryable transfer with typed evidence. |

The local client uses `ListFileTransfers` for the read-only list path. Transfer
creation, chunk upload, complete, and abort behavior is implemented in
`internal/httpapi/file_transfers.go`.

### Storage Browse Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/storage/tree` | `GET` | retired compatibility (`410 Gone`) | `GetStorageTree` |
| `/v1/storage/entries` | `GET` | read-only | `ListStorageEntries` |
| `/v1/storage/entries/{id}` | `GET` | read-only | `InspectStorageEntry` |
| `/v1/storage/entries/{id}/physical-refs` | `POST` | mutating | `RegisterStoragePhysicalRef` |
| `/v1/storage/resolve?path=...` | `GET` | read-only | `ResolveStoragePath` |
| `/v1/storage/inspect-path?path=...` | `GET` | read-only | `InspectStoragePath` |

The tree and entries routes accept filters such as node, source area, storage
class, file class, processing state, availability state, include-deleted, and
limit.

### Main Documents Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/storage/main-documents/status` | `GET` | read-only | `GetMainDocumentsStatus` |
| `/v1/storage/main-documents/reconcile` | `POST` | dry-run or mutating | `ReconcileMainDocuments` |
| `/v1/storage/main-documents/retention/backfill` | `POST` | dry-run or mutating | `BackfillMainDocumentsRetention` |
| `/v1/storage/main-documents/protection` | `POST` | read-only | `GetMainDocumentProtection` |
| `/v1/storage/main-documents/safe-delete` | `POST` | read-only | `CheckMainDocumentSafeDelete` |

Main `Documents` is a writable source area on main. It therefore has explicit
protection, retention, reconcile, and safe-delete routes rather than relying
on a generated filesystem tree.

### Export, Retention, Fidelity, And Lane Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/storage/export/status` | `GET` | read-only | `GetStorageExportStatus` |
| `/v1/storage/export/refresh` | `GET` | deprecated read-only status alias | `GetStorageExportRefreshStatus` |
| `/v1/storage/export/refresh` | `POST` | retired compatibility (`410 Gone`) | `RequestStorageExportRefresh` |
| `/v1/storage/export/rebuild` | `POST` | retired compatibility (`410 Gone`) | `RebuildStorageExport` |
| `/v1/storage/retention/status` | `GET` | read-only | `GetStorageRetentionStatus` |
| `/v1/storage/fidelity/backfill` | `POST` | dry-run or mutating | `BackfillStorageFidelity` |
| `/v1/storage/lane/accept` | `POST` | mutating | `AcceptLaneCustody` |

Only the export status/GET compatibility diagnostics remain readable. Export
refresh and rebuild requests always fail with `storage.export_retired` and
cannot mutate filesystem state. Fidelity backfill retains dry-run and explicit
apply modes for observation/finding rows; it never rewrites retained payloads.

### Archive, Safe Delete, Fetch, Restore, And Tombstone Routes

| Route | Method | Safety | Local Client |
|---|---|---|---|
| `/v1/storage/archive` | `POST` | dry-run or mutating | `ArchiveStorage` |
| `/v1/storage/safe-to-delete` | `POST` | read-only | `CheckStorageSafeToDelete` |
| `/v1/storage/fetch` | `POST` | mutating local destination | `FetchStorage` |
| `/v1/storage/restore` | `POST` | mutating local destination | `RestoreStorage` |
| `/v1/storage/tombstones` | `POST` | mutating | `RecordStorageTombstone` |

Use archive dry-runs and restore plans for user-facing workflows. Do not expose
casual examples that write archives, destination files, or tombstones without
operator confirmation.

### Error Behavior

Storage route handlers return structured errors with route-specific codes, for
example:

- `storage.invalid_filter`;
- `storage.path_required`;
- `storage.path_not_found`;
- `storage.main_documents_reconcile_failed`;
- `storage.export_rebuild_failed`;
- `storage.lane_accept_failed`;
- `file_transfer.action_unknown`.

Docs and clients should preserve the server correlation ID when reporting
failures.

### Developer Guidance

When adding a storage route:

1. add the route in `internal/httpapi/server.go`;
2. implement handler behavior with read-only, dry-run, and apply boundaries made
   explicit;
3. add a local-client wrapper in `internal/localclient/client.go`;
4. expose the behavior through CLI or portal only when the safety class is clear;
5. add tests for envelope success and route-specific error codes;
6. update [[Storage Box And Lane CLI Reference]] and this page when documenting
   the feature.
