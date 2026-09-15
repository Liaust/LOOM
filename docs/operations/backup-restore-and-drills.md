---
title: "Backup Restore And Drills"
description: "Create and verify main backups, validate canonical custody, rehearse database restore, and prepare a controlled recovery."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - backup
  - troubleshooting
status: verified
verified_at: "2026-09-06"
source_scope:
  - "internal/backup"
  - "internal/backupcoverage"
  - "internal/hermesprofile"
  - "nix/packages/hermes-agent.nix"
  - "internal/maintenance"
  - "internal/cloudstorage"
  - "internal/httpapi/cloud.go"
  - "internal/localclient/client.go"
  - "internal/loomcli/cloud.go"
  - "internal/restoreauthority"
  - "nix/modules/loom-restore-authority.nix"
  - "internal/backupstrategy/archive_manifest_v2.go"
  - "internal/workers/tick_policy.go"
  - "internal/workers/runtimes/cloud_snapshot.go"
  - "tests/smoke/canonical_filesystem_local.sh"
  - "tests/smoke/v2_cloud_backup_runtime_optimization_local.sh"
related:
  - "[[Backups Cloud And Restore]]"
  - "[[Backup Sync Retention And Cloud]]"
  - "[[Main And Mac Operations]]"
---
# Backup Restore And Drills

## Scope

This runbook covers operational main backups and non-destructive restore
drills. A destructive restore into live PostgreSQL or live filesystem roots
requires a separate production decision and maintenance window.

## Preflight

```bash
loom storage filesystem status
loom backup coverage
loom backup status
```

Resolve every critical coverage gap before creating a backup intended for
disaster recovery. Canonical custody batches/archives count only after their
manifest commit marker exists.

## Create

```bash
loom backup create --production --dry-run
loom backup create --production
loom backup list --limit 10
```

The operation captures PostgreSQL plus declared durable roots. Current
manifests name canonical Box/Storage custody, including Imports bytes and
portable evidence, and internal protection roots.
Generated exports are excluded; the generated Notes projection is excluded by
default unless an explicit compatibility-copy policy coherently includes it.

The production Imports provenance policy is not inferred from the cutover
flag. Markerless historical custody remains complete-physical after its bytes
move; committed-only mode is enabled only after a separate provenance review.
The local shared object store avoids a full payload copy per generation, but
each generation still adds directory entries/hardlinks and the store has no
automatic GC in this checkpoint. Do not enable an unattended schedule until a
reviewed local generation-retention policy or bounded manual cleanup exists.
The built-in main-backup and cloud-snapshot workers remain manual until a
reviewed production activation. The software supports the accepted
timezone-aware pair—main backup at `03:00`, then cloud archive at `03:15
Europe/Amsterdam`—but documentation or disposable acceptance does not apply
those policies. Production activation still requires measured local/cloud
duration, the existing two-hour cloud timeout, bounded local generation/shared-
store retention, remote capacity/retention review, and strict fetch/restore
evidence.

## Verify

```bash
loom backup verify <backup-ref-or-path>
```

Verification requires:

- a supported backup manifest and non-empty database dump;
- expected migration and health evidence;
- snapshot-local object, retention, backup-custody, and archive-custody paths;
- parsed current custody schemas with confined relative paths;
- referenced payload/object existence and size/checksum evidence;
- explicit handling for recognized legacy layouts.

Malformed current manifests must fail; they never fall through as legacy.

## Restore Drill

```bash
loom backup restore-drill <backup-ref-or-path> --dry-run
```

The legacy local-backup command remains a planning and verification path. Its
non-dry command does not silently receive privileged database authority and
fails unless an explicitly injected disposable test runner owns the operation.
Use the strict cloud direct-archive restore below for the deployed non-dry
operational and Provenance drill. Neither path stops `loomd` or copies files
into configured live roots.

## Cloud Recovery Copy

```bash
loom cloud snapshot push --backup latest --dry-run
loom cloud snapshot verify latest
loom cloud snapshot restore-drill latest --dry-run
```

Upload only a verified local backup with complete coverage. Fetch to an empty
staging directory; fetching is not live restoration.

For the first Imports-inclusive backup/cloud copy, measure actual duration and
review explicit worker/config timeouts (the defaults may be too small for tens
of GiB and roughly ninety thousand objects). A timeout must leave failed,
unpublished evidence with a reviewable staging/shared-store cleanup path.
Before cloud scheduling resumes, measure remote free space and retention and
either prove bounded legacy-tree generations or complete Borg initialization,
snapshot, fetch, strict verification, and restore-drill acceptance.

### Explicit Borg assurance profiles

Use the existing snapshot verify command for each assurance depth:

```bash
# Exact metadata graph for the resolved archive.
loom cloud snapshot verify latest --profile metadata

# A bounded repository-only pass that can make rolling progress.
loom cloud snapshot verify --profile rolling_repository --max-duration 30m

# Every data chunk reachable from one exact canonical archive.
loom cloud snapshot verify <exact-borg-archive> --profile archive_data
```

These are separate claims. `metadata` completes archive metadata verification
but does not reread all payload chunks. `rolling_repository` has a positive
time budget and reports `repository_check_time_bounded`; even a successful run
is not complete `archive_data` evidence. `archive_data` is an unbounded manual
deep check for one exact archive. Strict fetch/restore remains independent and
is the evidence that recovery into isolated targets works.

No assurance profile has an automatic cadence here. Do not add a recurring
deep check merely because a manual profile succeeded; cadence remains a
separate measured operator decision.

### Routine v2 archive evidence

New scheduled direct archives use
`loom.direct_archive.manifest.v2` with the explicit
`routine_incremental_v1` verification profile. A healthy receipt shows one
Borg create and one rename, the exact recovery-package identities, stage
durations, packed/deduplicated byte counts, and bounded metadata/envelope
checks. The Borg create pins `--files-cache ctime,size,inode` and
`--files-changed ctime`.

Do not interpret the receipt as a complete data reread. The v2 routine must not
perform LOOM's former second full source hash, an archive-wide `{sha256}` list,
payload export, prune, compact, or delete. An unchanged run still traverses the
namespace; its expected scaling is entries plus changed bytes. Any Borg warning
or non-zero exit, package drift, policy race, remote-lock overlap, or incomplete
pending verification fails closed before canonical rename.

