---
title: "Backup Cloud And Maintenance API"
description: "Typed routes for backup coverage and contracts, maintenance backups, database care, object storage, and cloud snapshot operations."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - backup
  - cloud
  - operations
status: verified
verified_at: "2026-08-31"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/httpapi/backup_coverage.go"
  - "internal/httpapi/backup_contracts.go"
  - "internal/httpapi/maintenance.go"
  - "internal/httpapi/cloud.go"
  - "internal/localclient/client.go"
related:
  - "[[Backup Cloud And Maintenance CLI Reference]]"
  - "[[Backup Restore And Drills]]"
---
# Backup Cloud And Maintenance API

These routes expose reviewed service operations. Filesystem roots come from
daemon runtime configuration; callers cannot replace canonical custody paths.

## Backup Coverage And Contracts

| Route | Purpose |
|---|---|
| `GET /v1/backup/coverage` | Evaluate canonical durable-root coverage. |
| `/v1/backup/contracts` | List/create protected-folder contracts. |
| `/v1/backup/contracts/{id}` | Inspect, preflight, enable, disable, recheck, retry, or delete according to the subroute. |

Coverage treats generated artifacts truthfully: the retired export is excluded,
and Notes projection coverage is optional compatibility behavior rather than a
canonical source.

## Maintenance Backup

| Route | Purpose |
|---|---|
| `GET /v1/maintenance/backup/status` | Current backup operation health. |
| `GET /v1/maintenance/backup/list` | Bounded operation history. |
| `POST /v1/maintenance/backup/run` | Dry-run or create according to input. |
| `POST /v1/maintenance/backup/verify` | Verify a registered backup or explicit path. |

Current backup verification parses custody manifests and validates
snapshot-local payload/object evidence. Malformed current schemas fail closed;
explicit legacy layouts have separate compatibility handling.

## Maintenance And Database

| Route | Purpose |
|---|---|
| `GET /v1/maintenance/status` | Summary of active operations/findings. |
| `GET /v1/maintenance/findings` | Bounded typed findings. |
| `GET /v1/maintenance/db/status` | Database maintenance state. |
| `POST /v1/maintenance/db/compact` | Plan/apply database compaction. |
| `GET /v1/maintenance/object-store/status` | Object-store health. |
| `POST /v1/maintenance/object-store/scan` | Bounded scan according to request. |

## Live Cloud Routes

The `/v1/cloud/*/live` family includes status/doctor, snapshot status/list/
verify, retention plan/apply, and backend status/init. The daemon owns backend
configuration and passes configured canonical backup roots into Borg and
legacy-tree flows.

Remote mutation is never implied by a status call. Snapshot push/fetch and
restore-drill orchestration remain explicit CLI/service workflows with their
own confirmation and destination checks.

For a Borg retention apply, `plan`, `confirm`, and `confirm_digest` are
required, and `confirm_digest` must equal `plan.plan_digest`. The server then
revalidates the plan, configured retention authority, repository identity,
complete archive inventory, and exact prune-glob membership before the
repository-wide check and prune. Any glob-matched archive outside the
authenticated exact-name same-node `user_data` plan refuses the apply. The
optional `compact` request is reported as a separate stage; `partial` is a
valid result when prune succeeded but compact was skipped or failed.

## Error Handling

- An incomplete coverage report prevents snapshot upload.
- A same-named but invalid remote snapshot is not overwritten.
- Unknown custody or manifest schemas fail closed.
- Database/maintenance errors return structured operation evidence; clients
  should not translate them into generic success.

## Related Docs

- [[Backup Cloud And Maintenance CLI Reference]]
- [[Backup Restore And Drills]]
- [[Backup Sync Retention And Cloud]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Envelope

The CLI normally reaches these endpoints through the local client and returns
LOOM response envelopes:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_..."
  }
}
```

Errors include an `error.code`, `domain`, `target`, summary, and correlation
id. Preserve the correlation id when reporting failures.

### Backup Coverage

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/backup/coverage` | GET | read-only | `loom backup coverage`, `loom backup contracts plan` |

