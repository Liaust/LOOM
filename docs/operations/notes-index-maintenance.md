---
title: "Notes Index Maintenance"
description: "Operational guide for keeping LOOM Notes roots, objects, extraction, search documents, embeddings, and projection healthy."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "go run ./cmd/loom --json notes overview"
  - "go run ./cmd/loom --json notes roots reconcile --dry-run"
  - "go run ./cmd/loom --json notes objects reconcile --dry-run"
  - "go run ./cmd/loom --json indexes failures --limit 5"
  - "go run ./cmd/loom --json notes projection rebuild --dry-run --max-results 5 --max-objects 10"
related:
  - "[[Operations]]"
  - "[[Notes And Search]]"
  - "[[Object Store Indexes And Search]]"
  - "[[Notes Search And Indexes CLI Reference]]"
aliases:
  - "Notes Maintenance"
  - "Knowledge Index Maintenance"
---
# Notes Index Maintenance

## What This Page Covers

This page is for operators maintaining the LOOM Notes index.

It covers safe inspection, dry-run reconciliation, when to run workers, and how
to interpret failures without turning routine docs into destructive operations.

## First Checks

Start with read-only state:

```bash
loom notes overview
loom notes pipelines status
loom notes pipelines failures
loom notes pipelines policy status
loom notes roots list --all
loom notes objects list --limit 20 --all
loom notes embeddings status
loom notes projection status
loom indexes failures --limit 20
loom indexes queue list --limit 20
```

Then check Doctor:

```bash
loom enter --start doctor
```

The portal Notes surface is also useful because it places attention, coverage,
file types, roots, processing, and projection in one view.

## Reconcile Roots

Dry-run:

```bash
loom notes roots reconcile --dry-run
```

Apply:

```bash
loom notes roots reconcile --apply --yes
```

Run root reconciliation when:

- a new Box Notes root should be visible;
- a project notes facet was added;
- a root was disabled or deleted;
- portal coverage does not match expected input points.

## Reconcile Objects

Dry-run:

```bash
loom notes objects reconcile --dry-run
```

Apply:

```bash
loom notes objects reconcile --apply --yes
```

Run object reconciliation when:

- files exist under notes roots but do not appear as knowledge objects;
- a root was newly registered;
- storage catalog state changed after a node sync;
- object counts differ from the expected root file counts.

Use `--root` to limit blast radius during investigation.

## Reprocess Notes

Reprocessing queues extraction work:

```bash
loom notes reprocess stale --limit 200
loom notes reprocess root <root-ref> --limit 200
loom notes reprocess object <knowledge-object-ref>
loom notes reprocess file-class markdown --limit 200
```

Use `--force` only when you intend to mark completed pipeline rows stale and
requeue them.

Reprocessing is appropriate when:

- extractor code changed;
- a file class needs a refresh;
- stale objects remain after source changes;
- a specific object has wrong extraction metadata.

## Run Workers Once

Unified coordinator:

```bash
loom notes run coordinator --once --reason "manual Notes pipeline catch-up"
```

Text indexer:

```bash
loom indexes run text --once --reason "manual text indexing catch-up"
```

Unified heavy executor:

```bash
loom notes run heavy --once --reason "manual Notes heavy-stage catch-up"
```

These commands claim work and write state. Prefer letting scheduled workers run
unless you are doing a controlled maintenance pass.

## Projection Maintenance

Check:

```bash
loom notes projection status
```

Dry-run rebuild:

```bash
loom notes projection rebuild --dry-run --max-results 100
```

Apply:

```bash
loom notes projection rebuild --apply --yes
```

A projection rebuild is appropriate when:

- source objects changed but projection files are stale;
- projection root is missing;
- projection findings show missing or skipped entries that have been repaired.

Projection rebuilds generated output. They should not change canonical notes
sources, and they do not build or refresh a generated Storage export.

## Embeddings Maintenance

Check:

```bash
loom notes embeddings status
```

Enable only with an accepted operator decision:

```bash
loom notes embeddings enable --yes
```

Disable:

```bash
loom notes embeddings disable --yes
```

When disabled, hybrid search falls back to lexical search. That is expected,
not a failure.

## Failure Triage

Use:

```bash
loom indexes failures --limit 50
loom indexes explain object <object-ref>
loom notes objects show <knowledge-object-ref>
```

Common extraction states:

- `too_large`: metadata retained, body skipped due to size or page limit;
- `password_required`: PDF appears encrypted;
- `no_embedded_text`: no embedded text was found; inspect PDF OCR policy, tools, and selected-page progress;
- `source_unavailable`: the source file could not be read;
- `unsupported_body_extraction`: metadata indexed, body extractor unavailable.

Retry only after understanding the failure:

```bash
loom indexes retry <index-status-id>
loom indexes retry-failed --limit 20
```

Unified pipeline failures use:

```bash
loom notes pipelines failures
loom notes pipelines inspect <pipeline-ref>
loom notes pipelines retry <pipeline-ref> --yes
```

## Controlled Backfill

After migrations and runtime configuration are reviewed, plan first:

```bash
loom notes pipelines backfill
```

Review file-class totals, stage counts, reused chunks/lexical documents/vectors,
source reprocessing, blocked reasons, and active legacy claims. Apply only after
that review:

```bash
loom notes pipelines backfill --apply --yes
```

Apply is idempotent. It never drops legacy embedding tables and fails closed if
an active legacy embedding claim remains. Production apply and hardware
validation require the feature acceptance checklist; local tests do not prove
model availability or performance.

## Safe Maintenance Order

Use this order for a normal notes repair pass:

1. `loom notes overview`
2. `loom notes roots list --all`
3. `loom notes roots reconcile --dry-run`
4. `loom notes objects reconcile --dry-run`
5. `loom notes pipelines status` and `loom notes pipelines failures`
6. `loom notes projection rebuild --dry-run --max-results 100`
7. Apply only the smallest reviewed reconciliation or rebuild step.
8. Recheck overview, search, and portal.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[LOOM Notes Portal]]
