---
title: "Tag Index"
description: "Reference for the frontmatter tags used by LOOM documentation."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - reference
status: draft
verified_at: "2026-07-07"
source_scope:
related:
  - "[[Reference]]"
  - "[[LOOM Documentation]]"
aliases:
  - "Documentation Tags"
---
# Tag Index

Tags make the docs searchable in Obsidian and the future LOOM docs generator.
Use lowercase kebab-case tags and avoid one-off tags unless they will be useful
across multiple pages.

## Core Tags

| Tag | Use |
|---|---|
| `loom` | Every public LOOM docs page. |
| `user-guide` | Task-oriented user pages. |
| `concepts` | Mental-model explanations. |
| `cli` | Command-line reference and examples. |
| `api` | HTTP/local-client API docs. |
| `portal` | Terminal portal surfaces and actions. |
| `operations` | Operator runbooks and production-like workflows. |
| `developer` | Development, testing, architecture, and repository docs. |
| `reference` | Lookup pages, indexes, schemas, paths, and terminology. |

## Domain Tags

| Tag | Use |
|---|---|
| `projects` | Projects, scopes, facets, contracts, and archive behavior. |
| `notes` | Notes roots, knowledge objects, extraction, search, embeddings, and projection. |
| `storage` | Box, Lane, storage views, retention, safe-delete, and file transfers. |
| `backup` | Backup coverage, backup create/verify, restore drills, and retention. |
| `cloud` | Cloud status, doctor, snapshots, retention, backend state, and remote lock behavior. |
| `automation` | Schedules, direct events, integrations, invocations, and automation center surfaces. |
| `jobs` | Jobs, runners, workers, scripts, artifacts, and background work. |
| `security` | Actors, authorization levels, policy, approvals, grants, and audit records. |
| `configuration` | Config files, environment variables, Nix options, and runtime flags. |
| `troubleshooting` | Diagnostics, support bundles, known failure modes, and recovery steps. |
| `architecture` | Developer or concept pages explaining system structure and boundaries. |
| `testing` | Test strategy, validation commands, smoke tests, and acceptance checks. |
| `database` | PostgreSQL schemas, migrations, database maintenance, and storage policy. |
| `events` | Durable events, realtime messages, communication, and event type references. |
| `modules` | Native modules, connectors, module manifests, installation, exposure, and module backups. |
| `realtime` | Topics, subscriptions, presence, notifications, progress, and leases. |
| `ai-workflow` | Codex worktree, agent planning, autopilot, handoff, and integration workflows. |

## Related Docs

- [[Reference]]
- [[LOOM Documentation]]
- `.project/protocols/DOCS_WRITING_PROTOCOL.md`
