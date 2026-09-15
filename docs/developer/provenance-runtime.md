---
title: "Provenance Runtime Development"
description: "Develop and validate the isolated provenance kernel, runtime, transport, recovery, and disposable acceptance boundary."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - testing
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
  - "internal/provenance/archivist.go"
  - "internal/httpapi/provenance.go"
  - "internal/localclient/provenance.go"
  - "internal/loomdapp/root.go"
  - "internal/provenance/recovery_package.go"
  - "internal/maintenance/backup_manifest.go"
  - "internal/backup/restore_drill.go"
  - "internal/workers/runtimes/main_backup.go"
  - "internal/workers/runtimes/provenance_archivist.go"
  - "internal/httpapi/backup_coverage.go"
  - "internal/backupstrategy/archive_manifest.go"
  - "internal/cloudstorage/direct_archive.go"
  - "tests/smoke/v2_provenance_runtime_local.sh"
  - "ai-loom-pack/templates/archivist"
  - "internal/knowledge/passages.go"
  - "tests/smoke/v2_box_knowledge_sources_local.sh"
related:
  - "[[Provenance Foundation Reference]]"
  - "[[Provenance Runtime Operations]]"
  - "[[Migrations]]"
  - "[[Testing]]"
aliases:
  - "Semantic Ledger Development"
---
# Provenance Runtime Development

## Adaptation Boundary

The foundation adapts the same-owner provenance design snapshot at commit
`93d3d94a76059802d00dbbe818cb188babcb18f4`. Its recorded migrations `001`
through `006` define the candidate, record, source/evidence, relationship, case,
lifecycle, and replay behavior used by LOOM.

Upstream migrations `007` through `009` are deliberately excluded because they
introduce project/repository state. LOOM's project system owns that authority.
LOOM migration `007` is instead an append-only, rebuildable project/repository
projection owned by the accepted search/reconciliation feature. The
implementation does not run Python, open the upstream SQLite database, or
vendor a second daemon.

## Package Map

| Area | Owner |
|---|---|
| Models, source snapshot, migrations, store | `internal/provenance` |
| Registration and lifecycle service | `internal/provenance` |
| Typed lexical search and exact projections | `internal/provenance/search.go` |
| Rebuildable project/repository snapshots | `internal/provenance/project_projection.go` |
| Deterministic Archivist policy engine | `internal/provenance/archivist.go` |
| Runtime pool and readiness | `internal/provenance/runtime.go` |
| Authorized HTTP transport | `internal/httpapi/provenance.go` |
| Deadline-bounded client | `internal/localclient/provenance.go` |
| Existing-process composition | `internal/loomdapp/root.go` |
| Manual-only worker runtime | `internal/workers/runtimes/provenance_archivist.go` |
| Isolated recovery-package producer | `internal/provenance/recovery_package.go` |
| Backup manifest verification | `internal/maintenance/backup_manifest.go` |
| Disposable logical restore comparison | `internal/backup/restore_drill.go` |
| Coverage classification | `internal/backupcoverage` |
| Trusted operation-evidence handoff | `internal/workers/runtimes/main_backup.go`, `internal/httpapi/backup_coverage.go` |
| Direct Borg package binding | `internal/backupstrategy/archive_manifest.go`, `internal/cloudstorage/direct_archive.go` |
| Metadata-only support collection | `internal/supportbundle/collect_provenance.go` |

Provenance migrations are embedded under
`internal/provenance/migrations/`. They must never move into root `migrations/`
or create relations in `loom_main`.

## Archivist Two-Layer Boundary

`main.provenance_archivist` is the deterministic engine inside `loomd`. It uses
existing worker leases, checkpoints, idempotency, bounded output, and the sole
append-only lifecycle service. Its descriptor and instance remain manual-only;
scheduled, startup, event, and policy-driven recurring execution are disabled.

The external `archivist` AI-pack template is a separate reasoning/control
layer for a later Codex task launched through Orca. It owns instructions,
routing and candidate-review protocols, investigations, and handoffs. It has
no semantic memory or database. It may invoke only supported provenance,
project, and worker CLI/capability control surfaces; read-only retrieval may use
Objects and Notes. It never accesses PostgreSQL or lifecycle-store internals.

The live main workspace has not been created or registered. No ORCA
configuration, agent launch, permission change, deployment, or scheduling is
part of the repository template.

## Retrieval Contract

Keep retrieval engines separate:

| Need | First engine |
|---|---|
| Object identity, version, extraction, failure | Objects |
| Source wording and document material | Notes |
| Qualified current semantic state | Provenance |
| Technical project/repository registry navigation | `loom project` |
| Semantic repository discovery | `loom provenance repo list` |

Search one compact engine first, exact-get or inspect the selected typed ID,
then expand sequentially only for a named gap. Pending candidates are opt-in
and structurally separate from accepted records.

## Source Citation Integration

Notes and Provenance keep separate persistence and authority. The existing
candidate API can retain an exact Notes citation in source submitted/details,
with its passage URL, version address and source content digest. No automatic
Notes-to-record writer or new semantic resolver is introduced. Verification
posture must describe the actual source read; a declared hash is not independent
verification.

