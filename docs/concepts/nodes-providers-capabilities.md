---
title: "Nodes Providers Capabilities"
description: "Explains how LOOM exposes work through nodes, providers, capability addresses, and routed capability calls."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - concepts
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
  - "internal/capabilities/address.go"
  - "internal/capabilities/models.go"
  - "internal/capabilities/seed.go"
  - "internal/httpapi/server.go"
  - "go run ./cmd/loom capabilities --help"
related:
  - "[[LOOM Architecture]]"
  - "[[Security Authorization And Policy]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Capabilities Policy Security And Routes API]]"
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Nodes Sync And Watched Roots API]]"
aliases:
  - "Capabilities"
  - "Providers"
  - "Capability Model"
---
# Nodes Providers Capabilities

## What This Page Covers

This page explains the LOOM execution model:

```text
Scope -> Node -> Provider -> Capability
```

It is the page to read when you need to understand why LOOM does not simply
give every script, agent, or portal action direct shell access.

## Mental Model

A node is a participating runtime, such as main, a Mac workspace node, a future
mobile node, a hardware node, or another registered runtime.

A provider is a component hosted by a node. Providers are the runtime owners of
specific actions. Examples from the current model include system providers,
filesystem providers, script runners, workflow runners, connector providers,
module providers, hardware providers, agent providers, and service providers.

A capability is a concrete typed action exposed by a provider. It has a form,
risk level, authorization level, status, runtime binding, and routeable address.

The important boundary is:

```text
A node hosts providers.
Providers expose capabilities.
Capabilities are called through LOOM.
```

## Capability Addresses

The current source parses capability addresses in this shape:

```text
scope/path@provider.capability.name
```

`internal/capabilities/address.go` validates that:

- the scope path is slash-separated lowercase slug segments;
- the provider key is a lowercase slug without dots;
- the capability name is dot-separated lowercase snake-case segments.

Example:

```text
main@system.status.read
```

This is not just a naming convention. It allows LOOM to reason separately about
the scope, provider, and action before the request reaches the runtime.

## Capability Metadata

The current capability model includes these major dimensions:

- provider type, provider status, provider health, and availability;
- capability form, such as query, command, job, session, stream,
  subscription, or lease;
- risk level, such as low, medium, high, or critical;
- endpoint status and version status;
- runtime kind, such as script, command, HTTP, node-agent, native, module,
  workflow, or external process;
- execution authorization level from 1 to 5.

The seed data in `internal/capabilities/seed.go` includes examples such as:

- `main@system.health.read`
- `main@system.status.read`
- `main@object_store.object.inspect`
- `main@object_store.object.ingest`
- `main@script_runner.script.register`
- `main@script_runner.script.run`
- `main@admin.policy.explain`
- `main@admin.approval.decide`
- `main@admin.grant.revoke`

Those examples show why capability calls need policy context. Reading system
status and running a script are not the same risk.

## Registry And Discovery

The current CLI exposes capability discovery through:

```bash
loom capabilities --help
```

Slice 3 verified that this command group currently includes:

- `loom capabilities list`
- `loom capabilities search`

The HTTP API has route groups for providers, provider advertisements,
capabilities, capability search, runtime bindings, routes, policy, approvals,
grants, and capability calls. The index page maps those groups in
[[API Route Index]].

Use the CLI and API reference pages for exact examples. This concept page only
explains what the registry means.

## Capability Calls

A healthy capability call should be:

- typed;
- permissioned;
- routed;
- logged;
- revocable;
- tied to actor identity;
- tied to node and provider identity;
- tied to a job or event trail when the action is meaningful.

That is why LOOM prefers capabilities over raw local commands for system
behavior. A raw shell command can mutate state, but LOOM cannot reason about
the action unless it is represented through a registered surface.

## Provider Advertisements And Remote Nodes

Remote or local node agents can advertise providers and capabilities. The main
node can receive, validate, approve, reject, and register those surfaces. This
lets LOOM grow without hard-coding every provider into main.

Current docs should avoid claiming that every future provider type is fully
operational. The safe claim is that the model, schemas, route groups, and CLI
surfaces exist; each provider workflow needs its own tested doc before being
presented as ready.

## Agent Tool Visibility

LOOM should not expose thousands of tools directly to agents. The architecture
model gives agents a small permanent operating toolbelt plus contextual
capabilities from the current scope, project, mounts, or grants.

Capability mounts are part of the architecture direction: a mount gives a scope
an alias to a real endpoint elsewhere in the network. Treat mounts as future or
deferred unless a later CLI/API page verifies the current implementation in a
specific workflow.

## User Impact

For a user, this model should eventually make workflows safer and clearer:

- portal actions can show what they will call;
- policy can explain why a request is allowed, denied, or waiting for approval;
- jobs and events can show what happened after a capability call;
- archived or disabled project contexts can suppress related actions;
- agents can be given a narrow set of tools instead of broad host access.

See [[Security Authorization And Policy]] for how authorization fits into this
model.

## Related Docs

- [[LOOM Architecture]]
- [[Security Authorization And Policy]]
- [[Command Index]]
- [[API Route Index]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Capabilities Policy Security And Routes API]]
