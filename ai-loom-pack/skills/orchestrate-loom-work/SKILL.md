---
name: orchestrate-loom-work
description: Inventory cross-project LOOM work, map dependencies, choose bounded owners, and prepare handoffs when Morathustra must coordinate rather than implement ordinary project work.
---

# Orchestrate LOOM Work

This is primarily a Morathustra planning skill. Coordinate broad work without
turning the broad workspace into a project root, task database, or source-code
checkout.

## Source-Of-Truth Order

1. the operator's current request and explicit priorities.
2. Canonical task state in the connected work interface, when available.
3. Project roots, `.loom/project.yaml`, project guidance, and project planning
   or handoff files.
4. LOOM project/runtime status and release-matched docs.
5. Repository development plans for LOOM-core work.
6. LOOM Provenance for qualified semantic navigation, verified against current
   sources; Hermes conversational memory only where no stronger home exists.
   Never maintain hand-written memory as project or task truth.

Do not pass Morathustra's SOUL, Hermes memory or unrelated conversations into a
project handoff. Hermes is the harness, not an additional identity. Keep facts,
decisions, hypotheses, and dependencies distinguishable.

## Inspect, Plan, Apply, Verify

1. Inventory active projects and requested outcomes from canonical sources.
2. Map dependencies, blockers, shared resources, authority boundaries, and
   validation owners.
3. Classify each item as project work, LOOM-core work, operations, credential
   work, incident investigation, or a the operator-only decision.
4. Propose the smallest independent slices with source root, expected files,
   forbidden files, validation, and stop conditions.
5. Delegate narrow work through the release-matched upstream Orca skills.
6. Track outcomes in canonical task/project state, not a private parallel
   registry.
7. Verify handoffs, integrate evidence, and surface unresolved dependencies.

Read [routing and handoff rules](references/routing.md) before delegation.

## Safety Classification

- **Inspect:** inventories, project status, planning files, handoffs, and
  dependency discovery.
- **Plan:** proposed ownership, ordering, slice boundaries, and validation.
- **Safe run:** task-scoped maintenance in an already authorized project/work
  interface when it records accepted work without external commitment.
- **Sensitive:** creating external commitments, production actions, credentials,
  publication, spending, destructive changes, permission changes, or work that
  crosses organizations/accounts.

Delegation does not broaden authority. Ask the operator when a missing decision
would materially change owner, scope, external effect, or risk.

## Orca Boundary

Orca owns repository registration, worktrees, terminals, task sessions, agent
processes, and client state. Use its installed release-matched `orca-cli` and
orchestration skills rather than encoding commands here. Do not manually create
worktrees, invent a LOOM session registry, or treat chat context as more
authoritative than repository files.

The completed pre-main proof verified Orca `1.4.183` only for a disposable
Mac-local repository, independent worktree, Codex terminal, PTY interaction,
and cleanup lifecycle. Main/headless hosting, browser/mobile/Relay clients,
live TLS/WSS or WireGuard client paths, restart continuity, and Mac-off
operation remain deferred. Use Orca's native skill/plugin mechanism; LOOM does
not install or reconcile Orca skills.

## Project Boundary

Ordinary feature implementation belongs to a worker rooted in the project or
Codex-managed worktree. Morathustra may synthesize, review, and follow through,
but should not carry project identity into other projects or implement from the
broad workspace.

## Current Versus Future And Escalation

Verify project/CLI state before routing. Mark future runtime-agent bridges or
automation as deferred. Escalate cross-project conflicts with the dependency
map, competing authorities, affected roots, evidence, proposed decision, and
safe default. This skill provides coordination instructions, not execution
authority.
