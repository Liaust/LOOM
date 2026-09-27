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

Defaults: 1 GiB/file, 250 PDF pages, minute polling; smaller limits remain.
Apply to update existing roots. Uploads stream above 32 MiB; views converge incrementally.

Optional knowledge fields:

```yaml
refresh: {quiet_for: 10m, max_wait: 30m}
processing: {ocr: auto, embeddings: true, image_descriptions: false}
```

Refresh waits once before processing, capped by max_wait; unchanged polling
does not reset it. Whole seconds only: quiet 0s..24h; maximum positive, >=quiet,
<=24h. Omission retains prompt native work / ten-minute heavy quiet time.
OCR: auto/off. Processing omission inherits Main; false opts out, true cannot
override host disablement. Models/sync remain host/application-owned.

Use `loom notes pipelines inspect <ref>` for policy/deadline/skip reasons;
selected revisions finish; newer edits coalesce. Search labels last-published
content during refresh. Use `--require-current` to exclude lag; preserve exact citations.

Saving source does not enroll it. Plan against the selected owner node:

```sh
loom project plan <ref-or-path> --node <node> --json
loom project apply <same-ref-or-path> --node <same-node> --plan-id <reviewed-plan-id> --idempotency-key <stable-key>
loom project status <ref> --node <node> --json
loom project operation <operation-id> --json
```

Supply required `--approval` references. Inspection does not resume operations.
Apply `--resume <operation-id>`, preserving original selectors,
effects, plan, key and approval references. Resolve stale plans/prerequisites first.

The parsed `--refresh-projections` flag is rejected, unlike automatic `.project` refresh
and `loom provenance project sync`. Do not work around absent composition with
manual owner calls. Activation has no general dry-run; runtime/archive/migration
retain separate authorization.

## Managed applications

Prepared application installation uses the same plan/apply/status route within
node policy. See [managed applications](managed-applications.md) for descriptor,
data, credentials and public HTTPS beneath `apps.liaust.com`; no per-app host setup.

[Schedules](project-schedules.md) use the same plan/apply route.

## ORCA ordinary folders

```sh
orca repo add --path <existing-folder> --kind folder --json
```

Registration neither creates missing directories nor validates existence.
Create the directory first. Native polling may convert its kind if Git appears.
Do not manufacture a Git wrapper.

## Export custody

`loom project export <path> --mode <human|portable|archival> --out <archive.tar>`;
`<ref> --backend` downloads locally. Review overwrites. Human omits controls,
managed AGENTS and `.loom-acceptance`; portable keeps contracts; archival adds
non-secret registration references. `.loomignore`: nested, last-match-wins,
no implicit `.gitignore`. Keep Git unless excluded; never re-include `.loom/state/`
or `.loom/tmp/`; reject escaping paths/symlinks. Edit source, not generated views.
