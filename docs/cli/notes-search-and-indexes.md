---
title: "Notes Search And Indexes CLI Reference"
description: "CLI reference for notes roots, knowledge objects, notes search, projection, embeddings, object search, and index maintenance."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "go run ./cmd/loom notes --help"
  - "go run ./cmd/loom search --help"
  - "go run ./cmd/loom index --help"
  - "go run ./cmd/loom indexes --help"
  - "go run ./cmd/loom object --help"
  - "go run ./cmd/loom objects --help"
  - "go run ./cmd/loom --json notes overview"
  - "go run ./cmd/loom --json notes search loom --limit 5"
  - "go run ./cmd/loom notes search --help"
  - "go run ./cmd/loom notes pipelines --help"
  - "go run ./cmd/loom notes pipelines backfill --help"
  - "go run ./cmd/loom --json notes projection status"
  - "go run ./cmd/loom --json indexes failures --limit 5"
related:
  - "[[CLI Reference]]"
  - "[[Notes And Search]]"
  - "[[Notes Knowledge Index]]"
  - "[[Object Store Indexes And Search]]"
  - "[[File Type Support]]"
aliases:
  - "Notes CLI"
  - "Search CLI"
  - "Indexes CLI"
---
# Notes Search And Indexes CLI Reference

## Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom notes overview` | read-only | Shows roots, files, processing, search, embeddings, and projection summary. |
| `loom notes roots list` | read-only | Lists registered notes roots. |
| `loom notes roots reconcile --dry-run` | dry-run | Plans root reconciliation. |
| `loom notes roots reconcile --apply --yes` | operator-mutating | Writes notes source-root reconciliation. |
| `loom notes objects list` | read-only | Lists knowledge objects. |
| `loom notes objects show` | read-only | Shows one knowledge object. |
| `loom notes objects reconcile --dry-run` | dry-run | Plans object reconciliation. |
| `loom notes objects reconcile --apply --yes` | operator-mutating | Writes knowledge-object reconciliation. |
| `loom notes search` | read-only | Searches notes knowledge documents. |
| `loom notes reprocess ...` | operator-mutating | Queues notes pipeline work. |
| `loom notes pipelines status/list/inspect/failures` | read-only | Inspects unified file pipelines without returning artifact bodies. |
| `loom notes pipelines retry ... --yes` | operator-mutating | Creates a new generation after a reviewed failure. |
| `loom notes pipelines policy status` | read-only | Shows OCR, image-description, and embedding policy. |
| `loom notes pipelines policy set ... --yes` | sensitive | Changes unified pipeline policy. |
| `loom notes pipelines backfill` | dry-run | Plans one pipeline per current object revision and reports reuse and legacy work. |
| `loom notes pipelines backfill --apply --yes` | operator-mutating | Applies the idempotent backfill and marks unclaimed legacy embedding rows historical. |
| `loom notes projection status` | read-only | Shows projection state. |
| `loom notes projection rebuild --dry-run` | dry-run | Plans projection rebuild. |
| `loom notes projection rebuild --apply --yes` | operator-mutating | Rebuilds generated projection files. |
| `loom notes embeddings status` | read-only | Shows embedding settings and queue state. |
| `loom notes embeddings enable/disable --yes` | sensitive | Changes global notes embedding policy. |
| `loom notes run coordinator --once` | operator-mutating | Advances one coordinator-class unified pipeline stage. |
| `loom notes run heavy --once` | operator-mutating | Runs one globally admitted OCR, image-description, or embedding stage. |
| `loom search` | read-only | General object search. |
| `loom index status`, `loom indexes status` | read-only | Inspect text index state. |
| `loom indexes queue list`, `loom indexes failures` | read-only | Inspect queue and failure state. |
| `loom indexes retry`, `loom indexes retry-failed`, `loom indexes rebuild object` | operator-mutating | Changes index queue state. |
| `loom indexes run text --once` | operator-mutating | Runs text index worker once. |
| `loom object list/inspect/versions` | read-only | Inspect object-store objects. |
| `loom object ingest` | mutating | Ingests a local file into the object store. |

## Important Naming

Use:

```bash
loom notes objects ...
loom object ...
```

`loom notes objects` is the notes knowledge-object tree. `loom object` is the
canonical top-level object-store tree. `loom objects` is accepted as a
compatibility alias for the top-level object-store command, not for notes
knowledge objects. New scripts and docs should prefer `loom object`.

## Overview

```bash
loom notes overview
loom --json notes overview
```

Useful filters:

```bash
loom notes overview --node main
loom notes overview --project-id <project-id>
loom notes overview --all
```

The overview is the best first command because it summarizes root coverage,
file classes, processing states, extraction states, index health, embeddings,
and projection state.

## Roots

List:

```bash
loom notes roots list
loom notes roots list --all
loom notes roots list --node main
loom notes roots list --root-kind project_notes
```

Dry-run reconcile:

```bash
loom notes roots reconcile --dry-run
```

Apply:

```bash
loom notes roots reconcile --apply --yes
```

The reconcile command defaults to dry-run. Apply mode requires `--yes`.

## Objects

List:

```bash
loom notes objects list --limit 20
loom notes objects list --node main
loom notes objects list --state chunked
loom notes objects list --all
```

Show one:

```bash
loom notes objects show <knowledge-object-ref>
```

Reconcile:

```bash
loom notes objects reconcile --dry-run
loom notes objects reconcile --dry-run --root <notes-source-root-id>
loom notes objects reconcile --apply --yes
```

