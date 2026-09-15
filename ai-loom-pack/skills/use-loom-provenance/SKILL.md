---
name: use-loom-provenance
description: Retrieve LOOM Main decisions, preferences and qualified project context, or register source-backed durable meaning as pending candidates. Use for MINA, coding agents and Archivist; not routine task logging or worker control.
---

# Use LOOM Provenance

MINA and coding agents use the same Main ledger as Archivist. Retrieve relevant
decisions, preferences, constraints and outcomes, or capture new durable meaning
that is likely to matter again. Skip transient chatter, routine commands and
task-state logging. Basecamp owns tasks; source files own project development.

Use `loom provenance`, not the standalone `provenance` command: that can address
a different host's ledger and is not automatically synchronized with Main.

## Routing Boundary

Choose one first engine:

- Objects for technical identity, version, extraction, and failure state.
- Notes for source wording and document material.
- Provenance for qualified decisions, preferences, constraints, outcomes,
  relationships, currentness, candidates, and unresolved cases.
- `loom project` for technical project registry and repository navigation.
- Provenance `project_state` for captured project purpose, focus, blockers and
  structure, even without Git or `.repo`. These are source declarations.
- `loom provenance repo list` for semantic repository discovery.

Read [retrieval routing](references/retrieval-routing.md) before expanding
beyond the first engine.

## Search Then Exact Get

1. Search or list compact typed results in one engine.
2. Select one result using identity, posture, currentness, and match reason.
3. Load its exact get or inspect projection.
4. Interpret evidence, lifecycle, relationships, qualification, and
   diagnostics only from that exact projection.
5. Expand sequentially to one other engine only for a named gap.

Do not fan out in parallel or merge scores across engines. Pending candidates
require deliberate inclusion and are clues, never accepted state.

When the installed CLI exposes the `project` subcommand, follow project
context's snapshot-qualified citation with
`loom provenance project get <project-id> --snapshot <snapshot-id>`.
The captured Markdown/hash describes that observation, not a later file read.
Use ordinary source files to change work; automatic context refresh does not
accept decisions, and absence of repository-level `.repo` is not an error.

## Capture And Reconciliation

Search for an existing candidate/record before registering the same meaning.
Read [registration](references/registration.md) for the supported local API
request, source evidence, producer identity, stable replay key and exact-get.
An ordinary authorized task may capture relevant durable meaning; it does not
need a separate approval for every pending candidate. Registration is not
acceptance, supersession or permission for an external action.

Read [reconciliation guidance](references/reconciliation.md) when work depends
on a candidate or reveals a conflict. Leave ordinary pending clues for review;
do not launch the Archivist to finish a capture. Worker controls belong in the
Archivist workspace's `protocols/CANDIDATE-REVIEW.md`, not this shared skill.
Objects and Notes are read-only retrieval surfaces here.

Never connect directly to PostgreSQL or any database, call lifecycle storage
internals, create another semantic ledger or task registry, or enable recurring
execution as part of capture. This skill does not change backend permissions or
authorize unrelated operations.
