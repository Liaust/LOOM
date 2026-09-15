---
title: "LOOM Notes Portal"
description: "Use the portal LOOM Notes surface to inspect notes coverage, file types, search, embeddings, roots, processing health, and projection state."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes"
  - "internal/loomcli/portal/notes_render.go"
  - "internal/loomcli/portal/notes_actions.go"
  - "internal/loomcli/portal/notes_loader.go"
related:
  - "[[Portal Guide]]"
  - "[[Notes And Search]]"
  - "[[Notes Knowledge Index]]"
  - "[[Notes Search And Indexes CLI Reference]]"
aliases:
  - "Notes Portal"
  - "Portal Notes"
---
# LOOM Notes Portal

## What This Page Covers

This page explains the LOOM Notes portal surface and how to interpret it.

Open it with:

```bash
loom enter --start notes
```

One-shot render for verification:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes
```

## Pipeline Actions

Pipeline actions are compacted into four groups:

- `Process...`: run the coordinator or globally admitted heavy executor once;
- `Inspect...`: inspect status, current runs, roots, and objects;
- `Policies...`: inspect pipeline policy and enable or disable embeddings;
- `Repair...`: inspect failed or blocked pipelines before retrying.

Mutating actions require main availability and confirmation where appropriate.
When main is offline, cached read-only material may remain visible but pipeline
actions are unavailable.

## Pipelines Line

The compact status shows active pipelines, waiting-heavy work, lexical and
semantic coverage, the current file/stage, policy, and heavy-executor
availability. Failed or blocked counts remain hidden when zero.

Raw/details mode adds stale claims, activation mismatches, queue age, resource
leases, and separate missing-tool, missing-runtime, and missing-model findings.
It does not display source text, OCR text, image descriptions, image bytes, or
vectors.

The embeddings policy action maps to:

```bash
loom notes embeddings enable --yes
loom notes embeddings disable --yes
```

Use the action only when changing global notes embedding policy is intentional.

## Search Prompt

The Notes surface shows:

```text
s notes search  Search notes...
```

Press `s` to search indexed notes from inside the portal.

The portal supports scoped search tokens:

```text
project:<slug> node:<key> tag:<tag> path:<path> class:<file-class> after:<date> before:<date> sort:<order> query
```

Search results show:

- title;
- location;
- one selected source date;
- match/source/extraction context;
- snippet;
- citation;
- source/observation/index provenance and score details in raw view.

`after:` is inclusive, `before:` is exclusive, and date-only values resolve to
midnight UTC. `sort:` accepts `relevance`, `newest`, or `oldest`. Normal rows
show one compact selected date. Raw details and result inspection show source
created/modified time, selected recency time and basis, observation fallback
when used, recency diagnostics, and index time.

Notes search requires main. When main is offline the Notes surface is marked
unavailable and `s` does not issue a backend request. LOOM does not create a
parallel offline search index for these date controls. Read-only chronology may
be rendered only when it is already part of an available view.

## Embeddings Line

The embeddings line shows:

- on/off state;
- runtime;
- model;
- queued count;
- ready count;
- processing count;
- active vector count;
- failures.

The embeddings line remains as a concise compatibility policy indicator; the
unified Pipelines line is the primary work-health surface.

## Notes Attention

Attention appears when Doctor has notes issues or local counters indicate a
problem.

Examples:

- failed index rows;
- `too_large` extraction counts;
- `password_required` counts;
- `no_embedded_text` counts;
- source unavailable counts;
- queued or processing backlog;
- projection findings or missing projection entries;
- missing projection root.

The normal next step is:

```bash
loom enter --start doctor
```

## Coverage

Coverage shows:

- total roots;
- active roots;
- files;
- directories;
- total bytes;
- search document count;
- generation time.

This is the fastest way to answer whether LOOM sees notes across the network.

## File Types

The File Types section aggregates active visible roots by file class.

Examples from verification:

- `markdown`;
- `pdf`;
- `image`;
- `text`;
- `directory`;
- `office_document`.

See [[File Type Support]] for what each file class means.

## Notes Across Network

This is the human map of notes input points.

It groups roots by node and then shows:

- LOOM Box Notes;
- project `notes/` facets;
- file counts per root.

In raw/details mode, root status and root IDs are also visible.

## Processing And Search Health

This section shows:

- object processing states;
- extraction states;
- queued and processing pipeline counts;
- complete and failed pipeline counts;
- skipped unsupported counts;
- search document count;
- last indexed time;
- last pipeline update time.

Use this section to decide whether search is stale, blocked, or healthy.

## Projection

Projection shows:

- projection root;
- whether it exists;
- whether it is read-only;
- whether raw writes are supported;
- entry counts;
- materialized count;
- missing count;
- skipped count;
- finding count;
- last rebuild time.

Current verified behavior is `read_only=true` and `raw_writes=false`.

## Selectable Records

The Notes surface has selectable records for:

- active unified pipelines;
- notes roots;
- search results.

Root inspect actions show root metadata. Search result inspect actions show the
search document, knowledge object, chunk, source, extraction state, normalized
source chronology, selected recency provenance, index time, and scoring
metadata.

## When To Record A Bug

Record a portal bug if:

- notes roots appear in CLI output but not the portal;
- file type totals contradict `loom notes overview`;
- projection says writable when CLI status says raw writes are unsupported;
- embeddings toggle runs without confirmation;
- search filters are parsed differently between portal and CLI;
- attention counters do not match Doctor or overview details.

Historical v0.9.6 findings remain archived. Capture new bugs through the current
`.project/` workflow.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Notes Search And Indexes CLI Reference]]
- [[Portal First Tour]]
- [[Notes Index Maintenance]]