The object show output includes class, MIME type, bytes, processing state,
pipeline, extraction status, extractor key, chunk count, text sections, links,
headings, and warnings where available.

## Notes Search

Basic:

```bash
loom notes search "query"
```

Filters:

```bash
loom notes search "query" --node main
loom notes search "query" --project osint-tools
loom notes search "query" --root <root-ref>
loom notes search "query" --file-class markdown
loom notes search "query" --path reports
loom notes search "query" --tag osint
loom notes search "query" --mode lexical
loom notes search "query" --after 2026-01-01 --before 2026-02-01
loom notes search "query" --sort newest
```

Search modes:

- `lexical`;
- `semantic`;
- `hybrid`.

Hybrid is the default requested mode. If embeddings are disabled or unavailable,
LOOM falls back to lexical search and reports the reason.

Absolute-time controls:

- `--after <timestamp-or-date>` includes results whose selected recency time is
  at or after the boundary;
- `--before <timestamp-or-date>` excludes results at or after the boundary, so
  the upper bound is exclusive;
- `--sort relevance|newest|oldest` selects bounded recency-aware relevance or
  explicit chronological order.

Timestamps must use RFC3339 with an explicit timezone. A date-only value such
as `2026-01-01` means `2026-01-01T00:00:00Z`. LOOM converts offset timestamps to
UTC before comparing them.

The same controls work as query tokens:

```bash
loom notes search "example after:2026-01-01 sort:newest"
loom notes search "incident before:2026-08-01T12:00:00+02:00"
```

Flag values override matching query-token values. Chronological sorts still
operate only on relevant search candidates; a recent unrelated file does not
enter the result set through its date alone.

Normal output has one compact `DATE` column containing the selected recency
date. JSON output includes `source_created_at`, `source_modified_at`,
`recency_at`, `recency_basis`, `recency_score`, `recency_rank`, and operational
`observed_at` and `indexed_at` when available. `observed_at` is when LOOM saw
the source revision; `indexed_at` reports index activity and is never used as
source recency.

## Reprocessing

Reprocess commands queue notes extraction work:

```bash
loom notes reprocess stale --limit 200
loom notes reprocess object <knowledge-object-ref>
loom notes reprocess root <root-ref>
loom notes reprocess project <project-ref>
loom notes reprocess node <node-ref>
loom notes reprocess file-class markdown
```

Common flags:

- `--file-class`;
- `--force`;
- `--limit`;
- `--priority`;
- `--idempotency-key`.

These commands are mutating because they enqueue or mark pipeline work.

## Unified Pipelines

Read-only inspection:

```bash
loom notes pipelines status
loom notes pipelines list --limit 100
loom notes pipelines inspect <pipeline-or-object-ref>
loom notes pipelines failures
loom notes pipelines policy status
loom notes pipelines backfill
```

Inspection reports stages and bounded progress but omits artifact bodies.
Backfill defaults to a non-mutating plan and reports file classes, stage counts,
reuse, reprocessing, blocked reasons, and legacy embedding claims.

Mutating operations require explicit confirmation:

```bash
loom notes pipelines retry <pipeline-ref> --yes
loom notes pipelines policy set --pdf-ocr=true --image-descriptions=false --embeddings=false --yes
loom notes pipelines backfill --apply --yes
loom notes run coordinator --once
loom notes run heavy --once
```

Backfill apply refuses to retire the legacy embedding queue while an active
legacy claim remains. It preserves the old tables as historical state.

## Projection

Status:

```bash
loom notes projection status
```

Dry-run rebuild:

```bash
loom notes projection rebuild --dry-run --max-results 100
```

Apply rebuild:

```bash
loom notes projection rebuild --apply --yes
```

Useful flags:

- `--include-all`;
- `--max-objects`;
- `--max-results`.

Projection output should report read-only status and whether raw writes are
supported. Current verified behavior is read-only and raw writes unsupported.

## Embeddings

Status:

```bash
loom notes embeddings status
```

Run the unified heavy executor once:

```bash
loom notes run heavy --once --reason "manual Notes heavy-stage catch-up"
```

Enable or disable:

```bash
loom notes embeddings enable --yes
loom notes embeddings disable --yes
```

Enable and disable are sensitive because they change global notes embedding
policy. The portal asks for confirmation before running the same action.

## General Search

```bash
loom search "query"
loom search "query" --project <project-ref>
loom search "query" --scope <scope-ref>
loom search "query" --type file
```

This searches the general object search index, not only LOOM Notes.

## Index Status And Queue

Singular commands:

```bash
loom index status --limit 20
loom index rebuild --object <object-ref>
```

Plural commands:

```bash
loom indexes status --limit 20
loom indexes queue list --limit 20
loom indexes queue show <index-status-id>
loom indexes failures --limit 20
loom indexes explain object <object-ref>
loom indexes rebuild object <object-ref>
loom indexes retry <index-status-id>
loom indexes retry-failed --limit 50
loom indexes run text --once --reason "manual index catch-up"
```

Use read-only status, queue, failures, and explain commands before retrying or
rebuilding.

## Object Store

```bash
loom object list --limit 20
loom object inspect <object-ref>
loom object versions <object-ref>
loom object ingest <path> --name <logical-name>
```

`object ingest` is mutating. Use it only when the goal is to create an
object-store record.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Object Store Indexes And Search]]
- [[Notes Knowledge And Search API]]
- [[LOOM Notes Portal]]
- [[Notes Index Maintenance]]
