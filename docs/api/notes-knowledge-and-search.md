---
title: "Notes Knowledge And Search API"
description: "HTTP route guide for notes roots, notes objects, notes search, projection, embeddings, object search, and index queues."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/httpapi/knowledge.go"
  - "internal/localclient/knowledge.go"
  - "internal/localclient/client.go"
related:
  - "[[API Reference]]"
  - "[[Notes And Search]]"
  - "[[Notes Knowledge Index]]"
  - "[[Object Store Indexes And Search]]"
aliases:
  - "Notes API"
  - "Knowledge Search API"
---
# Notes Knowledge And Search API

## What This Page Covers

This page maps the private LOOM HTTP API routes behind notes, search, objects,
projection, embeddings, and index queues.

The notes search contract is verified through service, HTTP, local-client, CLI,
and Portal tests.

## Notes Route Groups

| Route | Method | Purpose |
|---|---|---|
| `/v1/knowledge/notes/overview` | GET | Notes overview with roots, file classes, processing, search, embeddings, and projection summary. |
| `/v1/knowledge/notes/roots` | GET | List notes source roots. |
| `/v1/knowledge/notes/roots/reconcile` | POST | Dry-run or apply notes source-root reconciliation. |
| `/v1/knowledge/notes/objects` | GET | List notes knowledge objects. |
| `/v1/knowledge/notes/objects/{ref}` | GET | Inspect one notes knowledge object. |
| `/v1/knowledge/notes/objects/reconcile` | POST | Dry-run or apply object reconciliation from catalogued notes files. |
| `/v1/knowledge/notes/search` | POST | Search notes knowledge documents. |
| `/v1/knowledge/notes/reprocess` | POST | Queue notes reprocessing. |
| `/v1/knowledge/notes/pipelines/status` | GET | Overall unified pipeline, policy, heavy-resource, tool, runtime, and model status. |
| `/v1/knowledge/notes/pipelines` | GET | List unified pipelines with optional status/object filters. |
| `/v1/knowledge/notes/pipelines/failures` | GET | List failed or blocked pipelines. |
| `/v1/knowledge/notes/pipelines/{ref}` | GET | Inspect stages, page progress, and artifact metadata; artifact bodies are omitted. |
| `/v1/knowledge/notes/pipelines/{ref}/retry` | POST | Retry as a new generation. |
| `/v1/knowledge/notes/pipelines/policy` | GET, POST | Read or update OCR, image-description, and embedding policy. |
| `/v1/knowledge/notes/pipelines/backfill` | GET, POST | Plan or explicitly apply the idempotent unified-pipeline backfill. |
| `/v1/knowledge/notes/projection/status` | GET | Inspect notes projection status. |
| `/v1/knowledge/notes/projection/rebuild` | POST | Dry-run or apply notes projection rebuild. |
| `/v1/knowledge/notes/embeddings/status` | GET | Inspect embedding settings, queue, and vector counts. |
| `/v1/knowledge/notes/embeddings/enable` | POST | Enable notes embeddings. |
| `/v1/knowledge/notes/embeddings/disable` | POST | Disable notes embeddings. |
| `/v1/knowledge/notes/workers/coordinator/run` | POST | Run the unified coordinator once. |
| `/v1/knowledge/notes/workers/heavy/run` | POST | Run the globally admitted heavy executor once. |
| `/v1/knowledge/notes/workers/indexer/run` | POST | Compatibility alias for the coordinator. |
| `/v1/knowledge/notes/workers/embedder/run` | POST | Compatibility alias for the heavy executor; the specialized queue writer is retired. |

## Object And Search Route Groups

