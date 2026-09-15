---
title: "Node Enrollment And Watched Roots"
description: "Operator runbook for node enrollment, protected-folder reconciliation, watched-root status, and safe troubleshooting."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - configuration
  - storage
status: draft
verified_at: "2026-08-17"
source_scope:
  - "/tmp/loomdocs-v096 --json node list --limit 10"
  - "/tmp/loomdocs-v096 --json node health macbook"
  - "/tmp/loomdocs-v096 --json watched-roots status --node macbook --include-fidelity --limit 5"
  - "go run ./cmd/loom-node-agent --json watched-roots status"
  - "go run ./cmd/loom-node-agent --json sync status"
  - "go run ./cmd/loom backup contracts --help"
  - "internal/backupcontracts/status.go"
related:
  - "[[Backups Cloud And Restore]]"
  - "[[Node Agent CLI Reference]]"
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Main And Mac Operations]]"
  - "[[Storage Retention And Safe Delete]]"
aliases:
  - "Node Enrollment"
  - "Watched Roots Runbook"
---
# Node Enrollment And Watched Roots

## Purpose

Node enrollment gives a workspace node a registered identity and credential.
Watched roots tell the node-agent what local paths to observe, classify, back
up, and report to main.

Enrollment and watched-root configuration are linked but not the same:

- enrollment answers whether the node is allowed to talk to main;
- watched roots answer what local files the node-agent should expose or back
  up;
- sync reports answer what changes have been queued or accepted.

## Enrollment Checks

From main-backed CLI:

```bash
loom node list --limit 10
loom node inspect macbook
loom node health macbook
loom communication health
```

From the workspace node:

```bash
loom setup status
loom-node-agent status
loom-node-agent heartbeat
loom-node-agent poll
```

At verification time, Mac setup status showed credential configured and
verified-on-main state, and `node health macbook` returned heartbeat/message
counters.

## Enrollment Changes

Operator paths:

```bash
loom node enrollment-request list --limit 20
loom node enrollment-request inspect <request-ref>
loom node enrollment-request approve <request-ref>
loom node enrollment-request deny <request-ref>
loom node credential issue <node-ref>
loom node enrollment-token create
```

Credential and token outputs are sensitive. Do not put them in docs, support
bundles, or bug reports.

## Local Watched Roots

Workspace-local inspection:

```bash
loom-node-agent watched-roots list
loom-node-agent watched-roots status
loom-node-agent watched-roots explain <path>
loom-node-agent watched-roots backups status
```

Local configuration and run actions for project-derived or diagnostic roots:

```bash
loom-node-agent watched-roots add <root-key> <path>
loom-node-agent watched-roots apply-plan <watch-plan.json>
loom-node-agent watched-roots run --once
loom-node-agent watched-roots disable <root-key> --reason "project archived" --yes
```

Use `disable` when an archived or retired contract must stop its local
supervisor worker. The command preserves local configuration, checkpoints,
history, findings, queued evidence, and backup evidence; it does not remove or
rewrite source files. Confirm `enabled: false` and `status: disabled` afterward.

Use apply-plan for project-derived watch plans when possible. It keeps local
node-agent state aligned with project contracts. `apply-plan` is not the normal
Protect Folder path: user-facing protected folders are reconciled from main's
revisioned desired state to the owner node.

## Protected-Folder Reconciliation

Inspect user-facing lifecycle from the main-backed CLI:

```bash
loom backup contracts status --node macbook
loom backup contracts inspect <contract-key>
```

The protected-folder control plane has two durable owner-node operations:

1. Preflight performs a bounded, no-follow scan and returns policy/count/finding
   evidence without file contents.
2. Reconciliation atomically applies the current revision and configuration
   hash to the owner node, preserving the previous valid configuration if apply
   fails.

Main offline disables Portal actions because canonical intent cannot be safely
changed. If main already accepted a request and the owner node goes offline,
status remains `waiting_for_node`. Do not work around either case by editing
node-agent files, copying YAML, or using SSH.

After the owner returns, use the bounded control that matches the failure:

```bash
loom backup contracts recheck <contract-key>
loom backup contracts retry-activation <contract-key> --reason "owner node restored"
```

An accepted queue entry is not applied state. `active` requires the current
desired revision/configuration to be applied and reported. `protected`
additionally requires an accepted backup after that acknowledgement.

## Main-Backed Watched Root Reports

Main-backed inspection:

```bash
loom watched-roots status --node macbook --include-fidelity --limit 20
loom watched-roots findings --node macbook --limit 20
loom watched-roots failures --node macbook --limit 20
loom watched-roots backups status --node macbook
```

At verification time, Mac had two watched-root reports, zero findings, zero
failures, and watched-root backup status returned accepted/failed/skipped
counters.

## Sync And Deletion Requests

Use sync inspection to understand what the workspace has sent or requested:

```bash
loom sync status --node macbook
loom sync batches --node macbook --limit 20
loom sync replicas --node macbook --limit 20
loom sync conflicts --node macbook --limit 20
loom sync deletion-requests --node macbook --limit 20
```

Deletion requests are review records. They do not mean the source file should
be deleted immediately. Use safe-delete checks and retention docs before
marking a request complete.

## Troubleshooting Order

1. Confirm the node is active and recently seen: `loom node health macbook`.
2. Confirm local node-agent status: `loom-node-agent status`.
3. Inspect the protected-folder lifecycle and its preflight/apply reason.
4. Check local watched roots: `loom-node-agent watched-roots status`.
5. Check main reports: `loom watched-roots status --node macbook`.
6. Compare desired/applied revision and configuration hashes; do not call a
   mismatched report protected.
7. Check watched-root findings and the latest backup status.
8. Check sync batches and deletion requests only when the problem is actually
   a sync or deletion workflow.
9. Use `.loom-acceptance` fixtures for any test files.

Do not manually edit generated backup views to “fix” watched-root output.

## Related Docs

- [[Backups Cloud And Restore]]
- [[Node Agent CLI Reference]]
- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Files Storage And Lane]]
- [[Storage Retention And Safe Delete]]
