---
title: "Environment Variables"
description: "Runtime environment variables for LOOM identity, canonical filesystem roots, transport, bootstrap, and model pipelines."
audience:
  - operator
  - developer
tags:
  - loom
  - reference
  - configuration
status: verified
verified_at: "2026-08-28"
source_scope:
  - "internal/config/config.go"
  - "internal/cloudstorage/config.go"
related:
  - "[[Configuration]]"
  - "[[Canonical Paths]]"
aliases:
  - "LOOM Environment"
  - "Runtime Env Vars"
---
# Environment Variables

Values in a selected config file use the same names. Avoid shell history and
logs for secrets; most credentials do not belong in these variables at all.

## Identity And Runtime

| Variable | Purpose |
|---|---|
| `LOOM_ENV` | Environment name. |
| `LOOM_NODE_ID` | Node identity/reference. |
| `LOOM_NODE_KIND` | Main/workstation/device kind. |
| `LOOM_NODE_ROLE` | Runtime role. |
| `LOOM_RUNTIME_CLASS` | Runtime capability class. |
| `LOOM_DATA_DIR` | Internal runtime data root. |
| `LOOM_OBJECT_STORE` | Internal content-protection store. |
| `LOOM_SOCKET_PATH` | Unix socket path. |
| `LOOM_HTTP_LISTEN_ADDR` | Optional private HTTP listen address. |
| `LOOM_SERVICE_ALLOWLIST_PATH` | Service allowlist path. |
| `LOOM_LOG_LEVEL` | Runtime log level. |

## Canonical Filesystem

| Variable | Purpose |
|---|---|
| `LOOM_SERVICE_ROOT` | Service ownership parent. |
| `LOOM_BOX_PATH` | Active Box source root. |
| `LOOM_BOX_PROFILE` | `workspace` or `main` Box contract. |
| `LOOM_STORAGE_ROOT` | Physical Storage custody parent. |
| `LOOM_IMPORTS_ROOT` | Lane/import custody. |
| `LOOM_IMPORTS_BACKUP_POLICY` | Imports custody evidence policy: `committed_lane_custody_only` or `legacy_complete_physical_custody`. This policy does not change when the filesystem layout changes. |
| `LOOM_USER_BACKUPS_ROOT` | User/watched backup custody. |
| `LOOM_CANONICAL_USER_BACKUPS_ROOT` | Canonical logical watched-backup root used during the explicit legacy split-root transition; it equals `LOOM_USER_BACKUPS_ROOT` outside that transition. |
| `LOOM_ARCHIVE_ROOT` | Archive custody. |
| `LOOM_BOX_STATE_ROOT` | Node-owned Lane/Dropzone runtime state. |
| `LOOM_GENERATED_ROOT` | Generated artifacts parent. |
| `LOOM_NOTES_PROJECTION_ROOT` | Explicit generated Notes projection override. |
| `LOOM_STORAGE_RETENTION_ROOT` | Internal retention protection. |
| `LOOM_LEGACY_SPLIT_ROOTS` | Fail-closed runtime signal that the complete legacy writer-root set remains active while canonical target identities are represented. Nix derives it from the single `filesystemCutoverEnabled` operator decision. |

`LOOM_LEGACY_SPLIT_ROOTS=true` is not an independent partial-cutover control:
validation accepts only the complete production legacy root set. The Nix
`filesystemCutoverEnabled` option atomically selects the full pre/post
writer-and-share contract. Imports backup policy remains a separate custody
provenance decision, so moving roots never silently changes complete-physical
legacy coverage to committed-only coverage.

Deprecated compatibility inputs:

| Variable | Status |
|---|---|
| `LOOM_STORAGE_EXPORT_ROOT` | Migration/old-manifest input only; no active export writer. |
| `LOOM_MAIN_DOCUMENTS_ROOT` | Legacy Documents comparison input only; active Documents is under Box. |

Do not omit these from an old install manifest if migration evidence depends on
them, but do not introduce new runtime consumers.

## Transport, Database, And Bootstrap

