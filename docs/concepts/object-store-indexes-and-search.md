---
title: "Object Store Indexes And Search"
description: "Explains LOOM object indexing, general search, notes search, BM25 scoring, hybrid search, and index queues."
audience:
  - operator
  - developer
  - user
tags:
  - loom
  - concepts
  - notes
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/search/bm25.go"
  - "internal/search/fusion.go"
  - "internal/search/search.go"
  - "internal/knowledge/search_bridge.go"
  - "internal/knowledge/semantic_search.go"
  - "go run ./cmd/loom --json search loom --limit 5"
  - "go run ./cmd/loom --json index status --limit 5"
  - "go run ./cmd/loom --json indexes status --limit 5"
  - "go run ./cmd/loom objects --help"
related:
  - "[[Notes Knowledge Index]]"
  - "[[Notes And Search]]"
  - "[[Notes Search And Indexes CLI Reference]]"
  - "[[File Type Support]]"
aliases:
  - "Search Architecture"
  - "Indexing"
---
# Object Store Indexes And Search

## What This Page Covers

LOOM has two closely related search surfaces:

- general object search with `loom search`;
- notes-specific search with `loom notes search`.

Both depend on indexed text. Notes search adds notes roots, file classes,
extraction status, citations, scoped filters, and optional semantic search.

## General Object Search

General object search indexes LOOM objects and object versions. It is useful
when you are searching retained object-store content rather than specifically
notes roots.

Use:

```bash
loom search "query"
```

Filters:

```bash
loom search "query" --project <project-ref>
loom search "query" --scope <scope-ref>
loom search "query" --type file
```

Slice 6 verified:

```bash
loom --json search loom --limit 5
```

The JSON response uses the standard envelope with `ok`, `data`, and `meta`.

## Notes Search

Notes search is more specific:

```bash
loom notes search "query"
```

Notes filters:

- `--project`;
- `--node`;
- `--root`;
- `--file-class`;
- `--path`;
- `--tag`;
- `--mode lexical`;
- `--mode semantic`;
- `--mode hybrid`.

The portal search input also supports scoped tokens such as:

```text
project:osint-tools node:main tag:osint path:reports class:markdown query terms
```

## Lexical Search

The current lexical search uses:

- tokenization;
- PostgreSQL full-text search where indexed;
- BM25 scoring with default `k1=1.2` and `b=0.75`;
- ranking boosts;
- search document metadata.

BM25 scores each document by term frequency, inverse document frequency, and
document length normalization. Shorter focused chunks can rank above long files
when the query terms are concentrated.

## Hybrid Search

Hybrid notes search combines lexical and semantic candidates when embeddings
are enabled and active vectors exist.

The fusion mechanism uses reciprocal rank fusion. That means each ranked list
contributes based on position, and the final result is less dependent on one
score scale.

At verification time embeddings were disabled, so requested hybrid search
returned lexical mode with:

```text
fallback_reason=embeddings_disabled
```

This is healthy when embeddings are intentionally disabled.

## Semantic Search

Semantic search requires:

- notes embeddings enabled;
- active current chunk embeddings;
- an embedding runtime configured;
- query embedding dimensions matching configured dimensions.

Current runtime assumptions are in [[Notes Knowledge Index]].

If semantic mode is requested while embeddings are unavailable, LOOM reports a
fallback reason. Hybrid mode can fall back to lexical. Pure semantic mode
reports semantic unavailability in the result context.

## Index Status

Use:

```bash
loom index status
loom indexes status
```

The singular `index` commands are older workflow commands. The plural
`indexes` tree exposes queue, failures, retry, rebuild, and worker controls.

Useful status filters:

```bash
loom indexes status --failed
loom indexes status --index-type full_text
loom indexes status --object <object-ref>
loom indexes status --project <project-ref>
loom indexes status --scope <scope-ref>
```

Slice 6 verified both singular and plural status commands with `--json`.

## Queue And Failures

Read-only checks:

```bash
loom indexes queue list
loom indexes failures
```

Inspect one queue/status row:

```bash
loom indexes queue show <index-status-id>
```

Explain one object:

```bash
loom indexes explain object <object-ref>
```

At verification time, queue and failure checks returned empty lists, which is
healthy for an idle system.

## Retry And Rebuild

These commands change queue state:

```bash
loom indexes retry <index-status-id>
loom indexes retry-failed --limit 50
loom indexes rebuild object <object-ref>
loom index rebuild --object <object-ref>
```

Use them only after inspecting the failed object or queue row. They are not
read-only examples.

## Worker Runs

The text indexer can be run once:

```bash
loom indexes run text --once --reason "manual index catch-up"
```

Notes-specific workers:

```bash
loom notes run indexer --once --reason "manual notes extraction"
loom notes embeddings run --once --reason "manual embedding catch-up"
```

These are operator actions because they claim work and can write extraction,
chunk, search, or embedding state.

## Object Commands

Use singular `object` for the canonical object-store CLI:

```bash
loom object list
loom object inspect <object-ref>
loom object versions <object-ref>
```

`loom object ingest <path>` is mutating and creates object-store records.

`loom objects` is accepted as a compatibility alias for the same top-level
object-store command tree. It is separate from `loom notes objects`, which lists
and reconciles notes knowledge objects.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Notes Search And Indexes CLI Reference]]
- [[Notes Index Maintenance]]
- [[File Type Support]]
