---
title: "Notes And Search"
description: "Find and cite Notes, edit enrolled source files through the workspace, and distinguish sync from indexing and recovery."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - notes
status: draft
verified_at: "2026-09-29"
source_scope:
  - "go run ./cmd/loom --json notes overview"
  - "go run ./cmd/loom --json notes roots list --all"
  - "go run ./cmd/loom --json notes roots reconcile --dry-run"
  - "go run ./cmd/loom --json notes objects list --limit 5 --all"
  - "go run ./cmd/loom --json notes objects reconcile --dry-run"
  - "go run ./cmd/loom --json notes search loom --limit 5"
  - "go run ./cmd/loom --json notes projection status"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes"
related:
  - "[[Daily Use]]"
  - "[[Notes Knowledge Index]]"
  - "[[Object Store Indexes And Search]]"
  - "[[Notes Search And Indexes CLI Reference]]"
  - "[[LOOM Notes Portal]]"
  - "[[File Type Support]]"
aliases:
  - "LOOM Notes"
  - "Notes Workflow"
---
# Notes And Search

## What This Page Covers

This page explains the daily LOOM Notes workflow:

1. enroll source folders through project declarations;
2. edit selected collections through the writable workspace;
3. search indexed content and retrieve exact citations;
4. distinguish local saves, source acknowledgement and search freshness;
5. inspect processing or recovery issues without resetting durable state.

For the implementation model, read [[Notes Knowledge Index]]. For exact command
syntax, read [[Notes Search And Indexes CLI Reference]].

## Mental Model

LOOM Notes does not make one giant writable folder the source of truth.

The source of truth stays in admitted source folders, including configured Box
Topics and Library roots and project folders explicitly declared as knowledge.
A folder named `notes/` is not automatically enrolled just because of its name.
Older registered Notes roots remain supported.

A managed application's files can live outside its project folder. To read those
files, the project names the existing allocation instead of its physical path:

```yaml
resources:
  papers:
    kind: knowledge
    knowledge:
      application_data:
        application: webdav
        data: library
        subpath: papers
      category: research
```

`webdav` names an application declared in the same project; `library` names its
managed data key. That allocation must already exist. The optional `subpath`
selects a folder inside it; omitting it selects the whole allocation. Use this
instead of `path`, not alongside it. The ordinary project plan/apply workflow
enrolls the source and reports missing allocations as prerequisites.

The application remains the writer and owns storage and backup policy. Notes
only reads, checks current allocation ownership, and uses the same extraction,
refresh and search pipeline as project folders. This is not a synchronization
client. Cross-project references, arbitrary host paths and symlink substitutions
are not sources. Removing the declaration or archiving the project stops intake
and current search/read access without deleting retained versions. External data
does not move with a project-folder archive and receives no implied archive
custody. Re-enrollment requires current owner evidence again.

On managed Linux hosts, the operator enables
`loom.projectApplications.provisioning.notesSources` to let the node agent read
private application files without changing their modes or ACLs. This grants
daemon-wide `CAP_DAC_READ_SEARCH`, not a path-scoped kernel permission or write
bypass. It does not enroll the allocation pool: each source still requires its
own declaration and current owner evidence. The setting is off by default.

LOOM reconciles those roots into knowledge objects, extracts text where
supported, chunks that text, writes lexical search documents, optionally writes
embeddings, and builds a read-only `loom-notes` projection.

The legacy generated projection is for reading and browsing, not write-back.
The writable workspace is a separate source adapter described below; neither
one makes the search database the owner of your original files.

## Enrich Selected Files Later

An admitted source can keep automatic OCR, image descriptions and embeddings
disabled without losing the option to request them for selected file versions.
For example, receive a library cheaply, then enrich only a notebook you need.
The original file and the source's automatic settings are not changed.

```sh
loom --json notes enrich <knowledge-object-id> --ocr
loom --json notes enrich <knowledge-object-id> --ocr --yes
loom --json notes enrich <knowledge-object-id> --embeddings --yes
loom --json notes pipelines inspect <knowledge-object-id>
```

Without `--yes`, the command only previews. Choose `--ocr`, `--vision` and
`--embeddings` independently or together. Vision describes images only; PDF OCR
uses native-text-first sparse-page selection, not a new handwriting model.
Embeddings alone reuse compatible extracted text and chunks. Host-disabled
engines, unavailable models and existing file/page limits still apply, including
the 1 GiB preparation limit. Enrichment does not admit unknown files.

