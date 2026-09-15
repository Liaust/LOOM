---
title: "CLI Reference"
description: "Workflow-oriented reference for LOOM command-line tools."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom --help"
  - "go run ./cmd/loom-node-agent --help"
  - "go run ./cmd/loomd --help"
related:
  - "[[Command Safety]]"
  - "[[Command Index]]"
  - "[[User Guide]]"
aliases:
  - "LOOM CLI"
---
# CLI Reference

The CLI docs group commands by what the reader is trying to do. They are not a
raw copy of `--help`; each page should explain the workflow, safety level,
important flags, good output, and common failure modes.

## Start Here

- [[Command Safety]] explains read-only, dry-run, fixture-mutating,
  operator-mutating, and dangerous command examples.
- [[Runtime Status And Support]] covers `loom status`, `loom health`,
  `loom enter`, version checks, and support bundle commands.
- [[Documentation CLI]] covers daemon-free search and bounded inspection of
  the release-matched public docs corpus.
- [[Command Index]] is the quick lookup table for all CLI domains.

## Workflow References

- [[Projects And Scopes CLI Reference]]
- [[Notes Search And Indexes CLI Reference]]
- [[Storage Box And Lane CLI Reference]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Automation Jobs And Workers CLI Reference]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Modules Agents And Realtime CLI Reference]]
- [[Node Agent CLI Reference]]
- [[Daemon CLI Reference]]

## Verification Rule

The archived v0.9.6 command audit remains the original verification evidence.
If a command now fails while being documented, capture a bounded bugfix through
`.project/` and mark the affected docs section blocked or deferred instead of
fixing the runtime bug inside an unrelated docs branch.

## Related Docs

- [[User Guide]]
- [[Operations]]
- [[Developer Documentation]]
- [[API Reference]]