The Main producer includes the raw `Topics` and `Library` directories beneath
its configured Box as `box_topics` and `box_library`, alongside Projects, Notes,
Documents and the existing Storage/system roots. These are whole raw trees:
unindexed documents, non-Markdown files, nested folders and source files are
covered independently of Notes watch policies. Notes indexing or synced object
blobs alone do not establish complete raw-workspace backup. Adding backup roots
does not enroll files in Notes or promote them into Provenance.

Topics and Library must exist as real readable directories; an empty directory
is valid, including after its last file is removed. Missing, symlinked or
overlapping roots refuse backup rather than silently reduce coverage. The
existing Documents acceptance exclusion and Hermes recovery exclusions remain
separate; no new Topics/Library content exclusion is introduced.

Coverage belongs to each authenticated archive manifest. Archives made before
these roots were selected still restore their original declared contents; they
do not retroactively contain Topics or Library. Do not reuse their package or
archive identity with a different root set. After deploying this selection
change, a fresh recovery package and new committed archive are needed to prove
coverage. Strict recovery remains bound to the retained manifest, not today's
live source configuration or Notes enrollment.

For a manual daytime run with Morathustra enabled, first ensure its authenticated
recovery publication is within the unchanged two-hour freshness window. If the
02:45 scheduled publication has expired, run the existing gated
`loom-morathustra-recovery-publish.service` and require success before creating
the fresh Main package. Do not extend the age limit or bypass signature checks.

The complete disposable gate is:

```bash
bash tests/smoke/v2_cloud_backup_runtime_optimization_local.sh
```

It requires exact Borg 1.4.3 and owns all repositories, PostgreSQL databases,
runtime roots, binaries, and sockets. It is not a production smoke and must not
be pointed at live paths or credentials.

### Daily-local activation boundary

Inspect and dry-run each policy first:

```bash
loom worker policy inspect main.main_backup
loom worker policy set main.main_backup \
  --dry-run \
  --mode daily_local \
  --local-time 03:00 \
  --timezone Europe/Amsterdam \
  --expected-policy-fingerprint <captured-main-backup-fingerprint>

loom worker policy inspect main.cloud_snapshot_upload
loom worker policy set main.cloud_snapshot_upload \
  --dry-run \
  --mode daily_local \
  --local-time 03:15 \
  --timezone Europe/Amsterdam \
  --expected-policy-fingerprint <captured-cloud-fingerprint>
```

Applying either policy is a production mutation and requires a separately
reviewed activation checkpoint with `--yes`, an operator reason, an exact
idempotency key, and the still-current fingerprint. Do not apply from a docs or
disposable-acceptance task. A daily cloud run accepts only a recovery package
created between 03:00 and its own start on the same Amsterdam calendar day;
otherwise it fails before maintenance or Borg mutation. DST derivation is by
local calendar occurrence, not a fixed 24-hour interval.

### Direct Borg recovery

For a direct Borg archive, `restore-drill --dry-run` performs more than a Borg
extract check. It authenticates the archive manifest remotely, extracts into a
new isolated directory, proves the exact extracted tree, and independently
verifies the operational and provenance recovery packages named by the
archive. A full strict restore is successful only after both packages are
restored into disposable targets and return the same package and manifest
identities. Verification without the independent provenance restore step is
refused rather than reported as recovery. The operational PostgreSQL dump is
opened without following symlinks and bound to the authenticated artifact's
inode, regular-file type, mode, size, and hash. `pg_restore` receives only an
unlinked private copy made from that held descriptor; source replacement or
same-size mutation before or during restore fails closed, and any disposable
database cleanup failure is reported alongside the primary failure.

An authenticated v2 user-data symlink is restored as an inert leaf with its
exact archived target text. The target may be absolute or may resolve outside
the declared user root; LOOM neither follows it nor claims its referent is
present after restore. Before extraction, the complete Borg path inventory is
checked and any archive entry below a symlink path refuses the operation.
Declared user roots, operational and Provenance packages, archive evidence,
and synthetic archive ancestors must still be real directories/files rather
than symlinks. Escaping hardlinks, special files, unexpected roots, and all
other confinement failures remain hard failures.

Provenance recovery is versioned by the package's declared schema head. The
retained head-6 relation set and the current head-7 relation set are both exact;
unknown heads, cross-head relation sets, missing relations, and extra relations
fail closed. The restored database is first inspected and compared with the
authenticated package snapshot at that declared head. Historical recovery does
not compare package evidence with mutable live Provenance state. Only after the
at-head identity succeeds does LOOM migrate the disposable target to the
current head and verify current schema readiness and logical integrity. The
active `loom_provenance` database is never a valid drill target.

On the deployed main node, non-dry database work crosses one fixed local
authority boundary. Unprivileged `loomd` connects to
`/run/loom-restore-authority/restore-authority.sock`; the socket-activated
authority executes as the PostgreSQL operating-system user and accepts only
typed operational restore, typed Provenance restore, and exact disposable drop
requests. The listener activator, authority executor, and caller are separate
identities: systemd activates the listening endpoint as exact user `root`, the
authority service executes as exact user `postgres`, and the server accepts
only the exact `loom` client peer. On the client connection, Linux
`SO_PEERCRED` therefore identifies the systemd activator rather than the later
authority executor; the client requires that single root activator identity
and does not accept root-or-postgres alternatives. Runtime diagnostics expose
these as `restore_authority_socket_activator_user` and
`restore_authority_executor_user` so they cannot be confused.

The authority socket's dedicated parent is mode `0750` and owned by `postgres`
and the LOOM service group. The socket is mode `0660` with the same ownership,
has one link, has no network listener, and validates its exact path and custody
without following a link. Those inode identities are independent of the
systemd activator identity. `loomd` keeps its separate `/run/loom` runtime
directory; the old
`/run/loom/restore-authority.sock` path is forbidden and should be absent. The
authority derives database owners and PostgreSQL command arguments from the
reviewed node configuration. A caller cannot supply SQL, an executable, an
owner, a database URL, a server path, or PostgreSQL options.

