---
title: "Object Store Diagnostics Portal"
description: "Guide to the portal surface for object records, index status, deletion requests, and object-store diagnostics."
audience:
  - operator
  - developer
tags:
  - loom
  - portal
  - storage
  - notes
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start database"
  - "internal/loomcli/portal/database_render.go"
  - "internal/loomcli/portal/database_actions.go"
  - "internal/loomcli/portal/action_selectors.go"
related:
  - "[[Portal Guide]]"
  - "[[Notes Search And Indexes CLI Reference]]"
  - "[[Storage Box And Lane CLI Reference]]"
  - "[[Object Store Indexes And Search]]"
  - "[[Nodes And Capabilities Portal]]"
aliases:
  - "Database Portal"
  - "Object Diagnostics"
  - "Object Store Portal"
---
# Object Store Diagnostics Portal

## What This Page Covers

Object Store Diagnostics is the portal surface for object metadata and index
health. Open it with:

```bash
loom enter --start database
```

The historical name "database" still appears in the start id and command mode
scope. The human-facing surface title is Object Store Diagnostics.

## What The Surface Loads

The surface combines:

- object records;
- object version and source metadata;
- index status rows;
- index queue and failures;
- sync status;
- sync batches, conflicts, replicas, private backups, and deletion requests;
- object-store maintenance status;
- object search results when search mode is used;
- worker actions for indexing and object-store diagnostics.

It is an operator diagnostics surface. It is not the primary daily notes search
surface. For user-facing notes search, use [[LOOM Notes Portal]].

## Verified Render

Slice 11 verified a one-shot render. The current render showed:

- one pending deletion request attention item;
- object diagnostics summary with `objects=25`, `user_objects=15`,
  `indexed=20`, `queued=0`, `failed=0`, and `open_conflicts=0`;
- recent object rows such as `README.md`, `AGENTS.md`, and `loom.notes.yaml`;
- indexed object rows with extraction/indexing types such as `chunking`,
  `full_text`, and `text_extraction`;
- actions for explaining object index state, rebuilding an object index,
  retrying failed index work, and running the text indexer once.

Counts will change as objects and notes change. Treat the verified numbers as
evidence of the render shape, not as fixed expected state.

## Top-Level Sections

| Section | Meaning |
|---|---|
| Available Actions | Contextual object/index/deletion actions. Sensitive actions are marked by risk. |
| Diagnostics Attention | Deletion requests, index failures, conflicts, or other rows requiring review. |
| Diagnostics Summary | Object and index totals plus recent/failure hints. |
| Recent Object Changes | Recently changed objects and their index state. |
| Indexed Object Summary | Recent index status records. |

Use `tab` for raw/details mode when you need object ids, index ids, sync ids,
worker run ids, or deletion request ids.

## Available Actions

Common actions include:

| Action | Safety | Meaning |
|---|---|---|
| Explain Object Index | inspect | Shows index status and object relationship. |
| Rebuild Object Index | safe_run/operator action | Queues fresh index work for one object. |
| Retry Failed Index Work | safe_run/operator action | Retries failed index work. |
| Run Text Indexer Once | safe_run/operator action | Runs the text indexer once. |
| Review Deletion Request | sensitive workflow | Starts or inspects deletion review. |
| Approve/Deny/Complete Deletion Request | sensitive workflow | Changes deletion request lifecycle. |

Even when an action is displayed as `safe_run`, review the selected object or
failure row first. Rebuilding an index or retrying work changes queue state.

## Object Search

Press `s` from the surface to use object diagnostics search, or use scoped
search:

```text
#database object
#database README
```

Use command mode when you need exact CLI output:

```text
$ loom object list --limit 20
$ loom indexes status --limit 20
$ loom indexes explain object <object-ref>
```

## Interpreting Attention

Attention on this surface does not always mean data loss. It can mean:

- a deletion request is pending review;
- an index job failed;
- an object has queued or stale index work;
- a sync conflict exists;
- object-store maintenance has an open finding.

For deletion requests, review safe-delete, backup, retention, sync, and source
path state before approving anything. For index failures, inspect the failure
row and object metadata before retrying.

## Relationship To Notes And Storage

Object Store Diagnostics is lower-level than notes and storage pages:

- [[LOOM Notes Portal]] summarizes notes roots, file types, search, projection,
  and embeddings status.
- [[Storage And Timeline Portal]] summarizes storage safety, retention,
  transfers, and timeline events.
- Object Store Diagnostics shows the underlying object and index records that
  those higher-level views depend on.

When a higher-level surface looks wrong, this page helps determine whether the
problem is object metadata, extraction/indexing, sync, or deletion lifecycle.

## Related Docs

- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[Storage Box And Lane CLI Reference]]
- [[Nodes And Capabilities Portal]]
- [[Diagnostics And Support]]
