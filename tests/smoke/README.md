# Smoke Test Status Policy

## Focused project developer experience (G2a)

Run `bash tests/smoke/v2_project_developer_experience_local.sh --fixture-only`
for the frozen A–D prerequisites using one owned PostgreSQL cluster and actual
Projects, Notes and Provenance owners. `--case C` selects one case; `--gates`
adds focused/race/vet checks. After the fixture checkpoint,
`--native-preflight` probes the installed ephemeral CLI and preserves any cold
context/logging blocker without claiming model acceptance. See
`tests/acceptance/project_dx/README.md`. Evidence and cleanup receipts live in
`.loom-acceptance/g2a-<timestamp>`; no Main/ORCA or production data is used.

## Main-To-Mac Computer Use And MINA Routing (Wave 5)

`bash tests/smoke/v2_main_mac_computer_use_local.sh` runs the actual W4 Nix
render, pinned upstream/required patched-native Python fixtures, real packaged
wrapper/import probes, synthetic MCP/fake SSH sessions and MINA scaffold/export
reconstruction. It requires the reviewed cached Python/Hermes/connector outputs;
missing dependencies, a required skip or any new expected failure stop the run.
Only the sixteen frozen upstream/remote-absent expected failures are permitted;
all 95 required patched-native cases must pass. A small added F10 case ends one
synthetic session and proves the other still captures and accepts one input.
Receipts remain under `.loom-acceptance/main-mac-w5/` and the existing projection
smoke's `.loom-acceptance/mina-w4/`; other fixtures use their own temporary roots.

Run only in a development checkout. This does not contact Main or a real Mac,
initialize a live profile, install/activate a service, access credentials/TCC,
run a model, or prove Linux sandbox/real GUI behavior. H01-H04 remain operator
acceptance. The source receipt is
`.project/features/main-mac-agent-computer-use/local_acceptance_wave_5.md`.

## Notes Archive Lifecycle

`bash tests/smoke/v2_notes_archive_lifecycle_local.sh` owns a Unix-only
PostgreSQL 17/pgvector cluster, small source trees and compiled API/CLI helpers.
Six checkpoints cover the frozen contracts; real archive/projector/receipt and
writer/read fences; public search, exact object/passage, root and overview
reads; process restart and old-event replay; existing worker cursor recovery;
Notes Portal controls; and preserved active Box/PDF/citation behavior.

Topics use shared roots with active neighbours. Legacy and material project
Notes remain readable after exact inactive restore without enabling writers.
Library acceptance archives both a real failed PDF extraction and a binary
metadata-only source. Failure remains explicit in the retryable pipeline stage;
the object cache remains stale. Successfully indexed metadata does not acquire
source passages. Archive movement changes neither source/version/chunk identity
nor pipeline/embedding work. Private sources stay hidden even in archived mode.

The gap between owner intent/completion and Notes projection fails closed until
the exact object receipt exists. Paths impose a stop only, never read authority.
API/CLI identity and ordering match; only the live recency score is excluded
from sequential-request equality. Latency output describes a five-object warm
lexical corpus, not model speed, scheduler delay or production-scale throughput.
Semantic/hybrid tests use deterministic query vectors, not an external provider.

Required database acceptance cannot silently skip. Helpers and the disposable
cluster stop, and only their exact owned temporary roots are removed. The smoke
does not contact Main, deploy, enable schedules, modify Provenance, move user
data or authorize archive purge or production activation.

## Physical Project Custody And Runtime Fences (Slices 4A/4B)

`bash tests/smoke/v2_project_physical_archive_local.sh` creates its own short
temporary root, Unix-only PostgreSQL 17/pgvector cluster, separate databases,
manifest credential, CLI and actual daemon. No operator database URL or live
project is used. The local database migrates through head 66; historical
migrations are unchanged.

Supported scaffold/register and reviewed archive/restore/inspect/recover
commands survive real daemon restarts. Seventeen child-process exit points
exercise project and generic workspace durable boundaries against actual
PostgreSQL plan/journal/catalog/event stores. Two repositories, committed and
uncommitted Git content, `.loom/`, `.repo/`, a Notes source, binary payload,
hardlink and symlink retain exact bytes/modes/inodes/mtimes/targets/allocation.
Concurrent recovery, original event times and one event per transition are
checked. Go/installed-SQL ID parity and a fail-closed schema downgrade protect
canonical project IDs without rewriting old identities.

