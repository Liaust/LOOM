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

## Current Versus Future

Verify every diagnostic command in current CLI help. Mark deferred repair or
observability capabilities explicitly. The completed Mac evidence covers only
Orca's disposable local lifecycle, narrow WireGuard/VPS preflight, and Proton
CLI version/help plus fail-closed no-session behavior. Main/headless,
browser/mobile/Relay, live TLS/WSS or WireGuard client paths, Proton content and
credential use, restart continuity, and Mac-off operation remain deferred.

## Escalation

Route project defects to the project owner/worker, host and deployment defects
to the integrator/operator, credential workflow defects to the Proton owner,
and LOOM-core defects to the repository developer. Skills organize the handoff;
they do not grant permission to perform the repair.
