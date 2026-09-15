---
title: "Projects Scopes And Contracts API"
description: "HTTP route guide for project records, repository state, source contracts, layout migration, facets, scopes, and archive state."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - projects
status: verified
verified_at: "2026-08-30"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/httpapi/projects_test.go"
  - "internal/localclient/client.go"
  - "internal/localclient/client_test.go"
  - "internal/httpapi/project_repositories.go"
  - "internal/localclient/project_repositories.go"
  - "tests/smoke/v2_project_repository_state_local.sh"
related:
  - "[[API Reference]]"
  - "[[Projects]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Projects And Scopes CLI Reference]]"
aliases:
  - "Project API"
  - "Scopes API"
---
# Projects Scopes And Contracts API

## Scope

These private routes support the CLI and Portal. Prefer their typed clients
instead of treating the route list as a public unauthenticated HTTP API.

## Route Groups

| Route | Purpose |
|---|---|
| `/v1/scopes` | List and create scopes. |
| `/v1/scopes/{ref}` | Inspect one scope. |
| `/v1/projects` | List and create project records. |
| `/v1/projects/{ref}` | Inspect one project. |
| `/v1/projects/{ref}/repos` | List explicit repository members. |
| `/v1/projects/{ref}/repos/status` | Read bounded repository observation status. |
| `/v1/projects/{ref}/repos/inspect/{repository-ref}` | Inspect one explicit member by ID or key. |
| `/v1/project-scaffolds` | Scaffold a canonical backend project. |
| `/v1/project-facet-additions` | Add facets to backend project source. |
| `/v1/project-layout-migrations` | Dry-run or apply canonical source-layout migration. |
| `/v1/project-contract-analyses` | Analyze resolved backend project source. |
| `/v1/project-contract-registrations` | Register explicit analyzed input. |
| `/v1/project-contract-registrations/from-backend` | Analyze and register backend source. |
| `/v1/project-contract-registrations/{ref}` | Inspect registration and facets. |
| `/v1/projects/{ref}/watch-plan` | Compile watched-root intent. |
| `/v1/projects/{ref}/watch-policy/apply` | Apply watched-root desired state. |
| `/v1/projects/{ref}/sync-status` | Inspect project sync status. |
| `/v1/projects/{ref}/backup-status` | Inspect project backup status. |
| `/v1/projects/{ref}/archive` | Dry-run or apply project archive. |
| `/v1/projects/{ref}/archive/inspect` | Inspect runtime archive state. |
| `/v1/projects/{ref}/archive/restore` | Plan restore. |
| `/v1/projects/{ref}/archive/migrate-runtime` | Plan runtime migration. |

## Standard Envelopes

Successful typed-client responses use the normal LOOM envelope with `ok`,
`data`, and `meta`. Failures use structured error codes and correlation
metadata. Unknown JSON fields are rejected on the layout-migration route.

## Repository Read Surfaces

The repository routes are `GET`-only. They first resolve authenticated request
context, authorize the actor and origin node against one canonical project ID,
and only then perform local observation. A denial therefore precedes source,
`.repo`, or Git access.

List and status accept `limit` and `after`; the default is 50 and the maximum
is 200. Results are ordered by stable repository ID and expose a bounded source
revision, lifecycle, role, relative member path, observation posture, optional
reason, `.repo` posture, and Git summary. Inspect does not accept pagination.
Full source contracts and file content are never returned.

The routes remain readable for archived projects, but they do not create a
repository mutation surface. A remote-owned member that cannot be observed
through the current backend returns `remote_unavailable` without raw SSH or
local path inference.

## Layout Discovery

Backend analysis resolves `.loom/project.yaml` first, while remaining compatible
with `loom.project.yaml`. Its loaded project data reports:

- `root_path`;
- resolved `contract_path`;
- `layout`;
- canonical and legacy discovery paths and presence;
- the parsed contract and raw source.

Equivalent duplicate root contracts resolve to canonical with a warning.
Divergent duplicates fail analysis with `contract.layout_conflict`.

## Layout Migration Request

The typed request uses:

```json
{
  "project_ref": "example-project",
  "apply": false
}
```

`project_root` may be used by trusted callers when it resolves under the
backend Box Projects parent. Dry-run is represented by `apply: false` and is
the default. Apply must send both `apply: true` and `yes: true`.

The response includes before/after layout, ordered path actions, collisions,
preserved skips, a record path after apply, and backend-aware follow-up
commands. A dry-run collision returns structured collision data without
writing. Apply errors rather than partially accepting a collision.

The handler uses the existing backend project-root resolver. It never presents
the resolved main path as a downloadable export.

## Mutation Guards

Layout apply and facet addition use the registered project's normal mutation
guard when a project database is available. Archived projects return the
project-runtime archived error and remain unchanged. Layout dry-run is
read-only and may still explain why an archived source would be blocked.

Filesystem migration does not register implicitly. The later backend
registration path analyzes current files and stores the resolved canonical
contract path and new hash through the existing registration schema.

## Scaffold And Facet Compatibility

New scaffold responses point to `.loom/project.yaml`. Facet bootstrap inspects
both canonical and legacy root locations before deciding that a project is
missing; it does not create a legacy root beside an existing canonical one.

Applying a facet through the CLI or Portal may be followed by
`/v1/project-contract-registrations/from-backend`. The migration endpoint does
not perform that follow-up automatically.

## Safety

- Use typed clients so correlation, redaction, and response handling stay
  consistent.
- Prefer dry-run before filesystem or archive mutations.
- Do not bypass project-root containment checks.
- Do not add export/download semantics to a filesystem path response.
- Keep archived source immutable until an explicit restore/reactivation flow.

## Related Docs

- [[Projects]]
- [[Projects Facets And Contracts]]
- [[Projects And Scopes CLI Reference]]
- [[API Route Index]]
- [[Authentication Envelope And Errors]]