The separate combined runtime fixture starts active service providers,
endpoints/bindings, schedules/automations and an enabled watched worker. Actual
registry deactivation, routed calls, authenticated result ingestion, node
quiescence receipts and persisted fences run together before payload movement.
Only transport delivery and the host service manager are simulated; no real
systemd service is claimed. Failed stop preserves the source and exact recovery
converges after the simulated manager recovers. New connections/store instances,
read-only project observation and inactive restore preserve disabled runtime,
source/repository identity and event-once truth. The real activation and
registration services refuse restored-inactive mutation; no fence is removed
to bypass them. Successful explicit reactivation/fence release is still a
separate unimplemented contract, not a passing acceptance row.
The smoke stops its daemon/database and removes only its own disposable root,
including owner-write restoration on its generated read-only directories.
No Main, deployment, archive purge or real-data cleanup is performed.

## Box Source Citations And Routing (Slice 4)

`bash tests/smoke/v2_box_knowledge_sources_local.sh` owns a short local `/tmp`
root and pinned PostgreSQL 17/pgvector cluster. Separate disposable main-schema
and Provenance databases exercise the unchanged frozen sources through catalog
registration, admission and the native coordinator, never seeded chunk rows.

The smoke runs the 12 native Topics/Library/project search cases through API
and CLI, including separately attributed conflicting sources and metadata-only
inputs. Version-bound Notes passage reads prove exact old text after edits and
runtime reopen, API/client/CLI parity, hash/identity mismatch refusal, current
privacy/root/owner removal fences, and unchanged redacted pipeline diagnostics.
The Provenance foundation API proves pending versus separately reviewed accepted
decisions, cited-source identity and operation replay. Written routing coverage
checks all 24 frozen first-engine cases; it is not a model-accuracy benchmark.
Pack validation and docs health also run.

The coordinator is manually advanced in these fixtures. Unattended capture,
owner-watch/upload, daemon-restart/ACL/failure isolation, complete archive
lifecycle and Main pilot acceptance remain Slice 5. No production endpoint is
used. The cluster is stopped and its exact owned root removed on success or
failure. Citation tests skip outside this explicitly configured harness.

Version-tagged smoke scripts are compatibility records for the LOOM release
named in their filename. They validate the source format, runtime assumptions,
and operational workflow of that release. Do not mechanically rewrite an old
`v0_x` smoke to make it look like a current v2 test; doing so would erase useful
compatibility history while leaving the versioned name misleading.

Current cross-version behavior belongs in a newly named current smoke. The
canonical project-local layout is covered by:

```sh
bash tests/smoke/v2_project_local_loom_local.sh
scripts/loom-dev v2-project-local-loom-smoke
```

The v2 user-facing protected-folder contract is covered by:

```sh
bash tests/smoke/v2_protected_folder_local.sh
scripts/loom-dev v2-protected-folder-local-smoke
```

This smoke always creates and owns an isolated PostgreSQL 16 + pgvector
container, applies migrations through `00057`, and
runs focused main/API/message/node-agent/client/CLI/Portal fixtures. It uses
only temporary test directories and never contacts main, SSH, production
daemons, cloud services, or user folders.

The v2 service registry has two bounded smokes:

```sh
scripts/loom-dev v2-service-registry-local-smoke
scripts/loom-dev v2-service-registry-mac-smoke
```

The local smoke validates project-local contracts and external provisioning.
The Mac smoke builds a harmless HTTP fixture, installs one uniquely named user
LaunchAgent in a temporary `.loom-acceptance` directory, drives its reviewed
operations through the node-agent service-manager runtime binding, and proves
bootout plus fixture cleanup. It never uses sudo, a system LaunchDaemon, Box,
main, cloud, credentials, or production state. Non-macOS hosts report the Mac
acceptance as skipped rather than claiming launchd coverage.

The current Lane file-tree/bundle selector and local custody lifecycle are
covered by:

```sh
bash tests/smoke/v2_lane_bundle_local.sh
scripts/loom-dev v2-lane-bundle-local-smoke
```

This smoke uses deterministic temporary fixtures only: 2,001 small files for
automatic bundle selection and four 1 MiB files for file-tree selection. It
also runs the local mutation, checksum, malicious-archive, interruption,
resume, cleanup, content-policy, catalog, and offline Portal regressions. It
does not connect to main or claim hardware throughput.

The current canonical-filesystem source/custody/copy/generated contract is
covered end to end by:

