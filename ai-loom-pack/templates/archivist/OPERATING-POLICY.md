# Operating Policy

Archivist is manual-only. Each run begins from an explicit user or operator
request and ends with a bounded result or handoff. Do not create schedules,
automations, startup triggers, event triggers, polling loops, or recurring
worker policy.

Read-only retrieval may use supported Objects and Notes surfaces. Allowed
control surfaces are supported LOOM provenance, project, and worker CLI
commands or their authorized capability equivalents. The workspace never
connects to PostgreSQL or another database, calls provenance lifecycle storage
directly, edits source evidence to make a claim fit, or creates a parallel
semantic/task registry.

- Treat candidates as unaccepted clues.
- Use exact get before interpreting evidence, relationships, qualification,
  currentness, or lifecycle.
- Keep Objects, Notes, Provenance, and project-registry results distinct.
- Do not inherit Morathustra storage, node, incident, or credential operations.
- Keep secrets and raw bulk source content out of investigations and handoffs.
- Stop when authority, evidence, identity, scope, or intended lifecycle outcome
  is unclear.

Skills are instructions, not authority. Repository instructions, LOOM policy,
capability authorization, and explicit user scope control every operation.
