# Project Commands And Gates

## Create source

```sh
loom project create <name> --owner-node <node> --directory <parent> --dry-run
loom project create <name> --owner-node <node> --directory <parent>
```

Creates `.loom/project.yaml`, `.project/`, `AGENTS.md`, notes/ and repos/; no Git or
resource activation. Local creation is source-only; `--backend` automatically
connects source on the configured backend.
`--register` is backend-only compatibility; dry-run never registers.
For `source_created/context_pending`, resolve the prerequisite and retry;
identity/files are preserved.

## Declare and reconcile when requested

Edit `.loom/project.yaml`; preserve identity/schema:

```yaml
resources:
  reading:
    kind: knowledge
    knowledge: {path: journal, category: notes}
```

Use project-relative folders, including application data.
Categories: notes/docs/research. Notes reads eligible files after apply;
ignores/formats still apply. No implied backup or Provenance acceptance.
Allocation sources: [reference](application-data-notes.md).

For client placement, selection, intake filters and enrichment see
[Notes pipeline declaration](notes-pipeline.md); load only when needed.

Saving source does not enroll it. Plan against the selected owner node:

```sh
loom project plan <ref-or-path> --node <node> --json
loom project apply <same-ref-or-path> --node <same-node> --plan-id <reviewed-plan-id> --idempotency-key <stable-key>
loom project status <ref> --node <node> --json
loom project operation <operation-id> --json
```

Supply repeatable `--approval` references when required.
Historical operation inspection does not resume it.
Apply `--resume <operation-id>`, preserving original selectors,
effects, plan, key and approval references. Resolve stale plans/prerequisites first.

The parsed `--refresh-projections` flag is rejected, unlike automatic `.project` refresh
and `loom provenance project sync`. Do not work around absent composition with
manual owner calls. Activation has no general dry-run; runtime/archive/migration
retain separate authorization.

## Classification-only metadata

Passive means editable ongoing service work, not pause or physical archive.
For classification-only registration, read [lifecycle and custody](project-lifecycle.md).
Do not reconcile resources merely to change grouping or hand-move client folders.

## Managed applications

Prepared application installation uses the same plan/apply/status route within
node policy. See [managed applications](managed-applications.md) for descriptor,
data, credentials and public HTTPS beneath `apps.example.com` (operator-selected
domain); no per-app host setup.

[Schedules](project-schedules.md) use the same plan/apply route.

## Coding Client Folders

Open the existing registered project path in the native coding client. Client
sidebar membership is separate from LOOM registration; no ORCA registration or
Git wrapper is required.

## Restored projects

Restore leaves a project inactive. [Inspect and explicitly reactivate](project-lifecycle.md#restored-projects)
the exact completed restore for editing; this never starts runtime resources.

## Export custody

See [export custody](project-lifecycle.md#export-custody) before exporting or
overwriting a destination. Exclude runtime state/secrets and preserve path guards.