```sh
bash tests/smoke/canonical_filesystem_local.sh
```

This version-neutral smoke creates temporary Box, Storage, runtime, generated,
and migration fixtures plus a smoke-owned isolated PostgreSQL 16 + pgvector
database. It deliberately ignores an ambient `LOOM_TEST_DB_URL`, because its
migration and bootstrap checks may only run against the disposable database
created and cleaned up by this script. It proves Box/state separation, both Lane
transports, canonical Documents and Notes, backup/archive custody, safe-delete,
migration apply/verify/rollback, typed API/CLI/Portal status, and rendered SMB
configuration. It never contacts a host, mounts a share, activates Nix, moves
live bytes, or uses a production database.

The current production-update wrapper privilege and argument-boundary contract
is covered without contacting a host by:

```sh
bash tests/smoke/loom_main_update_wrapper.sh
```

This smoke replaces `ssh` with a local recorder. It proves planning, automatic
backup creation, and the outer apply coordinator remain unprivileged; automatic
backup path and maintenance-operation identity are passed separately; all
reviewed update/backup/maintenance arguments survive; unsafe remote-shell
characters fail before SSH; and a remote apply refusal preserves its failing
exit. The fixed rebuild step remains the only sudo operation inside the trusted
update transaction.

The v2 smokes are local and fixture-only. They do not require main, SSH,
production services, credentials, or production state. Versioned aggregate
commands continue to call only the scripts for their named release.

The physical workspace archive lifecycle is covered by:

```sh
bash tests/smoke/v2_workspace_archive_local.sh
```

This smoke owns a disposable PostgreSQL 17 cluster, same-filesystem Box and
Storage roots, a temporary systemd-style credential directory, private Unix
sockets, and temporary `loom`/`loomd` binaries. It runs the real journal,
catalog, event, archive/restore, process-crash, restart, replay/conflict,
fidelity, path-attack, and no-duplicate matrix, then drives the supported CLI,
local-client, and HTTP surfaces across a daemon restart. It never accepts an
ambient database URL, contacts Main or ORCA, enables a NixOS host profile,
reads a production key, or inspects or mutates live Box/Storage data.

The current provenance runtime foundation is covered by:

```sh
bash tests/smoke/v2_provenance_runtime_local.sh
```

This smoke always owns a disposable PostgreSQL 17 + pgvector cluster, separate
`loom_main` and `loom_provenance` databases, a least-privilege provenance role,
and temporary runtime/backup roots. It boots the real `loomd` process twice,
drives authorized registration, replay, manual lifecycle, exact reads, and
metadata-only health over the temporary Unix socket, and runs the real custom-
format dump/restore, authorization, support-redaction, and fail-closed coverage
contracts. It never accepts an ambient database URL, contacts main, activates
NixOS, reads user storage, or substitutes for the independently retained
provenance manifest identity supplied by committed backup operation evidence.
The accepted backup-transition wiring is covered by the full Go gate; this
smoke remains disposable and does not activate or exercise production.

The current Provenance search and reconciliation integration is evaluated by:

```sh
bash tests/smoke/v2_provenance_search_local.sh
```

This smoke owns its PostgreSQL 17 + pgvector cluster, main and provenance
databases, runtime roots, binaries, Unix socket, and temporary test overlay
used to seed the unchanged frozen corpus. It drives all frozen routing choices,
all Provenance-routed queries through real CLI and API paths, exact retrieval,
typed separation, contamination, citation and result/context bounds, then runs
three independent reset-process latency measurements. It recreates only its
own provenance database before the manual Archivist, replay/crash/retry,
dump/restore, daemon-restart, and portable-workspace checkpoints.

The integrated smoke preserves the unresolved-source candidate that previously
required 808 Unicode code points and proves it now fits the unchanged 800-code-
point result contract without changing pending posture, identity, source, or
exact get. Manual replay and a second cycle converge on one open case without
creating an accepted record. The same run proves deterministic crash/retry,
daemon restart, disposable dump/restore, and an applied/replayed portable
Archivist workspace free of host paths, credentials, database locators,
runtime state, and private memory. Every checkpoint remains disposable and
must not connect to main or ORCA, use live data, create the live Archivist
workspace, or enable scheduling.

The current explicit project and repository state contract is covered by:

```sh
bash tests/smoke/v2_project_repository_state_local.sh
```

