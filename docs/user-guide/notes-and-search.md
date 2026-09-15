---
title: "Notes And Search"
description: "Use LOOM Notes to see notes roots across the network, reconcile indexed objects, search notes, and understand the read-only projection."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - notes
status: draft
verified_at: "2026-08-17"
source_scope:
  - "go run ./cmd/loom --json notes overview"
  - "go run ./cmd/loom --json notes roots list --all"
  - "go run ./cmd/loom --json notes roots reconcile --dry-run"
  - "go run ./cmd/loom --json notes objects list --limit 5 --all"
  - "go run ./cmd/loom --json notes objects reconcile --dry-run"
  - "go run ./cmd/loom --json notes search loom --limit 5"
  - "go run ./cmd/loom --json notes projection status"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes"
related:
  - "[[Daily Use]]"
  - "[[Notes Knowledge Index]]"
  - "[[Object Store Indexes And Search]]"
  - "[[Notes Search And Indexes CLI Reference]]"
  - "[[LOOM Notes Portal]]"
  - "[[File Type Support]]"
aliases:
  - "LOOM Notes"
  - "Notes Workflow"
---
# Notes And Search

## What This Page Covers

This page explains the daily LOOM Notes workflow:

1. check the notes overview;
2. inspect which roots feed the knowledge index;
3. reconcile roots and objects safely;
4. search indexed notes;
5. understand embeddings, file types, and projection state.

For the implementation model, read [[Notes Knowledge Index]]. For exact command
syntax, read [[Notes Search And Indexes CLI Reference]].

## Mental Model

LOOM Notes does not make one giant writable folder the source of truth.

The source of truth stays in notes roots:

- LOOM Box Notes on each node;
- project `notes/` facets;
- future notes roots explicitly registered by LOOM.

LOOM reconciles those roots into knowledge objects, extracts text where
supported, chunks that text, writes lexical search documents, optionally writes
embeddings, and builds a read-only `loom-notes` projection.

The projection is for reading and browsing. It is not the current write path.
At verification time, `loom notes projection status` reported
`read_only=true` and `raw_writes_supported=false`.

## Open The Portal

Use:

```bash
loom enter --start notes
```

The LOOM Notes surface puts the most user-relevant information first:

- notes search action;
- embedding status;
- search results;
- attention summary;
- coverage totals;
- file type counts;
- notes roots grouped by node and project;
- processing and projection details.

