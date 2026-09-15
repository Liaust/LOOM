---
title: "Modules Agents And Realtime API"
description: "API reference for native modules, module installations, agent-facing access, and realtime primitives."
audience:
  - developer
  - operator
  - agent
tags:
  - loom
  - api
  - modules
  - realtime
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/localclient/client.go"
  - "internal/modules/service.go"
  - "internal/agents/service.go"
  - "internal/realtime/service.go"
  - "go run ./cmd/loom --json agent worklog list --limit 5"
  - "go run ./cmd/loom --json realtime topic create test/topic --retention retained --delivery-class local"
  - "/tmp/loomdocs-v096 --json module register modules/examples/loom-project-cockpit-minimal"
  - "/tmp/loomdocs-v096 --json realtime topic create acceptance/v096docs/slice11/<timestamp>"
related:
  - "[[API Reference]]"
  - "[[Modules Agents And Realtime CLI Reference]]"
  - "[[Modules Connectors And Agents]]"
  - "[[Events Communication And Realtime]]"
  - "[[Event Types]]"
aliases:
  - "Modules API"
  - "Agents API"
  - "Realtime API"
---
# Modules Agents And Realtime API

## Envelope

Modules, agents, and realtime endpoints use the normal LOOM API envelope:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_..."
  }
}
```

Mutating endpoints also participate in idempotency where the CLI provides an
idempotency key. Reuse the same key only for retries of the same logical
request.

## Module Registry

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/modules` | GET | read-only | `loom modules list` |
| `/v1/modules/register` | POST | developer-mutating | `loom module register` |
| `/v1/modules/{ref}` | GET | read-only | `loom module inspect` |
| `/v1/module-versions/{ref}` | GET | read-only | `loom module version inspect` |

`/v1/modules/register` validates a package directory containing `module.json`
and stores module package/version declarations. It does not install the module
or expose capabilities by itself.

Slice 11 verified registration with
`modules/examples/loom-project-cockpit-minimal`. The response contained one
module version, one provider declaration, one capability declaration, one usage
document, and one backup hook.

## Module Installations

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/modules/{ref}/install` | POST | operator-mutating | `loom module install` |
| `/v1/module-installations/{ref}` | GET | read-only | `loom module installation inspect` |
| `/v1/module-installations/{ref}/enable` | POST | operator-mutating | `loom module enable` |
| `/v1/module-installations/{ref}/disable` | POST | operator-mutating | `loom module disable` |
| `/v1/module-installations/{ref}/health` | GET | read-only | `loom module health` |
| `/v1/module-installations/{ref}/providers` | GET | read-only | `loom module providers` |
| `/v1/module-installations/{ref}/capabilities` | GET | read-only | `loom module capabilities` |

Installation is separate from registration. The service checks compatibility,
target node, install scope, module version state, runtime requirements, and
operator authority before creating installation state.

## Module Capability Exposure

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/module-installations/{installation}/capabilities/{capability}/expose` | POST | sensitive operator-mutating | `loom module capability expose` |
| `/v1/module-installations/{installation}/capabilities/{capability}/disable` | POST | sensitive operator-mutating | `loom module capability disable` |

Exposure changes provider and endpoint visibility in the normal capability
registry. Disable reverses that exposure. Clients should surface provider,
capability, risk, authorization, and usage-document metadata before allowing a
human to expose a module capability.

## Module Backup Exports

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/module-installations/{ref}/backup-exports` | POST | operator-mutating | `loom module backup-export` |
| `/v1/module-installations/{ref}/backup-exports` | GET | read-only | `loom module backups` |
| `/v1/module-backup-exports/{ref}` | GET | read-only | `loom module backup inspect` |

The current source supports manifest-style module backup exports. The export
payload includes module, installation, runtime, declarations, and manifest JSON
state.

## Agent Access

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/agents/access-sessions` | GET | read-only | `loom agent access-sessions` |
| `/v1/agents/access-sessions` | POST | agent-context-mutating | `loom agent access-session create` |
| `/v1/agents/access-sessions/{ref}` | GET | read-only | `loom agent access-session inspect` |
| `/v1/agents/work-contexts` | GET | read-only | `loom agent work-contexts` |
| `/v1/agents/work-contexts` | POST | agent-context-mutating | `loom agent work-context create` |
| `/v1/agents/work-contexts/{ref}` | GET | read-only | `loom agent work-context inspect` |

Access-session creation requires an existing actor whose kind is `agent`.
During Slice 11, live list calls returned zero sessions and zero work contexts,
and no active agent actor was available for a safe full lifecycle fixture.

