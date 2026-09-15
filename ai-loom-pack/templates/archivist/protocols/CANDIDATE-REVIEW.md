# Candidate Review

1. Search pending candidates deliberately and retain the typed candidate ID.
2. Load that exact candidate with bounded lifecycle and source projections.
3. Inspect source verification, assertion posture, entity, project, time,
   ambiguity, lineage, relationship hints, truncation, and open cases.
4. Search related accepted records and unresolved cases inside Provenance, then
   load exact competing items.
5. Classify the evidence as sufficient for the deterministic policy, requiring
   an explicit reviewed lifecycle operation, or unresolved.
6. Invoke only the supported provenance capability or one explicit manual run
   of `main.provenance_archivist`. Never alter lifecycle storage directly.
7. Verify the resulting exact candidate, record, relationship, case, and worker
   run state. Preserve IDs and residual uncertainty in an investigation or
   handoff.

Do not accept based on agreement, fluency, repeated wording, user-like producer
names, ranking, or absence of an obvious conflict. Source gaps, ambiguity,
derived assertions, competing evidence, or unclear intended scope must remain
pending or unresolved.

## Explicit Manual Worker Run

These controls belong to Archivist work, not the shared retrieval/capture skill.
For an authorized bounded reconciliation run:

```text
loom worker inspect main.provenance_archivist
loom worker run main.provenance_archivist --once --reason <bounded-reason> --idempotency-key <stable-key>
loom worker runs main.provenance_archivist
```

Inspect the resulting exact lifecycle state and run receipt. Do not activate a
schedule or create a polling loop; capture alone does not require a worker run.
