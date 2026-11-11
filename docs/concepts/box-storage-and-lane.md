---
title: "Box Storage And Lane"
description: "Separate working files, retained custody, application data and recovery without introducing another filesystem view."
audience: [user, operator, developer]
tags: [loom, concepts, storage, filesystem]
status: draft
verified_at: "2026-11-11"
source_scope: ["internal/boxregistry", "internal/workspacefiles", "internal/notesworkspace", "internal/storagecatalog", "internal/workspaceactions"]
related: ["[[Canonical Paths]]", "[[Files Storage And Lane]]", "[[Storage Retention And Safe Delete]]", "[[Desktop Workspace]]"]
aliases: ["LOOM Storage Model"]
---

# Box Storage And Lane

The current desktop exposes actual configured filesystem roots. Notes are
ordinary files in those roots, not a second vault that must be reconstructed
from indexing declarations. Additional metadata helps retrieval; it does not
decide whether a folder exists.

## Working Files And Retained Material

**Box** holds current writable work: documents, notes, topics, libraries and
projects. Their exact organization is an operator choice. A project can own
notes and repositories without making every document a separate project.
Additional Boxes can keep independent roots and contextual defaults. Stable
Box identity, not a sidebar label, binds persisted operations.

**Storage** holds retained or archived material. Its ordinary browsing view is
read-only, including supported archive-member access. A project may move into
archive custody through a reviewed lifecycle operation; a directory drag alone
must not leave an active registered project at an untracked path. Restore brings
files back without automatically restarting services or timers.

**Application Data** belongs to the application/storage owner. The workspace
can expose supported allocations and file references without claiming that
every application's database, container or archive is an editable document.

## Availability Is A Separate Axis

Main owns canonical shared files. A client may have:

- a durable local working copy with pending text edits;
- an explicitly selected offline copy or pinned attachment;
- an evictable cache of an on-demand file; or
- metadata for a remote-only original.

These are not separate conceptual files to organize independently. Their
availability, source revision and permission must remain explicit. A network
connection does not make a remote repository a local development filesystem;
run filesystem-heavy work where its data lives or use an ordinary local clone.

Main-first managed moves retain exact source/target identity and replay
receipts. Offline requests remain pending, preserving local work until the
source owner accepts the operation. A changed destination must not be overwritten
or disguised as a successful sync. See [[Desktop Workspace]].

## Indexes And Contracts

Search indexes, embeddings, link graphs and metadata catalogs are derived views.
File browsing does not require them. Indexing and heavy enrichment are explicit
policy choices, not a consequence of opening a folder. Missing or stale metadata
is not proof that an original was deleted.

Project declarations still describe resources that need system support:
applications, jobs, selected knowledge processing and recovery requirements.
They are not a second placement map that every ordinary file must pass through.
Project-local `.loom/` and `.project/` retain their resource/development roles;
runtime queue state belongs outside user document folders.

## Imports And Legacy Intake

The desktop's supported upload/import action transfers selected originals to
an actual Box destination with bounded staging, checksums and resume evidence.
It preserves the Mac original and refuses an occupied destination rather than
silently replacing it. Acceptance is distinct from indexing and cloud coverage.

Lane is retained for explicitly configured legacy transfer workflows and their
historical evidence. It is not a required visible folder in the current desktop
Box. Dropzone is retired; neither a rebuild nor documentation example should
recreate it as a default workspace. Existing custody and transfer histories are
not disposable merely because their old intake UI is no longer primary.

## Sync Is Not Backup

The storage catalog records identity, lineage, checksums and physical references.
Recovery packages and cloud archives retain selected state independently of the
working copy. Successful sync can propagate an unwanted edit; a catalog row or
second pathname is not necessarily another protected copy.

Inspect the exact capture and cloud inclusion before making recovery claims.
Source cleanup and retention have separate reviewed plans. Preserve unknown
history and user payloads; do not infer deletion authority from newer backup
timestamps. See [[Storage Retention And Safe Delete]].
