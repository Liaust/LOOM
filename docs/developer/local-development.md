---
title: "Local Development"
description: "Practical local development loop for building, running, testing, and validating LOOM changes."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - configuration
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go.mod"
  - "flake.nix"
  - "internal/loomcli/root.go"
  - "internal/loomdapp/root.go"
related:
  - "[[Developer Documentation]]"
  - "[[Repository Map]]"
  - "[[Testing]]"
  - "[[Configuration]]"
aliases:
  - "Development Loop"
---
# Local Development

## What This Page Covers

This page explains the normal local loop for changing LOOM code. It does not
replace production update runbooks. For main/Mac operations, use
[[Main And Mac Operations]] and [[Updating And Rebuilding]].

## Prerequisites

LOOM is a Go module:

```text
module loom.local/loom
go 1.25.7
```

The flake dev shell includes Go, Git, OpenSSH, Poppler utilities, and rsync:

```bash
nix develop
```

If you are not using Nix, use a Go version compatible with `go.mod` and make
sure PostgreSQL and any runtime tools needed by the feature are available.

## Build Checks

Build all three binaries:

```bash
go build ./cmd/loom
go build ./cmd/loomd
go build ./cmd/loom-node-agent
```

For a reusable docs/test binary, build to `/tmp`:

```bash
go build -o /tmp/loom-dev ./cmd/loom
```

Avoid writing generated binaries into the repository root.

## Fast Feedback Loop

For most changes:

1. Read the owning package and tests.
2. Make a small scoped edit.
3. Run the focused package tests.
4. Run a relevant CLI help or JSON check.
5. Run `git diff --check`.
6. Update docs or notes when the user-facing behavior changed.

Examples:

```bash
go test ./internal/loomcli
go test ./internal/httpapi ./internal/localclient
go test ./internal/capabilities ./internal/routing ./internal/policy
git diff --check
```

## Running The CLI

Use `go run` for source-backed command checks:

```bash
go run ./cmd/loom --help
go run ./cmd/loom --json status
go run ./cmd/loom enter --exit-after-render --no-animation --no-color
```

When a command talks to a daemon, it uses config and socket resolution from the
CLI context. On a production-like Mac/main setup, prefer read-only, dry-run, or
`.loom-acceptance` fixture checks.

## Running The Daemon

`loomd serve` loads config, optionally runs migrations, opens PostgreSQL,
bootstraps records, constructs domain services, and starts the HTTP API.

The important flags are:

```bash
go run ./cmd/loomd serve --help
go run ./cmd/loomd migrate --help
```

Use an explicit development config or environment. Do not point ad-hoc daemon
runs at production-like data unless the operation is planned.

## Local Config Boundary

Config can come from:

- config files;
- environment variables;
- command-line overrides;
- NixOS service configuration;
- setup manifests for local workspace install state.

See [[Configuration]] and [[Environment Variables]] before adding a new config
knob.

## Working With Main And Mac

The local checkout is the architect/integrator workspace. Feature
implementation should happen in isolated Codex worktrees when the user asks for
parallel feature work. Production rebuilds, backup/restore operations, and
live hardware validation are integrator responsibilities unless a plan
explicitly scopes them for a worker.

See [[AI Worktree Development]] for the branch and handoff model.

## Safe Fixture Paths

Manual acceptance artifacts should live under `.loom-acceptance` in a real
LOOM source root. Do not scatter test files through user storage roots.

Use:

```text
~/loom-box/Documents/.loom-acceptance/<scenario>/
```

or the relevant project/notes source root fixture folder.

## Related Docs

- [[Repository Map]]
- [[Testing]]
- [[CLI Development]]
- [[API Development]]
- [[AI Worktree Development]]