Restore dump bytes retain the authenticated package size and SHA-256 identity.
The authority receives exactly that bounded stream into an unlinked private
file before running a fixed PostgreSQL command vector. Truncation, trailing
bytes, digest drift, cancellation, an unsafe database namespace, or a socket or
peer mismatch fails closed. If database creation succeeded, restore failure
triggers bounded cleanup; the backup layer still performs its own mandatory
final exact drop after verification. `loomd` remains unprivileged with
`NoNewPrivileges=true`; no sudo rule or `CREATEDB` membership is part of this
design.

`restore-drill --dry-run` authenticates, extracts, and produces the restore
plan, but does not create either disposable database or invoke `pg_restore`.
Once database work starts, a non-dry run performs mandatory cleanup of its
disposable targets. Fetch failures start no database work and do not drop targets.
`--keep` is refused, and database owners and active-database names are not CLI
inputs. Structured output includes target names, package identities, restored
schema head, and final schema head; it does not include a PostgreSQL URL or
credential material.

After a successful direct-archive fetch, any failed non-dry strict restore
publishes `restore-failure.json` beside the fetched `backup` directory. The
receipt is an atomic, directory-synced, mode-`0600` regular file. It binds the
exact archive/ref and direct-manifest identities, strict-restore operation,
operational or Provenance disposable target, closed failure stage and code,
cleanup truth, and UTC occurrence time. It deliberately excludes command
output, dump bytes, database URLs, repository credentials, environment values,
staging paths, and arbitrary error text. An exact existing receipt is an
idempotent replay; malformed, linked, permissive, oversized, unknown-field,
identity-conflicting, or byte-conflicting evidence fails closed and is never
overwritten. A failure receipt records the failed attempt only: it does not
invalidate the archive, authorize a retry, retain a database, or replace the
authenticated archive/package evidence.

Cloud fetch failures also publish `restore-failure.json`, using schema
`loom.cloud_restore_failure.v2`. The receipt identifies the fetch stage
(resolution, destination preparation, authentication, archive verification,
extraction, or extracted verification), the selected archive and authenticated
manifest when available, and both requested disposable database names. Missing
identity is explicit. Every attempt uses a fresh directory with a random suffix;
the configured cloud state path and its ancestors must be real directories.

Fetch evidence records the Borg operation name, an available nonzero exit code,
and a closed list of diagnostic categories such as `permission_denied` or
`no_space_left`. Arbitrary process text is omitted. `diagnostic_text_omitted`
is always true; `diagnostic_truncated` separately reports loss from output
bounding. An empty category list means no recognized diagnostic, not that Borg
succeeded. These categories describe observed output, not a proven root cause.
No argument vector, repository locator, environment, database URL or raw stderr
is stored. Payload extraction captures at most 64 KiB per diagnostic stream.

The typed error `cloud.fetch_<stage>_failed` carries the failure stage and a
relative receipt locator through the service, local client and CLI. If safe
publication fails, the error says so; it never reports that evidence was saved.
Preflight failures before an attempt can be created have no receipt. The older
strict-restore v1 receipt remains readable and unchanged.

For a fetch failure, authority and database operations are `not_started`,
database cleanup is `not_attempted`, and extracted payload is `retained`. This
states what this attempt did; it does not claim a pre-existing target database
is absent. A nonzero Borg result remains a failure even when its extracted bytes
verify. No automatic transfer retry or payload deletion follows. Removing one
failed restore payload requires the separate operator-reviewed cleanup workflow
below. Archive retention and storage cleanup manage different objects.

The compact regression `tests/smoke/v2_cloud_fetch_failure_local.sh` uses a fresh
local Borg 1.4.3 repository, about 2 MiB of synthetic source data, and an owned
Unix-only PostgreSQL 17 cluster. It proves the actual service/client failure
route, native extraction refusal, failure after complete extraction, durable
receipts, and zero authority, `pg_restore`, or database mutation. It also tests
reviewed payload cleanup and unchanged exact replay through the actual cleanup
route. A real internal hardlink pair first receives one fixture-only external
alias: the unaccounted link must refuse before quarantine. After removing that
exact external fixture alias, a fresh reviewed plan cleans the internal pair
and records both link-count transitions without increasing archive payload
bytes. Failure and cleanup evidence remain under the worktree acceptance
folder. It stops only its own processes.
Its synthetic package dumps test the fetch boundary; this is not full
database-recovery acceptance.

The supported CLI error for an authority failure uses
`backup.restore_authority.<stable-code>`, domain `backup`, the exact disposable
database as target, and the command correlation ID. The summary names the
closed authority stage without exposing PostgreSQL/Borg output or local paths.

### Cleaning one failed cloud restore payload

`loom cloud snapshot restore-cleanup` addresses exactly one attempt basename
beneath the daemon's configured `<cloud-state>/restore-drills`. It removes only
that attempt's `backup` subtree. It preserves the attempt directory, original
failure evidence, lifecycle marker, reviewed plan and cleanup journal. There is
no automatic retry, age sweep, archive pruning, database operation or payload
transfer in this command.

An operator must first establish safe custody and review separate evidence that
the attempt is inactive, **both exact disposable target databases are absent**,
and the node is healthy and quiet with no restore, Borg or backup operation.
A fetch receipt's `not_started` fields describe that request and do not prove
current database absence. Production checks, evidence installation and apply
remain local-integrator/operator actions requiring their own authorization.

Install `restore-cleanup-preflight.json` outside `backup`, in the exact attempt,
as a daemon-owned, single-link regular file with mode `0600` and no mutating
ACL. The closed schema `loom.cloud_restore_cleanup_preflight.v1` contains:

| Field | Required value |
| --- | --- |
| `attempt` | Exact directory basename |
| `device`, `inode` | Numeric identity of the reviewed attempt directory |
| `operational_target`, `provenance_target` | Exact disposable names matching failure/adoption evidence |
| `inactive.status` | `inactive` |
| `database_absence.status` | `both_absent` |
| `health_quiet.status` | `healthy_no_restore_borg_backup` |

