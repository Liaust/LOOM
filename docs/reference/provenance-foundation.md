---
title: "Provenance Foundation Reference"
description: "Reference for the isolated provenance ledger, typed search and exact-get routes, project/repository projections, bounds, recovery metadata, and manual Archivist boundary."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - reference
  - api
  - database
  - security
status: verified
created_at: "2026-08-30T06:03:18Z"
updated_at: "2026-08-31T00:00:00Z"
verified_at: "2026-08-31"
source_scope:
  - "internal/provenance"
  - "internal/provenance/search.go"
  - "internal/provenance/project_projection.go"
  - "internal/workers/runtimes/provenance_archivist.go"
  - "internal/httpapi/provenance.go"
  - "internal/localclient/provenance.go"
  - "internal/capabilities/seed.go"
  - "internal/provenance/recovery_package.go"
  - "internal/maintenance/backup_manifest.go"
  - "internal/backup/restore_drill.go"
  - "internal/workers/runtimes/main_backup.go"
  - "internal/httpapi/backup_coverage.go"
  - "internal/backupstrategy/archive_manifest.go"
  - "internal/cloudstorage/direct_archive.go"
  - "ai-loom-pack/templates/archivist"
  - "internal/knowledge/passages.go"
  - "internal/provenance/box_source_citation_test.go"
related:
  - "[[Provenance Runtime Operations]]"
  - "[[Database Schemas]]"
  - "[[API Route Index]]"
  - "[[Capability Addresses]]"
aliases:
  - "Semantic Ledger Foundation Reference"
---
# Provenance Foundation Reference

## Boundary

The provenance runtime provides exact and bounded semantic-ledger operations.
It preserves candidates, records, sources/evidence, relationships, resolution
cases, representations, lifecycle events, and operation replay. The accepted
search/reconciliation extension adds bounded lexical search, rebuildable
project/repository snapshots, semantic repository discovery, exact CLI reads,
and a deterministic manual-only Archivist worker.

It does not provide embeddings, automatic broad extraction, Archivist
scheduling, Basecamp integration, a second project registry, a private agent
memory, or an upstream SQLite importer.

## Notes Evidence And Historical Citations

The Notes engine indexes source material, not accepted decisions. Registered
Notes, Topics drafts, Library sources and explicitly declared project
docs/research can supply evidence. Objects remains the first tool for custody,
identity and technical state; Notes is first for source wording; Provenance is
first for qualified assertions, accepted decisions and unresolved cases.
Documents and Imports do not become Notes sources merely by existing in Box.

A selected passage can support a deliberately registered pending candidate.
Its source submitted/details payload records the knowledge object ID, knowledge
version ID, chunk ID, source hash, heading/page locator and citation-time
category, owner and project context. The existing Provenance source fields pin
the exact passage URL (`canonical_locator`), version (`version_address`) and
source hash (`content_digest`). A locator alone is not proof that content was
verified, nor permission to accept the assertion.

Use the exact stored citation to follow the evidence:

```text
loom notes passage get <chunk-id> --object <object-id> --version <version-id> --source-hash sha256:<digest>
```

The matching read-only endpoint is
`GET /v1/knowledge/notes/passages/<chunk-id>?object_id=<object-id>&version_id=<version-id>&source_hash=sha256:<digest>`.
All four bindings are required. It returns `loom.notes.passage.v1`, the same
identities, untruncated `text`, `chunk_hash`, `structural_path`, `historical` and
`current_source_context`. The latter is explicitly current classification;
historical classification comes from the original citation, not a reconstructed
claim about an old root policy. The text bound is 65,536 UTF-8 bytes. Oversized,
missing, disabled, metadata-only or inconsistent passages are unavailable,
not silently abbreviated quotations.

For a new citation, obtain the current source hash from
`loom notes objects show <object-id>` and require the passage read to agree with
the search result's version and chunk. If a concurrent edit breaks that binding,
refresh the search for the new citation. Never do this to replace an existing
historical citation. Object metadata is not a passage read, and pipeline
diagnostics intentionally remain text-redacted.

Historical reads check current registered-root, owner, project and catalog or
replica eligibility. Privacy changes, deletion and owner-reported access loss
can therefore make old text unavailable. Reads use current recorded evidence;
they do not poll an offline owner's filesystem. Invalid bindings return 400;
missing, mismatched and inaccessible passages share a non-disclosing 404.
No raw-file, database or archive-path fallback is permitted.

Editing a source does not rewrite its prior citation or supersede a decision.
Topics remain drafts and Library claims remain attributed, even if several
sources agree. A separate authorized review operation establishes acceptance
or reconciliation. A conflicting hourly proposal may remain pending while a
daily decision remains accepted. Report unavailable evidence honestly while
preserving its identity and the semantic history.

This contract is verified in disposable Slice 4 acceptance. It does not claim
Main deployment, unattended all-category ingestion or archived-workspace reads;
the latter require the separate Notes archive lifecycle integration.

## Runtime And Configuration