This smoke owns a disposable PostgreSQL 17 + pgvector cluster, separate
`loom_main` and `loom_provenance` databases, temporary Box/runtime roots, real
temporary `loom` and `loomd` binaries, and a private Unix socket. It proves the
v0.3 compatibility and watched-root-equivalence path; explicit empty, one, and
many-member registration; direct/backend replay; exact unchanged-project
`source_relocation`; stable identity across relocation and member rename;
duplicate and cross-project conflicts; absent, dirty, nested-Git, linked-
worktree, valid/missing/malformed/mismatched `.repo`, and remote-unavailable
observation; deterministic pagination; and archived read-only state. It never
accepts an ambient database URL, contacts a node, migrates a real project or
LOOM, activates NixOS, or reads user storage.

The current cloud-backup routine, assurance, recovery, and daily-local policy
contract is covered by:

```sh
bash tests/smoke/v2_cloud_backup_runtime_optimization_local.sh
```

This smoke resolves exact Borg 1.4.3 plus PostgreSQL 17/pgvector from the
repository's pinned Nix input, creates only temporary repositories, databases,
runtime roots, binaries, and a Unix socket, and rejects an inexact Borg binary.
It runs real v1/v2 archive creation, unchanged/change and interrupted-resume
paths; the complete adversarial add/remove/metadata/exclusion/link/warning
matrix; v1 compatibility and v2 strict fetch/restore; all three assurance
profiles; and disposable PostgreSQL recovery. It dry-runs (but does not apply)
the exact `03:00` main-backup and `03:15 Europe/Amsterdam` cloud policies, then
proves normal/DST behavior and stale-package/overlap refusal before mutation.
It never accepts production database or Borg coordinates, contacts a node,
loads credentials, activates a worker policy, stages a release, or reads user
storage.

The current optional repository development-state contract is covered by:

```sh
bash tests/smoke/v2_repo_development_state_local.sh
```

This smoke builds one temporary `loom` binary and owns every disposable Git
repository, branch, linked worktree, runtime sentinel, and `.repo/` tree it
uses. It proves absent, explicit opt-in, current, stale, malformed, dirty,
owner-project mismatch, multiple-branch/worktree, and archived-runtime
behavior; the initialization path remains digest-reviewed, unstaged, and
uncommitted until the fixture creates an explicit Git commit. It also checks
that accepted portable state contains no host/worktree paths, secret-value or
session-ID assignments, sensitive filenames, runtime/cache directories,
private-memory sentinels, or mutable harness artifacts. A bounded negative
self-test proves the scanner rejects single- and multi-component POSIX paths,
drive paths, slash and backslash UNC shares, file URIs, symlinks, and FIFOs
while preserving valid HTTP(S) URLs.

The script does not contact Codex or ORCA. Native harness agreement, completed
session closure, and one bounded ORCA restart are recorded separately in
`.project/features/repo-development-state-contract/orca_hardware_acceptance.md`.
No real repository is initialized or migrated.

## Morathustra Runtime And Recovery (Slice 4)

```sh
bash tests/smoke/v2_morathustra_hermes_local.sh
```

The gate builds/resolves the current pinned Hermes 0.21.0 package and Borg 1.4.3,
scaffolds the actual Morathustra workspace, and evaluates the accepted Nix service
and baseline offline. Every mutable path is a fresh disposable fixture. It proves:

- one SOUL/profile/workspace, disabled-by-default service/platforms, readable
  LOOM-installed skills with write/chmod/rename denied, and approval-gated native
  memory/skill writes;
- native gateway foreground start/restart, distinct native gateway/TUI sessions,
  bidirectional supported search and gateway routing identity after reopening;
- the actual packaged Node TUI/native backend in a fixture PTY returning
  `No inference provider configured` for a synthetic prompt;
- compiled LOOM `object list`/`object inspect`, `notes search`/`notes objects show`,
  and Provenance search/exact record/candidate/case retrieval through a typed
  disposable Unix-socket fixture, with pending candidates explicit and separate
  and an offline refusal after that fixture closes;
- abrupt interruption of an observed native backup child while its fixture lock
  is held, followed by publication retry and byte-identical replay of the same
  request, preserving the interrupted staging directory;
- native backup during WAL writes, monitored SQLite sidecar activity, zero native
  archive errors and exact equality of declared sources, ZIP entries and signed
  inventory;
- all 327 bundled Nix skill files in the archive with their source mtimes still
  at 1970, correct bytes/modes, and ZIP timestamps clamped to 1980;