Each of the three observations also contains `observed_at` (UTC timestamp with
`Z`) and `source_sha256` (64 lowercase hexadecimal characters identifying the
separately retained, reviewed observation). Inactive and database-absence source
digests must differ. All observations must be at most 15 minutes old, with no
future timestamp, when planning or starting apply. These are operator-authored
claims: cleanup validates their structure and binding; it does not run external
checks or authenticate the referenced source artifacts. Renewing this evidence
invalidates an earlier plan digest.

Ordinary eligibility requires the existing valid v2 `restore-failure.json` and
its lifecycle marker. For a legacy attempt that never received fetch diagnostic
evidence, an operator may instead install the separate mode-0600
`restore-legacy-failure.json`. Its schema is
`loom.cloud_restore_legacy_failure.v1`, with `status: failed`,
`diagnostic: diagnostic_unavailable`, and exact `attempt`, `ref`, `archive`,
`manifest_sha256`, `operational_target` and `provenance_target` fields. The
retained manifest must match. This is a reviewed adoption of failure, not a
synthetic v2 receipt or evidence of Borg success. A v1 strict-restore receipt,
ambiguous v2 plus legacy evidence, or missing preflight evidence is refused.

With those inputs reviewed, use the daemon route selected by the normal CLI
context. Replace the placeholders with the one basename and exact returned
`plan_sha256`:

```sh
loom cloud snapshot restore-cleanup plan <attempt-basename>
loom cloud snapshot restore-cleanup apply <attempt-basename> \
  --confirm-digest <reviewed-sha256> --dry-run
loom cloud snapshot restore-cleanup apply <attempt-basename> \
  --confirm-digest <reviewed-sha256> --yes
```

Plan and dry-run write no files or payload. Apply requires both the exact digest
and explicit `--yes`; dry-run also requires the digest. The plan binds complete
metadata, configured root and ancestor identities, attempt identity, original
failure and preflight bytes, archive/ref/manifest, and every payload entry.
Changing these inputs requires a new review. Requests accept selectors only:
there are no local, root, path, config-override or keep flags. The HTTP routes are
`POST /v1/cloud/snapshot/restore-cleanup/plan` and
`POST /v1/cloud/snapshot/restore-cleanup/apply`; errors use the redacted
`cloud.restore_cleanup_refused` code. Public results expose bounded identity,
digest, status and counts. Full paths and metadata remain in private evidence.

Inventory is metadata-only for payload: it never rehashes payload bytes. It
refuses above 100,000 entries, 64 MiB serialized metadata, 128 levels, 4,096 bytes
per relative path, 1 TiB logical regular-file bytes, or two minutes per scan.
Only fixed, bounded manifest and control evidence is read and hashed. Directory
access/default ACL metadata is bound in the reviewed plan. The journal is capped
at 128 MiB; a conservative preflight estimate can refuse a plan earlier than
the other inventory limits. Each regular-file name counts toward logical bytes,
including each internal hardlink alias. Multi-link groups also have a conservative
aggregate revalidation-work limit of 2,000,000 units, calculated as
`3*k*k + 8*k` for each group of `k` names; a very large group can therefore
refuse even below the entry limit. Unaccounted hardlinks, special files, mount
crossings, unsafe owners/modes/ACLs,
unsupported inspection and observed replacements are refused. Symlink leaves
are removed as links; their targets are never traversed. The attempt, top-level
`backup`, and external ancestors must deny effective non-owner namespace write.
Linux access/default POSIX ACLs are validated separately, including each mask.
Nested restored directories may preserve source write grants or lack owner
write, such as `0775` and `0575`, when they are daemon-owned and the quarantine
seal below can make them deletable. Unreadable/unsearchable directories,
special permission bits, and unsupported ACL inspection refuse before moving
payload. Darwin supports mode sealing only when extended ACLs are already safe.

Ordinary regular payload files must be daemon-owned held inodes with bounded
nonnegative size and unchanged reviewed metadata. A multi-link inode is allowed
only when the complete no-follow inventory finds exactly its reported number
of unique names, all inside this one `backup`, with identical inode metadata.
Grouping includes device, inode and mount identity. An external sibling alias,
a missing name, or inconsistent identity refuses planning. All group names are
reopened after inventory and rechecked after quarantine and sealing, before
deletion begins. They may have
external content-write permissions, including mode `0674`, because the safe
parent directory controls entry replacement. On Darwin, payload ACL data-write
and append grants are accepted; grants to delete, change ownership, permissions
or other metadata are refused. Observed metadata drift refuses apply. Cleanup
counts namespace removals: it does not establish stable payload bytes, secure
erasure, or loss of access through an already-open external descriptor.

For each originally multi-link inode, cleanup revalidates all remaining aliases
before unlinking one name. It keeps that inode open through acknowledgement,
observes exactly one fewer link and the resulting ctime, and updates the expected
metadata of surviving aliases. Every other inode field must remain unchanged.
The journal records the exact before/after entry, including the final transition
from one link to zero. Before acknowledging, cleanup checks the removed name is
absent and reopens/revalidates surviving names under the held quarantine ancestry.
Changed content metadata, new links or substituted aliases fail the current step.
An interrupted unlink can have removed a name without a durable acknowledgement;
its count remains unconfirmed. Replay validates the recorded transition sequence
and never infers missing acknowledgements or automatically resumes partial work.

The retained manifest has a separate bounded no-follow evidence reader. It
must be daemon-owned and single-link, deny effective non-owner write, and match
the reviewed hash, held inode and inventory name. Read/execute modes and ACLs,
such as mode `0650`, are accepted. Private lifecycle, adoption, preflight, plan
and journal controls still require exact mode `0600` and no mutating ACL
authority. Cleanup does not rewrite payload bytes or ownership. Its explicit
quarantine seal changes only reviewed directory modes and the corresponding
Linux access ACL owner/mask entries for payload already authorized for deletion.