Slice 6 verified the one-shot render:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes
```

At verification time the surface showed 8 active roots, 52 files, 12
directories, 106 search documents, and a read-only projection under
`/var/lib/loom/loom-notes`.

## Check The Overview

Use the overview before running more specific commands:

```bash
loom notes overview
```

JSON output is useful for scripts:

```bash
loom --json notes overview
```

The overview tells you:

- how many notes roots are active;
- how many files and directories are represented;
- total indexed size;
- file classes such as `markdown`, `pdf`, `image`, `text`, and
  `office_document`;
- processing states such as `metadata_only` and `chunked`;
- extraction states such as `extracted`, `too_large`, and
  `source_unavailable`;
- search document count;
- projection state.

## Inspect Notes Roots

List roots:

```bash
loom notes roots list
```

Include inactive roots when troubleshooting:

```bash
loom notes roots list --all
```

Filter by node:

```bash
loom notes roots list --node main
```

Root kinds currently include:

- `box_notes`;
- `project_notes`.

The portal groups the same information under "Notes Across Network".

## Reconcile Roots Safely

Reconciliation is the process that makes known source roots match current
watched-root and project notes configuration.

Preview first:

```bash
loom notes roots reconcile --dry-run
```

Apply only after the dry-run looks right:

```bash
loom notes roots reconcile --apply --yes
```

The Mac CLI seeds local Box Notes candidates during reconcile. On main, the
backend owns the authoritative root records.

## Reconcile Objects Safely

Object reconciliation turns catalogued files under notes roots into knowledge
objects.

Preview:

```bash
loom notes objects reconcile --dry-run
```

Limit to one source root when you are investigating:

```bash
loom notes objects reconcile --dry-run --root <notes-source-root-id>
```

Apply mode writes knowledge-object rows, so use it as an operator action:

```bash
loom notes objects reconcile --apply --yes
```

After reconciliation, inspect the first few objects:

```bash
loom notes objects list --limit 20
```

Show one object:

```bash
loom notes objects show <knowledge-object-ref>
```

## Search Notes

Search all indexed notes:

```bash
loom notes search "threat model"
```

Useful filters:

```bash
loom notes search "runbook" --node main
loom notes search "timeline" --project osint-tools
loom notes search "source" --file-class markdown
loom notes search "incident" --path reports
loom notes search "lead" --tag osint
loom notes search "incident" --after 2026-01-01 --sort newest
```

The portal search box also understands scoped tokens:

```text
project:osint-tools node:main tag:osint path:reports class:markdown after:2026-01-01 sort:newest threat intel
```

Use `--after` and `--before` with RFC3339 timestamps that include a timezone, or
with date-only `YYYY-MM-DD` values. Date-only boundaries mean midnight UTC.
`after` includes the boundary and `before` excludes it. Use `--sort relevance`,
`--sort newest`, or `--sort oldest`; the equivalent query token is
`sort:<order>`.

Normal CLI and Portal rows show one selected source date. JSON output and Portal
inspection explain whether it came from origin filesystem mtime, source-object
metadata, embedded/frontmatter metadata, or observation fallback. Index time is
shown separately because reindexing is operational activity, not evidence that
a file was recently modified.

Recency adjusts the order only after relevance has selected candidates. It can
break a relevance tie in favor of newer material, but it does not pull an
unrelated new file into the results.

Search defaults to requested hybrid mode, but at verification time embeddings
were disabled, so the service returned lexical mode with
`fallback_reason=embeddings_disabled`.

When main is offline, the Notes portal surface and backend search are
unavailable. The date controls do not create an offline cache or mutation path;
restore main connectivity and press `r` before searching again.

## Embeddings Status

Check embeddings before expecting semantic search:

```bash
loom notes embeddings status
```

At verification time:

- embeddings were disabled;
- runtime was `ollama`;
- model was `mxbai-embed-large`;
- dimensions were `1024`;
- active vectors were `0`;
- all current objects were `disabled_by_policy`.

Enabling or disabling embeddings is a sensitive global action:

```bash
loom notes embeddings enable --yes
loom notes embeddings disable --yes
```

The portal exposes the same action behind a confirmation prompt. Do not enable
embeddings just to test docs unless that is part of an accepted operator plan.

## Projection

The projection is a read-only generated view:

```bash
loom notes projection status
```

Dry-run a rebuild:

```bash
loom notes projection rebuild --dry-run --max-results 20
```

Apply mode rebuilds the generated projection:

```bash
loom notes projection rebuild --apply --yes
```

The projection helps users browse notes from different nodes and projects in
one place. It should not be treated as the canonical writable source.

## File Types

LOOM accepts all catalogued file classes in notes roots, but extraction depth
depends on file class and extractor support.

Examples:

- Markdown and text can be extracted and chunked.
- Code and structured data have source-aware extraction when queued.
- PDFs use embedded text extraction and metadata, not OCR.
- DOCX has body extraction.
- `.gdoc`, legacy Office formats, images, media, archives, packages, binaries,
  and unknown files are metadata-oriented unless a later extractor adds body
  support.

See [[File Type Support]] for the detailed table.

## When Something Looks Wrong

Start with:

```bash
loom notes overview
loom notes roots list --all
loom notes objects list --limit 20 --all
loom notes projection status
loom notes embeddings status
```

Then use Doctor:

```bash
loom enter --start doctor
```

Common interpretations:

- `metadata_only` is normal for directories, unsupported body formats, large
  files, and files tracked only for metadata.
- `too_large` means LOOM retained metadata but did not extract body text because
  a size or page limit was exceeded.
- `password_required` means a PDF appears encrypted or password-protected.
- `no_embedded_text` means text extraction found no embedded text. OCR is not
  active in this version.
- `source_unavailable` means LOOM could not read the original source path.

## Related Docs

- [[Notes Knowledge Index]]
- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[LOOM Notes Portal]]
- [[Notes Index Maintenance]]
- [[File Type Support]]
