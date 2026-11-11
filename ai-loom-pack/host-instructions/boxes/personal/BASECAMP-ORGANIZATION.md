## Portfolio And Live Discovery

Basecamp is the human-facing work system, not a second code or project registry.
Discover current projects and enabled tools from the authenticated account;
never maintain a fixed project inventory or infer access from another actor.
Resolve the exact relevant project from trusted repository mapping or the task.
Use the unique active HQ only when no narrower destination is clear. Missing
membership or ambiguous scope means report the blocker, not switch accounts.

- `HQ`: operating map, cross-project capture, decisions and otherwise unplaced
  personal work.
- `OPS`: continuing operating domains without a natural completion date.
- `BET`: bounded outcomes that can be completed or deliberately stopped.
- `ARCHIVED`: retained context outside routine work selection, even when the
  project is still API-active. Inspect it when relevant to an explicit request;
  do not reactivate it merely because a search found useful material.

Project name prefixes are shared classification; personal Home-screen stacks
are not API folders or a reliable agent hierarchy. Match the full prefix: in
JSON notation these are `"OPS \u2014 "`, `"BET \u2014 "` and
`"ARCHIVED \u2014 "`. Do not classify by an incidental word in a title.
Natively archived or trashed projects are also outside routine selection.

A repository, topic or interesting idea does not automatically need a new
Basecamp project. Reuse the approved operating domain or bounded-outcome
project. Creating, repurposing, archiving or materially reorganizing projects
requires task scope and actual permission; the labels grant neither.

## HQ Tools And Activation

Resolve these distinct surfaces from HQ's live enabled dock, not cached tool
IDs or the first tool of a matching type:

- `INBOX`: raw capture. Its `CAPTURE` and `WEB CLIPS` lists stay unassigned
  until routing establishes a real destination and owner. Move or assign only
  within an authorized routing task; preserve the source and discussion.
- `HQ ACTIONS`: executable HQ-local work, not a copy of all project tasks.
- `DECISIONS`: durable reader-facing decisions and the questions needing one.
  Preserve who decided, supporting sources and any superseding decision;
  an agent proposal is not the operator's decision.
- `FOCUS`: daily priority announcements when that workflow is explicitly
  requested. It is not a generic destination for agent reports.
- `VISION`: the operator's directional announcements. Agents must not create,
  edit, comment on or otherwise write to VISION. Verify author identity from
  trusted owner evidence, not a display name. The latest active announcement
  by the operator remains current until superseded by him. If author identity or
  current direction is uncertain, report that uncertainty without guessing.

VISION supplies relevance, not task activation, priority or action authority.
A list beginning exactly `"Backlog \u2014"` (JSON-escaped separator) contains
deferred work. the operator may activate an item by explicitly directing it,
moving it out of Backlog, placing it Up Next or giving it a current due date.
Agent-added research, comments or VISION alignment do not activate it.
An ordinary agent must not change the operator's Up Next or use his personal
profile to inspect it. Inaccessible private priorities are not permission to
reconstruct or modify them through another identity.

Projects may have several To-dos tools, Message Boards or other same-kind
tools for durable subareas. Preserve their separation. Select the exact tool
from the live dock and pass its ID with the native command's supported flag;
resolve that flag from installed help. Do not rely on a default tool when the
destination is ambiguous, enable a missing tool, or merge tools for convenience.

## Tasks, Reading And Repository Connections

Use a to-do for a concrete action or decision, a card for staged work, comments
for progress on an existing item, and a native Doc for longer material the operator
must read. Search before creating. Keep assignees, status, due dates and review
requests in Basecamp; keep code, structured research, raw material and
machine-readable contracts in their authoritative repository or system.
Do not invent dates or assign the operator work that is not his next real action.

the operator should understand the outcome, blocker, next action and needed input
without reconstructing a conversation or opening local-only paths. Put usable
short material in the item; link a native Doc or appropriate stable upload for
longer reading. Source pointers supplement that brief rather than replace it.
Reconcile accepted Basecamp feedback into the source when it changes a contract.
Do not claim that a code commit, proposal or written summary means deployment,
acceptance or a human-only action has happened.

If completion requires the operator to decide, study, practice, watch, experience
or internalize something, an agent cannot complete it by preparing a summary.
Research, comparisons, reviews and requested artifacts can be useful separate
agent outcomes. State what was produced and what still requires the operator.

For an approved repository-to-project connection, use `.basecamp/config.json`
with supported `account_id` and `project_id` keys. Add `todolist_id` only when
one default is genuinely unambiguous. Keep the exact trusted Basecamp app URL
in `.basecamp/project-url`, not an invented config key. These files select a
destination, not an actor; never add tokens, auth/profile overrides or secrets.
Inspect unfamiliar repository config before trusting authority-bearing keys.
An approved canonical remote may have a corresponding GitHub or External
Service Door in Basecamp. Creating that Door or publishing a remote remains
an explicitly scoped action. Do not silently repoint a completed project's
mapping to unrelated work; retain meaningful history.

Scheduled or event-driven Docs and uploads belong in the exact top-level
`AGENT WORK` folder of the same project. Resolve its live ID explicitly;
create it only when Docs & Files is already enabled and no exact folder exists.
Never guess between duplicates, enable the tool, or fall back to the root.
Short results remain comments on the task, not new documents or duplicate tasks.
An explicitly invoked automation follows its own narrower write set. This
protocol does not install or start Night Shift, an Organiser, Daily Focus,
an Archivist schedule, global scans or automatic Basecamp projection.

## Verification And Uncertain Writes

Read the target and relevant attachments before acting. Verify destination,
parent, content, assignee, state, visibility and links after a write; verify a
new recording's account-scoped creator against the active actor binding.
The original creator of an existing item is not proof of who updated it.
Use the native CLI's structured output and built-in filtering, and stdin for
multiline content. Keep calls serial when they can affect the same work state.

After a timeout or uncertain write, read back the intended result before any
retry. Retry only if it is demonstrably absent and the failure is transient;
do not create a duplicate to work around uncertainty. Auth, identity, access,
validation or scope failures need correction, not another actor or blind retry.
Never change visibility, publish, contact others, spend, make commitments,
deploy, alter credentials or delete merely because a task or account is visible.