- actual local Borg create/replay/fetch, no raw `.hermes` or staging paths in the
  cloud archive, native import, restored gateway/TUI and WAL session retrieval,
  memory, created skills and all 327 bundled skills.

Transcripts supplied through native APIs are explicitly synthetic; no model
response is simulated. Query responses exercise supported CLI routing and typed
readback against a fixture, not a live LOOM backend. Native import uses its normal
creation permissions for new files. Exact source modes are authenticated on the
ZIP; imported native skills retain their intended writable-store semantics and
approval settings. The external LOOM-installed tree remains protected throughout
import and restored readback.

macOS `sandbox-exec` is required. Native operations use an empty inherited
environment, fixture-only writes, protected installed skills, restricted private
reads, no networking and no Mach service lookup. Gateway/session/import probes
cannot fork. TUI descendants inherit containment, execute only the exact pinned
interpreter/wrapper chain and signal only the same sandbox. The recovery
interruption supervisor stops only its observed process group. Negative probes
precede native execution. The LOOM query policy alone permits outbound access to
its exact disposable Unix socket; TCP, unrelated Unix sockets and private sibling
reads stay denied. Both query sockets are closed and removed with inode checks.

Host checks require absent Hermes LaunchAgent/jobs and runtime processes, no
remaining fixture sockets, and unchanged LaunchAgents metadata, including failure
exits. Existing host artifacts cause refusal and are never removed by the gate.
Native gateway control/watchdog socket denials are expected under this offline
boundary. No Main, ORCA, live profile/storage, real provider/platform/account
credential, deployment, external messaging, retention or scheduled action runs.

Receipts remain under `/private/tmp/loom-h4.*/.loom-acceptance`, including native
logs/session IDs, query requests/results, containment policies/probes,
`interruption.json`, `native-recovery-audit.json`, `cloud-restore.json`, restored
readback and host snapshots. The README stays an ordinary scaffold contract;
failed staging never becomes evidence. Exact final hashes and full validation
results are in `.project/features/morathustra-workspace-activation/handoff.md`.

Linux inotify execution remains a separate pre-deployment integrator check. This
Darwin gate does not authorize a Linux host connection, Main deployment, model
login, platform activation or later-phase work.

## Project-Layout Assertion Registry

This registry classifies every pre-existing match returned by:

```sh
rg -n 'loom\.project\.yaml|notes/loom\.notes\.yaml|policies/(sync|backup|workers|credentials)\.yaml|tests/validate_project\.sh' tests/smoke scripts/loom-dev
```

| Script | Classification | Reason |
|---|---|---|
| `v0_3_slice_01_project_contracts.sh` | intentional legacy fixtures | The valid, invalid, and warning fixtures prove that the v0.3 root contract remains readable and diagnosable during the compatibility window. |
| `v0_3_slice_03_project_scaffold.sh` | superseded historical assertions | The v0.3 scaffold contract intentionally records the visible root contract, declarations, policies, facet guidance, and validation helper emitted by that release. `v2_project_local_loom_local.sh` replaces it for current scaffolds. |
| `v0_3_slice_04_project_registration.sh` | superseded historical assertion | The release smoke edits the v0.3 root contract to prove registration drift. Current canonical registration coverage uses `.loom/project.yaml`. |
| `v0_4_1_box_init.sh` | superseded historical assertions | The v0.4.1 Box smoke records the project layout produced when Box-default and explicit-directory scaffolds were introduced. |
| `v0_4_1_loom_box_foundation.sh` | superseded historical assertion | The v0.4.1 acceptance smoke records the explicit-directory scaffold path used by that release. |
| `v0_7_projects_surface_contextual.sh` | superseded historical assertions | The v0.7 hardware/Portal smoke records the backend project source path used by that release; it is not a v2 project-layout acceptance test. |

Current defects found in the pre-existing scan: **none**. The matching legacy
paths in `v2_project_local_loom_local.sh` are deliberate negative assertions,
legacy migration fixtures, and divergent-layout safety fixtures.

## Canonical-Filesystem Assertion Registry

This registry classifies versioned smokes that still mention roots or generated
storage behavior retired by the current canonical-filesystem contract. They
remain historical compatibility evidence; they are not silently skipped and
do not make current-layout claims.

