---
title: "Developer Documentation"
description: "Find LOOM's implementation, contribution workflow and extension interfaces."
audience: [developer, agent]
tags: [loom, developer]
status: draft
verified_at: "2026-09-15"
source_scope: ["AGENTS.md", "CONTRIBUTING.md"]
related: ["[[LOOM Architecture]]", "[[Repository Map]]", "[[Testing]]"]
aliases: ["LOOM Developer Docs"]
---

# Developer Documentation

Start with [CONTRIBUTING.md](../../CONTRIBUTING.md). You do not need the
maintainer's machines, Basecamp account or a named agent to contribute.

## Orientation

- [Architecture](architecture.md): implemented software structure.
- [Repository map](repository-map.md): commands, packages and configuration owners.
- [Local development](local-development.md): building and focused checks.
- [Testing](testing.md): test dependencies and interpretation.
- [AI/worktree development](ai-worktree-development.md): optional parallel work.
- [Project/repository development state](repository-development-state.md):
  portable project context and legacy compatibility.

## Implementation Areas

[Migrations](migrations.md), [capabilities](capability-development.md),
[HTTP API](api-development.md), [CLI](cli-development.md),
[Portal](portal-development.md) and [Provenance](provenance-runtime.md)
describe their respective owners. [Release flow](release-flow.md) distinguishes
source publication from deploying an operator's installed system.

The public [.project state](../../.project/STATE.md) and
[roadmap](../../.project/ROADMAP.md) describe current direction. Private historic
feature folders and deployment receipts are not prerequisites in this source
baseline. Some older reference pages still need deeper verification; preserve
their draft status and report specific discrepancies.
