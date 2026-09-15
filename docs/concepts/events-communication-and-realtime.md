---
title: "Events Communication And Realtime"
description: "Explains the difference between durable events, transport messages, operational logs, and realtime state in LOOM."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - concepts
  - jobs
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
  - "internal/events/events.go"
  - "internal/communication/communication.go"
  - "internal/realtime/service.go"
  - "internal/httpapi/server.go"
  - "go run ./cmd/loom realtime --help"
related:
  - "[[LOOM Architecture]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Automation Jobs And Workers CLI Reference]]"
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Modules Agents And Realtime API]]"
  - "[[API Route Index]]"
aliases:
  - "Events"
  - "Realtime"
  - "Communication"
---
# Events Communication And Realtime

## What This Page Covers

This page explains four words that are easy to confuse:

- events;
- communication messages;
- logs;
- realtime state.

They are related, but they do different jobs.

## The Core Rule

LOOM uses this rule from `AGENTS.md` and the architecture notes:

```text
Events are durable truth.
Messages are transport.
Logs are operational detail.
```

An event should record a meaningful state transition or action that LOOM needs
to remember. A communication message moves work or information between nodes. A
log helps debug what happened while software was running. Realtime state helps
the user or system see active status, subscriptions, notifications, progress,
and leases.

## Events

Events are the historical spine of LOOM. They should represent things like:

- node registration;
- provider advertisement;
- capability registration or request;
- policy allow, deny, or approval-required decisions;
- approval and grant changes;
- job lifecycle changes;
- file ingestion, versioning, indexing, or transfer changes;
- backup and restore actions;
- module lifecycle changes;
- node heartbeat or status changes;
- security revocation or quarantine actions.

The current repo has an `internal/events` package and `/v1/events` routes in
`internal/httpapi/server.go`. Later CLI and API pages must verify concrete
event filtering and response examples before documenting exact user commands or
payloads.

## Communication Messages

Communication messages are transport records. They help node agents and main
move work reliably across boundaries. A message may cause an event, but the
message itself is not the durable domain fact.

The current repo has an `internal/communication` package and API routes for
communication health and messages. Node-agent commands also include inbox,
outbox, poll, heartbeat, and related surfaces. See the later node and
communication CLI/API pages for exact workflows.

The default architecture routes control through main:

```text
Node -> Main Node -> Node
```

Direct node-to-node coordination is future or deferred unless a later doc
verifies a specific implementation.

## Logs

Logs are operational detail. They can explain why a service failed, why a
worker retried, or why a job emitted specific output. They are not the primary
source of truth for LOOM state.

When debugging, use logs to explain symptoms, but prefer events, job records,
policy decisions, backup manifests, object records, and indexed state when you
need to know what LOOM believes happened.

## Realtime State

Realtime state is for active coordination. Slice 3 verified that the current
CLI exposes:

```bash
loom realtime --help
```

That command group currently lists primitives for:

- topics;
- subscriptions;
- presence;
- notifications;
- progress feeds;
- leases.

The HTTP API exposes matching realtime route groups under `/v1/realtime/*`.

Realtime state is not a replacement for events. For example, a progress feed may
show a job currently moving forward, while durable job and event records explain
what happened after the fact.

## How The Layers Work Together

A typical action may touch several layers:

1. A user starts a workflow from the CLI or portal.
2. A service checks policy and possibly creates an approval request.
3. A capability call or job starts.
4. Communication messages may move the request between main and a node.
5. Realtime progress or notifications update the user.
6. Durable events, job records, artifacts, object records, or backup manifests
   preserve the result.
7. Logs help diagnose runtime failures.

Good docs should not collapse these layers into one word. If a page says
"event", it should mean durable event. If it says "message", it should mean
transport. If it says "notification" or "progress", it should mean realtime
state.

## User Impact

For users, this distinction helps answer different questions:

- "What changed?" Look for events or domain records.
- "Is something still running?" Look for jobs, workers, realtime progress, or
  portal status.
- "Did main send work to another node?" Look at communication, node-agent
  inbox/outbox, or route records.
- "Why did software fail?" Look at logs and support bundles, then correlate
  them with durable records.

## Future Or Deferred Behavior

The current docs should not claim complete offline-first message replay,
full network failure simulation, or complete direct node-to-node realtime
coordination unless those paths are tested in later slices. The architecture
expects richer sync and replication, but current public docs must describe only
verified workflows as ready.

## Related Docs

- [[LOOM Architecture]]
- [[Automation Jobs And Workers]]
- [[Runtime Status And Support]]
- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Modules Agents And Realtime API]]
- [[API Route Index]]
