# Notes Pipeline Declaration

## Loom App Is The Normal File Interface

the operator uses the installed Loom desktop app to browse/edit actual Main Box
files, read-only Storage/Application Data and Mac Box. Keep project notes at
their canonical source; provide the verified Main path and Box-relative path,
and use folder focus for a project working view. No separate vault export,
`.obsidian` directory, indexing declaration or legacy client placement is needed
merely to open a file. Obsidian instructions below apply only to an explicitly
requested legacy client task, not the default Loom app hierarchy.

Browsing, selected local/offline copies, search/enrichment and verified recovery
are independent. Preserve current processing/bulk pauses. Desktop availability
does not imply an implemented standalone iPhone/iPad app or device delivery.

New projects use `project.contract.v0.6` and one `files` knowledge resource at
`path: .`. The actual filesystem hierarchy is the source: create files and
folders normally; do not add declarations for descendants or reapply after each
edit/move. Existing projects keep their reviewed v0.5/legacy selections and aliases
until an equivalent change is reviewed through project plan/apply/status.
Application-data sources use the same controls; see
[application-data Notes](application-data-notes.md).

Original-file browsing is independent of enrollment: use scoped filesystem tools
on the owning machine or `loom files ls/stat/read` through Main's local socket.
Opening a file does not require indexing, embeddings or client publication.
`loom files explain <Box-relative-path>` explains declared consumer intent; its
device-local and verified-protection qualifications are not completion receipts.

For a compatibility project without a knowledge resource, declare the actual folder
(for example `knowledge: {path: vault, category: notes}`); do not copy it into a
special directory just to enroll it. Apply the declaration and check the intended
source/search/client result, not only the registration receipt.

New v0.6 defaults search ordinary text and permit Markdown/text embeddings;
PDFs/images receive metadata only, with automatic OCR, vision and embeddings off.
Device defaults are Markdown-first plus small supported attachments, Canvas and
Bases, without automatic PDF delivery. Use rare descriptor-relative exceptions:

```yaml
files:
  rules:
    - {path: notes/books, search: metadata, enrichment: []}
    - {path: notes/reviewed, search: text, enrichment: [embeddings]}
    - {path: scratch, search: none, protect: false}
```

Fields inherit independently. A rule cannot enable a disabled consumer or grant
access. `protect: true` is intent under an existing backup owner, not verified
coverage or a request to start a backup. Rebuildable/private exclusions remain
purpose-specific; indexing exclusions must not remove Git history from backup.
Use `loom notes enrich` for a deliberate one-time heavy operation, not new roots.

Limits remain 1 GiB/file and 250 PDF pages where applicable; smaller limits remain.
Watching is event-driven with sparse repair, not a minute scan of every file.
Apply updates existing roots. Uploads stream above 32 MiB; views converge
incrementally. Optional knowledge fields:

```yaml
include: ['**/*.[pP][dD][fF]']
exclude: ['**/partial/**', '**/*.part.*']
refresh: {quiet_for: 10m, max_wait: 30m}
processing: {ocr: auto, embeddings: true, image_descriptions: false}
```

Filters are source-relative watcher globs, including application-data sources.
In v0.5/legacy, omitted/empty filters default to include `**/*`, exclude nothing. Shared protection
roots inherit policy filters; explicit filters must match. Application-data
protection stays on its application declaration.

To sync text that should stay outside research indexing, explicitly separate
workspace selection on that same knowledge resource:

```yaml
include: ['**/*.md']
exclude: ['historical/**']
workspace: {include: ['**/*.md'], exclude: []}
```

Omitting `workspace` preserves inherited indexing/sync selection. With an explicit
selection the shared watcher covers both consumers, while search/enrichment keeps
the original filters. Privacy rules, `.loomignore`, file limits and source custody
still apply. Independent selection cannot alter a shared protection owner's
coverage in v0.5; use a separate protection owner rather than broadening it implicitly.
V0.6 compiles separate index, transport and backup selections over one observation
envelope. A device working-set choice is still separate from Main's searchable
source; retain curated whitelists and Goodnotes exclusions during conversion.

## Legacy Obsidian Client Placement And Corrections

Source path, indexing selection and Obsidian location are different decisions.
New v0.6 collections use `Projects/<current display name>/` plus the unchanged
source-relative hierarchy (`path: .` adds no suffix). Existing aliases are retained.
For v0.5 compatibility collections, only exact `notes`, `docs`/`documentation` and
`research` folders receive the conventional display labels. A nested source such
as `repos/example/research/path-a` keeps that whole suffix; `category: research`
does not shorten it. Existing collections retain their earlier client roots even
after the project is renamed. Adding a collection can therefore split one project
across two visible folder names without duplicating its canonical source.