The implementation retains the full ancestor descriptor chain and rechecks
custody immediately before each descriptor-relative unlink. A per-attempt
lifecycle lock excludes cooperating LOOM fetch, restore and cleanup writers for
the entire window. Standalone fetch rejects marked attempt ancestors,
including aliases and alternate caller configurations. An unmarked manual
destination such as `<cloud-state>/restore-drills/manual/backup` remains
supported when custody is safe; a directory name alone does not identify a
protected restore attempt. Ordinary fetch resolves
the destination once and uses its canonical path, retaining and checking its
existing ancestor chain until the backend returns. Unsafe writable custody is
refused, including a missing destination directly below a shared writable
directory. Root-owned sticky ancestors are permitted only when the next
already-existing child is trusted. This is a cooperating
writer contract: arbitrary root intervention and a rogue independent writer
using the service UID are outside it. Already-open external directory descriptors
or working directories survive relocation and can still permit mutation until
sealing removes their effective write authority. Fresh reviewed inactivity and
health evidence remains required; quarantine is not descriptor revocation. The
final name-based rename/unlink is not atomic with its expected-source-inode
check, and the regression suite preserves that limitation.

After digest confirmation, apply preserves the original metadata in private
`restore-cleanup-plan.json` and writes a durable prepared record to
`restore-cleanup-receipt.jsonl`. It exclusively creates the fixed
`.restore-cleanup-private` directory beneath the safe attempt and verifies
actual daemon-owned `0700` custody. The plan binds its name and prior absence;
the journal records its newly created identity. Existing entries are refused.

Apply records move intent, then atomically moves the single `backup` directory
inside that private directory without replacing any destination. There is no
copy fallback. Both parent directories are synced before acknowledgement, and
the complete relocated inventory is compared with the reviewed source before
any permission change. A new original-location payload is refused.

Next, each directory is sealed from the top down. A durable intent records its
original stat/ACL metadata and target mode; descriptor-bound `fchmod` adds owner
write and removes group/other write. On Linux this also lowers the access ACL
mask for named users/groups. Default ACL bytes stay unchanged: they affect
future creation, and cleanup creates no descendants in the payload. Each seal
is synced, verified and acknowledged. The complete sealed inventory must match
before deletion starts. A partial attempt may therefore retain directories
whose modes have changed; its original modes and ACLs remain in the plan.

Deletion proceeds deepest first through held descriptors rooted in quarantine.
The journal records intent before each unlink and acknowledges removal only
after the parent and journal are durable. Counts include payload entries only.
After confirmed payload absence, apply records an intent to remove the exact
empty private quarantine, removes and syncs it, then records completion. Original
failure, legacy adoption, preflight, lifecycle, plan and journal evidence remains
outside the deleted payload.

A crash can leave an unacknowledged creation, move, seal, or removal even when
the syscall occurred. Every incomplete, torn, substituted or ambiguous sequence
refuses replay and requires integrator review; no automatic resume or inferred
acknowledgement is supported. Inspect both `backup` and
`.restore-cleanup-private/backup` and the recorded phase without treating absence
as proof of deletion. Preserve all controls and any quarantine; do not remove a
journal, adoption record or private directory to force a retry.

Only an exact completed replay with the full valid phase sequence, original
evidence/custody, and both payload and quarantine absent returns `completed`
without rewriting evidence. Earlier cleanup plans without the quarantine/seal
protocol marker require integrator review rather than reinterpretation.

### Borg retention review

Create and save a read-only plan first:

```bash
loom cloud snapshot retention plan --json > /secure/review/loom-retention-plan.json
```

Review the repository id, fixed `14 daily / 8 weekly / 6 monthly` policy,
inventory digest, protected classes, prune candidates, and plan digest. Apply
only that saved plan and exact digest:

```bash
loom cloud snapshot retention apply \
  --plan /secure/review/loom-retention-plan.json \
  --confirm-digest <exact-plan-digest> \
  --confirm
```

Add `--compact` only after reviewing the prune result and deciding to request
the separately reported compact stage. A `partial` result can mean prune
succeeded while compact was skipped or failed. Do not infer reclaimed bytes
from prune or compact success.

Routine writer configuration is insufficient for this command. The daemon
must have a distinct enabled retention authority. Writer and retention
passphrase files must be distinct underlying regular files, not path,
hard-link, or symlink aliases. All writer/retention cache and security roots
must be absolute and mutually non-overlapping after symlink resolution. For an
SSH repository, each side must explicitly select exactly one absolute SSH
identity file, those identities must be different underlying files, and
`IdentitiesOnly=no` is refused. Missing or shared authority configuration fails
before Borg runs.

Before the repository-wide check or prune, LOOM proves that every live archive
caught by the reviewed prune glob is an authenticated, exact-name, same-node
`user_data` archive represented in the canonical plan. A canonical-looking
malformed, unauthenticated, foreign-node, or otherwise unclassified glob match
refuses the apply and remains present. Routine writer command execution also
requires the Borg command as the first argument from an explicit allowlist;
option-prefix forms cannot reach prune, delete, or compact.

Strict restore evidence can make exact legacy generations *eligible* in the
read-only migration report, while the F9/milestone, shared store, operational
packages, failed evidence, unknown entries, and active staging remain
protected. Eligibility is not cleanup approval and this slice provides no
cleanup apply operation.

## Morathustra Native Recovery

The disposable acceptance gate covers the initialized native runtime through
cloud fetch and profile restore. The scaffold README, legitimate SQLite sidecar
activity and bundled Nix skill timestamps are supported by the accepted recovery
contract. Every declared source must appear in the authenticated native ZIP;
source timestamps are never normalized to force acceptance. Production activation
remains a separate integrator phase, including native Linux inotify validation.

Morathustra uses one Hermes profile at
`/srv/loom/agents/morathustra/.hermes`. Its online SQLite state must be captured
by the pinned Hermes 0.21.0 `backup` command. LOOM holds a no-follow database
descriptor only for inode identity and kernel change monitoring; it never reads
or copies live database bytes. Direct cloud archives exclude the entire live
`.hermes` tree;
approved native and created skills travel inside the authenticated native ZIP.
Workspace contracts outside `.hermes`, including `AGENTS.md`, remain ordinary
cloud sources.

The gated `loom-hermes-recovery` helper publishes exactly two files under
`/srv/loom/agents/morathustra/recovery/<request-id>`: `profile.zip` and
`manifest.json`. The manifest is signed with Ed25519 and binds the pinned
version/revision, executable hash, canonical profile, source identities, UTC
creation time, complete inventory, modes, sizes and hashes. Package directories
are `0750`; files are `0440`. These modes preserve an already provisioned reader
ACL mask; they do not grant `loom` access or prevent the owner from changing a
file. Verification uses an independently provisioned public key and detects
changed bytes rather than trusting file modes alone.

