# Storage Commands And Safety Gates

## Read-Only Orientation

```bash
loom box path
loom box status
loom box watch-status
loom lane status
loom storage list
loom storage tree
loom storage inspect <view-path-or-entry-id>
loom storage inspect-path <path>
loom storage doctor
loom storage transfers
loom backup status
loom backup coverage
loom backup contracts status
loom backup contracts inspect <contract-key>
loom cloud status
loom cloud doctor
```

Command availability does not prove main is reachable. Preserve connection
errors as evidence and avoid replacing a failed LOOM check with direct writes.

## Plan Before Mutation

```bash
loom lane send --dry-run
loom storage inventory --root <export-root>
loom storage cleanup plan
loom storage fidelity report
loom storage fidelity backfill --dry-run
loom storage safe-delete check <path>
loom backup restore-drill <backup-ref-or-path> --dry-run
```

Lane defaults to automatic transport selection. Inspect requested,
recommended, and selected mode, the reason, and estimated temporary bytes in
status or dry-run output. Use `--bundle` or `--no-bundle` only when that
evidence justifies overriding the recommendation; the flags are mutually
exclusive and do not change `faithful`, `source_only`, or `exact` content
policy.

Lane status also reports retained recovery bytes, successful-grace bytes,
protected evidence, cleanup-quarantine bytes, transport-safety bytes, untracked
bytes, and the next successful-quarantine expiry. Status and dry-run are
read-only: automatic expiry belongs to the local node-agent housekeeping
worker. A successful quarantine has a 30-minute grace period; failed,
interrupted, active, and cleanup-withheld evidence has no automatic age-based
deletion, including after acknowledgement or archival.

Use each subcommand's `--help` to confirm required identifiers and node-local
preconditions. Cloud offload, restore, archive, repair, and cleanup apply paths
need an explicit reviewed plan.

## Protect Folder Lifecycle

Request bounded owner-node evidence before creating desired protection:

```bash
loom backup contracts preflight --node <node> --path <absolute-path>
loom backup contracts create <contract-key> \
  --node <node> \
  --path <absolute-path> \
  --preflight <completed-preflight-id>
```

Review the effective policy and blocking findings before create. Do not treat a
queued result as owner-node application or backup custody. Verify lifecycle:

```bash
loom backup contracts status --node <node>
loom backup contracts inspect <contract-key>
```

Status boundaries:

- `waiting_for_node`: main accepted pending work but the owner is unavailable;
- `ready`: matching preflight evidence completed before desired registration;
- `activating`: current apply/report/config evidence is incomplete;
- `active`: current desired revision and config are applied and reported;
- `protected`: a backup was accepted after the current acknowledgement;
- `attention`: review blocking, failed, mismatched, or missing evidence;
- `disabled`: canonical desired policy remains but its root is inactive.

Use bounded lifecycle controls:

```bash
loom backup contracts recheck <contract-key>
loom backup contracts retry-activation <contract-key> --reason <reason>
loom backup contracts enable <contract-key> --dry-run
loom backup contracts disable <contract-key> --dry-run
loom backup contracts delete <contract-key> --dry-run
loom backup contracts delete <contract-key> --yes
```

Use the non-dry-run enable/disable command only after reviewing intent. Delete
requires explicit confirmation and removes neither source files nor retained
backups. When main is offline, do not mutate. When only the owner is offline,
leave accepted work Waiting For Node instead of adding a transport fallback.

## Verification

```bash
loom storage verify <path>
loom storage verify-path <path>
loom storage retention status
loom storage status <path>
```

Verify the accepted destination and protection state before any source-side
deletion. A generated backup path is not a writable recovery shortcut.
For a Lane bundle, treat its tar and manifest as local retry evidence and
verify the extracted accepted tree and catalog totals on main; do not treat the
bundle artifacts themselves as cataloged user storage.
After a successful completed record is durable, the redundant bundle artifact
or file-tree safety tree should be removed and only the current cleanup
quarantine should remain during its grace period. If protected evidence remains,
do not delete it merely to reduce retained bytes.

## Fixture Hygiene

Put manual storage evidence under an explicit `.loom-acceptance/<scenario>/`
folder in a canonical source root. Do not write test files into production
storage exports or generated backup views.
