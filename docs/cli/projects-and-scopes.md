---
title: "Projects And Scopes CLI Reference"
description: "Current project creation, development context, declaration operations and physical lifecycle entry points."
audience: [user, operator, developer, agent]
tags: [loom, cli, projects]
status: draft
verified_at: "2026-09-15"
source_scope: [internal/loomcli/project_contracts.go, internal/loomcli/project_apply.go, internal/loomcli/project_physical_archive.go]
related:
  - "[[CLI Reference]]"
  - "[[Projects]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Projects Portal]]"
aliases: ["Projects And Scopes", "Project CLI", "Scope CLI"]
---
# Projects And Scopes CLI Reference

Use the singular `loom project` command tree. These examples contain placeholders;
replace them with values from your own project and command results.

## Create And Read Context

```sh
loom project create <name> --backend --owner-node main --dry-run
loom project create <name> --backend --owner-node main
loom project list
loom project context <project-ref-or-path>
loom project inspect <project-ref>
loom project validate <project-ref> --backend
```

Creation produces ordinary `notes/`, `repos/`, project-wide `.project/` context
and `.loom/project.yaml`. It does not initialize Git or activate resources.
With `--backend`, the same identity is registered on the configured backend;
without it, creation is local source-only. The backend flag does not select an
arbitrary machine: use the configured owner and connection.

A `source_created/context_pending` result preserves the created source and
identity. Resolve the reported prerequisite and retry; do not delete edited
context or create a second project.

## Plan, Apply And Inspect

```sh
loom project plan <project-ref>
loom project apply <project-ref> --plan-id <reviewed-plan-id> --idempotency-key <request-key>
loom project status <project-ref>
loom project operation <operation-id>
```

Plan is read-only. It reports resolved resource owners, intended effects and
prerequisites. Apply is mutating and requires the exact reviewed plan and a
stable request key. Use its returned next command for approvals or resumption
of a partial operation; preserve the original request identity after a lost
response. Status and operation inspection do not advance work.

For current declarations, `status` means readiness, not the older registration
inventory. Development context remains available even if deployment is blocked.
Application installation, public exposure and credential delivery need their
actual configured prerequisites; source validation alone does not prove them.

## Repositories

```sh
loom project repos list <project-ref>
loom project repos inspect <project-ref> <repository-ref>
loom project repos status <project-ref>
```

These inspect explicit members, not every nested Git folder. List/status support
bounded `--limit` and `--after` pagination. Declaration paths are relative to the
project; legacy member paths are relative to the `repos/` facet. Git/worktree
observations and optional legacy `.repo/` state retain their freshness and
availability qualifications.

## Physical Archive And Inactive Restore

```sh
loom project archive plan <project-ref> --reason "completed"
loom project archive inspect <project-ref>
loom project archive restore plan <project-ref> --reason "return files for review"
```

Applying a physical archive or restore takes the project reference, reviewed
plan JSON file, exact digest and explicit confirmation:

```sh
loom project archive apply <project-ref> <plan-json-file> --plan-digest <digest> --yes
loom project archive restore apply <project-ref> <plan-json-file> --plan-digest <digest> --yes
```

Use the operation's own recovery instructions after an interruption. Never use
the retired bare archive writer as a fallback. Restored files do not imply
restarted services, released fences or active schedules.

## Export And Compatibility

`loom project export` creates a portable copy; it does not archive, deactivate
or publish a project. See the [project guide](../user-guide/projects.md) for
export modes and custody.

Older `scaffold`, `facet add`, `migrate-layout` and registration surfaces remain
for existing facet-based projects. New projects need no preset or facet-selection
step. Inspect the existing source before using a legacy command.

`loom scope list` and `loom scope inspect` read lower-level scope records.
Creating a bare scope is not equivalent to creating a project source tree.
Use `loom project create` for normal project work.
