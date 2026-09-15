---
title: "Nodes And Capabilities Portal"
description: "Guide to the portal surfaces for protected folders, active watched roots, node topology, providers, and capabilities."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - security
status: draft
verified_at: "2026-08-17"
source_scope:
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start nodes"
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start capabilities"
  - "internal/loomcli/portal/nodes_render.go"
  - "internal/loomcli/portal/protected_folder_actions.go"
  - "internal/loomcli/portal/protected_folder_action_executor.go"
  - "internal/loomcli/portal/protected_folder_test.go"
  - "internal/loomcli/portal/capability_explorer.go"
  - "internal/loomcli/portal/action_selectors.go"
related:
  - "[[Backups Cloud And Restore]]"
  - "[[Portal Guide]]"
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Capability Addresses]]"
aliases:
  - "Nodes Portal"
  - "Capabilities Portal"
  - "Providers Portal"
---
# Nodes And Capabilities Portal

## What This Page Covers

This page covers two Network And Admin portal surfaces:

- Nodes And Watched Roots;
- Capabilities And Providers.

Open them with:

```bash
loom enter --start nodes
loom enter --start capabilities
```

Slice 11 verified one-shot renders for both surfaces.

## Nodes And Watched Roots

The Nodes surface answers:

- which nodes are known;
- whether node agents are heartbeating;
- which arbitrary node-local folders have a user-facing protection contract;
- which watched roots and notes roots are reported;
- whether sync conflicts, watched-root findings, backup findings, or deletion
  requests need attention;
- which node-specific actions are safe to inspect from the portal.

The feature is source- and test-verified with protected folders kept separate
from the complete active watched-root inventory. Hardware acceptance remains a
separate integrator operation.

## Nodes Surface Sections

| Section | Meaning |
|---|---|
| Available Actions | Protect Folder, node health checks, and deletion-request actions when present. Sensitive actions are marked by risk. |
| Attention | Cross-surface items that should be reviewed before assuming topology is clean. |
| Node Cards | Per-node status, runtime profile, heartbeat recency, watcher count, findings, sync conflicts, and backup state. |
| Protected Folders | User-facing backup contracts grouped by Waiting, Active / Protected, Attention, and Disabled lifecycle state. |
| Active Watched Roots | The complete reported inventory, including system, Box, project, notes, and protected-folder roots. |
| Diagnostics | Raw node ids, watched-root keys, sync rows, backup batches, replicas, private backups, and deletion requests behind the detail toggle. |

Use `tab` for raw/detail mode when the summary is not enough.

## Protect Folder Workflow

Choose **Protect Folder**, then select the owner node and enter an absolute path
on that node. The Portal stages the request through these states:

```text
input -> preflighting -> review -> confirmation -> mutation -> result
```

Preflight is bounded, durable, and performed by the owner node. Review the
canonical path, file/directory/byte counts, truncation marker, effective
managed-policy files, protected/ignored counts, and findings. The Portal does
not expose file contents. A blocking finding prevents confirmation.

After confirmation, the details view shows the friendly node name, source path,
scan evidence, desired/applied revision, configuration agreement, and latest
accepted backup. A queued response is only accepted desired state. The status
becomes **Active** after the current configuration is applied and reported, and
**Protected** only after a later backup is accepted.

Available lifecycle actions depend on state:

- **Recheck Folder** repeats preflight after path or policy changes.
- **Retry Activation** queues a new owner-node reconciliation attempt.
- **Disable Protection** keeps canonical YAML but removes the managed root from desired
  active state.
- **Enable Protection** restores desired active state.
- **Delete Protection Contract** requires strong confirmation and removes only
  the desired contract; it never deletes source files or retained backup data.

When main is offline, main-dependent actions are disabled. When main has
accepted work but the owner node is offline, the item remains **Waiting For
Node** and can be resumed after the node returns.

## Deletion Requests

The verified Nodes render included a pending deletion request. The portal
showed:

- Review Deletion Request as disabled because it was already pending review;
- Approve, Deny, and Complete actions as sensitive.

Do not use sensitive deletion actions from the portal unless the matching
object, source path, retention state, backup state, and operator intent have
been reviewed. The CLI docs for sync deletion requests explain the command
surface in [[Nodes Communication Sync And Setup CLI Reference]].

## Capabilities And Providers

The Capabilities surface answers:

- which task groups have callable tools;
- which scopes contain providers;
- which providers are active or unhealthy;
- which capability endpoints are active;
- what risk and authorization levels the visible endpoints carry;
- whether provider advertisements need attention.

The verified render showed task groups for Projects, Storage, Network, and
Diagnostics. It also showed a hierarchical Capability Explorer:

```text
Capabilities
  main  6 providers, 20 capabilities
  workspace/macbook  2 providers, 5 capabilities
```

## Capability Explorer Levels

The explorer has three practical levels:

| Level | What It Shows |
|---|---|
| Scope | Node/scope rows such as `main` or `workspace/macbook`. |
| Provider | Provider rows inside a scope. |
| Capability | Capability endpoints inside a provider. |

Use `enter` to open the selected row. Use `esc` to move back. Use `space` to
open actions for a provider or capability. Use `tab` to reveal raw ids,
provider health, endpoint ids, form, risk, and authorization level.

## Capability Actions

Provider rows can expose:

- Inspect Provider;
- Provider Health.

Capability rows can expose:

- Inspect Capability;
- Usage Docs;
- Call Capability.

The portal suppresses capability-call actions when archived project context
marks the related capability as archived. If a mutating archived-project action
appears where it should be suppressed, record a portal bug before using it.

## Search

Use scoped search for capability records:

```text
#capabilities main@system
#capabilities status
```

Use command mode when you need exact CLI behavior:

```text
$ loom capabilities list --node main --limit 20
$ loom capability inspect main@system.status.read
```

## Healthy State

A healthy control-plane view usually means:

- expected nodes are online or recently seen;
- protected folders show **Protected**, or any Waiting/Attention state has an
  understood cause;
- watched-root findings are zero or explained;
- sync conflicts are zero or under review;
- provider health is `ok` or any degraded provider has a known cause;
- active capabilities have expected risk and authorization values;
- provider advertisements are either approved, rejected, or clearly pending
  operator review.

## Related Docs

- [[Backups Cloud And Restore]]
- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Nodes Providers Capabilities]]
- [[Capability Addresses]]
- [[Object Store Diagnostics Portal]]