The scaffold's exact `recovery/README.md` remains an ordinary versioned workspace
contract. It is optional for recovery discovery and cannot satisfy freshness or
authenticate a package. Discovery accepts it only as a no-follow, single-link
regular file with mode `0644`, the recovery directory's owner, and at most 1 MiB
of content. Its held identity, bytes and pathname must remain stable throughout
discovery. Edits between checks are allowed; its repository bytes are not pinned
as recovery evidence. No other loose file or unknown directory is allowed in
the recovery root.

Borg includes this README with the ordinary workspace sources. The frozen
recovery namespace checks its exact path, regular-file type, mode, bounded size,
health, absence of link targets and owner matching the archived recovery root.
Every authenticated package must still contain exactly `manifest.json` and
`profile.zip`, with their authenticated modes, sizes and hashes. Staging and
undeclared siblings remain forbidden in the archive. Direct archive v1 retains
its ordinary-source verification; v2 retains its existing Borg ordinary-source
optimization and does not hash the README as signed package evidence.

Runtime and recovery have separate switches. `loom.morathustra.enable` controls
the Hermes runtime and gateway. `loom.morathustra.recoveryEnabled` defaults to
`false` and alone controls recovery coverage, the producer in the `agents`
account, and its daily timer. The module supplies the separate
`LOOM_MORATHUSTRA_RECOVERY_ENABLED` flag to `loomd` when either switch is on;
when both are off, absent flags retain the daemon's false defaults.

| Runtime | Recovery | Backup contract |
|---|---|---|
| Off | Off | Existing backup behavior; no Morathustra recovery claim |
| On | Off | Bootstrap and observe the real profile; no Morathustra recovery claim |
| Off | On | Recovery remains required for the existing profile while the gateway is stopped |
| On | On | Runtime active; authenticated recovery required |

During runtime bootstrap with recovery off, 03:00 local and 03:15 cloud behavior
stay unchanged. Live `.hermes` and recovery payloads remain excluded from direct
cloud archives. Runtime enablement does not require a public verification key.
After real-profile configuration, exercise and observation, the integrator
validates native Linux custody monitoring and the existing publication/cloud
restore path, then enables recovery before model or messaging activation.
Recovery enablement requires a 64-character hexadecimal Ed25519 public key and
preserves the existing fail-closed freshness, authenticity and revalidation
checks. A malformed key supplied directly to daemon configuration is still
rejected even when recovery is off; omitting it while off is valid.

When recovery is enabled, `loom-morathustra-recovery-publish.timer` publishes
one authenticated online snapshot at 02:45 `Europe/Amsterdam`, before the
existing 03:00 Main backup and 03:15 cloud upload. The timer is persistent, but
the publisher still refuses an absent or substituted key, unexpected profile
socket, source mutation, incomplete native ZIP, or failed signature check. It
accepts only Hermes's exact owner-only `gateway.sock` and
`state/gateway.loop-tick.<pid>.sock` runtime sockets, holds their namespace
identity stable for the snapshot, and excludes them from recovery bytes as the
pinned native backup does. The timer does not change either LOOM worker policy.

Set the public `recoveryPublicKey` option only after the integrator has arranged
an external owner-only signing key and the existing `agents`/`loom` access
boundary. The signing key is a 64-byte Ed25519 private key file outside the
workspace at `/var/lib/loom/morathustra-recovery/signing.key`, owned by
`agents:agents` with mode `0600`. It is never written
to Nix configuration, Git, the manifest or command output. Missing public trust
is a configuration error when recovery is enabled. The private key and profile
are never generated by Nix; enabling recovery declares the reviewed publisher
and timer only after those external inputs exist.

During activation, the exact recovery-key root is excluded from ORCA's broad
runtime-state ACL reconciliation. The recovery-specific custody pass validates
the canonical root and one-link 64-byte key identity, removes inherited named
ACLs, and restores root/key modes `0700`/`0600` without reading, copying, or
replacing key content. Publication remains refused until that custody contract
is exact.

The live `.hermes` profile is also excluded from the `loom` service's broad
agent-workspace read-only ACL pass. Before the gateway starts, an agents-owned
custody helper rejects links, unsupported special entries, ownership drift, and
multiply linked files, then removes inherited ACLs and group/other access from
profile directories and regular files without reading content or changing
ownership. LOOM reads the authenticated recovery package outside `.hermes`;
it does not need direct access to Hermes' mutable databases or logs. This keeps
Hermes' own managed file-mode updates from invalidating an online snapshot.

Recovery verification binds the complete absolute ancestor chain with
no-follow descriptors. On Linux, ancestors use path-only descriptors so a
deliberately execute-only custody parent such as `/srv/loom` remains
traversable without granting directory-list permission. The final recovery
root or package still opens read-only and is enumerated normally. Verification
must not widen a parent ACL merely to inspect a child package.

The sibling `recovery/` tree has a separate read-only contract. The broad
workspace ACL pass does not recurse into it. Its root carries only the default
`loom` read/traverse ACL needed by newly published packages, while the publisher
still seals package directories and files to exact `0750`/`0440` modes. The
ordinary versioned `recovery/README.md` is normalized to exact `0644` without
reading or replacing it. This prevents a generic ACL mask from changing the
README mode and causing Main backup verification to fail; signed packages and
interrupted staging are never rewritten by activation.

Publication requires the `agents` identity, the canonical initialized profile,
the exact pinned Nix Hermes executable, a stable request ID and a fresh UTC
request timestamp. Retrying the same ID and timestamp verifies and returns the
existing package without running native backup again. A different timestamp or
source-root identity for that ID is a conflict. Interrupted attempts stay under
`recovery/.staging`; they are excluded from cloud sources and cannot satisfy
coverage. There is no automatic retention or cleanup here.

