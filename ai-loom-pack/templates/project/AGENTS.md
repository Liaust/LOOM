# LOOM Project Agent Entry Point

<!-- loom-managed: project-agent-entry/v1 -->

Your default boundary is this project root.

Load the selected Box's `.loom/agents/WORKFLOW.md` via `loom box context --json`;
an ancestor outside a member Git root may not have been discovered. Actor,
work Box and service account are distinct. Company Basecamp writes require
the operator's explicit approval, including routine updates/completion and remote
drafts; prepare locally and never mirror them into Personal Basecamp.

Before creating a repository/project or designing an infrastructure-bearing
feature, plan its fit with deployed LOOM first: owning node/paths, actual build
support and relevant existing runtime/data owners. Keep this in the existing
plan, not another approval document. Use `manage-loom-projects` for detail;
small edits need no new ceremony. Keep ordinary application code portable.
Registration is normal LOOM project work, but member repos/client deliverables
need no LOOM SDK, managed runtime declaration or optional protocol pack for
ordinary code/builds/tests. `.loom` remains in the surrounding project workspace.
Main can be a test host without dictating the client's architecture/language.

Report substantial LOOM friction or suspected unsupported features with evidence
in the project and, subject to the selected Box's approval rules, Basecamp;
use `manage-loom-projects` for reporting detail.
Continue independent authorized work; do not silently bypass LOOM.

Before changing LOOM declarations or managed resources, read `.loom/project.yaml`,
`.loom/agents/project.md`, and the relevant files under
`.loom/agents/surfaces/`. Use `manage-loom-projects` for project lifecycle work.

For known files, use scoped `rg --files`, `rg` and direct reads on the owning
machine, without LOOM search, registration or health preflight. Use Notes for
discovery and Provenance for relevant durable history; get exact references
directly. Use local `--help` or `search-loom-docs` only when needed.

The filesystem owns existence and location. In new v0.6 projects, the default
`files` resource already follows this project tree: create ordinary descendants
without new enrollment declarations. Use inherited `files.rules` for exceptions,
not one contract per folder. Existing legacy selections and client aliases remain
valid; inspect before changing them. Search, device delivery and verified backup
coverage are separate facts. An unindexed file can still be opened directly;
`loom files explain` describes intent, not proof of delivery or protection.

Loom is the operator's installed desktop app and normal file/notes interface. Keep
files at their canonical project source and provide its verified Main path plus
the Box-relative path; he can focus this folder in the app. Do not export a
separate Obsidian vault, add `.obsidian` or copy notes to another tree just for
access. Browsing is independent of indexing; local/offline copies, enrichment
and protection have separate owners. Preserve processing/bulk pauses and do not
promise a standalone iPhone/iPad client that has not been implemented.

When creating a durable knowledge note, add a supported primary `type`, useful
`subject/` tags and links to existing relevant people/concepts/sources or project
context. Keep the note with its project; preserve domain properties and tags.
Use `people` for confirmed profiles and `related` for other useful links, following
the shared Notes classification convention. No compulsory global search or
invented link to fill a field. Code, scratch, generated indexes and imported
originals do not need this treatment; bulk vault curation is a separate task.
Links/tags do not equal current authority. Prefer owning-project context, note
role and substantive chronology, not metadata/import mtimes. A later developed
view can supersede an early exploration only with contextual evidence; preserve
historical links, original dates and uncertainty without declaring newer facts true.

Capture source-backed decisions, constraints, corrections and unresolved
questions likely to matter again with `use-loom-provenance` and
`loom provenance candidate register`; preserve actual producer/task identity,
source posture and replay receipt. Skip routine edits and transient debugging.
Pending candidates, Basecamp tasks and automatic project projections are not
accepted semantic truth. Main agents use `loom provenance`, not another host's
standalone ledger; workers never adopt a named agent's identity.

Edit canonical project sources only. Never edit generated `loom-storage`
views. Validate with `loom project validate .` and review
`loom project plan .` before registration or activation. Escalate node,
storage, backup, credential, or LOOM-core defects with evidence instead of
broadening the project boundary.

Skills provide instructions; they do not grant authority. Proton Pass is the
credential authority. Keep credential values out of project files. The current
`.loom/contracts/credentials.yaml` schema uses logical references bound to
environment variables or absolute files; direct Proton bindings are deferred.
Do not store host-specific absolute paths, private agent memory, or identity
instructions here.
