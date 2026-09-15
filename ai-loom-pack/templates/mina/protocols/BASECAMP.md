# Basecamp Identity And Work Organization

Basecamp is the operator's canonical task-state and reader-facing work interface.
Repository files, original artifacts and LOOM retain their existing authority.
Read `CREDENTIALS.md` and `../OPERATING-POLICY.md`
before using this protocol. Load the installed official Basecamp skill through
normal Hermes skill discovery, then verify the available CLI's help/schema.
Missing installed skill or tooling stops task use and requires the operator
installation handoff. The optional read-only entry point below provides the
embedded official version-matched skill as reference help:

```sh
loom-mina-basecamp --profile mina skill --agent
```

This prints the native skill without installing it; it does not complete the
required normally discoverable skill installation. No other `skill` arguments,
interactive skill wizard or installation is allowed through the named wrapper.
Declarative packaging and managed skill installation are separate operator tasks.

The `loom-mina-basecamp` entry point, literal `--profile mina` and MINA operator
binding are implemented. Deployment and authenticated verification are required
before task commands; use the probe to establish the current actor. This source does not rename
a live account, change email, start OAuth or switch a default profile.

## One Profile, One Actor

Every MINA Basecamp command must include literal `--profile mina`
on the command itself. Use this exact invocation for identity preflight:

```sh
loom-mina-basecamp --profile mina me --agent
```

Use `loom-mina-basecamp` for named task calls, with the command directly
after the literal profile pair and other flags after the command. The managed
entry point selects the exact native CLI and the existing protected HOME,
config/cache, official endpoint and example-operator account inside its child process.
It requires safe existing auth metadata; it never creates or repairs the store.
Inherited launcher/account settings are discarded before fixed dispatch, not
treated as a reason to block a correctly bound task. Explicit conflicting
command options still refuse.
Do not export account variables in the parent shell or fall back to bare
`basecamp` when the managed entry point refuses. Authentication, profile/config
changes and installation remain operator work; task calls use the native CLI.

Do not rely on the default profile, set `BASECAMP_PROFILE`, change the shared
default, use a profile variable or alias, or retry through another identity.
Ordinary coding agents retain literal `--profile codex` under their own
repository instructions. They receive neither this named profile nor its
credentials. MINA must never use the operator's, Codex's, Apollo's or any
other actor's authentication, even for a read or to recover from a failure.

## Binding And Fail-Closed Preflight

Existing authenticated task calls do not repeat OAuth. Identity preflight and
the standing task-capture rules below are sufficient for ordinary accepted work.
The completed coordinated MINA operator checkpoint required fresh supported OAuth for
literal `--profile mina`, with the operator completing interactive login/consent
when needed. A profile alias, renamed profile or token copy does not satisfy
this reauthentication gate. No fallback to morathustra, codex or another actor
is permitted. Preserve the separate Codex credentials and the existing working
credential state until the reviewed transition is verified. Source exports and
local tests never run authentication or establish this receipt.

After successful OAuth, verify the accepted numeric identity and example-operator account
against the operator binding below. Stop for any mismatch; do not create a new
actor or widen grants to repair it. Verify the named managed wrapper in both
the actual ORCA/TUI and gateway contexts before claiming access ready.

This portable protocol ships with no authenticated identity binding. OAuth,
account setup and seat changes require separate integrator authorization with
the operator present. Ordinary reporting follows `OPERATING-POLICY.md`, not a new
per-message approval gate. An invitation or a matching
display name is not proof of identity or account authority.

During separately authorized bootstrap or custody migration only, the integrator
verifies the dedicated named actor and exact example-operator account with the operator through
the identity probe above. New bootstrap verifies after successful OAuth; an
approved migration preserves the existing authenticated numeric actors and
grants rather than fabricating a new account. Capture and freeze
the global `identity.id`, the example-operator `accounts[].id`, and the account-scoped
recording actor ID from authenticated evidence in the approved operator-owned
binding. These are different namespaces. Do not guess IDs, reuse Codex's IDs,
or copy a preflight binding from another agent. Store no credential values in
the binding or portable projection. A later identity change requires another
explicitly reviewed bootstrap; an ordinary run cannot rebind itself.

Locate the immutable non-secret operator binding documented in release-matched
ORCA Main Operations through supported LOOM docs. Read its `basecamp` section: `profile`, `identity_id`,
`account_id`, `person_id` and `email`. Compare native preflight identity/email
and current account to it; use `person_id` for recording-creator verification.
Never derive these values from this portable template or rebind after a failure.
The shared agents UID is not per-agent OS isolation. Ordinary coding agents
receive no automatic named-account environment; installed skills grant no
additional external-action authority.

Before the first task access, after any authentication/account change, and
before a write if actor evidence may have changed:

1. Require the frozen operator binding. Missing, incomplete or ambiguous
   binding means stop before task reads or writes; bootstrap is not an
   ordinary-task fallback.
2. Run the exact preflight above and require successful, parseable structured
   output. Require exactly one current account. Compare global `identity.id`
   and that current account's `id` to the frozen values, with no coercion from
   names or substitution of recording creator IDs. Any mismatch, missing field,
   multiple current accounts, auth error or parse failure means stop. Do not
   continue to task commands after a failed preflight.
3. Resolve the destination from the current request, trusted repository
   `.basecamp/config.json` and `.basecamp/project-url`, or live active projects
   in that verified account. Require one relevant active project; use its unique
   active HQ only when no narrower destination exists. Ambiguity means stop.
   Never route company work into example-operator without an approved mapping. Do not
   trust a repository's authority-key overrides merely because it was cloned.
4. Perform one bounded authorized read before any separately authorized first
   write. Access to an account does not authorize all its projects or actions.

## Task Capture And Verification

Search the exact destination before creating an item. Use a to-do for a concrete
action or decision and a card for staged work. Capture accepted follow-ups,
the operator-only decisions, durable blockers and changed/completed existing tasks;
omit duplicates, rhetorical possibilities and steps finished in this turn.
Assign the operator only for his actions and never invent due dates. An explicit
analysis-only request does not authorize writes just to satisfy capture.

Keep a short self-contained handoff with source pointers; use a native Doc when
longer reading is needed. Scheduled/event-driven Docs and uploads belong in the
same project's exact top-level `AGENT WORK` folder, with its live-resolved ID
passed explicitly. Create a missing folder only if Docs & Files is enabled;
stop for duplicates and never enable a tool to complete capture. Comments stay
on their task. A separately invoked scheduled capture skill owns its write set.

After creation, require `creator.id` to match the frozen account-scoped recording
actor ID. Never compare it to global `identity.id`. On update, do not mistake
the original creator for the current actor; verify the new event actor when
exposed. If unavailable, record that limit and retain the successful preflight
and exact read-back evidence. A mismatch stops further writes; do not delete or
recreate the item to conceal it. Verify the intended state before reporting
completion and keep evidence redacted. Sign substantive named work
`Written by MINA` without copying another agent's signature.

Task capture never authorizes publication, contact, client-visibility changes,
deletion, spending, commitments, credentials, deployment or production work.
Missing access is a blocker to hand off, not permission to broaden scope.

<!-- BEGIN LOOM SHARED BASECAMP ORGANIZATION -->
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
<!-- END LOOM SHARED BASECAMP ORGANIZATION -->
