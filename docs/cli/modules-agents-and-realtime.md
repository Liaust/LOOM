---
title: "Modules Agents And Realtime CLI Reference"
description: "CLI reference for LOOM native module registry commands, agent-facing tool context, and realtime primitives."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - cli
  - modules
  - realtime
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 modules --help"
  - "/tmp/loomdocs-v096 module --help"
  - "/tmp/loomdocs-v096 realtime --help"
  - "/tmp/loomdocs-v096 --json module register modules/examples/loom-project-cockpit-minimal"
  - "/tmp/loomdocs-v096 --json realtime topic create acceptance/v096docs/slice11/<timestamp>"
  - "go run ./cmd/loom --json realtime topic create test/topic --retention retained --delivery-class local"
  - "/tmp/loomdocs-v096 --json realtime lease request --resource docs:<ref>"
  - "go run ./cmd/loom agent pack validate --pack-dir ai-loom-pack"
  - "internal/agentpack"
related:
  - "[[CLI Reference]]"
  - "[[Modules Connectors And Agents]]"
  - "[[Events Communication And Realtime]]"
  - "[[Modules Agents And Realtime API]]"
  - "[[Event Types]]"
aliases:
  - "Modules CLI"
  - "Realtime CLI"
  - "Agent Tools CLI"
---
# Modules Agents And Realtime CLI Reference

## What This Page Covers

This page covers two extension/runtime domains:

- native LOOM modules;
- realtime topics, subscriptions, presence, notifications, progress, and
  leases.

Agent-facing commands live partly here conceptually and partly in
[[Capabilities Security And Policy CLI Reference]] because agent tools route
through the same policy and capability systems.

## Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom modules list`, `loom module inspect`, `loom module version inspect` | read-only | Module registry inspection. |
| `loom module register` | developer-mutating | Validates and records a module package manifest. Does not install or expose by itself. |
| `loom module install`, `enable`, `disable` | operator-mutating | Changes module installation state on a node/scope. |
| `loom module providers`, `loom module capabilities`, `loom module health`, `loom module installation inspect` | read-only | Installation inspection. |
| `loom module capability expose/disable` | sensitive operator-mutating | Exposes or disables installed module capability endpoints. |
| `loom module backup-export` | operator-mutating | Creates a module backup export record/payload. |
| `loom module backups`, `loom module backup inspect` | read-only | Module backup export inspection. |
| `loom realtime topics/subscriptions/presence/notifications/leases list` | read-only | Realtime inventory. |
| `loom realtime topic create/publish`, `subscription create/ack/cancel`, `notification ack/dismiss`, `lease request/release` | operator or application mutating | Creates or changes realtime state. |
| `loom realtime subscription poll`, `topic inspect`, `presence inspect`, `progress inspect`, `lease inspect` | read-only or cursor-adjacent read | Poll reads retained publications; ack changes the cursor. |

## Modules

List modules:

```bash
loom modules list --limit 20
loom modules list --status valid
loom modules list --module-id loom.project-cockpit
```

Register a module package:

```bash
loom module register modules/examples/loom-project-cockpit-minimal
```

Slice 11 verified this registration fixture. The first run registered
`loom.project-cockpit` version `0.1.0`; `modules list`, `module inspect`, and
`module version inspect` then returned the registered module, one version, one
provider declaration, one capability declaration, and one usage document.

Registration is not installation. The fixture README explicitly states that the
minimal package validates and registers a native module manifest without
installing the module, creating providers, exposing capabilities, creating
databases, or creating filesystem namespaces.

## Module Inspection

Inspect registry records:

```bash
loom module inspect loom.project-cockpit
loom module version inspect <module-version-ref>
```

After installation, inspect runtime records:

```bash
loom module installation inspect <installation-ref>
loom module health <installation-ref>
loom module providers <installation-ref>
loom module capabilities <installation-ref>
```

The current module model separates declarations from installed runtime rows:

- module version declarations say what a package provides;
- installation rows say where it is installed;
- provider and capability rows say what has been created for that installation;
- exposure state says what is callable through normal routing.

## Module Installation And Exposure

Installation is an operator action:

```bash
loom module install <module-ref> --node main --scope system
loom module enable <installation-ref>
loom module disable <installation-ref>
```

Capability exposure is separate:

```bash
loom module capability expose <installation-ref> <capability-ref>
loom module capability disable <installation-ref> <capability-ref>
```

This split matters. A module can be registered and installed without making its
capabilities visible to normal capability routing. Exposure should happen only
after the operator has reviewed provider identity, capability risk,
authorization level, usage docs, and expected runtime behavior.

## Module Backup Exports

Module backup commands inspect or create module-owned export records:

```bash
loom module backups <installation-ref> --limit 20
loom module backup inspect <module-backup-export-ref>
loom module backup-export <installation-ref> --kind manifest
```

`backup-export` is mutating because it creates an export record. Use it only
after the installation is known and the module's backup hook behavior is
understood.

## Realtime Topics

List and inspect topics:

```bash
loom realtime topics list --limit 20
loom realtime topic inspect <topic-ref>
```

Create a bounded topic:

```bash
loom realtime topic create acceptance/v096docs/slice11/example \
  --display-name "v0.9.6 docs slice 11" \
  --scope system \
  --retention retain_bounded \
  --delivery-class polling \
  --idempotency-key v096docs-slice11-topic
```

Valid retention modes in current source include:

- `retain_latest`
- `retain_bounded`
- `durable_event_only`

Valid delivery classes include:

- `polling`
- `actor_inbox`
- `main_outbox`

The CLI validates non-empty `--retention` and `--delivery-class` values before
calling the runtime. Invalid retention returns
`realtime.retention_mode_invalid`; invalid delivery class returns
`realtime.delivery_class_invalid`. The help text lists the accepted values from
`internal/realtime/schema.go`.

## Realtime Subscriptions

Create, poll, acknowledge, and cancel a subscription:

```bash
loom realtime subscription create <topic-ref> \
  --cursor from_start \
  --delivery-target docs-slice11 \
  --idempotency-key v096docs-slice11-sub

loom realtime subscription poll <subscription-ref> --limit 5
loom realtime subscription ack <subscription-ref> --sequence 1
loom realtime subscription cancel <subscription-ref>
```

Slice 11 verified this full lifecycle against an acceptance topic:

- topic created;
- subscription created;
- publication written;
- poll returned one publication;
- ack advanced cursor to sequence `1`;
- cancel changed the subscription status to `cancelled`.

`poll` reads retained publications. `ack` changes cursor state and should be
treated as application-mutating.

## Publishing

Publish a retained message:

```bash
loom realtime topic publish <topic-ref> \
  --type docs.acceptance \
  --input '{"phrase":"v096 docs slice11 realtime"}' \
  --idempotency-key v096docs-slice11-pub
```

The input must be a JSON object. Use `--input-file` for larger payloads.

## Presence

Presence records are currently inspection-oriented through the CLI:

```bash
loom realtime presence list --limit 20
loom realtime presence list --subject-kind node
loom realtime presence inspect <presence-or-subject>
```

Slice 11 verified that live presence returned two node-oriented records.
Presence is active state, not durable truth. Use events and node records when
you need historical proof.

## Notifications

Notifications can be listed and transitioned:

```bash
loom realtime notifications list --limit 20
loom realtime notification inspect <notification-ref>
loom realtime notification ack <notification-ref>
loom realtime notification dismiss <notification-ref>
```

Ack and dismiss mutate notification state. Use filters such as `--status`,
`--category`, `--target-kind`, `--target-ref`, and `--approval` when searching
for a specific notification.

## Progress

Progress feeds are read through a source ref:

```bash
loom realtime progress inspect <source> --limit 20
```

Progress is live operational state. For completed jobs and long-running work,
cross-check the relevant job, worker, event, route, or capability-call record.

## Leases

List and inspect leases:

```bash
loom realtime leases list --limit 20
loom realtime lease inspect <lease-ref>
```

Request and release a short lease:

```bash
loom realtime lease request \
  --resource docs:slice11-example \
  --mode exclusive \
  --duration 30s \
  --holder-kind system \
  --holder-ref docs-slice11 \
  --scope system \
  --idempotency-key v096docs-slice11-lease

loom realtime lease release <lease-ref> \
  --idempotency-key v096docs-slice11-release
```

Slice 11 verified a lease request and release. The request returned
`status=granted`; the release returned `status=released`.

Valid lease holder kinds include `actor`, `node`, `job`, `route`,
`capability_call`, `provider`, and `system`. Valid modes include `read`,
`shared`, `write`, `control`, and `exclusive`.

## External AI LOOM Pack

The local `loom agent pack` subgroup inspects and validates instructions for
external Codex-style agents and scaffolds the Morathustra workspace template.
It does not install skills, contact `loomd`, or create LOOM agent actors,
access sessions, work contexts, tool views, or authorization grants.

Inspect and validate the release-matched pack:

```bash
loom agent pack status
loom agent pack validate
```

Catalogue status and recommended skill sets are static metadata. Deploy a
reviewed selection through Codex's native skills locations or installer, or
through Orca's native skill/plugin mechanism. LOOM stores no installed-file
registry and performs no profile selection, conflict reconciliation, stale-file
removal, update, or uninstall operation.

The separate `scaffold-workspace` command remains available because it creates
the Morathustra workspace template, not a harness skill installation.

## Agent Commands

Runtime agent access commands are documented in
[[Capabilities Security And Policy CLI Reference]] because their behavior
depends on actor kind, work context, capability visibility, policy decisions,
approvals, and grants. The current live system did not have an active
`actor_kind=agent` actor available for a full fixture during Slice 11.

## Related Docs

- [[Modules Connectors And Agents]]
- [[Events Communication And Realtime]]
- [[Modules Agents And Realtime API]]
- [[Event Types]]
- [[Capabilities Security And Policy CLI Reference]]