The Notes passage reader joins chunk, version and object identities in one
database statement and checks current source eligibility in that same snapshot.
It does not require the historical version's hash to equal the live object's
hash. This preserves citations after ordinary edits without bypassing current
privacy, owner/project status, root policy or catalog/replica checks. The
response exposes a bounded, integrity-checked passage and locator, never raw
storage paths, arbitrary artifacts or a fallback to the latest body. Current
classification is labeled `current_source_context`; citation-time classification
belongs in the immutable source evidence payload.

Run the source integration smoke with:

```bash
bash tests/smoke/v2_box_knowledge_sources_local.sh
```

It creates its own pinned PostgreSQL 17/pgvector cluster, main schema and
Provenance database. Native extraction creates the tested version/chunk rows
through ordinary catalog/admission/coordinator services. Frozen Topics,
Library and project cases include conflicting source claims and unsupported
inputs. Exact Notes reads are exercised over HTTP, the local client and CLI.
The original note passage survives a live-source edit and runtime reopen;
identity mismatch and current-access revocation refuse both old and current
content without rewriting either citation.

Separate foundation API registration and explicit review produce the frozen
accepted cadence and opt-in pending proposal. Reads and registration alone
produce no accepted record. Exact source IDs and replay remain unchanged.
Routing tests cover the written one-engine-first protocol, not autonomous model
selection accuracy. Pipeline inspection remains content-redacted.

The harness advances coordinator stages explicitly. Full unattended
watch/upload/projection, non-root ACL and daemon-failure acceptance, archived
workspace integration and Main deployment remain the next operator/integration
slice. Do not present this focused cross-database proof as those wider gates.

## Identity Rule

Keep two identity domains explicit in every change:

```text
authenticated LOOM actor + origin node
  -> execution authorization and idempotency scope

semantic producer/source/author/approver
  -> evidence stored in the provenance ledger
```

Semantic content cannot authorize its own mutation. Transport tests must prove
denial happens before store access and that execution evidence is projected
separately from semantic producer fields.

## Focused Test Loop

PostgreSQL-backed package tests use `LOOM_PROVENANCE_TEST_DB_URL` only as an
administrator connection to create uniquely named disposable databases. A test
must skip when the variable is absent and must drop every database it creates.
Do not point this variable at a production or shared database.

Useful focused commands are:

```bash
go test -count=1 ./internal/provenance
go test -count=1 ./internal/httpapi ./internal/localclient ./internal/loomdapp
go test -count=1 ./internal/maintenance ./internal/backupcoverage \
  ./internal/backup ./internal/supportbundle
go test -race -count=1 ./internal/provenance ./internal/httpapi \
  ./internal/localclient ./internal/backup
```

The source snapshot test is the guard against accidental adaptation drift. The
migration tests assert exact-head replay, append-only trigger shape, schema
tamper failure, absence from `loom_main`, and absence of deferred project/search
tables.

## Disposable Integrated Smoke

Run the complete local foundation composition with:

```bash
bash tests/smoke/v2_provenance_runtime_local.sh
```

The smoke resolves the repository's pinned PostgreSQL 17 + pgvector toolchain
through Nix, initializes a new local cluster, and ignores ambient database
URLs. It creates separate `loom_main` and `loom_provenance` databases, denies
the provenance role access to main, builds one temporary `loomd`, and confines
all runtime and backup artifacts to temporary roots.

The first daemon boot applies both migration systems and then drives live
health, candidate registration, semantic replay, manual rejection, lifecycle
replay, and the explicit absence of a search route over a temporary Unix
socket. The smoke stops the process, restarts without migration writes, and
proves the rejected state remains durable.

It then runs real `pg_dump --format=custom`/`pg_restore` acceptance plus focused
authorization, health-query-shape, backup-coverage, support-redaction, and
shutdown-order tests. Synthetic claim, source, payload, and credential markers
must remain absent from health, support, and daemon logs.

The smoke is destructive only to resources it creates. Never adapt it to use
`LOOM_DB_URL`, `LOOM_PROVENANCE_DB_URL`, `loom-main`, `/srv/loom`,
`/var/lib/loom`, or `~/loom-box`.

## Full Foundation Gate

After the smoke passes, run:

```bash
go test -count=1 ./...
go vet ./...
nix --extra-experimental-features 'nix-command flakes' flake check --no-build
git diff --check
```

This is the integration boundary for the foundation. The accepted
backup-transition runtime now produces and verifies the provenance recovery
package, retains its manifest identity in committed operation evidence, and
binds it into direct Borg archive evidence. Production Nix activation and live
backup or restore remain integrator-owned work.

## Change Checklist

When changing the foundation:

1. Preserve candidate/record separation and append-only lifecycle evidence.
2. Preserve UUID semantic IDs and exact replay/conflict behavior.
3. Keep lists, excerpts, bodies, deadlines, and filters bounded.
4. Keep runtime readiness and support output free of semantic content.
5. Keep all provenance relations and transactions outside `loom_main`.
6. Extend recovery relation/count/digest coverage for every new durable row.
7. Treat missing independent manifest identity as a critical unknown.
8. Keep search, projection, Archivist, Basecamp, and project behavior inside
   their separately accepted feature boundaries.
9. Keep the portable Archivist workspace free of host paths, secrets, database
   locators, runtime state, private memory, and recurring execution.

## Related Docs

- [[Provenance Foundation Reference]]
- [[Provenance Runtime Operations]]
- [[Migrations]]
- [[Testing]]
- [[API Development]]
