---
title: "Daemon CLI Reference"
description: "CLI reference for loomd serve, migrate, and version commands."
audience:
  - operator
  - developer
tags:
  - loom
  - cli
  - operations
  - developer
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loomd --help"
  - "go run ./cmd/loomd serve --help"
  - "go run ./cmd/loomd migrate --help"
  - "go run ./cmd/loomd version"
  - "internal/loomdapp/root.go"
related:
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Migrations]]"
  - "[[Main And Mac Operations]]"
  - "[[Configuration]]"
aliases:
  - "loomd"
  - "LOOM Daemon"
---
# Daemon CLI Reference

## Purpose

`loomd` is the long-running main service binary. It opens the database, object
store, core services, HTTP/local socket API, workers, bootstrap checks, and
optional private HTTP listener.

Users normally interact with LOOM through `loom`. Operators and developers use
`loomd` for service startup, migrations, and version checks.

## Commands

```bash
loomd version
loomd serve
loomd migrate status
loomd migrate up
```

At verification time, `loomd version` printed `loomd 0.0.0-dev`, and root,
serve, and migrate help rendered successfully.

## Serve Flags

`loomd serve` reads config from file, environment, and flags. Important flags:

| Flag | Meaning |
|---|---|
| `--config` | Path to LOOM config file. |
| `--env` | LOOM environment name. |
| `--node-id`, `--node-kind`, `--node-role`, `--runtime-class` | Node identity and runtime profile. |
| `--data-dir` | Runtime data directory. |
| `--object-store` | Object-store directory. |
| `--storage-export-root`, `--storage-retention-root` | Storage export and retention roots. |
| `--main-documents-root` | Main Documents backing root. |
| `--box-path`, `--box-profile` | Box root and profile. |
| `--db-url` | PostgreSQL connection URL. |
| `--socket` | Unix socket path. |
| `--http-listen-addr` | Optional private HTTP listener. |
| `--migrations-dir` | Database migrations directory. |
| `--auto-migrate` | Apply migrations before serving. |
| `--bootstrap-mode` | `none`, `dev`, or `production`. |

Production runtime should be managed by the operating system service, not by a
manual shell that can disappear.

## Migrations

Migration commands:

```bash
loomd migrate status
loomd migrate up
```

`migrate status` is read-only. `migrate up` changes the database and should be
handled through the release/update flow or an explicit operator maintenance
step.

For developer details, use [[Migrations]].

## Bootstrap Modes

`loomd serve` can run bootstrap checks:

- `--bootstrap-mode none` does not create bootstrap records;
- `--bootstrap-mode dev` ensures deterministic development bootstrap records;
- `--bootstrap-mode production` ensures production bootstrap records and fails
  if production bootstrap is incomplete.

Runtime/deployment must not depend on UTM or macOS-specific behavior. Main
production service configuration belongs in NixOS/systemd state.

## Related Docs

- [[Migrations]]
- [[Configuration]]
- [[Main And Mac Operations]]
- [[Updating And Rebuilding]]
