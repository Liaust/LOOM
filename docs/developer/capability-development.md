---
title: "Capability Development"
description: "How to add or change LOOM providers, capabilities, runtime bindings, routes, policy checks, and module capability exposure."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/capabilities"
  - "internal/routing"
  - "internal/capabilityruntime"
  - "internal/policy"
  - "internal/modules"
related:
  - "[[Nodes Providers Capabilities]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Capability Addresses]]"
  - "[[Security Authorization And Policy]]"
aliases:
  - "Developing Capabilities"
---
# Capability Development

## What This Page Covers

Capabilities are the callable surface of LOOM. They must stay typed,
permissioned, routed, logged, and revocable. This page explains the developer
path for adding or changing providers, capabilities, runtime bindings, routes,
and policy behavior.

## Main Packages

| Package | Role |
|---|---|
| `internal/capabilities` | Provider registry, endpoint definitions, versions, usage docs, provider advertisements, addresses. |
| `internal/routing` | Capability calls, routes, runtime dispatch, runtime bindings, command/HTTP/script/workflow executors. |
| `internal/capabilityruntime` | Runtime binding configuration helpers. |
| `internal/policy` | Authorization levels, decisions, approvals, grants. |
| `internal/agents` | Agent access sessions, work contexts, tool views, and tool calls. |
| `internal/modules` | Module declarations, installation, provider/capability exposure, backup exports. |

## Address Model

Capability and provider addresses should follow the current parser in
`internal/capabilities/address.go`. Use [[Capability Addresses]] for the
documented syntax.

Do not introduce a new address shorthand without updating parser tests, CLI
docs, and portal rendering.

## Adding A Capability

A normal capability addition should define:

1. provider identity and scope;
2. capability endpoint key and class;
3. input/output schema expectations;
4. authorization level and risk;
5. usage docs;
6. runtime binding or module exposure path;
7. policy behavior;
8. route/call audit behavior;
9. tests and docs.

If the capability can mutate user data, run jobs, contact external systems, or
perform filesystem/network operations, treat it as sensitive until policy and
dry-run behavior are clear.

## Runtime Bindings

Runtime bindings connect endpoint versions to executable runtimes. Current
runtime executors include command, HTTP, script, workflow, declarative/system,
and remote/node-agent paths.

When adding a binding kind or changing binding behavior:

- update schema and model validation;
- update runtime executor tests;
- preserve structured errors;
- keep execution logs and route/call records useful;
- document how dry-run behaves.

## Policy And Approval

Policy should be checked before dispatch. Policy decisions should be auditable,
and approval/grant paths should be explicit.

Use existing authorization levels and actor/node scoping. Do not bypass policy
because a command is local, a portal action is convenient, or a test fixture is
trusted.

## Module Capability Exposure

Modules separate declaration, installation, and exposure:

- module manifests declare providers and capabilities;
- installation records say where the module is installed;
- exposure records connect installed module capabilities to normal capability
  routing.

Do not expose a module capability automatically unless the feature plan
explicitly requires it and policy has been reviewed.

## Tests

Recommended focused tests:

```bash
go test ./internal/capabilities ./internal/routing ./internal/capabilityruntime
go test ./internal/policy ./internal/agents ./internal/modules
go test ./internal/httpapi ./internal/localclient ./internal/loomcli
```

For user-facing behavior, add safe CLI checks:

```bash
go run ./cmd/loom capabilities list --help
go run ./cmd/loom --json capabilities list --limit 5
go run ./cmd/loom --json capability call <capability-ref> --dry-run --input '{}'
```

Use real calls only against accepted fixtures or explicit operator-approved
targets.

## Related Docs

- [[Nodes Providers Capabilities]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Capability Addresses]]
- [[Security Authorization And Policy]]
- [[Modules Connectors And Agents]]