The handler builds a main-backed backup coverage report from runtime config.
It does not accept arbitrary local path overrides over HTTP. Local override
planning is a CLI-local feature.

At verification time, the CLI/local-client path returned `status=ok`, mode
`main_backed`, 22 coverage entries, and zero critical missing entries.

### Backup Contracts

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/backup/contracts` | GET | read-only | `loom backup contracts list`, `status` |
| `/v1/backup/contracts/<key>` | GET | read-only | `loom backup contracts inspect <key>` |
| `/v1/backup/contracts/preflights` | POST/GET | queued read-only / read-only | `loom backup contracts preflight` and Portal resume |
| `/v1/backup/contracts/preflights/<id>` | GET | read-only | Portal preflight resume |
| `/v1/backup/contracts/preflights/<id>/retry` | POST | queued read-only | Portal retry |
| `/v1/backup/contracts` | POST | dry-run or operator-mutating | `loom backup contracts create <key>` |
| `/v1/backup/contracts/<key>/enable` | POST | dry-run or operator-mutating | `loom backup contracts enable <key>` |
| `/v1/backup/contracts/<key>/disable` | POST | dry-run or operator-mutating | `loom backup contracts disable <key>` |
| `/v1/backup/contracts/<key>/recheck` | POST | queued read-only | `loom backup contracts recheck <key>` |
| `/v1/backup/contracts/<key>/retry-activation` | POST | queued operator control | `loom backup contracts retry-activation <key>` |
| `/v1/backup/contracts/<key>` | DELETE | dry-run or operator-mutating | `loom backup contracts delete <key>` |

Backup contracts are standalone YAML policies stored in the main daemon's
configured hidden Box metadata directory:

```text
<box-root>/.loom/contracts/backup/<contract-key>.yaml
```

The Box contract discovers that directory through
`policies.backup_contracts`, normally `.loom/contracts/backup`. The contract's
`owner_node` is independently the node that owns, preflights, and backs up the
source path. Callers use the main daemon; they do not write node-local YAML or
use a transport fallback.

Create request fields are represented by `backupcontracts.CreateRequest` and
include the contract key, target path, owner node, target scope, include/exclude
globs, backup mode, size limits, `dry_run`, `replace`, and optional completed
`preflight_id`. Mutations use the normal request/idempotency context.

Preflight requests are durable records. A pending request or pending lookup
returns `202 Accepted`; completed/failed records return `200 OK`. Evidence is
bounded and contains path, filesystem/mount, counts, bytes, truncation,
effective policy, and findings, never file contents. Create rejects a supplied
preflight that is unfinished, expired, blocking, or does not match the owner
node and path.

The contract `/recheck` route adds a bounded identity derived from the existing
contract key, managed safe-root key, watched-root key, and runtime worker key.
The owner node may ignore only that exact self-overlap. Anonymous preflight and
overlap with another protected folder, configured safe root, watched root, or
generated/runtime path remain blocking.

A successful non-dry-run mutation writes canonical YAML and registers a
revisioned desired configuration for each affected owner node. When node work
is queued, create/enable/disable/delete returns `202 Accepted`; `200 OK` is used
when no owner-node apply remains. The result separates
`desired_state_registered`, `node_apply_queued`, `node_applied`, desired/applied
revision, and the older compatibility `applied` field. Accepted or queued is
not equivalent to owner-node application or backup custody.

Overlapping, conflicting, or unsafe contract mutations return `409 Conflict`.
Invalid input returns `400`; missing contract/preflight resources return `404`.
Idempotency conflicts also return `409` through the shared API behavior.

List/status is an aggregate projection over canonical YAML, preflight,
desired/applied revision and configuration hashes, owner-node presence,
watched-root reports/findings, and backup batches. Its lifecycle values are:

- `waiting_for_node`: pending preflight/reconcile work and an unavailable owner;
- `ready`: completed preflight before desired registration;
- `activating`: current application/report/config evidence is incomplete;
- `active`: current desired state is applied and reported, without a later
  accepted backup;
- `protected`: current state is applied/reported and a backup accepted after
  the current acknowledgement;
- `attention`: blocking finding, apply error, config mismatch, latest backup
  failure, or canonical YAML missing without recorded delete intent;
- `disabled`: canonical intent remains disabled.

`disable` keeps the YAML file and reconciles the managed root out of active
desired state. `delete` removes YAML and reconciles its managed root away when
confirmed by the CLI with `--yes`; it does not delete the source folder or
retained backup data. A queued tombstone remains visible as
`waiting_for_node`/`activating`; after its current-generation acknowledgement,
normal list/detail endpoints omit it while durable audit and registration
evidence remain.

### Maintenance Status And Findings

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/maintenance/status` | GET | read-only | `loom maintenance status` |
| `/v1/maintenance/findings` | GET | read-only | `loom maintenance findings list` |

