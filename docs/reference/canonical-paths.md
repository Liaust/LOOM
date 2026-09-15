---
title: "Canonical Paths"
description: "Identify editable source, physical custody, generated views, portable contracts and runtime paths in the accepted Main and workspace layout."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - reference
  - storage
  - projects
status: draft
verified_at: "2026-09-10"
source_scope:
  - "AGENTS.md"
  - "internal/filesystemlayout/layout.go"
  - "internal/box/contract.go"
  - "internal/config/config.go"
  - "internal/repostate/schema.go"
  - "nix/modules/loom-orca.nix"
  - "nix/modules/loom-smb.nix"
related:
  - "[[Getting Started]]"
  - "[[Projects]]"
  - "[[Configuration]]"
  - "[[Box Storage And Lane]]"
  - "[[Storage Retention And Safe Delete]]"
  - "[[Main And Mac Operations]]"
  - "[[ORCA Main Operations]]"
  - "[[External Agents And AI LOOM Pack]]"
---
# Canonical Paths

## Choose The Source Before Editing

Use this reference to determine what a path represents and who may change it.
Configured roots and current inspection take precedence over a copied example.
The layout below is grounded in source and dated operator receipts reviewed
on 2026-09-10; this page did not inspect live mounts.

Edit owned Box/project source. Inspect generated views and backup/import/archive
custody through supported surfaces. A mount, retained copy or old path does not
become writable source because it is accessible.

## Main's Accepted Two-SSD Layout

The 2026-09-08 migration/reboot receipt places the complete `/srv/loom` tree
on the new UUID-bound, unencrypted ext4 2 TB SSD. Box and Storage Archive share
one filesystem for the physical lifecycle's same-filesystem move. The original
1 TB SSD still holds the OS, Nix store, PostgreSQL and `/var/lib/loom`.

| Path | Meaning | Normal mutation owner |
|---|---|---|
| `/srv/loom/box` | Active Main Box source | User/approved source services |
| `/srv/loom/storage` | Physical custody parent | LOOM storage/lifecycle services |
| `/srv/loom/agents` | Persistent agent workspaces | Approved workspace/runtime tooling |
| `/srv/loom/releases` | Reviewed release trees | Deployment operator |
| `/srv/loom/current` | Selected release link | Supported deployment workflow |
| `/var/lib/loom` | Internal runtime, indexes' supporting state and recovery data | Owning services/operator |
| PostgreSQL and `/nix/store` | Database authority and immutable packages on the original SSD | Database/package tooling |

The old source beneath the new mount was retained and became stale after new
writes. It is not a second current copy or a safe rollback to expose blindly.
Cleanup remains separately scoped. This expansion supplies capacity and the
required filesystem boundary; it is not another backup failure domain.

Earlier directions saying Main still actively uses `/home/loomadmin/loom-box`
or awaits the canonical filesystem flag are historical. Do not repeat the
cutover from those old instructions.

## User Box Categories And Workspace Entry Points

Main's active Box contains `Topics/`, `Projects/`, `Library/`, `Notes/`
and `Documents/`. Topics and Library are raw source areas; their presence does
not mean all contents are enrolled in Notes. Main has no active Lane or
Dropzone intake. Workspace nodes retain the explicit Lane transfer source.

| User path | Purpose and boundary |
|---|---|
| `~/loom-box` | The current user's workspace Box; `~` is local to that user/node. |
| `~/loom-box/Documents` | General document source. |
| `~/loom-box/Notes` | Non-project notes source. |
| `~/loom-box/Projects` | Project source folders. |
| `~/loom-box/loom-lane` | Explicit workspace transfer staging; accepted batches enter Main Imports. |
| `~/loom-storage` | Configured storage link/mount for inspection. |
| `~/loom-main-box` | Main Box link when configured; verify its destination. |
| `.loom-acceptance/<scenario>/` | Manual fixture area inside an approved source root or disposable test directory. |

Box `.loom/box.yaml`, `.loom/policies/` and `.loom/contracts/` belong to the
Box, not to one project. The former Box `.loom/state/` is a compatibility
input; new Box runtime writers use node-owned state. Box initialization places
scoped guidance in Notes and Documents without adding a Box-root `AGENTS.md`,
and preserves user-edited guidance.

## Physical Custody And Generated Views

| Path | Class and use |
|---|---|
| `/srv/loom/storage/imports` | Accepted Lane/import custody, managed by LOOM. |
| `/srv/loom/storage/backups` | Watched-root backup custody, not an editing directory. |
| `/srv/loom/storage/archive` | Canonical inactive physical custody. Use reviewed lifecycle commands. |
| `/var/lib/loom/box-state` | Internal Box runtime state. |
| `/var/lib/loom/generated/notes` | Generated read-only Notes projection. |
| `/var/lib/loom/object-store` | Object protection data. |
| `/var/lib/loom/storage-retention` | Retained payload protection data. |
| `/var/lib/loom/backups/main` | Main backup root from the filesystem layout contract. |
| `/var/lib/loom/backups/operational` | Bounded operational packages in current recovery receipts. |
| `/run/loom/loomd.sock` | Runtime transport, not durable content. |