For an exact source path, add `--file`; use `--node` if the path is ambiguous.
For a folder, save a bounded selection before applying it:

```sh
loom --json notes enrich /admitted/folder --folder --recursive --embeddings > preview.json
loom notes enrich --preview-file preview.json --yes
```

The preview contains at most100 already admitted entries, not a complete disk
inventory. Without `--recursive`, it includes only immediate children. A truncated
preview supplies `next_after`; use `--after` to preview another page. Applying a
saved preview never adds later arrivals or substitutes changed file versions.
Single-file callers can also supply `--source-revision` and `--source-hash`.

A successful request means queued, not finished. Inspect its pipeline and exact
search citation after completion. Partial folder failures are reported per item.
Identical requests return existing work; use pipeline retry for failed work.
Ordinary forced reprocessing instead restores automatic defaults. A newly
uploaded revision never inherits this one-time enrichment request.

Adding OCR later does not silently recompute source-disabled embeddings. Earlier
semantic results can remain available through semantic search while lexical
search uses newly extracted text; their exact citations and semantic lag remain
distinct. Request embeddings again for the new derived text when wanted.

## Edit Through The Notes Workspace

An explicitly enrolled workspace presents selected personal and project
collections in one client vault, organized by content rather than host disks.
For example, `Notes/Personal/overview.md` and
`Projects/Research/Notes/overview.md` can map to different canonical source files.
The binding, not their identical basename, determines the destination.

The adopted workspace is the live LOOM NOTES vault, not a pilot. Its configured
source collections determine visibility; there is no eight-collection limit.
Adding a knowledge declaration enables indexing, while adding its workspace
mapping enables client delivery. These are currently separate enrollment steps.
Legacy opaque identifiers can still contain `pilot` without changing access or
selecting another database. Preserve them when renaming a vault or cleaning up
test content: source bindings and pending edit receipts refer to those identities.

Use the LOOM-compatible pinned LiveSync client for this workspace. It carries
stable file identity and exact edit/rename/delete bases in addition to native
replication. A stock plugin connected to the same database does not understand
this contract. Do not enable upstream auto-updates without the reviewed overlay.

Ordinary local saves remain local and responsive, including offline. The client
retains pending intent; Main applies an edit only against its recorded base.
Concurrent edits preserve a conflict instead of choosing a winner silently.
Create, rename and recoverable deletion use the same source mapping. No automatic
Git commit is made. PDF and image attachments currently use read-only reference
bindings up to64 MiB; derived OCR/vision text is not an editable original.
Writable text/control payloads remain limited to1 MiB.

Source enrollment, installed client settings and Main's workspace worker are
separate. Inspect `loom worker inspect main.notes_workspace_sync` for runtime
status; a healthy manual run does not mean periodic sync is enabled. The current
enrollment and policy determine which folders synchronize automatically. Main
can run its worker on a short interval while the client uses native continuous
replication. Offline edits remain queued until both endpoints return. Do not
enable a second sync owner on an existing vault as part of setup.

### Save, Sync And Search Are Different

- **Local save/search:** the device can read its own files without Main.
- **Transport publication:** data entered native sync, but Main may not have
  applied the operation yet.
- **Source acknowledgement:** the exact operation was applied to its canonical
  source, or held with a specific conflict/recovery reason.
- **Lexical/semantic publication:** the selected file revision reached the
  corresponding index. These may advance at different times.

Do not reset a client database or discard pending operations to clear a hold.
Recovering only the vault files is not enough to recover unacknowledged intent.
Source journals, SQL bindings, native receipts, client intent and protected
settings also matter. Full workspace recovery and additional device acceptance
remain explicit adoption gates; search success does not prove backup coverage.
The client also exports alternating complete intent checkpoints beside its
plugin settings. These contain private pending edits, not ordinary Notes or
files to commit to Git. They preserve recoverable intent but do not replace a
cold native client database or authorize cloning a device identity. See
[[Backup Restore And Drills]] for the protected recurring capture boundary.

## Declare A Changing Source Folder

A project can choose how a folder's documents are processed in its existing
`.loom/project.yaml` declaration. For example:

```yaml
resources:
  notebooks:
    kind: knowledge
    knowledge:
      path: notes/notebooks
      category: notes
      refresh:
        quiet_for: 10m
        max_wait: 30m
      processing:
        ocr: auto
        embeddings: true
        image_descriptions: false
```

Use the ordinary project plan/apply workflow to enroll or change the source.
This does not install a notebook app or synchronize documents from it: the
application or sync tool delivers files, then LOOM processes admitted revisions.
Notes does not automatically accept their contents into Provenance.

