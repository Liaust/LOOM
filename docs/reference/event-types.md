---
title: "Event Types"
description: "Reference for LOOM durable event type families and how to interpret them."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - reference
  - events
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/events/events.go"
  - "docs/concepts/events-communication-and-realtime.md"
related:
  - "[[Reference]]"
  - "[[Events Communication And Realtime]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Security Authorization And Policy]]"
  - "[[Modules Agents And Realtime API]]"
aliases:
  - "LOOM Events"
  - "Event Type Reference"
---
# Event Types

## What This Page Covers

This page lists the durable event families currently defined by
`internal/events/events.go`.

Events are durable truth. They are not the same as communication messages,
runtime logs, realtime notifications, or progress feeds. See
[[Events Communication And Realtime]] for the conceptual split.

## System, Identity, And Scope

| Event Type | Meaning |
|---|---|
| `system.bootstrapped` | System bootstrap completed. |
| `actor.created` | Actor record created. |
| `node.created` | Node record created. |
| `scope.created` | Scope record created. |

## Projects

| Event Type | Meaning |
|---|---|
| `project.created` | Project created. |
| `project.policy_profile.created` | Project policy profile created. |
| `project.workspace_view.created` | Project workspace view created. |
| `project.member_added` | Project member added. |
| `project.contract.registered` | Project contract registered. |
| `project.contract.updated` | Project contract updated. |
| `project.base_activated` | Project base activation changed. |
| `project.facet.activated` | Project facet activated. |
| `project.facet.deactivated` | Project facet deactivated. |
| `project.archived` | Project archived. |
| `project.script.exposed` | Script capability exposed through a project. |
| `project.script_exposure.stale` | Project script exposure became stale. |
| `project.script_exposure.blocked` | Project script exposure was blocked. |

## Objects, Text, And Search

| Event Type | Meaning |
|---|---|
| `object.ingested` | Object ingestion completed. |
| `object.version.created` | Object version created. |
| `object.scope_linked` | Object linked to a scope. |
| `text.extracted` | Text extraction succeeded. |
| `text.extraction_failed` | Text extraction failed. |
| `chunks.created` | Text chunks created. |
| `search.indexed` | Search indexing succeeded. |
| `search.index_failed` | Search indexing failed. |

## Jobs, Scripts, Workflows, Artifacts, And Runners

| Event Type | Meaning |
|---|---|
| `job.created`, `job.queued`, `job.started` | Job lifecycle start. |
| `job.completed`, `job.failed`, `job.timed_out`, `job.cancelled` | Job terminal states. |
| `job.attention.acknowledged`, `job.attention.archived` | Job attention lifecycle. |
| `script.registered`, `script.version.created` | Script registration and versioning. |
| `script.run.requested`, `script.run.started`, `script.run.completed`, `script.run.failed` | Script run lifecycle. |
| `workflow.run.requested`, `workflow.run.started`, `workflow.run.completed`, `workflow.run.failed` | Workflow run lifecycle. |
| `artifact.created` | Artifact created. |
| `runner.registered`, `runner.heartbeat` | Runner registration and heartbeat. |

## Providers, Policy, Approvals, Grants, Routes, And Calls

| Event Type | Meaning |
|---|---|
| `provider.registered` | Provider registered. |
| `provider.health.updated` | Provider health changed. |
| `provider.advertisement.received`, `provider.advertisement.validated` | Provider advertisement received and validated. |
| `provider.advertisement.approved`, `provider.advertisement.rejected`, `provider.advertisement.stale` | Provider advertisement review lifecycle. |
| `policy.decision.created` | Policy decision recorded. |
| `approval.requested`, `approval.approved`, `approval.denied`, `approval.expired` | Approval lifecycle. |
| `grant.issued`, `grant.revoked`, `grant.expired`, `grant.used` | Grant lifecycle. |
| `admin.policy_action.recorded` | Administrative policy action recorded. |
| `route.created`, `route.authorized`, `route.waiting_for_approval`, `route.dispatched`, `route.executing`, `route.completed`, `route.failed` | Route lifecycle. |
| `capability_call.created`, `capability_call.authorized`, `capability_call.approval_required`, `capability_call.dispatched`, `capability_call.completed`, `capability_call.failed` | Capability call lifecycle. |

## Nodes, Communication, Sync, And Watched State

| Event Type | Meaning |
|---|---|
| `node.enrollment_token.created` | Enrollment token created. |
| `node.enrollment.requested`, `node.enrollment.approved`, `node.enrollment.denied` | Node enrollment lifecycle. |
| `node.credential.issued`, `node.credential.revoked` | Node credential lifecycle. |
| `node.heartbeat.received` | Node heartbeat received. |
| `node.presence.changed` | Node presence changed. |
| `communication.message.created`, `communication.message.claimed`, `communication.message.acked`, `communication.message.failed` | Communication message lifecycle. |
| `node.local_test_event` | Local node test event. |
| `sync.private_backup.stored` | Private backup record stored. |
| `sync.deletion.requested` | Sync deletion request created. |