A backup batch may contain `manifest.json` and `payload/`; archive formats
have their own manifests and operation history. Do not infer custody or
recoverability from a familiar directory shape alone. Dot-prefixed staging and
lock paths are internal. Moving files by hand bypasses that evidence.

Catalog names such as `main/Documents`, `main/Archive`, `<node>/Backups`
and `<node>/Lane` identify logical surfaces, not arbitrary local filesystem
paths. `loom-notes` is a generated projection. Write Documents through its
owned source, and use supported operations for controlled custody. Never write
into `loom-storage/<node>/Backups/...` or generated Notes; change the owned
source and let LOOM converge.

The 2026-09-10 raw-root cloud receipt authenticates all eleven roots of one
exact archive, including Topics/Library, with bounded read-back of two tiny
files. Older archives retain their older declared scope. Raw coverage is
independent of Notes enrollment and does not automatically accept Provenance.
It is not proof of a new full restore.

## Project Contracts And Repository Development State

| Relative path | Owner and meaning |
|---|---|
| `<project>/.loom/project.yaml` | Canonical project identity and runtime contract entry. |
| `<project>/.loom/contracts/` | Project singleton/keyed contracts, including non-secret credential references. |
| `<project>/.loom/agents/` | Project and enabled-surface guidance. |
| `<project>/.loom/agent-packs/repo-development/` | Portable opt-in repository development templates/instructions. |
| `<project>/.loom/tools/validate-project.sh` | Generated project validation helper. |
| `<project>/.loom/state/` and `.loom/tmp/` | Runtime/migration records and temporary material; excluded from portable export. |
| `<repository>/.repo/repo.yaml` | Optional Git-tracked development identity, purpose and owning-project backlink. |
| `<repository>/.repo/` | Portable development objects and handoffs, excluding live harness/session state. |

The two directories are separate contracts. A project scaffold does not opt
every repository into `.repo/`, and Git folders do not automatically become
project members. Registered member paths are relative to the `repos/` facet.
LOOM's existing `.project/` development control plane remains authoritative
until an explicit migration is accepted; do not rename it mechanically.

Legacy root `loom.project.yaml` remains readable for compatibility. Migration
to `.loom/project.yaml` is reviewed and explicit. Project credential contracts
store logical environment/file references, never credential values. The direct
`proton_pass`/`pass://` source remains deferred. See [[Projects]].

## Agent And Harness Locations

| Path | Status at the reviewed snapshot |
|---|---|
| `/srv/loom/agents/morathustra` | Recorded live Main agent workspace; its `.hermes` is the one shared profile. |
| `/srv/loom/agents/archivist` | Workspace/ORCA non-Git registration and manual acceptance recorded 2026-09-01. |
| `/srv/loom/agents/mina` | Selected MINA source target, not a deployed second workspace. |
| `/home/agents/.orca`, `/home/agents/.config/orca`, `/home/agents/.config/Orca` | ORCA-owned runtime/profile locations; not portable project metadata. |
| `/home/agents/orca/workspaces` | Default ORCA-managed worktrees, separate from canonical project/runtime custody. |
| `<release>/share/loom/docs` | Release-matched documentation source. |
| `<release>/share/loom/ai-loom-pack` | Read-only instruction/template distribution source. |

MINA's live cutover must move/rename the one existing instance while preserving
its history. Pack scaffolding requires an explicit disposable path; it does
not migrate that runtime. Harnesses own their native skill destinations and
worktrees. Installed copies do not replace canonical pack source, and a
shared service UID is not per-agent isolation. See
[[External Agents And AI LOOM Pack]].

## Mac Mount Identity And Historical Inputs

Configured Mac SMB entry points are `/Volumes/loom-main-box` for read/write
Main Box and `/Volumes/loom-storage` for read-only Storage. A direct-cloud
mount has its own distinct configured identity. Verify exact host, share,
mountpoint and access mode; a directory with the right name is not proof of a
healthy mount. If Main or a mount is unavailable, do not write into an empty
local mountpoint or treat a retained view as synchronized live source.

These paths are historical compatibility inputs, never new writer targets:

- `/var/lib/loom/storage-views/main-export`: retired generated Storage export.
- `/var/lib/loom/main-documents`: former Main Documents backing root.
- `/var/lib/loom/lane/accepted`, `/var/lib/loom/private-backups` and
  `/var/lib/loom/storage-archive`: former pre-cutover custody locations.
- `<box>/.loom/state`: old Box runtime-state location.

Their presence is not permission to delete them. Follow the current operations
runbook and exact retained evidence for migration, recovery or cleanup.
Generic inactive restore acceptance does not reactivate a project's runtime;
see [[Projects]] and [[Storage Retention And Safe Delete]].