Ordinary profile files must remain byte-for-byte stable during capture. SQLite
contents may change through Hermes's online snapshot API. The sole ordinary-file
exception is operational log content under the exact `.hermes/logs/` subtree:
Hermes itself logs during backup, so the signed ZIP records those captured log
bytes without claiming an atomic log history. Log names, directory identities,
types, owners, modes, size bounds and single-link status remain checked. Log
rotation, new files or namespace replacement during capture cause refusal and
require a later retry. First-time native logging initialization must finish
before inventory. This allowance does not cover sessions, memories, skills,
config, cron, SOUL or any other path. External memory providers and nested
profiles are outside this adapter's boundary and are refused.

Exact SQLite sidecars (`<declared>.db-wal`, `.db-shm`, and `.db-journal`)
may appear, change or disappear only beside an online database already declared
in that same held directory. Every sidecar present in the initial or fresh
inventory must be a bounded regular file, have one link and no special mode
bits, and share its database's owner. Unknown sidecar-like names are refused.
All other namespace entries, including static exclusions, retain their bindings.

Publication now monitors the held root, child directories and source inodes
before enumeration or content validation. Linux uses inotify; Darwin uses
kqueue. Rename, deletion and link changes of a bound source remain fatal even
when a rename-away-and-back is hidden by simultaneous sidecar activity. Monitor
setup, read or event-loss failures refuse publication. Unsupported platforms
refuse publication rather than silently omit monitoring. Database and log
content writes may coalesce with Darwin attribute notifications; final identity,
mode, owner, link and type checks still apply. Bound logs also permit the
attribute-only event from Hermes reapplying its existing managed mode. On Linux,
inotify cannot distinguish this from a hardlink to that same log created and
removed entirely outside watched directories; that transient case cannot
substitute the source pathname or introduce an external inode. Persistent links
and links inside the watched namespace remain refused. Ordinary file bytes
remain hash stable. A transient unbound entry absent at both inventories and
absent from the ZIP is not claimed as source evidence; Darwin's directory events cannot identify
that non-effect. Any extra entry captured in the ZIP still fails exact inventory
matching.

The pinned Nix package applies a small immutable Python timestamp shim only to
`hermes backup`. When the caller omits Python's `strict_timestamps` argument for
ZIP creation, it uses `False`, letting Python clamp timestamps to ZIP's supported
range. This includes Nix-bundled skills whose source mtimes are in 1970, with
unchanged bytes, modes and relative paths. Source timestamps are never modified,
and every expected skill must still be present. Explicit caller timestamp
settings are preserved. Ordinary Hermes commands, gateway, TUI and import do not
receive this behavior. Packaging fails if the pinned wrapper or Python command
shape changes; the upstream version and revision stay pinned.

When enabled, Main coverage, the Main backup worker and direct cloud archive
creation require at least one authenticated package no more than two hours old
and reject future timestamps. Every included committed package must verify.
Workers revalidate evidence before success; cloud archives also hash the bounded
frozen recovery package inside Borg before committing it. An invalid old package
cannot silently enter a new archive alongside a fresh one. When disabled, both
live `.hermes` and the unauthenticated recovery tree are excluded. A cloud root
nested inside either internal tree is refused.

Verify a retained or fetched package using its independent public key and its
original workspace identity. This operation does not open the live profile:

```bash
loom-hermes-recovery --mode verify \
  --package <exact-retained-or-fetched-package-directory> \
  --public-key <independently-retained-public-key-hex>
```

Successful JSON names the request ID, creation time, manifest hash and both
package files. Do not obtain the public trust key from the package being
verified. The package contract covers contents and POSIX modes, not host xattr
fidelity. The native archive is crash-consistent for SQLite and stable for the
other required profile data; it is not a global transaction across unrelated
Hermes stores.

The disposable repository gate is:

```bash
bash tests/smoke/v2_morathustra_hermes_local.sh
```

The gate verifies the real scaffold and disabled Nix baseline, approval-gated
memory/skills, protected installed skills, foreground gateway start/restart,
distinct gateway/TUI sessions and supported cross-session search. The packaged
TUI returns its provider-absence error. Synthetic transcripts use native APIs;
Objects, Notes and Provenance commands use a typed disposable Unix-socket fixture.
These checks do not authorize or simulate a live model or messaging conversation.

It interrupts an observed native backup process while holding its fixture-only
backup lock, preserves the unaccepted staging attempt, and retries the same
request during native WAL activity. Exact replay, zero archive errors, complete
source/ZIP inventory and all 327 Nix skill timestamps are checked before Borg.
The real disposable Borg archive excludes raw `.hermes` and staging paths. Native
import restores sessions, memory and skills, including gateway/TUI cross-search.

The authenticated ZIP preserves source file modes. Native import uses its normal
creation permissions for new files, so restored native skill files are not
required to keep the original Nix read-only modes. Their bytes and restored
approval settings are checked; the separate LOOM-installed skill tree remains
read-only throughout import and readback.

The gate requires macOS `sandbox-exec`, restricts private reads and writes, and
denies native networking and host-service lookup. Only the compiled LOOM query
fixture receives access to its exact temporary Unix socket. Native import cannot
fork; TUI children inherit containment and a pinned exec allowlist. Host checks
require unchanged LaunchAgent metadata, absent Hermes jobs and no remaining
runtime process or fixture socket. Receipts stay under a fresh `.loom-acceptance`
directory. No production database, repository, profile or credential is used.

Pinned `hermes import` automatically attempts gateway service installation,
even with managed mode and no messaging platforms. A synthetic `HOME` alone
does not contain that behavior: service helpers can resolve the real account
home. The smoke proves outside writes and subprocess creation are denied before
calling import, and checks host service/LaunchAgent absence before and after.
Do not run native import as a casual restore check on an operator host.
Production provisioning, cadence, native import, gateway activation and live
restoration remain separate integrator decisions. Linux runtime compilation
does not establish equivalent Linux import containment.

## Controlled Live Recovery Boundary

Before a live restore, the operator must independently approve:

1. target node identity and installed canonical layout;
2. service/writer shutdown;
3. exact backup reference and successful drill;
4. PostgreSQL replacement procedure;
5. filesystem root replacement without path escape;
6. ownership/permission restoration;
7. post-restore migrations, status, custody, and SMB checks;
8. rollback or abandon criteria.

Do not improvise `rsync --delete`, database drops, or live-root moves from this
page alone.

## Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Sync Retention And Cloud]]
- [[Main And Mac Operations]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Purpose

This runbook is for operators proving that LOOM can recover main-node state. It
starts with read-only checks, uses dry-runs before mutation, and keeps restore
drills away from the active database.

### Start With Status

```bash
loom backup status
loom backup list --limit 5
loom maintenance backup status
loom maintenance findings list --status open --limit 20
```

Healthy state means:

- backup status is `ok`;
- the latest successful backup is recent enough for the operating policy;
- open backup-related findings are zero or understood;
- backup operations have succeeded artifacts.

At verification time, the latest backup operation was succeeded and backup
status was `ok`.

### Check Coverage

```bash
loom backup coverage
loom backup contracts plan
```

Healthy coverage means critical paths are present and contracts are satisfied.
If coverage is critical, do not treat source-side deletion, cleanup, or cloud
upload as safe until the missing coverage is repaired.

For extra folders that are not project-owned, create a standalone backup
contract through main. The Portal's **Protect Folder** workflow is the normal
user path; this is the advanced CLI equivalent:

```bash
loom backup contracts preflight \
  --node main \
  --path /srv/field-data

loom backup contracts create field-data \
  --node main \
  --path /srv/field-data \
  --display-name "Field Data" \
  --preflight <completed-preflight-id>
```

Main stores canonical desired YAML under its configured hidden Box metadata,
while the selected owner node owns and scans the source folder:

```text
<box-root>/.loom/contracts/backup/field-data.yaml
```

The Box contract should point at `.loom/contracts/backup` through
`policies.backup_contracts`. After creating, enabling, disabling, rechecking,
retrying, or deleting a standalone contract, inspect lifecycle evidence:

```bash
loom backup contracts status --node main
loom backup contracts inspect field-data
loom backup coverage
loom backup contracts plan
```

Queued or `202 Accepted` work is not backup custody. `active` proves the current
configuration is applied and reported; only `protected` also proves an accepted
backup after that acknowledgement. `disable` keeps YAML and makes the policy
inactive. `delete --yes` removes desired YAML and the managed watched root.
Neither action deletes source data or retained backups.

### Plan A Manual Backup

Always run dry-run first:

```bash
loom backup create --dry-run --production
```

Review the captures and exclusions. Current dry-run output includes capture
groups for PostgreSQL, object store, private backups, main Documents, storage
retention, storage archive, `loom-notes`, health, and manifest evidence.
Excluded classes include private keys, credential tokens, enrollment tokens,
secret environment files, and raw database URLs.

Run the backup only after review:

```bash
loom backup create --production --reason "operator reviewed backup plan"
```

Non-production fixtures must say so explicitly:

```bash
loom backup create --allow-non-production --reason "fixture"
```

### Verify A Backup

```bash
loom backup verify <backup-ref-or-path>
```

The ref can be a registered backup operation id, a local backup directory, or a
path that the command can resolve. At verification time, the latest registered
main backup verified successfully from Mac through the main-backed endpoint.

### Restore Drill Dry-Run

Use a temporary database name:

```bash
loom backup restore-drill <backup-ref-or-path> \
  --dry-run \
  --target-database loom_restore_drill_manual
```

Rules:

- target database names must start with `loom_restore_drill_`;
- never use the active LOOM database as the target;
- run the drill where the backup directory is readable.

From Mac, a registered main backup ref can verify, but restore-drill dry-run
can fail with `backup.restore_drill_requires_local_directory` because the Mac
process cannot read `/var/lib/loom/backups/main/...`. That is expected for a
main-local backup path. Run the drill on main or against a local fetched cloud
snapshot staging directory.

### Run A Real Restore Drill

The deployed non-dry path is the strict cloud snapshot workflow in the next
section. The legacy local-backup command deliberately has no implicit sudo or
restore-authority fallback.

### Cloud Snapshot Restore Drill

Cloud restore drills run through `loomd` by default. Run the operator CLI on
Main with its normal runtime configuration; the daemon reads the cloud config
and credential references in its existing service context. The CLI does not
need access to those credentials. Do not widen key permissions to run a drill.

First inspect the selected archive:

```bash
loom cloud snapshot list
loom cloud snapshot verify latest
```

Then ask the daemon to fetch, authenticate, verify and plan the drill:

```bash
loom cloud snapshot restore-drill latest \
  --dry-run \
  --target-database loom_restore_drill_cloud \
  --provenance-target-database loom_provenance_restore_drill_cloud
```

Cloud snapshot drills are operator workflows because they may download backup
payloads and create temporary databases. `--target-database` names the
operational target; `--provenance-target-database` names the independent
Provenance target. Omit both to use generated safe names. The authority derives
both owners and both active-database refusals from reviewed service
configuration; they cannot be changed on the command line. Never place database
URLs or credentials on the command line.

The delegated operation and CLI wait each have a two-hour bound and honor
request cancellation. Staging belongs to the daemon's configured cloud state
directory. `--cloud-config` accepts only the exact default
`/etc/loom/cloud/config.json` (or an empty selection); alternate paths and
`--state-dir` overrides require explicit `--local` mode. `--keep` is refused in
both modes. A daemon error is returned directly without retry or local fallback;
typed restore-authority failures retain their code, stage summary, target and
correlation ID without exposing raw PostgreSQL output.

`--local` retains in-process execution for disposable/development fixtures that
provide their own cloud configuration and staging directory. It does not bypass
the existing restore-authority peer checks or grant access to service-only
credentials. Non-dry local tests still need their reviewed socket authority.
The separate `snapshot fetch` command continues to execute locally and is not a
prerequisite for the delegated drill. Dry-run still downloads and verifies the
archive; it skips database creation and restore, rather than skipping extraction.

### Evidence To Preserve

For an incident or release gate, preserve:

- command output or JSON envelopes;
- backup operation id;
- backup directory or cloud snapshot ref;
- restore-drill target database name;
- correlation ids;
- coverage report status;
- cloud status and backend status;
- any findings or worker failure ids.

Do not paste secrets, raw DB URLs, passphrases, or private key material into
notes or bug reports.

### Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Cloud Storage]]
- [[Database Maintenance]]