An explicit `refresh` block delays the start of the whole pipeline until the
file has stopped changing for `quiet_for`, or the first pending change reaches
`max_wait`, whichever comes first. Repeated discovery of identical content does
not restart the timer. The wait is paid once, before extraction, not again before
OCR and embeddings. An empty block defaults to 10 minutes quiet / 30 minutes
maximum. Durations use whole seconds up to 24 hours; maximum wait must be positive
and at least the quiet interval. `quiet_for: 0s` makes a revision immediately
eligible. Capacity and execution time are additional waits, not part of this
deadline.

Omitting `refresh` preserves the existing prompt native/lexical processing and
Main's heavy-stage quiet window. Omitting `processing` inherits Main's settings.
`ocr: auto` uses the existing native-text-first PDF processing and optional image
OCR; `off` disables OCR for the source. `embeddings` and `image_descriptions`
accept booleans. A source can opt out but cannot enable a stage disabled by Main.
Models, token limits, file/page limits and worker capacity remain host settings.

`loom notes pipelines inspect <object-or-pipeline-ref>` shows the requested and
effective policy, wait deadline and stage skip reasons. JSON inspection retains
the same policy in the run's `plan_snapshot`. Disabled stages are not successful
extractions. Changing the declaration still requires a fresh reviewed apply.

Once processing selects a revision, it finishes against those captured bytes.
Further edits coalesce into the newest pending revision instead of repeatedly
cancelling extraction. The pending deadline starts when those changes arrive,
not when the previous run finishes. A maximum wait bounds debouncing, not model
runtime or queue capacity. Withdrawal, deletion and source-policy changes still
take precedence over finishing old work.

Search keeps the last published content available during a refresh or failure.
Every result identifies its indexed and latest observed revision and hash,
publication time, refresh state and whether it is current. Lexical results can
advance before embeddings; hybrid search never combines different revisions of
the same document. Semantic search may still return the older, explicitly
labelled semantic publication until the replacement vector set is complete.

Use `loom notes search "query" --require-current` when lagging content is not
acceptable. It excludes older publications and reports bounded omitted-match
counts; an empty result does not prove that no relevant document exists. Exact
passage citations retain their original revision and remain readable subject to
current access. The read-only Notes projection represents source material,
not a promise that its latest bytes have already reached every search index.

Pipeline inspection's `refresh` object separates latest source, selected work,
pending changes and lexical/semantic publications. Failed extraction does not
erase the previous publication. A successfully processed empty replacement does
retire its previous searchable body. Compatible unchanged chunks can reuse
vectors; changed-page-only PDF OCR caching is not implemented.

## Declare A Changing Source Folder

A project can choose how a folder's documents are processed in its existing
`.loom/project.yaml` declaration. For example:

```yaml
resources:
  notebooks:
    kind: knowledge
    knowledge:
      path: notes/notebooks
      category: notes
      refresh:
        quiet_for: 10m
        max_wait: 30m
      processing:
        ocr: auto
        embeddings: true
        image_descriptions: false
```

Use the ordinary project plan/apply workflow to enroll or change the source.
This does not install a notebook app or synchronize documents from it: the
application or sync tool delivers files, then LOOM processes admitted revisions.
Notes does not automatically accept their contents into Provenance.

An explicit `refresh` block delays the start of the whole pipeline until the
file has stopped changing for `quiet_for`, or the first pending change reaches
`max_wait`, whichever comes first. Repeated discovery of identical content does
not restart the timer. The wait is paid once, before extraction, not again before
OCR and embeddings. An empty block defaults to 10 minutes quiet / 30 minutes
maximum. Durations use whole seconds up to 24 hours; maximum wait must be positive
and at least the quiet interval. `quiet_for: 0s` makes a revision immediately
eligible. Capacity and execution time are additional waits, not part of this
deadline.

Omitting `refresh` preserves the existing prompt native/lexical processing and
Main's heavy-stage quiet window. Omitting `processing` inherits Main's settings.
`ocr: auto` uses the existing native-text-first PDF processing and optional image
OCR; `off` disables OCR for the source. `embeddings` and `image_descriptions`
accept booleans. A source can opt out but cannot enable a stage disabled by Main.
Models, token limits, file/page limits and worker capacity remain host settings.