| Script | Classification | Reason |
|---|---|---|
| `v0_6_2_slice_12_production_acceptance.sh` | superseded historical export smoke | Records the v0.6.2 generated-export rebuild and mounted single-share workflow. The version-neutral canonical smoke replaces it for current behavior. |
| `v0_6_3_slice_02_loomd_boot_bind.sh` | superseded historical bind/export smoke | Records the former `/var/lib/loom/main-documents` bind and generated export startup contract. |
| `v0_6_3_slice_03_main_documents_reconcile.sh` | superseded historical Documents/export smoke | Records reconciliation into the former generated tree. Current coverage uses canonical Box Documents and typed status. |
| `v0_6_3_slice_05_main_backup_coverage.sh` | superseded historical backup-root smoke | Records backup coverage of the former Main Documents root. Current backup coverage derives canonical Box, Storage, archive, backup, and generated roots from configuration. |
| `v0_6_3_slice_06_cloud_foundation.sh` | intentional legacy configuration fixture | Keeps the deprecated export-root input readable for its release while its cloud-root checks remain historical compatibility evidence. |
| `v0_6_3_slice_07_cloud_snapshot.sh` | intentional legacy configuration fixture | Keeps the deprecated export-root input readable for its release while current cloud snapshot coverage uses configured canonical roots. |
| `v0_6_3_slice_08_cloud_retention.sh` | intentional legacy configuration fixture | Preserves the v0.6.3 cloud-retention environment surface, including the deprecated export-root input. |
| `v0_6_4_packed_cloud_backup_local.sh` and `v0_6_4_packed_cloud_backup_production.sh` | intentional legacy configuration fixtures | Preserve the v0.6.4 environment surface, including the deprecated export-root input; current backup coverage explicitly excludes generated exports. |
| `v0_6_8_filesystem_fidelity_macbook.sh` | superseded historical mounted-view wording | Records the v0.6.8 Mac acceptance workflow and its former mounted storage-view observation. Current fidelity diagnostics use typed catalog and physical-root status. |
| `v0_6_8_filesystem_fidelity_main.sh` | superseded historical physical-root smoke | Records the old Main Documents backing root and production-only acceptance workflow. Current fidelity and Documents tests use configured canonical roots. |

Current defects found in this registry: **none**. Any new unversioned smoke that
constructs these legacy roots or rebuilds a generated export is a current defect
and must be repaired rather than added to this table.

## Maintenance Rules

- Keep historical versioned scripts runnable against an environment built for
  their release when practical, but do not include them in current-layout
  claims.
- Add a new current smoke when a source format changes materially.
- Do not silently add current smokes to old versioned aggregate commands.
- Mark any newly discovered unversioned or current assertion as a current defect
  and replace or repair it in the same bounded change when safe.
- Preserve legacy fixtures that exercise supported compatibility behavior.

## Compact cloud fetch failure regression

`v2_cloud_fetch_failure_local.sh` takes `LOOM_TEST_BORG_BINARY` (exact Borg
1.4.3) and `LOOM_TEST_PG_BIN` (PostgreSQL 17 bin directory). It creates one
local repository with about 2 MiB of synthetic input (hard cap 8 MiB / 128
source entries), plus an owned Unix-only PostgreSQL cluster. It tests the
registered restore-drill HTTP route and local client with a native extraction
conflict and a nonzero exit after complete extraction. Both cases require a
mode-0600 durable failure receipt and zero authority connection, `pg_restore`,
CREATE/DROP or target-database mutation after setup. Synthetic package dumps
exercise fetch authentication, not database restoration. CLI rendering has
focused Go regression coverage. The smoke stops its PostgreSQL process and
keeps all original failure and cleanup evidence under its printed
`.loom-acceptance` root inside the worktree. The native-conflict case exercises
selector-only plan, read-only dry-run, digest/yes apply, and unchanged exact
replay. The complete-extraction case adds one fixture-only external alias to a
real internal hardlink pair and requires cleanup refusal before quarantine.
After removing that exact external fixture alias, it reviews a fresh plan,
cleans the complete internal pair, verifies both observed link-count transitions,
and replays without rewriting evidence. No extra Borg commands may occur during
cleanup. It never accepts a production repository or database URL. Socket paths use a
separate private, bounded directory under canonical `/tmp`, so the caller need
not override `TMPDIR`. A custody receipt binds its parent and directory identity;
exit cleanup removes only that verified empty socket directory after PostgreSQL
stops. Original failure evidence, cluster files and logs remain in their fixture roots.
Payload custody uses safe worktree ancestors; the smoke never weakens the
cleanup permission/ACL gate to admit a globally writable temporary ancestor.
