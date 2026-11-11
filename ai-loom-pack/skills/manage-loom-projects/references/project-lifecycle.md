# Project Lifecycle And Custody

Load the applicable section for classification, restore or export; ordinary
source work needs none of these operations. [Project commands](project-commands.md)
owns the ordinary create/plan/apply/status route.

## Classification-only metadata

Preserve identity/schema and runtime `project.status`; set optional
`project.work_state: working` or `passive` in `.loom/project.yaml`. Omission means
working for view selection, not a new persisted default. Passive is not pause,
completion or physical archive. Services, schedules and intake keep running.

For an authorized change to this field only:

```sh
loom project diff <ref> --backend --json
loom project register <ref> --backend --json
loom project context <ref> --node <owner-node> --json
```

`--backend` targets the configured backend, not an arbitrary owner. Registration
projects the explicit string into project metadata `work_state`; removing the
field removes that key. It does not rewrite Notes watched-root metadata/config
or source ownership generation, or deactivate runtime. Contract/repository
registration evidence still changes normally. A legacy owner-transition refusal
is a boundary to resolve, not permission for SQL or watch-owner bypasses.

Full `project apply` still reconciles declared resources; do not use it merely to
classify a project. Live status/context shows source classification separately
from readiness. Notes enrollment reads the registered metadata; desired/current
client roots and actual delivery are separate. `_Passive` is editable live work;
`_Archived` requires verified archive custody and is read-only regardless of work
state. Rollout switch/client compatibility still apply; do not hand-move folders.

Work state is not `.project` development posture or a processing-policy change.
Do not refresh/reapply watches or run OCR/embeddings just to change grouping.
Archived references include Markdown/text: retain drafts on refusal and use an
explicit copy/restore, never implicit source writes or runtime restart. Check
recovery and archive/cloud evidence independently; metadata does not prove device
delivery or iPhone adoption. For source processing and reference recovery details,
read [Notes pipeline](notes-pipeline.md).

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
