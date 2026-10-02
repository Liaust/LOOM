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

For intake filters, quiet windows and enrichment see
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

## Managed applications

Prepared application installation uses the same plan/apply/status route within
node policy. See [managed applications](managed-applications.md) for descriptor,
data, credentials and public HTTPS beneath `apps.example.com` (operator-selected
domain); no per-app host setup.

[Schedules](project-schedules.md) use the same plan/apply route.

## ORCA ordinary folders

```sh
orca repo add --path <existing-folder> --kind folder --json
```

Registration neither creates missing directories nor validates existence.
Create the directory first. Native polling may convert its kind if Git appears.
Do not manufacture a Git wrapper.

## Restored projects

Physical restore returns the files and registration but deliberately leaves the
project inactive. Inspect it with `loom project archive inspect <project>`, then
explicitly release that completed restore:

```sh
loom project archive reactivate <project> --restore-operation <restore-operation-id> --yes
```

Reactivation makes the project editable again; it does not start watchers,
services or schedules. Review `loom project plan` before `loom project apply`
to resume selected declarations. Deliberately paused or completed schedules stay
stopped. Archive and restore receipts remain historical evidence, not a claim
that runtime is running. The same reactivation command is safe to retry after a
lost response; it must refer to the exact completed restore.

## Export custody

`loom project export <path> --mode <human|portable|archival> --out <archive.tar>`;
`<ref> --backend` downloads locally. Review overwrites. Human omits controls,
managed AGENTS and `.loom-acceptance`; portable keeps contracts; archival adds
non-secret registration references. `.loomignore`: nested, last-match-wins,
no implicit `.gitignore`. Keep Git unless excluded; never re-include `.loom/state/`
or `.loom/tmp/`; reject escaping paths/symlinks. Edit source, not generated views.