| Variable | Purpose |
|---|---|
| `LOOM_MAIN_URL` | Private main HTTP endpoint for non-main clients. |
| `LOOM_DB_URL` | PostgreSQL connection; sensitive. |
| `LOOM_CONFIG_FILE` | Explicit environment-style config or install manifest path. |
| `LOOM_MIGRATIONS_DIR` | Migration source directory. |
| `LOOM_AUTO_MIGRATE` | Boolean runtime migration policy. |
| `LOOM_BOOTSTRAP_DEV` | Legacy boolean development bootstrap input. |
| `LOOM_BOOTSTRAP_MODE` | Typed bootstrap mode. |

`LOOM_TEST_DB_URL`, `LOOM_BOOTSTRAP_TEST_DB_URL`, and related test variables
are test-only and must point to dedicated disposable databases.

## Embeddings And Vision

| Variable | Purpose |
|---|---|
| `LOOM_EMBEDDINGS_ENABLED` | Enable embeddings. |
| `LOOM_EMBEDDING_RUNTIME` | Embedding runtime. |
| `LOOM_EMBEDDING_MODEL` | Model name. |
| `LOOM_EMBEDDING_OLLAMA_URL` | Ollama endpoint. |
| `LOOM_EMBEDDING_DIMENSIONS` | Vector dimensions. |
| `LOOM_EMBEDDING_QUIET_WINDOW_SECONDS` | Source quiet window. |
| `LOOM_EMBEDDING_CONCURRENCY` | Worker concurrency. |
| `LOOM_VISION_ENABLED` | Enable vision pipeline. |
| `LOOM_VISION_RUNTIME` | Vision runtime. |
| `LOOM_VISION_MODEL` | Vision model. |
| `LOOM_VISION_OLLAMA_URL` | Vision Ollama endpoint. |
| `LOOM_VISION_MAX_BYTES` | Per-input byte limit. |
| `LOOM_VISION_MAX_PIXELS` | Per-input pixel limit. |

## Security Notes

- DB URLs may contain credentials and are redacted from safe support output.
- SMB passwords belong in the platform credential store/Keychain, not Git.
- Borg passphrases and rclone credentials use protected files referenced by
  cloud config.
- Never use environment overrides to bypass canonical root confinement or a
  production cutover plan.

## Related Docs