Findings support query filters through the local client:

- `limit`;
- `status`;
- `severity`;
- `worker`.

At verification time, `maintenance status` was `ok`, five workers were present,
and there were zero open findings.

### Database Maintenance

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/maintenance/db/status` | GET | read-only | `loom database status`, `loom database doctor`, `loom maintenance db status` |
| `/v1/maintenance/db/compact` | POST | dry-run or operator-mutating | `loom database compact`, `loom maintenance retention dry-run/apply` |

Compaction request fields are represented by
`maintenance.DatabaseCompactInput`. The safe default is dry-run:

```json
{
  "dry_run": true,
  "confirm": false,
  "recent_success_days": 14
}
```

Reviewed-plan fields are:

```json
{
  "plan": {},
  "plan_hash": "sha256:...",
  "max_rows_per_batch": 5000,
  "max_total_rows": 50000,
  "reason": "operator reviewed"
}
```

The preferred CLI flow exports the plan and then applies that exact plan:

```bash
loom database compact --dry-run --out /tmp/loom-db-compaction-plan.json
loom database compact --plan /tmp/loom-db-compaction-plan.json --confirm
```

The maintenance wrapper uses the same endpoint:

```bash
loom maintenance retention dry-run --out /tmp/loom-db-compaction-plan.json
loom maintenance retention apply --plan /tmp/loom-db-compaction-plan.json --yes
```

Compatibility apply paths remain available:

```bash
loom database compact --dry-run=false --confirm
loom maintenance retention apply --yes
```

The response includes plan id/hash, cutoff, candidates, retention plans,
allowed destructive tables, per-table row limits, max batch/total limits,
warnings, rollups, deleted counts on apply, evidence operation status, and a
physical storage note. A stale or tampered reviewed plan is rejected by hash.

The first destructive policy deletes only old routine success events in
`events.events` and old settled leases in `workers.worker_leases`. Worker runs,
worker controls, maintenance operations, findings, audit/security/failure/manual
events, and recent rows are preserved. Logical deletion does not guarantee
immediate PostgreSQL file-size shrink.

At verification time, dry-run returned `mutates_database=false`.

### Maintenance Backup

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/maintenance/backup/status` | GET | read-only | `loom backup status`, `loom maintenance backup status` |
| `/v1/maintenance/backup/list` | GET | read-only | `loom backup list`, `loom maintenance backup list` |
| `/v1/maintenance/backup/run` | POST | operator-mutating | `loom backup create`, `loom maintenance backup run --once` |
| `/v1/maintenance/backup/verify` | POST | read-only | `loom backup verify`, `loom maintenance backup verify` |

List supports filters such as `limit` and `status`. Run requests should carry
a reason and idempotency key when an operator or automation is initiating the
backup. Verification takes a backup ref or backup path and returns structured
verification status.

At verification time, the latest registered backup verified successfully
through the main-backed endpoint.