## Agent Tools And Worklogs

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/agents/tool-views/{ref}` | GET | read-only | `loom agent tool-view inspect` |
| `/v1/agents/work-contexts/{context}/tools/search` | POST | context read/search | `loom agent tools search` |
| `/v1/agents/work-contexts/{context}/tools/{tool}/inspect` | GET | read-only | `loom agent tool inspect` |
| `/v1/agents/work-contexts/{context}/tools/{tool}/call` | POST | capability/tool mutating | `loom agent call` |
| `/v1/agents/tool-calls/{ref}` | GET | read-only | `loom agent tool-call inspect` |
| `/v1/agents/work-contexts/{context}/worklog` | GET | read-only | `loom agent worklog list` |
| `/v1/agents/work-contexts/{context}/worklog` | POST | context-mutating | `loom agent worklog write` |

Tool search and tool calls are always work-context-bound. An agent tool may be
an operating tool or a capability-backed tool. Capability-backed tools still go
through routing, policy, approvals, grants, and audit records.

The `{context}` path segment is required. The CLI validates this before making a
request: `loom agent worklog list` or `loom agent worklog write` without
`--work-context` returns `agents.work_context_required` locally instead of
building `/v1/agents/work-contexts//worklog`.

## Realtime Topics

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/realtime/topics` | GET | read-only | `loom realtime topics list` |
| `/v1/realtime/topics` | POST | realtime-mutating | `loom realtime topic create` |
| `/v1/realtime/topics/{ref}` | GET | read-only | `loom realtime topic inspect` |
| `/v1/realtime/topics/{ref}/publish` | POST | realtime-mutating | `loom realtime topic publish` |

Valid retention modes are `retain_latest`, `retain_bounded`, and
`durable_event_only`. Valid delivery classes are `polling`, `actor_inbox`, and
`main_outbox`.

The CLI validates those enum values locally before calling the API. Direct API
callers should send the same exact constants; unsupported values are rejected by
the realtime service.

## Realtime Subscriptions

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/realtime/subscriptions` | GET | read-only | `loom realtime subscriptions list` |
| `/v1/realtime/subscriptions` | POST | realtime-mutating | `loom realtime subscription create` |
| `/v1/realtime/subscriptions/{ref}` | GET | read-only | `loom realtime subscription inspect` |
| `/v1/realtime/subscriptions/{ref}/poll` | POST | read retained publications | `loom realtime subscription poll` |
| `/v1/realtime/subscriptions/{ref}/ack` | POST | cursor-mutating | `loom realtime subscription ack` |
| `/v1/realtime/subscriptions/{ref}/cancel` | POST | realtime-mutating | `loom realtime subscription cancel` |

Slice 11 verified subscription create, poll, ack, and cancel. Poll returned the
published acceptance message; ack advanced the cursor; cancel returned
`status=cancelled`.

## Presence, Notifications, Progress, And Leases

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/realtime/presence` | GET | read-only | `loom realtime presence list` |
| `/v1/realtime/presence/{ref}` | GET | read-only | `loom realtime presence inspect` |
| `/v1/realtime/notifications` | GET | read-only | `loom realtime notifications list` |
| `/v1/realtime/notifications/{ref}` | GET | read-only | `loom realtime notification inspect` |
| `/v1/realtime/notifications/{ref}/ack` | POST | notification-mutating | `loom realtime notification ack` |
| `/v1/realtime/notifications/{ref}/dismiss` | POST | notification-mutating | `loom realtime notification dismiss` |
| `/v1/realtime/progress/{source}` | GET | read-only | `loom realtime progress inspect` |
| `/v1/realtime/leases` | GET | read-only | `loom realtime leases list` |
| `/v1/realtime/leases` | POST | lease-mutating | `loom realtime lease request` |
| `/v1/realtime/leases/{ref}` | GET | read-only | `loom realtime lease inspect` |
| `/v1/realtime/leases/{ref}/release` | POST | lease-mutating | `loom realtime lease release` |

Slice 11 verified presence list, notifications list, leases list, lease
request, and lease release. The acceptance lease returned `status=granted` and
then `status=released`.

## Error Notes

CLI-side invalid realtime topic enum values use:

- `realtime.retention_mode_invalid` for unsupported `--retention`;
- `realtime.delivery_class_invalid` for unsupported `--delivery-class`.

Direct API errors still use the standard LOOM error envelope and should be
handled by code plus the endpoint and correlation id.

## Related Docs

- [[Modules Agents And Realtime CLI Reference]]
- [[Modules Connectors And Agents]]
- [[Events Communication And Realtime]]
- [[Event Types]]
- [[Capability Addresses]]
