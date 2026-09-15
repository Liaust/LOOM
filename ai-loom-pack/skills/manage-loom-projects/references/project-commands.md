# Project Commands And Gates

Choose the requested effect; these are not prerequisites for ordinary edits.

## Create source

```sh
loom project create <name> --owner-node <node> --directory <parent> --dry-run
loom project create <name> --owner-node <node> --directory <parent>
```

Creates `.loom/project.yaml`, `.project/` Markdown, a short `AGENTS.md` and
ordinary notes/ and repos/ folders. No Git initialization or resource activation.
Caller-local creation reads no remote owner filesystem and stays source-only.
`owner_node` is metadata; `--backend` selects the configured backend filesystem.
`--backend` automatically connects that same project identity/source to LOOM;
`--register` is backend-only compatibility. Dry-run never registers. If connection
fails, `source_created/context_pending` retains the identity and files; retry
the same create command after resolving the reported prerequisite.
Context refresh is automatic; no per-folder enrollment or resource activation.

## Declare and reconcile when requested

Edit the existing `.loom/project.yaml`, preserving schema and project identity;
there is no separate declare command. Example, with an ordinary journal folder:

```yaml
resources:
  reading:
    kind: knowledge
    knowledge: {path: journal, category: notes}
```

Saving source does not enroll it. Review a plan against source visible on the
selected owner node; resolve unmet prerequisites before applying supported effects:

```sh
loom project plan <ref-or-path> --node <node> --json
loom project apply <same-ref-or-path> --node <same-node> --plan-id <reviewed-plan-id> --idempotency-key <stable-key>
loom project status <ref> --node <node> --json
loom project operation <operation-id> --json
```

Supply required existing approvals using repeatable `--approval` references.
Historical operation inspection does not resume it. Resume through apply with
`--resume <operation-id>`, preserving original selectors, effects, plan, key and
approval references. Do not blindly retry stale plans or unmet prerequisites.

The parsed `--refresh-projections` flag is rejected by the real owner resolver.
This is separate from automatic `.project` refresh
and the existing `loom provenance project sync` diagnostic. Do not work around absent composition with manual
owner calls. Declaration reconciliation does not activate every facet;
activation has no general dry-run. Runtime/archive/migration actions retain
their separate authorization and storage review.

## Managed applications

Prepared application installation uses the same plan/apply/status route within
node policy. See [managed applications](managed-applications.md) for descriptor,
data, credentials and public HTTPS beneath `apps.example.com`; no per-app host setup.

## ORCA ordinary folders

```sh
orca repo add --path <existing-folder> --kind folder --json
```

Accepted source-bound Linux proof covers the patched ORCA 1.4.191 package, not
its installation or current health. Registration neither creates missing
directories nor validates existence; unavailable/file paths can also register.
Create an ordinary directory first. Native polling may convert its kind if Git
appears. Preserve default/explicit Git behavior; do not manufacture a Git wrapper.

## Export custody

Use `loom project export <path> --mode <human|portable|archival> --out <archive.tar>`;
`<ref> --backend` downloads backend source to caller-local output. Preserve existing
output unless overwrite is reviewed. Human omits portable controls, `.loom-acceptance`
and LOOM's managed root AGENTS; portable keeps contracts; archival adds bounded
registration references, never credential values. Nested `.loomignore` is
Gitignore-style, last-match-wins, without implicit `.gitignore`. Keep `.git` unless
excluded. Rules cannot re-include `.loom/state/` or `.loom/tmp/`; reject escaping
paths/symlinks. Edit canonical source, never generated views.