| Route | Method | Purpose |
|---|---|---|
| `/v1/objects` | GET | List objects. |
| `/v1/objects/{ref}` | GET | Inspect one object. |
| `/v1/objects/{ref}/versions` | GET | List object versions. |
| `/v1/objects/ingest` | POST | Ingest a local file into the object store. |
| `/v1/search` | POST | General object search. |
| `/v1/index/status` | GET | Legacy/singular index status route. |
| `/v1/index/rebuild` | POST | Legacy/singular object index rebuild route. |
| `/v1/indexes/status` | GET | List index status records. |
| `/v1/indexes/queue` | GET | List queued index work. |
| `/v1/indexes/queue/{ref}` | GET | Inspect one queue/status record. |
| `/v1/indexes/failures` | GET | List failed index work. |
| `/v1/indexes/explain` | GET | Explain index state for an object. |
| `/v1/indexes/retry` | POST | Retry one failed or blocked index item. |
| `/v1/indexes/retry-failed` | POST | Retry failed index items. |
| `/v1/indexes/rebuild/object` | POST | Queue rebuild for an object's latest version. |

## Envelopes

Most notes, search, object, and index API calls use the standard LOOM response
envelope:

- `ok`;
- `data`;
- `meta`;
- structured error details on failure.

Some CLI commands render the envelope `data` directly when `--json` is used.
Check the local-client method and CLI renderer before depending on exact output
shape.

## Notes Search Input

Notes search accepts:

- `query`;
- `mode`;
- `phrases`;
- `project_id` or `project_ref`;
- `source_node_key`;
- `notes_source_root_id` or `root_ref`;
- `file_class`;
- `path`;
- `tags`;
- `after`;
- `before`;
- `sort`;
- `limit`.

The local CLI can parse scoped query tokens into this shape. For example:

```text
project:osint-tools node:main tag:osint path:reports class:markdown threat intel
```

`after` is inclusive and `before` is exclusive. Each accepts RFC3339 with an
explicit timezone or `YYYY-MM-DD`; date-only values mean midnight UTC. `sort`
accepts `relevance`, `newest`, or `oldest`.

Example request body:

```json
{
  "query": "incident runbook",
  "mode": "hybrid",
  "project_ref": "osint-tools",
  "after": "2026-01-01",
  "before": "2026-08-01T12:00:00+02:00",
  "sort": "newest",
  "limit": 10
}
```

Each result can include:

- `source_created_at` and `source_modified_at` for normalized source
  chronology;
- `recency_at` and `recency_basis` for the selected time and provenance;
- `recency_score` and `recency_rank` for ranking diagnostics;
- `observed_at` for when LOOM observed the source revision;
- `indexed_at` for operational index activity.

`indexed_at` is not a recency source. A fallback selected from observation time
is explicitly labelled with `recency_basis=observed_at_fallback`.

Invalid date syntax returns a different user-facing summary from an empty
query. Unknown JSON fields remain rejected.

## Projection Rebuild Input

Projection rebuild supports:

- dry-run versus apply;
- maximum object count;
- maximum returned result count;
- include-all details.

Use dry-run first. Apply mode writes generated projection files.

## Embeddings Routes

Embedding status is read-only.

Enable and disable routes are sensitive and idempotent through the LOOM client
path. They change global notes embedding policy, so users should normally use
the CLI or portal confirmation workflow instead of raw HTTP.

## Unified Pipeline And Backfill Routes

Pipeline status and inspection return operational metadata only. Derived
artifact text and payload references are stripped from inspect responses.
Retry, policy update, and backfill apply use idempotency handling.

`GET /v1/knowledge/notes/pipelines/backfill` is always a dry-run. `POST` needs
`{"confirm":true}`. Apply creates at most one unified pipeline for each current
object revision, preserves compatible chunks, lexical documents, and vectors,
marks legacy embedding rows historical without dropping their tables, and
refuses retirement while active legacy claims exist.

## Worker Run Routes

Worker run routes are operator actions. They can claim work and write:

- extraction statuses;
- chunks;
- search documents;
- unified stages, derived artifacts, lexical documents, and vector state;
- worker run records.

Use these routes only through CLI or portal actions unless a developer test
explicitly needs the lower-level route.

## Current Verification

Slice 6 verified these behaviors through CLI calls:

- notes overview;
- roots list;
- roots reconcile dry-run;
- objects list;
- objects reconcile dry-run;
- notes search;
- embeddings status;
- projection status;
- projection rebuild dry-run;
- general search;
- index status;
- queue and failure list;
- object list.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[API Route Index]]
