---
name: use-loom-provenance
description: Retrieve LOOM Main decisions, preferences and qualified project context, or register source-backed durable meaning as pending candidates. Use for MINA, coding agents and Archivist; not routine task logging or worker control.
---

# Use LOOM Provenance

MINA and coding agents use the same Main ledger as Archivist. Retrieve relevant
decisions, preferences, constraints and outcomes, or capture new durable meaning
that is likely to matter again. Skip transient chatter, routine commands and
task-state logging. Basecamp owns tasks; source files own project development.
These are core peers: use Provenance when prior meaning affects work and capture
new durable meaning without waiting for its name to be mentioned or for nightly
review. Equal standing does not require a call to each surface for every task.

Use `loom provenance`, not the standalone `provenance` command: that can address
a different host's ledger and is not automatically synchronized with Main.

## Box Context

Keep the current workspace's `LOOM_BOX_ID` or use explicit `loom --box <box-id>`.
With no selection, Main discovery defaults to Personal, not every Box. A Box is
workflow context, not an OS access boundary or another agent's identity.
Cross-Box discovery requires the operator's explicit request; only then use the
requested Boxes or `--all-boxes`. Do not broaden a failing lookup automatically.

New non-project candidates must explicitly include their immutable Box ID in
`anchors.boxes`. Project-anchored meaning may inherit verified current project
membership. Do not relabel old records from their wording or merely because a
Box is selected. Unattributed historical meaning remains available in an
explicit all-Box view, with its uncertainty preserved. Membership metadata
repair does not imply a fresh source observation, review or accepted decision.

## Routing Boundary

Read accessible known source files directly with ordinary filesystem tools.
That needs no semantic lookup. Use this skill when qualified durable context
or capture is relevant, not as a preamble to every file read.
Choose the surface that answers the missing question:

- Objects for technical identity, version, extraction, and failure state.
- Notes for unknown source locations, cross-collection material or indexed
  PDF/image extraction.
- Provenance for qualified decisions, preferences, constraints, outcomes,
  relationships, currentness, candidates, and unresolved cases.
- `loom project` for technical project registry and repository navigation.
- Provenance `project_state` for captured project purpose, focus, blockers and
  structure, even without Git or `.repo`. These are source declarations.
- `loom provenance repo list` for semantic repository discovery.

Consult [retrieval routing](references/retrieval-routing.md) when the surface or
historical citation requirements are unclear.

## Exact References And Discovery

An exact record, candidate, case or snapshot reference goes straight to its get
operation; do not search again just to recover an already known identity.

Search or list when you need discovery. A useful preview may already answer a
navigation question; get a selected result when its claim, source evidence,
qualifications or lifecycle are needed. Do not add a get merely to repeat
information already sufficient for the task. Before a lifecycle mutation, read
the exact state and evidence required by its contract.

Independent retrievals may run in parallel when they answer independent
questions. Do not merge uncalibrated scores across engines or assume separate
reads share a snapshot. Pending candidates require deliberate inclusion and are
clues, never accepted state. Sources and explicit current instructions remain
authoritative over previews and semantic interpretations.

Routine human/JSON search and record/candidate/case get return bounded useful
views, with relevant posture, currentness, scope and completeness. Use `--details`
for full diagnostics or lifecycle representations; `--details --json` retains
the full API-shaped envelope. Request `--sources` when linked stored receipts
matter; that is not a live source fetch. Preserve source expansion's separate
incomplete/truncated state. `--plain` is ID-only, not evidence for interpretation.

When the installed CLI exposes the `project` subcommand, follow project
context's snapshot-qualified citation with
`loom provenance project get <project-id> --snapshot <snapshot-id>`.
The captured Markdown/hash describes that observation, not a later file read.
Use ordinary source files to change work; automatic context refresh does not
accept decisions, and absence of repository-level `.repo` is not an error.

## Capture And Reconciliation

Capture a source-backed durable decision, preference, constraint, correction or
unresolved question when it is likely to matter again, not only after a search.
Reuse a known exact record or check for an existing candidate/record when needed
to avoid registering the same meaning. Skip routine edits/debugging and quotas.
Use `loom provenance candidate register --file candidate.json
--idempotency-key <stable-key> --yes --json`. Read
[registration](references/registration.md) when preparing the request, source
evidence, actual producer identity, stable replay key and exact-get receipt.
An ordinary authorized task may capture relevant durable meaning; it does not
need a separate approval for every pending candidate. Registration is not
acceptance, supersession or permission for an external action.

Read [reconciliation guidance](references/reconciliation.md) when work depends
on a candidate or reveals a conflict. Reconcile within the task when the needed
evidence and authority are available; otherwise leave qualified clues for review.
`loom provenance review prepare` can build a saved one-candidate review batch
without acceptance, preserving actual capture/reviewer contexts. Exact
relationships use `loom provenance relationship get`, not another search.
Do not launch the Archivist to finish a capture. Worker controls belong in the
Archivist workspace's `protocols/CANDIDATE-REVIEW.md`, not this shared skill.
Objects and Notes are read-only retrieval surfaces here.

Never connect directly to PostgreSQL or any database, call lifecycle storage
internals, create another semantic ledger or task registry, or enable recurring
execution as part of capture. This skill does not change backend permissions or
authorize unrelated operations.
