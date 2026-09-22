---
name: operate-loom-nodes
description: Diagnose LOOM node reachability, providers, capabilities, services or jobs, and perform a bounded node operation within its existing authority.
---

# Operate LOOM Nodes

## Bounded diagnosis

An ordinary source edit needs no node preflight. For a node task, establish the
intended machine, configured endpoint and relevant symptom from current scope.
Inspect the relevant layer: local CLI/socket, Main reachability, node heartbeat,
provider/capability, or workload/job. A failure at one layer does not prove
another is broken. Preserve the original connection, timeout or authorization
error and correlation ID; never silently switch endpoint or identity.
A missing reported backup root is not data-loss evidence. Follow its read-only
next step when relevant; that category alone does not justify backup or repair.

Use known commands and [node commands](references/node-commands.md). Load the
matching runbook for the actual operation or uncertainty; do not repeat broad
health scans and documentation searches for every task. Apply only the requested,
authorized bounded action, then verify its component and downstream effect.

## Evidence and availability

Current task and repository/node instructions govern scope. Runtime records and
approved host/service observations establish health; release-matched source and
help establish support. Plans are future intent.

The committed ORCA 1.4.191 and non-Git registration evidence is historical,
not installed patch availability or current health. Use the host's native skill.

## Authority and production

For task-authorized Mac work, use the configured `loom-mac` alias and explicit
remote paths: `ssh loom-mac 'uname -a'`. Shell commands and file transfers need
no per-command LOOM registration. Inspect bindings with `ssh -G loom-mac`.
Keep the desktop key separate, preserve host verification, and report connection
failures. Reconcile mutations before retrying. SSH grants no sudo/privacy bypass.

Read the matching machine note and operations runbook before connecting to a
LOOM host or changing a service. Production updates, NixOS rebuilds, rollback,
backup/restore and live data repair belong to the integrator/operator unless an
accepted plan explicitly delegates them.

Registration, an installed skill or a capability listing grants no execution
authority. Actor/node authorization, policy, approvals and OS permissions still
apply. Enrollment, credentials, capability effects and cross-node changes retain
their actual authorization. A single retry needs known idempotency and bounded
effects. Do not substitute raw production shell access for an unavailable LOOM
operation without an approved runbook and operator scope.

Escalate with affected node/component, observed error and correlation/job ID,
reproduction, attempted repair, known effects and remaining uncertainty. Keep
credential values out of diagnostics; distinguish observation from hypothesis.