## Realtime

| Event Type | Meaning |
|---|---|
| `realtime.topic.created`, `realtime.topic.closed` | Topic lifecycle. |
| `realtime.publication.created` | Topic publication created. |
| `realtime.subscription.created`, `realtime.subscription.acknowledged`, `realtime.subscription.cancelled` | Subscription lifecycle. |
| `realtime.presence.changed` | Presence changed. |
| `realtime.notification.created`, `realtime.notification.routed`, `realtime.notification.acknowledged`, `realtime.notification.dismissed`, `realtime.notification.expired` | Notification lifecycle. |
| `realtime.progress.updated`, `realtime.progress.closed` | Progress lifecycle. |
| `realtime.lease.granted`, `realtime.lease.released`, `realtime.lease.expired`, `realtime.lease.conflict` | Lease lifecycle. |

## Agents

| Event Type | Meaning |
|---|---|
| `agent.access_session.created` | Agent access session created. |
| `agent.work_context.created` | Agent work context created. |
| `agent.tool_view.created` | Agent tool view created. |
| `agent.tool.searched` | Agent tool search recorded. |
| `agent.tool.inspected` | Agent tool inspection recorded. |
| `agent.tool.called` | Agent tool call recorded. |
| `agent.worklog.written` | Agent worklog entry written. |

## Modules

| Event Type | Meaning |
|---|---|
| `module.registered`, `module.registration_failed` | Module registration lifecycle. |
| `module.manifest.validated`, `module.manifest.rejected` | Module manifest validation lifecycle. |
| `module.install_started`, `module.installed`, `module.install_failed` | Module install lifecycle. |
| `module.enabled`, `module.disabled` | Module installation enabled or disabled. |
| `module.capability_exposed`, `module.capability_disabled` | Module capability exposure lifecycle. |
| `module.backup_exported` | Module backup export completed. |

## Workers And Maintenance

| Event Type | Meaning |
|---|---|
| `worker.kind.registered`, `worker.instance.registered`, `worker.instance.updated` | Worker registration and update. |
| `worker.run.started`, `worker.run.succeeded`, `worker.run.failed`, `worker.run.cancelled` | Worker run lifecycle. |
| `worker.lease.acquired`, `worker.lease.released`, `worker.lease.expired` | Worker lease lifecycle. |
| `worker.checkpoint.updated` | Worker checkpoint changed. |
| `worker.health.updated` | Worker health changed. |
| `worker.control.requested`, `worker.control.applied`, `worker.control.failed` | Worker control lifecycle. |
| `maintenance.operation.started`, `maintenance.operation.succeeded`, `maintenance.operation.failed` | Maintenance operation lifecycle. |
| `maintenance.finding.opened`, `maintenance.finding.resolved` | Maintenance finding lifecycle. |

## Automation, Schedules, Invocations, Integrations, And Direct Events

| Event Type | Meaning |
|---|---|
| `automation.created`, `automation.updated`, `automation.paused`, `automation.resumed`, `automation.disabled` | Automation lifecycle. |
| `schedule.created`, `schedule.updated`, `schedule.paused`, `schedule.resumed`, `schedule.disabled` | Schedule lifecycle. |
| `schedule.fire.created`, `schedule.fire.missed`, `schedule.fire.skipped`, `schedule.fire.invocation_created`, `schedule.fire.completed`, `schedule.fire.failed` | Schedule fire lifecycle. |
| `invocation.created`, `invocation.leased`, `invocation.calling`, `invocation.completed`, `invocation.failed`, `invocation.approval_required`, `invocation.timed_out` | Invocation lifecycle. |
| `integration.created`, `integration.updated`, `integration.disabled`, `integration.revoked` | Integration lifecycle. |
| `integration.auth_profile.created`, `integration.auth_profile.rotated`, `integration.auth_profile.revoked` | Integration auth profile lifecycle. |
| `direct_event.endpoint.created`, `direct_event.endpoint.updated`, `direct_event.endpoint.paused`, `direct_event.endpoint.resumed`, `direct_event.endpoint.disabled` | Direct-event endpoint lifecycle. |
| `direct_event.received`, `direct_event.authenticated`, `direct_event.rejected`, `direct_event.duplicate_detected` | Direct-event intake lifecycle. |
| `direct_event.mapping.previewed`, `direct_event.mapped`, `direct_event.mapping_failed` | Direct-event mapping lifecycle. |
| `direct_event.invocation_created`, `direct_event.completed`, `direct_event.failed`, `direct_event.timed_out` | Direct-event dispatch lifecycle. |

## How To Use This Reference

Use event type filters when you need durable history for a specific domain.
For security review, start with policy, approval, and grant event families. For
job debugging, start with job, worker, script, workflow, and artifact events.
For realtime issues, use both realtime events and the current realtime state
commands because events are history and realtime rows are active state.

## Related Docs

- [[Events Communication And Realtime]]
- [[Security Authorization And Policy]]
- [[Automation Jobs And Workers]]
- [[Modules Agents And Realtime API]]
- [[Command Index]]
