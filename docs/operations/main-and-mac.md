---
title: "Main And Mac Operations"
description: "Operate the private Main/desktop connection and distinguish server sources, client working copies and independent agent access."
audience: [operator, developer]
tags: [loom, operations, configuration, workspace]
status: draft
verified_at: "2026-11-11"
source_scope: ["modules/workspace-electron/src/transport.mjs", "internal/workspacefiles", "internal/notesworkspace", "nix/modules/loom-agents.nix", "nix/modules/loom-hermes-platform.nix"]
related: ["[[Canonical Paths]]", "[[Desktop Workspace]]", "[[Box Storage And Lane]]", "[[Backup Restore And Drills]]", "[[Main-To-Mac Computer Use]]"]
---

# Main And Mac Operations

## Separate The Owners

Main coordinates shared project, file, knowledge, job and recovery state. The
Mac desktop owns its local working copies, drafts and outbox. Its independent
local Mac Box is not a replica of every Main file. A replaceable edge can route
the private network or public application ingress, but must not hold canonical
LOOM state.

The reference server is NixOS and the reference desktop is macOS. Addresses,
SSH aliases, users, mounts and credentials belong to the operator. The public
source supplies no working connection to the maintainer's installation.

## Desktop Connection

The desktop connects through ordinary private SSH and forwards the configured
Main Unix-socket API. `LOOM_FILES_SSH_HOST` selects the existing operator-owned
SSH host entry. Configure its identity, pinned host key and bounded connection
behavior before starting the client. Do not expose a database or desktop-control
service publicly to make the file browser connect.

Opening a folder requests a shallow listing. Opening working text establishes
an exact base and durable local copy. Large originals can stay remote and be
read through bounded, version-checked downloads. Main-first operations retain
their request identity across a lost response. Offline local save, Main
acceptance and later backup coverage are three different results.

The client does not require an Obsidian vault or SMB mount. Existing mounts and
legacy vaults remain independent user state: do not overwrite them or treat a
new app build as permission to migrate them. Local development can point its
read-only recovery browser at `LOOM_FILES_LOCAL_ROOT`; that is not a replacement
for the native writable working-set owner.

## Inspect Actual State

On your configured node/client:

```sh
loom version
loom status
loom health
loom storage doctor
loom backup coverage
```

Check the selected node, source roots, workers and explicit failures. A healthy
daemon can coexist with a deliberately paused indexer. File existence does not
prove source publication, index coverage, embeddings or cloud protection.

## Agent And Device Access

Native Hermes on Main owns conversational sessions and schedules; independent
Codex sessions own coding work. Neither needs ORCA or the Mac client online to
perform server-side work once configured. A remote conversation client is not
the same as a browser cookie store or graphical desktop session.

Main-side browser/desktop tools and the paired Main-to-Mac connector are
different providers. A Mac capture/input permission is not shell access, and
an SSH key is not permission to capture a screen. Preserve session/profile
selection and actual device-consent boundaries; do not substitute another
device when the intended provider is unavailable.

## Changes And Recovery

Keep host configuration outside public source. Review actual mounts and
writer/recovery owners before activation. Box and archive must share a
filesystem where the operation relies on atomic rename; a Nix rebuild is not
a data migration.

Preserve current drafts, pending operations, live agent profiles and relevant
existing recovery. Use the supported update/rollback and file-operation paths,
not a historical machine receipt. Ordinary source patches do not need another
full backup transfer; destructive migrations need their own inventory,
protection evidence and authority. Never remove retained data simply because
its path or client looks old.
