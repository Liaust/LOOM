---
name: investigate-loom-incidents
description: Investigate ambiguous degraded or failed LOOM behavior when the task needs a timeline, preserved evidence, bounded remediation, defect classification, and a complete handoff.
---

# Investigate LOOM Incidents

Build the evidence record before attempting repair. Use this skill when routine
project, storage, or node inspection does not already identify a bounded fix.

## Source-Of-Truth Order

1. User report, affected scope instructions, and current safety state.
2. Durable LOOM events, job attempts, correlation IDs, and structured status.
3. Canonical project/Box/config source and storage custody evidence.
4. Redacted logs and host evidence from approved read-only checks.
5. Release-matched docs, current CLI help, and the relevant runbook.
6. Hypotheses only after observations are timestamped and preserved.

Do not edit generated views, delete failed artifacts, rotate credentials, or
restart/rebuild production merely to see whether the symptom disappears.

## Inspect, Plan, Apply, Verify

1. Define incident start, expected behavior, observed behavior, affected
   node/project/path, impact, and current risk.
2. Capture timestamps, correlation/job/transfer/storage IDs, commands, relevant
   output, and a minimal reproduction.
3. Build a timeline and identify the first confirmed divergence.
4. Classify the defect as project, configuration, data/custody, environment,
   external dependency, or LOOM core.
5. Plan the smallest reversible remediation with explicit stop conditions.
6. Apply only when scope and authority cover that remediation.
7. Verify the original symptom, downstream state, durable records, and absence
   of collateral effects.
8. Produce a complete handoff if the owner changes or uncertainty remains.

Read [the evidence and handoff contract](references/handoff.md) before
collecting a support artifact or proposing repair.

## Safety Classification

- **Inspect:** status, events, job/worker/transfer records, redacted logs,
  source contracts, health, and support-bundle previews.
- **Plan:** timeline, reproduction, defect classification, rollback-aware repair
  plan, and owner handoff.
- **Safe run:** a single reversible repair in a non-production or explicitly
  scoped project fixture with direct validation.
- **Sensitive:** production restart/update/rebuild, credential use, data repair,
  restore, deletion, bulk retry, policy/authorization change, or external
  contact.

Repeated retries can destroy evidence or duplicate effects. Stop when the fix
path is no longer bounded or validation keeps failing after focused repair.

## Evidence Hygiene

Preserve concise relevant output, not indiscriminate logs. Redact secrets and
personal data. Never put credentials, Proton session state, tokens, private
memory, or unrelated user files into Git, Notes, Basecamp, handoffs, or support
artifacts. Correlation IDs and timestamps should use the system's actual values.

## Current Environment

Main is the live system. MINA uses native Hermes; independent native Codex
and direct SSH/CLI remain available. ORCA is retired. Existing Main browser and Mac bridge
tools have separate targets. Check the affected route, not every node, and use
known commands directly; consult current help for an unfamiliar operation.
Old acceptance notes establish only their dated scenario, not present availability
or a blanket restriction. Report a route as unavailable only from current evidence.

## Escalation

Fix task-owned project defects directly. The current agent may implement a
LOOM-core repair in its real development checkout when that scope is authorized;
coordinate actual concurrent owners rather than requiring another coding agent.
For host policy, credentials or another effect outside the task, request the
exact missing authority or involve its actual owner. Hold only dependent work
and continue independent implementation. Skills organize work; they do not grant
permission or justify bypassing a failed supported interface with an unmanaged
service, copied validators or a substitute test platform.