When adding client-visible material to an existing project, compare the intended
location with its retained collection roots. A bounded read of
`loom worker inspect main.notes_workspace_sync --json` exposes them under
`data.last_run.result_summary_json.enrollment.collections`; filter to the exact
`SourceCollection` identifiers rather than loading the entire worker history.
This reports current enrollment. `loom project plan` also previews each declared
knowledge source's indexing and client selection, current root, desired root and
enroll/layout/unchanged action. Review this before applying; a successful apply
alone does not prove client delivery.

Use optional `workspace.path` to select an exact client root within this project's
existing namespace, such as `Projects/Superdeterminism/Research`. Keep the canonical
source in place. Paths are relative, cannot overlap another collection or claim
another project's namespace, and follow passive/archive classification. Omitting
the path retains the existing location; new collections reuse their project's
unambiguous retained group. For a new source, independently authorized indexing
can proceed without delivering files using
`workspace: {include: ['**/*'], exclude: ['**/*']}`. Empty includes default to all
files, not none. Do not invent a placement field or move canonical research merely
to shorten its Obsidian path.

If already enrolled, preserve source, collection/file identities, sync bases and
pending edits. Changing the project name or filters does not relocate the retained
root. Do not delete/recreate the collection, hand-edit enrollment/DB state, or
rename the generated client folder as a remap. Correct `workspace.path`, inspect
the fresh plan, then apply it using its plan ID and stable request key. The normal
layout owner preserves identities and holds conflicting local edits. The
source-move commands below reconcile actual source moves, not client placement.
Excluding files is a delivery-policy change, not a rollback of prior publication.

Ordinary unmodified Main-side text moves reconcile automatically when one exact
absent predecessor is known. Copies and ambiguous/changed matches are not guessed.
For an already-performed move, use `loom notes moves review <old-vault-path>
<new-vault-path>`, inspect both hashes and content-change flag, then apply those
same paths with `--review <token> --yes`. Destination publication precedes old-path
retirement. Pending/local edits stay held; old requests cannot recreate the source.
Publication is not proof that an offline device has received the move.

Refresh waits once before processing, capped by max_wait; unchanged polling
does not reset it. Whole seconds only: quiet 0s..24h; maximum positive, >=quiet,
<=24h. Omission retains prompt native work / ten-minute heavy quiet time.
OCR: auto/off. Processing omission inherits Main; false opts out, true cannot
override host disablement. Models/sync remain host/application-owned.

Use `loom notes pipelines inspect <ref>` for policy/deadline/skip reasons;
selected revisions finish; newer edits coalesce. Search labels last-published
content during refresh. Use `--require-current` to exclude lag; preserve citations.
Sync delivery, source admission, lexical publication and embedding completion
are separate steps, not proof of each other.

## Classification, references and recovery

For changes to knowledge include/exclude filters, workspace selection or source
paths, review a normal `loom project plan <ref>` and apply that fresh plan.
`--watches-only` is limited to watch timing and work-state convergence; it cannot
change enrollment policy. Its binding/policy refusal is not fixed by repeatedly
registering the project or reusing an older plan. Review the normal plan's full
effects before applying; never silently broaden a previously reviewed request.

`project.work_state: passive` changes Notes grouping, not these processing
settings or runtime status. Omission/working stays in `Projects/<name>`; passive
selects `Projects/_Passive/<name>`. Register the change, then review/apply
`loom project plan <ref> --watches-only` using the returned plan ID. This refreshes
source evidence through normal ACK without applying applications or schedules;
source generation may advance. No full import or enrichment run is required.
Keep canonical files and bindings; compatible moves require the rollout switch.

Verified archived material selects `Projects/_Archived/<name>` regardless of work
state. Original bytes, including Markdown/text, are read-only references under
current archive custody. Existing privacy/exclusions/budgets still apply. A draft
must survive refusal; explicit copy into a live source or reviewed restore is a
separate action. Never restore or restart runtime to make a reference readable.

Enrollment `active` entries retain live source paths. Admitted `reference` entries
carry `archive_operation`, `archive_manifest`, `source_generation` and optional
`reference_files`/`reference_bytes`, not a required live path. Unverified archived
candidates use `reference_held` and can lack identity/generation. Recovery retains
both qualifications but copies no archive payload as another live root.
Live holds or missing admitted-reference identity still fail; unadmitted archive
candidates do not block live capture. Archive payload protection remains with its
existing owner. Neither this metadata nor enrollment proves cloud verification,
client delivery or protection of unexported offline edits. iPhone adoption remains
separately deferred.