`loom notes pipelines inspect <object-or-pipeline-ref>` shows the requested and
effective policy, wait deadline and stage skip reasons. JSON inspection retains
the same policy in the run's `plan_snapshot`. Disabled stages are not successful
extractions. Changing the declaration still requires a fresh reviewed apply.

Once processing selects a revision, it finishes against those captured bytes.
Further edits coalesce into the newest pending revision instead of repeatedly
cancelling extraction. The pending deadline starts when those changes arrive,
not when the previous run finishes. A maximum wait bounds debouncing, not model
runtime or queue capacity. Withdrawal, deletion and source-policy changes still
take precedence over finishing old work.

Search keeps the last published content available during a refresh or failure.
Every result identifies its indexed and latest observed revision and hash,
publication time, refresh state and whether it is current. Lexical results can
advance before embeddings; hybrid search never combines different revisions of
the same document. Semantic search may still return the older, explicitly
labelled semantic publication until the replacement vector set is complete.

Use `loom notes search "query" --require-current` when lagging content is not
acceptable. It excludes older publications and reports bounded omitted-match
counts; an empty result does not prove that no relevant document exists. Exact
passage citations retain their original revision and remain readable subject to
current access. The read-only Notes projection represents source material,
not a promise that its latest bytes have already reached every search index.

Pipeline inspection's `refresh` object separates latest source, selected work,
pending changes and lexical/semantic publications. Failed extraction does not
erase the previous publication. A successfully processed empty replacement does
retire its previous searchable body. Compatible unchanged chunks can reuse
vectors; changed-page-only PDF OCR caching is not implemented.

## Open The Portal

Use:

```bash
loom enter --start notes
```

The LOOM Notes surface puts the most user-relevant information first:

- notes search action;
- embedding status;
- search results;
- attention summary;
- coverage totals;
- file type counts;
- notes roots grouped by node and project;
- processing and projection details.

