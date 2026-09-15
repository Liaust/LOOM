---
title: "Repository Map"
description: "Source ownership and navigation for the public LOOM repository."
audience: [developer, agent]
tags: [loom, developer, architecture]
status: draft
verified_at: "2026-09-15"
source_scope: [cmd, internal, nix, tests, docs]
related:
  - "[[Architecture]]"
  - "[[Local Development]]"
  - "[[Testing]]"
---
# Repository Map

Start with the package that owns the behavior, not with a private deployment
runbook. Contributors do not need a maintainer's machines or task accounts.

## Top Level

| Path | Responsibility |
|---|---|
| `cmd/` | Binary entry points: CLI, daemon, node agent and bounded helpers. |
| `internal/` | Go implementation and package tests. |
| `migrations/` | Core PostgreSQL schema history. |
| `nix/modules/`, `nix/packages/` | Service integration and pinned dependencies. |
| `nix/hosts/`, `nix/profiles/` | Example host configurations; configure your own hardware and identities. |
| `nix/locks/` | Upstream compatibility metadata required by package definitions. |
| `ai-loom-pack/` | LOOM-owned agent skills and optional workspace templates. |
| `schemas/`, `examples/`, `modules/` | Contracts, examples and module source. |
| `scripts/`, `tests/` | Helpers, fixtures, focused acceptance and historical regression scripts. |
| `docs/` | Human documentation, references and operator guides. |
| `.project/` | Public implementation state, roadmap and documentation guidance. |

The clean public baseline intentionally excludes private machine runbooks,
old internal planning, task-account mappings and operational receipts. Those
are not prerequisites for building LOOM. Historical test comments may mention
the development checkpoints that introduced a regression.

## Find The Owner

| Behavior | Main packages |
|---|---|
| API and CLI | `httpapi`, `localclient`, `loomcli`, `loomcli/portal` |
| Runtime composition | `loomdapp`, `config`, `nodeagent` |
| Capabilities and execution | `capabilities`, `routing`, `policy`, `jobs`, `workers`, `automation` |
| Project declarations | `projectcontracts`, `projectapply`, `projectstate`, `projects` |
| Application deployment | `serviceregistry`, `preparation`, `projectapply` |
| Files and transfer | `filesystemlayout`, `box`, `lane`, `filetransfer`, `watchedroots` |
| Storage custody | `storagecatalog`, `storagearchive`, `storagefidelity`, `storageretention` |
| Backup and recovery | `backupcoverage`, `maintenance`, `cloudstorage`, `backup`, `restoreauthority` |
| Source retrieval | `knowledge`, `notesprojection`, `objects`, `objectstore` |
| Semantic decisions and context | `provenance`, `projectstate`, `repostate` |
| Installation and updates | `setup`, `update`, `bootstrapssh` |
| Agent source packages | `agentpack`, `projectcontracts`, plus `ai-loom-pack/` |

Package names are relative to `internal/`. Compatibility readers for retired
storage views or project layouts are not the ownership model for new features.

## Development And Dependencies

Keep entry points small. Change the owning package and relevant focused tests;
include its API/client/CLI callers when a contract changes. See
[Contributing](../../CONTRIBUTING.md) for the normal workflow.

Hermes owns model sessions, its gateway, memory and native tool execution.
ORCA owns its workspace and terminal interface. LOOM supplies the integration,
skills, configuration and durable system services; it does not replace their
upstream source ownership. See [Third-party notices](../../THIRD_PARTY_NOTICES.md).

Use [Local Development](local-development.md) for builds,
[Testing](testing.md) for relevant checks and
[Installation](../operations/installation.md) for manual host configuration.