| Contract | Value |
|---|---|
| Process | existing `loomd` |
| Database | `loom_provenance` |
| Database role | `loom_provenance` |
| Environment variable | `LOOM_PROVENANCE_DB_URL` |
| Daemon flag | `--provenance-db-url` |
| Schema version | `1.0` |
| Packaged migration head | `7` |
| Migration owner | `internal/provenance/migrations/*.sql` |

The provenance URL is mandatory for `loomd`. The configured database and role
are checked both before and after connection. The runtime refuses `loom_main`,
the wrong role, an unavailable database, a behind/ahead schema, migration
history mismatch, or a tampered schema.

## Owned Relations

The `provenance` schema owns exactly these foundation relations:

```text
schema_migrations
candidates
source_references
records
record_sources
record_producers
relationships
resolution_cases
case_members
candidate_events
record_events
relationship_events
case_events
processing_runs
operation_history
evidence_registrations
candidate_evidence_links
candidate_lineage
registration_replays
project_projection_snapshots
repository_projection_snapshots
```

There are no provenance relations in `loom_main`. The two projection relations
are append-only and rebuildable from accepted LOOM project/repository state;
they do not replace the technical project registry. Archivist scheduling,
clarification-delivery, and a parallel lifecycle store remain excluded.

## Capability Authorization

| Capability | Level | Surface |
|---|---:|---|
| `main@provenance.health.read` | 1 | readiness metadata |
| `main@provenance.foundation.read` | 2 | bounded list and exact get |
| `main@provenance.candidate.register` | 3 | candidate registration |
| `main@provenance.lifecycle.apply` | 4 | explicit manual lifecycle |

Every route resolves the authenticated LOOM actor, origin node, and scope from
the main database, then asks the normal policy service for the exact capability.
A candidate producer, source actor, artifact author, or approving actor is
semantic evidence only. Matching an authenticated actor ID in submitted content
does not grant authority.

## HTTP Routes

| Method | Route | Capability | Result |
|---|---|---|---|
| `GET` | `/v1/provenance/health` | health read | database/schema readiness |
| `POST` | `/v1/provenance/search` | foundation read | typed project-state, repository-state, accepted-record, unresolved-case, and opt-in pending search |
| `GET` | `/v1/provenance/projects/{project-id}` | foundation read | captured project context; optional `snapshot_id` selects an immutable citation |
| `GET` | `/v1/provenance/repos` | foundation read | compact semantic repository cards |
| `GET` | `/v1/provenance/repos/{repo-id}` | foundation read | exact repository awareness card |
| `POST` | `/v1/provenance/projects/{project-ref}/sync` | lifecycle apply plus project read | manual one-project projection sync |
| `GET` | `/v1/provenance/candidates` | foundation read | candidate summaries |
| `POST` | `/v1/provenance/candidates` | candidate register | replay-safe registration receipt |
| `GET` | `/v1/provenance/candidates/{uuid}` | foundation read | exact candidate lifecycle |
| `GET` | `/v1/provenance/records` | foundation read | record summaries |
| `GET` | `/v1/provenance/records/{uuid}` | foundation read | exact record lifecycle |
| `GET` | `/v1/provenance/relationships` | foundation read | relationship summaries |
| `GET` | `/v1/provenance/relationships/{uuid}` | foundation read | exact relationship lifecycle |
| `GET` | `/v1/provenance/cases` | foundation read | resolution-case summaries |
| `GET` | `/v1/provenance/cases/{uuid}` | foundation read | exact case lifecycle |
| `POST` | `/v1/provenance/operations` | lifecycle apply | manual-operation receipts |

Mutation requests require `X-Loom-Idempotency-Key`. Registration replay binds
the key and semantic workflow digest to the original candidate IDs and receipts.
Manual operations carry stable UUID operation IDs and append evidence rather
than rewriting the original candidate or prior event.

## List And Transport Bounds

- Request deadline: 5 seconds.
- Maximum request and response body: 8 MiB each.
- Maximum registration or manual-operation batch: 100 items.
- Default/max page size: 50/100.
- Domain and visibility filters: 256 bytes each.
- Summary claim/context excerpt: 512 bytes.
- Registration source-posture receipt fields: 128 bytes.
- Maximum sources/evidence links per semantic object: 50.

List routes accept `limit`, `domain`, and `visibility`. Stable continuation uses
the tuple `after_time` plus `after_id`; supplying only one cursor component is
invalid. Exact routes require a canonical UUID and accept a bounded `limit` for
lifecycle history.

Transport registration receipts omit source context, submitted documents, and
internal payload JSON. Authorized exact reads may return semantic content;
health and support outputs may not.

## Manual Lifecycle Operations

The foundation accepts only the explicit operation types represented by the
included kernel: candidate accept, consolidate, reject, and defer; candidate
evidence linking; record representation/qualification/posture/temporal events;
relationship add/end; and resolution-case create/member/event/close operations.

Operations are scoped by domain and visibility. Terminal candidate and case
outcomes are monotonic. Exact replay returns the prior receipt; a changed
payload under the same replay identity conflicts without partial mutation.

## Search, Repository Discovery, And Exact Get

