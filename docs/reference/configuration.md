---
title: "Configuration"
description: "LOOM runtime configuration precedence, canonical filesystem layout, validation, installation manifests, and compatibility inputs."
audience:
  - operator
  - developer
tags:
  - loom
  - reference
  - configuration
status: verified
verified_at: "2026-08-27"
source_scope:
  - "internal/config/config.go"
  - "internal/filesystemlayout/layout.go"
  - "internal/setup/manifest.go"
  - "internal/cloudstorage/config.go"
related:
  - "[[Environment Variables]]"
  - "[[Canonical Paths]]"
aliases:
  - "Runtime Configuration"
---
# Configuration

## Precedence

The CLI/runtime resolves configuration in this order:

1. built-in defaults;
2. an explicit environment-style config file;
3. process environment variables;
4. explicit command/programmatic overrides.

An install manifest named `install.yaml` is a typed setup artifact. When it is
explicitly selected, or discovered at the standard user/system location, its
installed roots are converted into runtime overrides so custom installation
paths reach CLI, daemon, Box, Portal, Lane, backup, and archive consumers.

Do not put credentials in an install manifest, repository config, status
output, or support bundle. Safe diagnostics redact URLs/credentials and emit a
bounded layout summary.

## Canonical Layout Fields

| Field | Default/derivation | Meaning |
|---|---|---|
| `ServiceRoot` | `/srv/loom` | Main service ownership parent. |
| `BoxPath` | `<ServiceRoot>/box` | Active Box source root. |
| `StorageRoot` | `<ServiceRoot>/storage` | Physical custody parent. |
| `ImportsRoot` | `<StorageRoot>/imports` | Lane/import custody. |
| `UserBackupsRoot` | `<StorageRoot>/backups` | Watched/private backup custody. |
| `ArchiveRoot` | `<StorageRoot>/archive` | Archive custody. |
| `DataDir` | `/var/lib/loom` | Runtime/internal parent. |
| `BoxStateRoot` | `<DataDir>/box-state` | Lane/Dropzone runtime state. |
| `GeneratedRoot` | `<DataDir>/generated` | Generated artifacts. |
| `NotesProjection` | `<GeneratedRoot>/notes` | Generated Notes projection. |
| `ObjectStore` | `<DataDir>/object-store` | Internal content protection. |
| `StorageRetention` | `<DataDir>/storage-retention` | Retention copy root. |

The operational main backup root derives as `<DataDir>/backups/main`.

## Validation

Every root must be absolute, clean, and free of control characters. Layout
validation enforces:

- Imports/backups/archive are proper descendants of Storage;
- state/generated/retention/operational backup roots have their required
  runtime ancestry;
- Box, agents, Storage, and runtime data ownership roots are disjoint;
- the three Storage custody children are mutually disjoint;
- internal state/generated/retention/backup roots are mutually disjoint;
- canonical roots cannot be nested inside the deprecated generated export.

Filesystem operations add no-symlink/path-confinement and effective-access
checks where required.

## Deprecated Migration Inputs

`StorageExport` and `MainDocuments` remain typed fields only so old installs and
manifests can be inspected/migrated. `MainDocuments` is not the active Documents
root. The active importer uses `<BoxPath>/Documents`. `StorageExport` is not a
share, backup source, generated target, or repair destination.

## Pre-Cutover Hardware Main

The hardware profile defines canonical targets while
`filesystemCutoverEnabled = false`. In that state every mutating runtime stays
on the complete legacy writer set: Box and Box state, Imports, watched-root
backup custody, archive custody, and generated Notes. The legacy read-only SMB
inspection export also remains the advertised Storage share. Canonical target
roots may be inspected, but they are not selected piecemeal.

Slice 10 may set `filesystemCutoverEnabled = true` only after writers are
stopped, the reviewed migration applies cleanly, and the operator has verified
the target roots. That single decision switches Box, node-owned Box state,
Imports, user backups, archive custody, generated Notes, and the direct Box and
Storage SMB shares together. A pre-cutover activation therefore cannot split
writers between legacy and canonical custody.

Imports backup policy is deliberately independent of the filesystem-cutover
flag. Production retains complete-physical-custody coverage before and after
the move because its inherited Imports tree contains markerless legacy custody.
Changing filesystem layout alone does not claim Lane commit-manifest provenance
or enable committed-only backup policy; that later conversion requires its own
reviewed evidence and operator decision.

## Cloud Configuration

Cloud backend configuration is separate and non-secret. Borg passphrases,
rclone secrets, WireGuard keys, SMB credentials, and node tokens remain in
approved external/protected stores. Cloud snapshot runtimes receive the same
canonical backup coverage options as direct CLI flows.

## Related Docs

