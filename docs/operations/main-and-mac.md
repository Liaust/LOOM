---
title: "Main And Mac Operations"
description: "Understand the reference Main/workspace topology, storage roles and separately authorized device routes."
audience: [operator, developer]
tags: [loom, operations, configuration]
status: draft
verified_at: "2026-09-15"
source_scope: ["nix/profiles/main-hardware-desktop.nix", "nix/modules/loom-smb.nix", "nix/modules/loom-orca.nix", "scripts/loom-macbook"]
related: ["[[Canonical Paths]]", "[[ORCA Main Operations]]", "[[Files Storage And Lane]]", "[[Backup Restore And Drills]]", "[[Main-To-Mac Computer Use]]"]
---

# Main And Mac Operations

## Reference Topology, Not A Live Inventory

Main coordinates policy, providers, jobs, storage and knowledge. A Mac workspace
owns its local Box and node-agent state. An optional replaceable edge host
provides private-network routing and public ingress; it must not become the
canonical store for project or database state.

The Nix reference uses a private WireGuard network, with Main at `10.44.0.2`.
Those are example bindings, not discovery results or authority to connect.
Supply your own peers, keys and addresses. The public example contains no
working maintainer account, public endpoint or hardware configuration.

## Storage Roles

The current reference separates Main's operating/database storage from a data
filesystem mounted at `/srv/loom`. Box lives at `/srv/loom/box`, archive and
retained user-data custody under `/srv/loom/storage`, and agent workspaces under
`/srv/loom/agents`. The exact layout is configured, not created by reading this page.

Box and archive payloads must share a filesystem where lifecycle operations use
atomic rename. Mount dependencies prevent services from silently writing into
an unmounted underlying directory. A Nix rebuild does not itself migrate
existing payloads, catalog paths or ownership.

A Mac can mount a writable `loom-main-box` share and read-only `loom-storage`
inspection share. The Mac's own `~/loom-box` remains a different source.
Generated or retained copies are not alternate writable sources.

## Inspect Before Acting

On the configured node/client:

```sh
loom status
loom health
loom storage filesystem status
loom storage doctor
loom backup coverage
```

Check the selected backend, source roots and errors. The helper
`scripts/loom-macbook` supports the reference Mac mount/workspace operations,
but its defaults need the operator's own configuration. It is not a
general-purpose installer or an instruction to create historical aliases.

## Network And Device Access

Keep PostgreSQL, private APIs, SMB, remote terminals and desktop services on
their intended private interfaces. Public application HTTPS is a separate
Caddy/edge configuration with explicit domain and credential prerequisites.
Never expose a private management port merely to get a client connected.

ORCA pairing connects its workspace/terminal client to the configured Main
runtime. Main Chromium, ORCA's shared browser, Mac computer use and normal SSH
are distinct routes. A paired computer-use key is restricted to that endpoint;
it is not a general shell credential. See [[ORCA Main Operations]] and
[[Main-To-Mac Computer Use]].

## Changes And Recovery

Review the exact update or migration plan. Preserve relevant existing recovery,
identify writers and rollback behavior, then perform the authorized operation.
For ordinary patches use focused checks and the supported update path;
a full cloud transfer or restore drill is not required for every change.

For an actual filesystem move, inventory the selected data, verify recovery,
stop the affected writers, preserve identity and metadata, and recheck the
result before resuming. Do not copy commands from a historical machine receipt
or treat a different mount layout as interchangeable.

General maintenance must not remove old data or keys merely because their
paths appear stale. Use a reviewed cleanup scope and preserve evidence of
what was retained, moved or deleted.