### Object Store Maintenance

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/maintenance/object-store/status` | GET | read-only | `loom maintenance object-store status` |
| `/v1/maintenance/object-store/scan` | POST | operator-mutating | `loom maintenance object-store scan` |

Object-store scans run as maintenance worker operations. The CLI requires
exactly one mode: `--sample` or `--blob <blob-ref>`.

### Cloud Status And Doctor

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/cloud/status/live` | POST | read-only, may probe remote | `loom cloud status` |
| `/v1/cloud/doctor/live` | POST | read-only, live diagnostics | `loom cloud doctor` |

The request body can ask for cached status or force live status. Main-backed
HTTP handlers reject arbitrary cloud config path overrides unless the server is
configured to allow them. This prevents a remote caller from making main read
unexpected local config paths.

At verification time, cached cloud status was healthy, enabled, and backed by
Borg. Cloud doctor returned `status=ok`.

### Cloud Snapshots

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/cloud/snapshot/status/live` | POST | live read-only | `loom cloud snapshot status` |
| `/v1/cloud/snapshot/list/live` | POST | live read-only | `loom cloud snapshot list` |
| `/v1/cloud/snapshot/verify/live` | POST | live read-only | `loom cloud snapshot verify` |
| `/v1/cloud/snapshot/retention/plan/live` | POST | live read-only | `loom cloud snapshot retention status/plan` |
| `/v1/cloud/snapshot/retention/apply/live` | POST | operator-mutating, digest-bound | `loom cloud snapshot retention apply --plan <file> --confirm-digest <digest> --confirm` |
| `/v1/cloud/snapshot/backend/status/live` | POST | read-only | `loom cloud snapshot backend status` |
| `/v1/cloud/snapshot/backend/init/live` | POST | operator-mutating | `loom cloud snapshot backend init --confirm` |

Retention apply and backend init both require explicit confirmation. Borg
retention additionally requires the exact reviewed plan and matching digest;
the request may set `compact` only as a separately reported post-prune stage.
Snapshot status/list/retention can return lock-related errors or statuses when
another cloud operation holds the remote lock.

`POST /v1/cloud/snapshot/verify/live` exposes three mutually exclusive Borg
assurance profiles through the existing verify route:

| `profile` | Required selector | Exact Borg check | Result `coverage` |
|---|---|---|---|
| `metadata` | Optional `ref`; missing means `latest` | `check --archives-only ::<archive>` after exact archive `info` | `archive_metadata_complete` |
| `rolling_repository` | No `ref`; positive `max_duration_seconds` | `check --repository-only --max-duration <seconds>` and no archive list/info command | `repository_check_time_bounded` |
| `archive_data` | Exact canonical Borg archive name; not `latest` | `check --archives-only --verify-data ::<archive>` after exact archive `info` | `archive_data_complete` |

For example, a bounded repository pass uses:

```json
{
  "profile": "rolling_repository",
  "max_duration_seconds": 1800
}
```

Every result repeats `profile` and any `max_duration_seconds`; a successful
result also records its exact `coverage`. A successful rolling response records bounded
repository progress; it is never typed or rendered as complete archive-data
verification. Supplying a ref to `rolling_repository`, omitting its positive
duration, using `latest` for `archive_data`, adding a duration to another
profile, or naming an unknown profile returns
`cloud.snapshot_verify_profile_invalid`. Omitting `profile` preserves the
read-only `metadata` default. These API selectors do not create or change a
worker policy, timer, or automatic cadence.

At verification time, snapshot list returned a clean empty Borg inventory.
Retention status returned empty with zero removal candidates.

### What Is Not A Public API Shortcut

The raw HTTP API is not the primary interface for manual backup creation,
restore drills, or cloud snapshot pushes. Use the CLI and worker surfaces
because they apply command context, idempotency metadata, confirmation flags,
and safer rendering.

### Related Docs

- [[Backup Cloud And Maintenance CLI Reference]]
- [[Backup Restore And Drills]]
- [[Cloud Storage]]
- [[Database Maintenance]]
- [[API Route Index]]