`loom provenance search <query>` returns separate `project_state`, `repo_state`,
`accepted_records`, and `unresolved_cases` collections by default. Pending
candidates appear only through `--include-pending` or the explicit
`pending_candidates` collection and remain in their own typed field. An
explicit `--collection` selection is exact, apart from an independently
explicit `--include-pending` opt-in.

Repository search matches come only from the latest repository projections
attached to each owning project's latest snapshot and use the stored bounded
searchable summary. A compact match carries the exact repository ID,
rebuildable-projection posture, observed currentness, owning project, and
`/v1/provenance/repos/{repo-id}` navigation. Full awareness cards remain on
`loom provenance repo list|get`; search does not copy their accepted context,
diagnostics, or complete field-source map into the compact response.

Project matches capture the registered owner's selected `.project` Markdown,
feature metadata and decision headings, without requiring Git or `.repo`.
They are source declarations, not accepted semantic records. Each result names
its project and immutable snapshot. `loom provenance project get <project-id>
--snapshot <snapshot-id>` returns the captured excerpts, file hashes and omission
postures without rereading today's files. Omitting `--snapshot` selects the latest
stored capture, not an implicit refresh.

`main.project_context_refresh` uses the existing worker supervisor: every minute,
at most twenty registered owner-local projects, selected by a durable rotating
cursor. It reads bounded metadata, never crawls repository payloads, accepts
candidates or starts the Archivist. Existing repository observations retain
their original freshness while unchanged authoritative membership is rebound
to the new parent snapshot. Explicit `loom provenance project sync <ref>` remains
available for diagnostics and full repository observation. This source is locally
implemented; deployment and live adoption remain a separate operator step.

All five collections share the frozen total, per-collection, 800-code-point
result, and 6,000-code-point response bounds. Ordering and the opaque cursor
compose across collection boundaries without merging their types. Existing
lifecycle cursor IDs remain canonical UUIDs; repository cursor IDs use the
typed `repo_` contract, while project cursors use typed `project_` IDs. Project
and repository filters apply before composition. Use `project get`, `repo get`,
`record get`, `candidate get`, or `case get`
before interpreting complete source posture, evidence, lifecycle,
relationships, or qualification.

`loom provenance repo list` is semantic repository discovery over aliases,
purpose, topics, role, current focus, accepted context, and tracking posture.
Its optional bounded query is split into distinct normalized terms after
generic finder wording is removed. A card must match at least half of those
terms in its existing deterministic searchable summary, rounded up; a one-term
query must match that term. A non-empty query with no searchable terms returns
no cards. Project, topic, role, and tracking-status filters still apply, and
results remain limited and ordered by repository ID without ranking output.
`loom provenance repo get` loads the selected card. Technical ownership,
contracts, lifecycle, and physical observation stay under `loom project` and
`loom project repos`.

Agents choose one first engine, exact-get second, and expand sequentially.
Objects answer technical state, Notes answer source-material questions, and
Provenance answers qualified-state questions. Candidates are clues, not
accepted records.

## Archivist Operation

The deterministic `main.provenance_archivist` worker runs inside `loomd` and is
manual-only. The portable external workspace is a reasoning/control template,
not another semantic store. It may use supported read-only Objects and Notes
retrieval; control actions stay on provenance, project, and worker surfaces.
It never uses direct database or lifecycle-store access, a parallel task
registry, or recurring execution.

The intended main workspace is not yet created or registered in Orca. Template
validation and disposable scaffolding do not activate an agent, permissions,
deployment, or scheduling.

## Stable Error Classes

| Code | Meaning |
|---|---|
| `provenance.invalid_request` | invalid UUID, cursor, bound, or document |
| `provenance.idempotency_key_required` | mutation lacks the bounded header |
| `provenance.forbidden` | actor/node policy denied the capability |
| `provenance.not_found` | exact semantic object does not exist |
| `provenance.conflict` | replay or lifecycle state conflicts |
| `provenance.deadline_exceeded` | the five-second request deadline elapsed |
| `provenance.output_too_large` | bounded response encoding was exceeded |
| `provenance.not_ready` | the runtime service is unavailable |

## Recovery Contract

Manifest schema `loom.backup.manifest.v0.10` adds a `provenance` object with:

- state and start/completion times;
- database name, dump filename, and `pg_dump_custom` format;
- exact schema head and required-relation list;
- per-relation logical counts and stable graph digest;
- dump byte size and SHA-256.

The normal artifact list must contain exactly one matching
`provenance_postgres_dump`. Package verification additionally requires an
independently retained manifest SHA-256. Restore succeeds only into a disposable
`loom_provenance_restore_drill_*` target that reproduces the same logical state.

The accepted backup-transition runtime creates and independently verifies the
package, then retains the manifest path and SHA-256 in successful committed
main-backup operation evidence. Coverage consumes that evidence rather than
deriving trust from the manifest under inspection. The direct Borg archive path
requires and binds the same verified package identity. Missing operation
evidence or an identity mismatch is critical/unknown, never covered.

This is an implemented runtime contract, not evidence of production activation.
Live production backup and restore remain integrator-owned operations.

## Related Docs

- [[Provenance Runtime Operations]]
- [[Provenance Runtime Development]]
- [[Database Schemas]]
- [[API Route Index]]
- [[Capability Addresses]]
