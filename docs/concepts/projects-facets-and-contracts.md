---
title: "Projects Facets And Contracts"
description: "File-first projects, explicit resource declarations, portable context and legacy facet compatibility."
audience: [user, operator, developer, agent]
tags: [loom, concepts, projects]
status: draft
verified_at: "2026-09-15"
source_scope: [internal/projectcontracts, internal/projectapply, internal/projectstate]
related:
  - "[[Projects]]"
  - "[[Projects And Scopes CLI Reference]]"
  - "[[Canonical Paths]]"
aliases: ["Project Concepts", "Facets And Contracts"]
---
# Projects Facets And Contracts

## Projects Are Scopes

A project is a scope for work, not the universal parent of every LOOM object.
Files remain ordinary files. LOOM manages only the resources declared for it;
a folder name, Git repository or agent conversation does not enroll a resource.

New projects use one minimal scaffold, without a preset or facet selection:

```text
project/
  AGENTS.md
  .loom/project.yaml
  .project/
  notes/
  repos/
```

Git initialization is separate. Additional folders are allowed. A project can
contain several repositories, or none, without losing project-wide context.

## Two Different Contracts

**`.loom/` describes system intent.** Its root declaration identifies the
project and the resources to manage. These can include repository membership,
source ingestion, protection or an application, depending on the supported
resource kind. Planning resolves ownership and prerequisites. Applying the
reviewed plan produces durable operation state.

**`.project/` describes development context.** Overview, state, roadmap, map
and workflow files are editable, portable project-level information. Agents
update them alongside the work. They are neither an application manifest nor
a runtime database.

The root `AGENTS.md` connects an agent to these contracts. Ordinary coding
does not require a new plan, registration or documentation search. When an
isolated component worktree lacks its parent context, pass the relevant context
through the harness's normal handoff.

## Declaration Is Not Execution

Source validity, registered identity, readiness and successful execution are
different facts. A valid application declaration can still lack a host grant,
credential reference or publication prerequisite.

Use `loom project context` for development orientation, `plan` to inspect
managed effects, `apply` for the reviewed request and `status` for readiness.
A partial operation must be inspected and resumed through its returned next
action, not replaced with guessed shell operations. See the
[project guide](../user-guide/projects.md).

Credential values do not belong in project files. Projects reference configured
credential delivery; access and creation policies depend on the installation.
Proton Pass is an optional integration, not a universal prerequisite for editing
a project or storing development context.

## Repositories And Provenance

Repository identity is explicit. A nested Git directory or an ORCA worktree
does not automatically become a registered member. In the current declaration
format, repository paths are project-relative. Legacy facet-based contracts
resolve member paths relative to their `repos/` facet.

Project and repository projections are rebuildable observations. They can show
declared focus, observed Git state and freshness; they are not accepted semantic
decisions. Provenance separately stores candidates, accepted records, sources,
supersession and unresolved cases. Editing `.project/STATE.md` must not silently
accept a candidate.

Legacy repository-level `.repo/` remains supported where installed. It is not
required in addition to the default project-level `.project/`, and should not
be mass-migrated merely to make an ordinary source edit.

## Legacy Facets And Layouts

Earlier v0.3/v0.4 projects used selected facets, singleton files under
`.loom/contracts/`, per-surface guidance and scaffold presets. Their readers
and compatibility commands remain for existing projects. They are not the
onboarding model for new projects.

The canonical root declaration is `.loom/project.yaml`. Older
`loom.project.yaml` roots can be diagnosed and migrated deliberately. Conflicting
canonical and legacy declarations are errors, not an invitation to choose
whichever file happens to be convenient.

Do not delete old contracts before comparing their meaning. Public examples and
historical smoke scripts may intentionally exercise these older formats.

## Lifecycle

Archiving is a reviewed physical lifecycle operation, not an export or a Git
branch change. It can deactivate managed runtime and move project files.
Restoring files leaves runtime inactive until a separate supported activation
workflow establishes readiness. Historical retention does not confer current
project membership or make historical search results current.
