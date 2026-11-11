---
title: "Desktop Workspace"
description: "Navigate ordinary files, edit local working notes and inspect Main-owned work."
audience: [user, operator, developer]
tags: [loom, workspace, files, desktop, offline]
status: draft
verified_at: "2026-11-11"
source_scope: ["modules/workspace-electron", "internal/workspacefiles", "internal/notesworkspace", "internal/boxregistry"]
related: ["[[Installation]]", "[[Box Storage And Lane]]", "[[Projects]]", "[[Notes And Search]]", "[[External Agents And AI LOOM Pack]]", "[[Main And Mac Operations]]"]
---

# Desktop Workspace

The desktop is an interface to existing LOOM files and services. It does not
create another vault or require every visible file to be indexed. The current
reference client is Electron on macOS, connected privately to Main. Start with
[[Installation]] if those services are not configured.

## Choose The Source

The explorer shows the selected Box, Storage and supported application-data
locations. A Box contains writable work. Storage is retained/archive custody,
read-only in the ordinary browser. Application Data stays with its application
owner; an archive member is not an independent writable original. A separate
Mac root represents local Mac files, not another name for Main's Box.

Multiple Boxes keep work contexts and selected operations distinct. They are
not separate Unix users or a security sandbox. Changing Boxes does not change
an existing conversation's identity or grant account access. Folder expansion
reads a shallow page rather than walking an entire project. Focus makes one
real folder the explorer root without moving, registering or downloading it.

## Open And Edit

Writable text opens ready for editing. Markdown retains live formatting; its
source, properties and wikilinks remain ordinary text. Source/configuration
files use syntax highlighting and line numbers. The title renames the file
only when explicitly committed, not on every keystroke.

New names without an extension default to Markdown; explicit supported
extensions are preserved. Renaming an extension is not binary conversion.
Property edits preserve unrelated source where supported; ambiguous conversions
are refused rather than flattening metadata. Current writable text/structured
files are bounded to 512 KiB. Larger/unsupported originals remain read-only.
PDF/image previews use bounded downloads; external images are not fetched
automatically as tracking pixels.

## Local Work And Main Acceptance

An opened working note has an exact source base and an application-owned local
copy. Saving persists content and an immutable pending operation before remote
publication. Drafts, tabs and pending work survive ordinary restart. A local
save does not mean Main accepted it or a cloud backup contains it.

Keep editing downloaded text while disconnected. Reconnect replays the original
operation; conflicts preserve both versions. Do not reset the client or change
its source base to make a warning disappear. Inspect Activity and use supported
review/recovery instead.

Main-first rename/move/removal requests remain pending until Main accepts the
exact operation. Local placement follows that receipt. A pending move does not
justify an inferred delete/create; occupied destinations are not overwritten.
Read-only roots do not acquire writes because their file type is editable
elsewhere.

## Decide What Stays Offline

Working text, cached attachments and remote-only originals have different
availability. Explicit download/folder controls preview and retain bounded
progress. Restart leaves unfinished bulk work paused. Pinning prevents normal
cache eviction but is not backup protection.

The working set bounds text to 100,000 records / 256 MiB; an explicit download
operation is bounded to 256 MiB / 100,000 entries. These are operational bounds,
not a server-mirroring promise. Existing drafts and pending edits are not
discarded to meet a budget. Saved directory pages help offline navigation, but
unseen remote folders cannot be invented while disconnected.

## Structured Files And Knowledge

- CSV/TSV has a bounded cell editor, preserved string values and explicit
  delimiter conversion. Sorting/filtering is display-only; formulas do not run.
- JSON Canvas supports cards, connections, movement, pan/zoom and undo while
  retaining unknown fields. It does not execute embedded programs or websites.
- Bases uses YAML and a restricted expression language. Supported table/card/
  grouping/filter/formula views are a subset, not full Obsidian compatibility.
- Categories, People, Reading, tags and backlinks navigate known metadata.
  Pending local notes can overlay it. Missing metadata is unknown, not proof
  that a file is absent or permission to start OCR/embeddings.
- Graphs are bounded link views (up to 400 nodes / 2,000 edges), not measures of
  truth or relevance. A link does not make an older note equally current.
- Timeline shows recorded path/content-operation evidence, not complete
  filesystem snapshots, Git history or an unrestricted restore interface.

Known paths can be opened directly. Search files and Search contents are
different palette modes; indexed matches retain source/coverage limits.
Browseability, extracted text, embeddings and offline bytes are separate facts.
See [[Notes And Search]] for processing and source retrieval.

## Projects And Operations

Projects link actual files, repositories and declared resources. Passive work
can keep running without occupying active-development views. Archive/restore
changes lifecycle and custody, not merely visibility. Moving a registered
project across workspace/archive boundaries needs the supported review rather
than leaving services and registration behind through a manual relocation.

Activity distinguishes queued work, acceptance, failures and recovery holds.
Schedules retain owner/profile/Box. Hermes owns native agent schedules; LOOM
owns system/capability work. System views expose health and protection evidence
without treating one green service as proof of everything.

## Build And Distribution

See the [client source and commands](../../modules/workspace-electron/README.md).
Public source contains no ready SSH identity, signed installer or update feed.
Development builds cannot receive native signed updates. Operators must
configure their own signed channel. Standalone mobile delivery and voice/wake
reliability remain separate work.