- [[Configuration]]
- [[Canonical Paths]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Runtime Identity

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_ENV` | Runtime environment label. | `dev` |
| `LOOM_NODE_ID` | Current node id. | `dev-main` |
| `LOOM_NODE_KIND` | Node kind; lowercased by config. | `main` |
| `LOOM_NODE_ROLE` | Node role. | `main` |
| `LOOM_RUNTIME_CLASS` | Runtime class; lowercased by config. | `main_full` |

Production main should be explicit. Do not rely on development defaults for
hardware or production-like services.

### Storage And Data Paths

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_DATA_DIR` | Main LOOM data directory. | `/var/lib/loom` |
| `LOOM_OBJECT_STORE` | Object-store root. | `/var/lib/loom/object-store` |
| `LOOM_STORAGE_EXPORT_ROOT` | Deprecated migration/old-manifest compatibility input; no active export writer consumes it. | `/var/lib/loom/storage-views/main-export` |
| `LOOM_STORAGE_RETENTION_ROOT` | Storage retained-payload root. | `/var/lib/loom/storage-retention` |
| `LOOM_MAIN_DOCUMENTS_ROOT` | Deprecated migration input; active Documents resolves below configured Box. | `/var/lib/loom/main-documents` |
| `LOOM_NOTES_PROJECTION_ROOT` | Generated Notes projection override. | `<generated-root>/notes` |
| `LOOM_BOX_PATH` | Local Box root override. | unset |
| `LOOM_BOX_PROFILE` | Local Box profile; lowercased by config. | unset |

Canonical custody and generated projections are not writable source folders.
See [[Canonical Paths]].

### Runtime Access

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_MAIN_URL` | Main HTTP URL, with trailing slash trimmed. | unset |
| `LOOM_DB_URL` | Database connection URL. | unset |
| `LOOM_SOCKET_PATH` | Unix socket path for local daemon communication. | `/run/loom/loomd.sock` |
| `LOOM_HTTP_LISTEN_ADDR` | Optional HTTP bind address. | unset |
| `LOOM_LOG_LEVEL` | Log level. | `info` |
| `LOOM_CONFIG_FILE` | Env-file style runtime config path. | unset |
| `LOOM_MIGRATIONS_DIR` | Migrations directory. | `migrations` |
| `LOOM_DOCS_DIR` | Local documentation corpus override for `loom docs`. | unset |
| `LOOM_AGENT_PACK_DIR` | Local AI LOOM pack root override for `loom agent pack`. | unset |

`LOOM_DB_URL` is sensitive. Safe config output exposes only whether the DB URL
is configured, not the raw URL.

`LOOM_HTTP_LISTEN_ADDR` cannot use wildcard or public binds. Use loopback or a
private interface.

`LOOM_DOCS_DIR` is read only by the local documentation command family. An
explicit missing path fails rather than falling back to packaged or repository
docs. It does not affect user Notes or the knowledge index.

`LOOM_AGENT_PACK_DIR` follows the same fail-closed override behavior for the
local external-agent pack tools. It does not configure a runtime agent.

### Bootstrapping

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_AUTO_MIGRATE` | Boolean migration toggle. | `false` when unset |
| `LOOM_BOOTSTRAP_DEV` | Boolean development bootstrap toggle. | `false` when unset |
| `LOOM_BOOTSTRAP_MODE` | Bootstrap mode: `none`, `dev`, or `production`. | `none` |

Boolean values are parsed with Go boolean parsing. Invalid values fail config
loading.

### Embeddings

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_EMBEDDINGS_ENABLED` | Enables embedding processing. | `false` |
| `LOOM_EMBEDDING_RUNTIME` | Embedding runtime. | `ollama` |
| `LOOM_EMBEDDING_MODEL` | Embedding model. | `mxbai-embed-large` |
| `LOOM_EMBEDDING_OLLAMA_URL` | Ollama base URL. | `http://127.0.0.1:11434` |
| `LOOM_EMBEDDING_DIMENSIONS` | Positive embedding dimension count. | `1024` |
| `LOOM_EMBEDDING_QUIET_WINDOW_SECONDS` | Positive queue quiet window. | `600` |
| `LOOM_EMBEDDING_CONCURRENCY` | Positive embedding concurrency. | `1` |

Validation currently accepts only the `ollama` embedding runtime and requires an
HTTP or HTTPS Ollama URL with a host.

### Standalone Image Descriptions

| Variable | Meaning | Default |
|---|---|---|
| `LOOM_VISION_ENABLED` | Enables standalone-image description execution. | `false` |
| `LOOM_VISION_RUNTIME` | Local vision runtime. | `ollama` |
| `LOOM_VISION_MODEL` | Explicit local vision model name. | unset |
| `LOOM_VISION_OLLAMA_URL` | Ollama base URL. | `http://127.0.0.1:11434` |
| `LOOM_VISION_MAX_BYTES` | Maximum decoded image bytes. | `20971520` |
| `LOOM_VISION_MAX_PIXELS` | Maximum decoded image pixels. | `40000000` |

Enabling vision requires a non-empty model name. Configuration does not pull a
model, contact an external service, or prove hardware acceptance. PDF OCR does
not use the vision runtime and has no model variable.

### Cloud Config Is Not Environment-Only

Cloud storage uses a JSON config file rather than only runtime env vars. The
default path is:

```text
/etc/loom/cloud/config.json
```

Related local files include:

- `/etc/loom/cloud/rclone.conf`;
- `/etc/loom/cloud/borg.passphrase`, when Borg is configured;
- `/var/lib/loom/cloud`, for cloud state, locks, manifests, and caches.

Do not paste passphrases, private keys, or raw credentials into docs, notes, or
bug reports.

### Related Docs

- [[Configuration]]
- [[Cloud Storage]]
- [[Notes Knowledge Index]]
- [[Backup Cloud And Maintenance CLI Reference]]