- [[Environment Variables]]
- [[Canonical Paths]]
- [[Main And Mac Operations]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Runtime Config Loading

LOOM runtime config is loaded in this order:

1. built-in defaults;
2. config file selected by `LOOM_CONFIG_FILE` or explicit command override;
3. process environment variables;
4. explicit in-process overrides.

The config file format used by `internal/config/config.go` is env-file style:

```text
LOOM_ENV=production
LOOM_NODE_ID=main
LOOM_DATA_DIR=/var/lib/loom
```

Blank lines and comments are ignored. Values may be quoted.

### Local Documentation Resolution

`loom docs` does not load the normal runtime config or contact the daemon. Its
documentation root is selected from `--docs-dir`, `LOOM_DOCS_DIR`, the
executable-relative release path `../share/loom/docs`, or a repository-local
`docs/` directory, in that order. See [[Documentation CLI]] for status and
inspection behavior.

### Runtime Defaults

| Field | Default |
|---|---|
| `LOOM_ENV` | `dev` |
| `LOOM_NODE_ID` | `dev-main` |
| `LOOM_NODE_KIND` | `main` |
| `LOOM_NODE_ROLE` | `main` |
| `LOOM_RUNTIME_CLASS` | `main_full` |
| `LOOM_DATA_DIR` | `/var/lib/loom` |
| `LOOM_OBJECT_STORE` | `/var/lib/loom/object-store` |
| `LOOM_STORAGE_EXPORT_ROOT` | `/var/lib/loom/storage-views/main-export` (deprecated compatibility input only) |
| `LOOM_STORAGE_RETENTION_ROOT` | `/var/lib/loom/storage-retention` |
| `LOOM_MAIN_DOCUMENTS_ROOT` | `/var/lib/loom/main-documents` (deprecated migration input) |
| `LOOM_NOTES_PROJECTION_ROOT` | `<generated-root>/notes` when unset |
| `LOOM_SOCKET_PATH` | `/run/loom/loomd.sock` |
| `LOOM_LOG_LEVEL` | `info` |
| `LOOM_MIGRATIONS_DIR` | `migrations` |
| `LOOM_BOOTSTRAP_MODE` | `none` |

The configuration safe-fields output intentionally exposes only safe values and
a `db_configured` boolean. It does not expose the raw database URL.

### Required Runtime Fields

Validation requires non-empty:

- `LOOM_ENV`;
- `LOOM_NODE_ID`;
- `LOOM_NODE_KIND`;
- `LOOM_NODE_ROLE`;
- `LOOM_RUNTIME_CLASS`;
- `LOOM_DATA_DIR`;
- `LOOM_OBJECT_STORE`;
- `LOOM_STORAGE_EXPORT_ROOT`;
- `LOOM_STORAGE_RETENTION_ROOT`;
- `LOOM_MAIN_DOCUMENTS_ROOT`;
- `LOOM_SOCKET_PATH`;
- `LOOM_MIGRATIONS_DIR`.

### HTTP Listen Address Validation

`LOOM_HTTP_LISTEN_ADDR` is optional. When set, it must be `host:port`.

Allowed hosts:

- `localhost`;
- loopback IP addresses;
- private IP addresses.

Wildcard binds and public interface binds are rejected. Ports must be between 1
and 65535.

### Bootstrap And Notes AI

`LOOM_BOOTSTRAP_MODE` accepts:

- `none`;
- `dev`;
- `production`.

Embedding defaults:

| Field | Default |
|---|---|
| `LOOM_EMBEDDINGS_ENABLED` | `false` |
| `LOOM_EMBEDDING_RUNTIME` | `ollama` |
| `LOOM_EMBEDDING_MODEL` | `mxbai-embed-large` |
| `LOOM_EMBEDDING_OLLAMA_URL` | `http://127.0.0.1:11434` |
| `LOOM_EMBEDDING_DIMENSIONS` | `1024` |
| `LOOM_EMBEDDING_QUIET_WINDOW_SECONDS` | `600` |
| `LOOM_EMBEDDING_CONCURRENCY` | `1` |

Only the `ollama` embedding runtime is currently accepted by validation.

Standalone-image description defaults:

| Field | Default |
|---|---|
| `LOOM_VISION_ENABLED` | `false` |
| `LOOM_VISION_RUNTIME` | `ollama` |
| `LOOM_VISION_MODEL` | unset; required when vision is enabled |
| `LOOM_VISION_OLLAMA_URL` | `http://127.0.0.1:11434` |
| `LOOM_VISION_MAX_BYTES` | `20971520` |
| `LOOM_VISION_MAX_PIXELS` | `40000000` |

PDF OCR policy is stored with the unified pipeline policy. It is inspected or
changed through `loom notes pipelines policy`; it is not a credential or a
model-download switch. Heavy OCR, vision, and embedding stages share one
globally admitted `knowledge_heavy` slot.

### Cloud Config

Cloud config is separate JSON, defaulting to:

```text
/etc/loom/cloud/config.json
```

Important defaults:

| Field | Default |
|---|---|
| schema version | `loom.cloud.config.v0.6.4` |
| enabled | `false` |
| provider | `hetzner_storage_box` |
| driver | `rclone` |
| remote name | `loom-cloud` |
| remote root | `loom` |
| rclone config path | `/etc/loom/cloud/rclone.conf` |
| state dir | `/var/lib/loom/cloud` |
| root `main_snapshots` | `main-snapshots` |
| root `full_offload` | `full-offload` |
| root `cloud_folder` | `cloud-folder` |
| default snapshot backend | `legacy_tree` |

Main can configure Borg as the active snapshot backend. Borg defaults include
binary `borg`, encryption `repokey-blake2`, compression `zstd,6`, check mode
`repository`, lock wait 5 seconds, and inventory cache TTL 300 seconds.

Cloud remote prefixes must be relative remote prefixes. Absolute paths, rclone
URIs, backslashes, colons, empty segments, `.`, and `..` are rejected.

### Related Docs

- [[Environment Variables]]
- [[Canonical Paths]]
- [[Cloud Storage]]
- [[Notes And Search]]
- [[Database Maintenance]]
