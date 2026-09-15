---
title: "LOOM Architecture"
description: "Explains LOOM as a local-first distributed operating substrate and how its main layers fit together."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - concepts
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
  - "internal/httpapi/server.go"
  - "go run ./cmd/loom capabilities --help"
  - "go run ./cmd/loom policy --help"
  - "go run ./cmd/loom modules --help"
  - "go run ./cmd/loom realtime --help"
related:
  - "[[Concepts]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Events Communication And Realtime]]"
  - "[[Security Authorization And Policy]]"
  - "[[Modules Connectors And Agents]]"
  - "[[Command Index]]"
  - "[[API Route Index]]"
aliases:
  - "LOOM System Architecture"
  - "LOOM Mental Model"
---
# LOOM Architecture

## What This Page Covers

This page explains the system-level shape of LOOM: what kind of system it is,
why the main node matters, and how nodes, capabilities, events, storage,
security, modules, and agents connect.

Read this page before the deeper concept docs when you need the whole mental
model instead of one feature workflow.

## The Short Version

LOOM is a local-first, capability-based distributed operating substrate for a
single owner. It is not only a terminal app, note index, backup tool, automation
tool, or AI assistant. Those are applications that sit on top of the substrate.

LOOM coordinates:

- nodes and the providers they host;
- typed capabilities and routed capability calls;
- projects, scopes, contracts, and facets;
- events, jobs, workers, scripts, and workflows;
- files, object metadata, storage views, notes, indexes, search, and backups;
- actors, authorization levels, approvals, grants, and policy decisions;
- modules, connectors, and agent-facing tool access.

The current repo exposes these areas through CLI commands, HTTP route groups,
services under `internal/`, and the terminal portal. Later docs explain each
domain from a user, operator, CLI, API, and developer angle.

## Core Execution Model

The central architecture model is:

```text
Scope -> Node -> Provider -> Capability
```

A node does not expose work directly. A node hosts providers, and providers
expose capabilities. A capability is a typed action with metadata such as
address, risk, authorization level, runtime binding, status, and documentation.

The compact address format is source-verified in `internal/capabilities`:

```text
scope/path@provider.capability.name
```

Examples from the architecture notes include:

```text
main@compute.job.run
main@communications.email.send
personal/computers/macbook@filesystem.file.read
```

The provider part identifies who exposes the action. The capability part
identifies what action is being requested. The scope path keeps the action tied
to the correct conceptual or execution context.

See [[Nodes Providers Capabilities]] for the detailed model.

## Main Node And Workspace Nodes

The main node is the authoritative coordination layer. It owns or coordinates
global registries, policy, archive, discovery, knowledge, scheduling, indexes,
and routes. Workspace nodes expose local state and local providers. In the
current Mac/main setup, many user workflows are launched from the Mac while
acting against main-owned state.

Main is not intended to be a temporary relay with no memory. It is the system
root for global LOOM coordination. At the same time, LOOM is not intended to be
hard-coded to one deployment shape. The architecture notes explicitly treat
UTM, macOS, VPS, and bare-metal hosts as deployment profiles, not core
assumptions.

Target network details such as WireGuard and Caddy are architectural direction.
Treat them as target infrastructure unless a later operations page verifies a
specific deployment step for the current hardware.

## Durable State Layers

LOOM separates state into layers:

- the filesystem and object store hold bytes, files, artifacts, and package
  files;
- PostgreSQL holds identity, state, metadata, relationships, permissions, and
  history on database-capable nodes;
- events preserve durable truth about meaningful changes;
- indexes accelerate keyword, full-text, and semantic retrieval;
- queues, inboxes, outboxes, and communication messages move work between
  components.

The strongest rule from the architecture notes is:

```text
Filesystem stores content.
Database stores identity, structure, state, permissions, and history.
Indexes accelerate retrieval.
Queues handle communication.
Events preserve durable truth.
```

This matters for user workflows. A file in a notes root, a project contract, a
backup snapshot, and a job record are not all the same kind of object. They may
touch several layers at once.

## Control Plane And Data Plane

LOOM has a control plane and a data plane.

The control plane covers capability requests, jobs, events, approvals, routes,
node status, policy decisions, provider advertisements, and realtime status. The
data plane covers files, artifacts, streams, backups, datasets, and large
payloads.

The current architecture routes coordination through main. Direct node-to-node
coordination is not the default model. If later docs describe direct
node-to-node behavior, they must mark exactly which implementation supports it.

## Current Implemented Surfaces

Slice 3 verified that the current repo exposes architecture domains through
these public surfaces:

- `loom capabilities --help` lists capability registry commands.
- `loom policy --help` lists policy decision and explanation commands.
- `loom modules --help` lists native module package commands.
- `loom realtime --help` lists realtime primitives such as topics,
  subscriptions, presence, notifications, progress, and leases.
- `internal/httpapi/server.go` registers services and route groups for events,
  capabilities, policy, communication, realtime, agents, modules, and the other
  LOOM domains.

That verification means these docs can describe the surfaces as present. It
does not mean every route or workflow example has been tested yet. Domain pages
must still verify concrete commands, request bodies, portal actions, and
workflow results before marking examples as working.

## Current, Future, And Deferred

Current:

- main/workspace node architecture;
- CLI and HTTP surfaces for many LOOM domains;
- capability, policy, module, realtime, events, storage, notes, backup,
  project, automation, jobs, and node-management packages in the repo;
- docs-writing workflow that tests documented behavior and records bugs.

Future or deferred:

- fully local-first autonomous workspace nodes with offline sync for every
  domain;
- broad direct node-to-node capability or file transfer behavior;
- complete LOOM-owned note-taking module behavior;
- complete module ecosystem and external connector library;
- docs site generator behavior beyond the source Markdown conventions defined
  in [[Tag Index]] and the internal documentation protocol.

## Healthy Interpretation

When LOOM is behaving coherently, a user or operator should be able to start
from a high-level workflow and trace it through the layers:

- user action: create a project, search notes, run a script, inspect backup;
- CLI or portal action: a human-friendly surface calls a command or API route;
- service layer: a domain service validates and performs the request;
- policy layer: risky work is checked, approved, granted, or denied;
- durable records: events, jobs, object metadata, indexes, or backup records
  change;
- status surfaces: the portal, CLI, and API show the new state.

If those layers disagree, treat the disagreement as a product or operations
bug. During docs work, record it in the active bug report instead of documenting
the broken behavior as expected.

## Related Docs

- [[Nodes Providers Capabilities]]
- [[Events Communication And Realtime]]
- [[Security Authorization And Policy]]
- [[Modules Connectors And Agents]]
- [[Terminology]]
- [[Command Index]]
- [[API Route Index]]