For a one-shot render:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start notes
```

Counts and configured projection paths come from the live system. Do not treat
an older screenshot or example path as the current source inventory.

## Check The Overview

Use the overview before running more specific commands:

```bash
loom notes overview
```

JSON output is useful for scripts:

```bash
loom --json notes overview
```

The overview tells you:

- how many notes roots are active;
- how many files and directories are represented;
- total indexed size;
- file classes such as `markdown`, `pdf`, `image`, `text`, and
  `office_document`;
- processing states such as `metadata_only` and `chunked`;
- extraction states such as `extracted`, `too_large`, and
  `source_unavailable`;
- search document count;
- projection state.

## Inspect Notes Roots

List roots:

```bash
loom notes roots list
```

Include inactive roots when troubleshooting:

```bash
loom notes roots list --all
```

Filter by node:

```bash
loom notes roots list --node main
```

Root kinds currently include:

- `box_notes`, `box_topics`, and `box_library`;
- `project_material` for declared project/application sources;
- `project_notes` for supported legacy registrations.

The portal groups the same information under "Notes Across Network".

## Reconcile Roots Safely

Normal background admission reconciles registered sources automatically. Manual
reconciliation is an operator diagnostic/repair, not a step required for every
save or agent search. It makes known roots match current configuration.

Preview first:

```bash
loom notes roots reconcile --dry-run
```

Apply only after the dry-run looks right:

```bash
loom notes roots reconcile --apply --yes
```

The Mac CLI seeds local Box Notes candidates during reconcile. On main, the
backend owns the authoritative root records.

## Reconcile Objects Safely

Object reconciliation turns catalogued files under notes roots into knowledge
objects.

Preview:

```bash
loom notes objects reconcile --dry-run
```

Limit to one source root when you are investigating:

```bash
loom notes objects reconcile --dry-run --root <notes-source-root-id>
```

Apply mode writes knowledge-object rows, so use it as an operator action:

```bash
loom notes objects reconcile --apply --yes
```

After reconciliation, inspect the first few objects:

```bash
loom notes objects list --limit 20
```

Show one object:

```bash
loom notes objects show <knowledge-object-ref>
```

## Search Notes

Search all indexed notes:

```bash
loom notes search "threat model"
```

Useful filters:

```bash
loom notes search "runbook" --node main
loom notes search "timeline" --project osint-tools
loom notes search "source" --file-class markdown
loom notes search "incident" --path reports
loom notes search "lead" --tag osint
loom notes search "incident" --after 2026-01-01 --sort newest
```

The portal search box also understands scoped tokens:

```text
project:osint-tools node:main tag:osint path:reports class:markdown after:2026-01-01 sort:newest threat intel
```

Use `--after` and `--before` with RFC3339 timestamps that include a timezone, or
with date-only `YYYY-MM-DD` values. Date-only boundaries mean midnight UTC.
`after` includes the boundary and `before` excludes it. Use `--sort relevance`,
`--sort newest`, or `--sort oldest`; the equivalent query token is
`sort:<order>`.

Normal CLI and Portal rows show one selected source date. JSON output and Portal
inspection explain whether it came from origin filesystem mtime, source-object
metadata, embedded/frontmatter metadata, or observation fallback. Index time is
shown separately because reindexing is operational activity, not evidence that
a file was recently modified.

Recency adjusts the order only after relevance has selected candidates. It can
break a relevance tie in favor of newer material, but it does not pull an
unrelated new file into the results.

Search defaults to requested hybrid mode. Inspect the returned mode and any
fallback reason rather than assuming embeddings are enabled on every host.

When Main is offline, backend search is unavailable. Obsidian's local search
can still search files already present on that device; it is not remote LOOM
search or proof that all sources have synchronized.

### Open The Exact Workspace File

Search returns a complete `passage_followup` tuple. Use its object, version,
chunk, source hash and lifecycle together with `loom notes passage get` for
exact text, or `loom notes passage locate` for workspace navigation:

```sh
loom --json notes passage locate <chunk-id> --object <object-id> --version <version-id> --source-hash sha256:<digest> --source-lifecycle active
```

Only `available` provides a verified current binding. Its workspace-relative
path is not the physical Main source path. Archived, stale, unbound and unavailable
results explain why no current target is provided. A server-side binding is not
proof that a particular device has received the file; preserve the separate
sync/device status. Never replace an old citation's hash with the current hash.

## Embeddings Status

Check embeddings before expecting semantic search:

```bash
loom notes embeddings status
```

Runtime/model, dimensions, active vectors and effective policy are live settings.
The current Main pilot uses local Ollama embeddings; other installs can disable
them. Compatible completed vectors survive bounded processing turns and retries.
Semantic publication switches only once the selected version is complete.

Enabling or disabling embeddings is a sensitive global action:

```bash
loom notes embeddings enable --yes
loom notes embeddings disable --yes
```

The portal exposes the same action behind a confirmation prompt. Do not enable
embeddings just to test docs unless that is part of an accepted operator plan.

## Projection

The projection is a read-only generated view:

```bash
loom notes projection status
```

Dry-run a rebuild:

```bash
loom notes projection rebuild --dry-run --max-results 20
```

Apply mode rebuilds the generated projection:

```bash
loom notes projection rebuild --apply --yes
```

The projection helps users browse notes from different nodes and projects in
one place. It should not be treated as the canonical writable source.

## File Types

LOOM accepts all catalogued file classes in notes roots, but extraction depth
depends on file class and extractor support.

Examples:

- Markdown and text can be extracted and chunked.
- Code and structured data have source-aware extraction when queued.
- PDFs use native text/page analysis followed by selective OCR when enabled.
- DOCX has body extraction.
- Images can use OCR and separately enabled local vision descriptions; identify
  machine-generated descriptions as such rather than treating them as source text.
- `.gdoc`, legacy Office formats, media, archives, packages, binaries,
  and unknown files are metadata-oriented unless a later extractor adds body
  support.

See [[File Type Support]] for the detailed table.

## When Something Looks Wrong

Start with:

```bash
loom notes overview
loom notes roots list --all
loom notes objects list --limit 20 --all
loom notes projection status
loom notes embeddings status
```

Then use Doctor:

```bash
loom enter --start doctor
```

Common interpretations:

- `metadata_only` is normal for directories, unsupported body formats, large
  files, and files tracked only for metadata.
- `too_large` means LOOM retained metadata but did not extract body text because
  a size or page limit was exceeded.
- `password_required` means a PDF appears encrypted or password-protected.
- `no_embedded_text` means native extraction found no text; inspect the selected
  OCR policy/stage before concluding that processing is finished.
- `source_unavailable` means LOOM could not read the original source path.

Latency includes file-stability scanning, bounded admission sweeps, any declared
quiet window and extraction/model work. An idle60-second policy is not a
per-file completion guarantee. The coordinator drains unfinished admission pages
with bounded continuations, then returns to its configured idle cadence. Never
mistake a configured debounce deadline for a bound on end-to-end indexing time.

## Related Docs

- [[Notes Knowledge Index]]
- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[LOOM Notes Portal]]
- [[Notes Index Maintenance]]
- [[File Type Support]]
