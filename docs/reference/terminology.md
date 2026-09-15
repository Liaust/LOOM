---
title: "Terminology"
description: "Definitions for LOOM storage, runtime, recovery, projects, providers, and operational state."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - reference
  - concepts
status: verified
verified_at: "2026-08-30"
source_scope:
  - "AGENTS.md"
  - "internal/storagecatalog/models.go"
related:
  - "[[Box Storage And Lane]]"
  - "[[Architecture]]"
  - "[[Command Index]]"
aliases:
  - "Glossary"
---
# Terminology

## System And Runtime

- **LOOM**: the system name.
- **ANET**: the technical class; **AOS** is legacy terminology.
- **node**: a runtime participant. Main coordinates global policy, catalog,
  archive, discovery, and knowledge; local nodes own live local state.
- **provider**: a hosted implementation that exposes capabilities.
- **capability**: a typed, permissioned, routed, logged, and revocable action.
- **event**: durable truth about an occurrence. A message is transport; a log
  is operational detail.
- **worker**: durable scheduled/queued execution with explicit status.
- **runtime state**: volatile operational data owned by a node, not by a user
  source tree.

## Filesystem And Storage

- **Box**: human-owned workspace containing editable Documents, Notes,
  Projects, profile-local workspace intake, and durable `.loom/` contracts.
- **project-local `.loom/`**: portable project contract/guidance root. It is not
  Box runtime state.
- **Lane**: deliberate workspace Box-to-main batch transfer with file-tree or
  bundle transport and one acceptance/cleanup lifecycle. Main receives it in
  Storage Imports and has no Lane input surface.
- **Dropzone**: retired upload/custody surface. Only bounded read-only decoding
  of historical records remains; there is no active folder, worker, Portal
  section, search alias, or mutation action.
- **live source**: the location intentionally edited by a user/application.
- **canonical custody**: authoritative physical accepted location on main.
- **protection copy**: internal retained copy used for loss/deletion protection,
  such as an object blob or retention payload.
- **operational backup**: verified database/filesystem recovery snapshot.
- **cloud snapshot**: verified off-site copy of an operational backup.
- **generated artifact**: reproducible derived data, such as Notes projection.
- **storage catalog**: authoritative identity, lineage, checksum, availability,
  retention, and physical-reference model.
- **physical reference**: catalog evidence locating a source, custody, or
  protection copy.
- **view path**: stable human/catalog presentation path; not necessarily a
  materialized filesystem tree.
- **storage export**: retired generated filesystem tree. A read-only deprecated
  diagnostic remains temporarily; there is no active writer/repair.
- **manifest commit marker**: final `manifest.json` proving a custody batch/key
  is complete and immutable for backup traversal.

## Safety And Lifecycle

- **safe-delete**: typed decision that required retained evidence protects a
  source before deletion.
- **partially safe**: payload retained but known safe-view/fidelity differences
  remain.
- **filesystem fidelity**: observations about metadata/content differences
  among source and copies.
- **retention**: protected content kept under a policy after source lifecycle
  changes.
- **tombstone**: catalog record of deliberate source deletion/lifecycle state.
- **quarantine**: recoverable cleanup destination; distinct from immediate
  deletion.
- **attention**: persisted incomplete/failed state with inspect/repair/
  acknowledge/archive actions.
- **fail closed**: refuse mutation or success when identity, path, evidence, or
  configuration cannot be proven.
- **idempotent retry**: repeating the same operation identity produces the same
  completed state without duplicating bytes/catalog effects.

## Migration

- **migration input**: deprecated old path retained only for inventory and
  compatibility evidence.
- **reviewed manifest**: deterministic integrity-checked description of catalog
  rebinds and byte-move evidence. It does not move bytes.
- **cutover**: maintenance-window sequence that drains writers, moves bytes,
  applies catalog/config changes, verifies, and preserves rollback boundaries.
- **rollback**: reviewed inverse operation; not an unplanned copy or blind Nix
  generation switch.

## Projects And Agents

- **project**: a scope, not a universal parent of all objects.
- **repo**: project-owned code workspace, not a capability by itself.
- **skill**: instruction package for an agent, not directly executable code.
- **agent workspace**: working directory under the configured agents root;
  ORCA owns its lifecycle.
- **acceptance fixture**: bounded test material under a temporary root or
  `.loom-acceptance`, never loose production scratch data.

## Related Docs

- [[Box Storage And Lane]]
- [[Architecture]]
- [[Command Index]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

This page defines the terms used throughout the LOOM docs. Later concept pages
will expand these definitions into full mental models.

### Core Terms

LOOM:
The system name. LOOM is the implemented local-first automation, knowledge,
project, storage, and operations system.

ANET:
The technical class of system LOOM belongs to. ANET is not the product name.

AOS:
Legacy terminology. Current docs should prefer LOOM unless historical context
requires the old term.

Node:
A LOOM runtime participant. The main node coordinates global policy, archive,
discovery, and knowledge. Workspace nodes expose local state and capabilities.

Main:
The coordinating LOOM node. Main owns global coordination, policy, archive,
discovery, and knowledge.

Provider:
A runtime component that exposes one or more capabilities.

Capability:
A typed, permissioned action exposed by a provider. Capability calls are routed,
logged, and revocable.

Project:
A scope for work. Projects can have facets, contracts, notes, scripts, and
archive behavior, but projects are not universal parents for every LOOM object.

Facet:
A project capability bundle such as scripts or notes. Facets describe what a
project exposes and how LOOM should manage the related files and runtime
surfaces.

Contract:
Structured project metadata that records project shape, facets, watched roots,
and related operational intent.

Event:
Durable truth. Events record state transitions and causality.

Message:
Transport state for communication between nodes. Messages are not the same as
durable domain events.

Job:
An executable unit of work. Scripts and workflows run through jobs and
capabilities.

Worker:
A background process that performs queued or periodic work.

Object:
Content or metadata that LOOM can ingest, retain, index, search, or reference.

Knowledge Index:
The notes/search indexing surface that tracks notes roots, objects, extraction,
chunks, lexical search, embeddings status, and projection state.

LOOM Box:
The user-visible local workspace root, normally `~/loom-box`.

LOOM Lane:
The workspace-only transfer area under the Box root, normally
`~/loom-box/loom-lane`. Main receives accepted content in Storage Imports.

LOOM Storage:
Canonical physical imports/backup/archive custody, exposed read-only to users,
plus typed catalog paths and explicitly generated projections.

Module:
A native LOOM-owned application built on LOOM primitives.

Connector:
A LOOM-owned wrapper around an external app, service, device, or protocol.

Agent:
A software actor using scoped LOOM tool access. Agents should call capabilities
instead of raw shell inside LOOM itself.

### Related Docs

- [[LOOM Architecture]]
- [[Nodes Providers Capabilities]]
- [[Projects Facets And Contracts]]
- [[Notes Knowledge Index]]
- [[Security Authorization And Policy]]
