# Personal Box Policy Example

This is an unconfigured example, not standing authorization. The operator must
explicitly adopt this policy for a selected Personal Box and define the task's
permitted actions. Repository access alone does not adopt it.

Actor identity, work Box and service account are separate. An ordinary worker
remains Codex; a named agent keeps its own trusted bootstrap and signature.
Use `loom box context --json` to resolve the selected Box and policy. Unknown
Company work never falls back to Personal. Another Box requires explicit scope.
This is soft workflow separation, not an OS sandbox.

Before Personal Basecamp work, load the installed official Basecamp skill and
`CODEX-ACCOUNT.md`. Require its operator-configured, verified binding before
account reads or writes. A named agent uses its own binding, never Codex's.
Missing context, policy or binding holds account work, not authorized local
coding. Do not change defaults, copy credentials or retry as another actor.

`BASECAMP-ORGANIZATION.md` is the source-derived optional organizational example
described in the host-instructions README. Use it only when the operator adopts
that layout; discover actual projects and tools live in the verified account.
Do not create its suggested projects, tools or schedules merely to match it.

Task capture is permitted only within the operator-authorized write scope.
Search the exact destination first; capture accepted follow-ups, human-only
input and durable blockers, not duplicates or implementation finished this turn.
Use a to-do for an action or decision, a card for staged work, and a self-contained
handoff. Never invent due dates or mark a human-only action complete.
Analysis-only work does not create tasks merely to satisfy a capture sweep.
Without write authorization, prepare a local proposal and report the gap.

Keep code and original artifacts in their owning repository. Keep qualified
durable context in this Box; do not copy Company data into Personal memory,
skills, Notes, Provenance or Basecamp. A pending candidate is not permission.
Preserve files, pending edits, learned state and recovery history.

Verify destination, resulting state and new recording creator against the
account-scoped person binding; do not compare it to the global identity ID.
On update, an item's original creator does not prove the current actor.
Read back uncertain writes before retrying. Task capture never authorizes
publication, contact, deployment, spending, credentials or deletion.
