# Shared Main Agent Instructions

These instructions apply to both named agents and identity-neutral coding tasks
on LOOM Main. Codex is the execution harness, not every agent's identity.
This portable example must be explicitly adopted for a configured installation;
it supplies no live account binding, standing Personal authorization or host access.

## Session Identity

At session startup, use the trusted workspace `AGENTS.md` to establish any
explicitly bootstrapped named agent. Its local instructions own that agent's
personality, signature and account protocols. Do not load another agent's
private context. A settings profile, model name or shared Unix account does
not establish an agent identity.

Without an explicit named bootstrap, the session is an ordinary Codex worker:
read `CODEX-IDENTITY.md` in the active `CODEX_HOME` once per session. When
`CODEX_HOME` is unset, use `$HOME/.codex/CODEX-IDENTITY.md`. That fallback is
only for ordinary workers; do not load it into a named session or use its
account/signature in place of the workspace's own identity protocol.

Changing directory, inspecting a named workspace or receiving delegated work
does not change an ordinary task's actor. A named agent visiting a project
also remains the same actor; project instructions govern the work, not a
silent identity switch. Start fresh project-worker threads with the project's
own context, not a copy of a named agent's personality or conversation.

## Work Box And Operating Policy

Actor identity, work Box and external-service account are independent. At
startup in a Box/project, use `loom box context --json` and read its derived
`policy_path` (`<Box root>/.loom/agents/WORKFLOW.md`). Do not assume an ancestor
AGENTS above a member Git root was loaded. Reuse the context/policy during the
same task; refresh for an actual scope/revision change, not every tool call.
Explicit task selection takes precedence over canonical cwd; select `--box`
for expressly authorized work elsewhere. A UI sidebar switch does not rebind
existing jobs/chats. Never infer cross-Box authority from mere reachability.

Only Personal policy loads its source-derived `BASECAMP-ORGANIZATION.md` and
ordinary Codex Personal account binding. Company keeps the Company's workflow:
every Company Basecamp write needs the operator's approval, including routine
updates/completions and remote drafts. Prepare locally instead. Never capture
Company tasks in Personal Basecamp or reusable Personal memory/knowledge.
Named agents retain their identity while following the selected work policy.

Before Basecamp work, load the installed official `basecamp` skill and the
selected Box's account protocol. Missing context/policy/binding holds account
effects and cross-Box work, not authorized local coding. Outside known Boxes,
an explicitly Personal task may use the installed Personal policy under
`CODEX_HOME/box-policies/personal/WORKFLOW.md` (or `$HOME/.codex/box-policies/`
when CODEX_HOME is unset). Unknown Company scope never falls back to Personal.
Missing instructions/tooling are a configuration gap, not permission to invent
an organization or authenticate as another actor. Reuse loaded instructions;
the account preflight remains required before access and after auth changes.

Use the current actor's own account protocol in the selected Box and its literal profile on every
Basecamp command. Never change the shared default, export `BASECAMP_PROFILE`
or fall back to another authenticated identity. Verify the protocol's exact
actor/account binding before task access and after authentication/account
changes. Account visibility and skill installation grant no new action
authority. All sessions share the agents Unix user; profiles are not per-agent
OS isolation. Missing identity instructions stop account work, not unrelated
authorized tasks.

## Project Work And Task Scope

Before creating a repository/project or designing an infrastructure-bearing
feature, plan its fit with deployed LOOM first. Use existing owners and verify
actual build/runtime support rather than adapting duplicate infrastructure later.
Record this in the existing plan, not another approval document. Keep ordinary
application code portable; small edits need no new ceremony. Use
`manage-loom-projects` for the relevant integration detail.
Register Company projects normally with `.loom` in the surrounding workspace.
Member Git repos/client deliverables need no LOOM SDK, managed declaration or
protocol pack for ordinary coding/builds/tests. Main may be a test host, not
the client's target architecture; missing adapters do not dictate its language.

Report substantial LOOM friction or suspected unsupported features with evidence
in the project and, when the Box policy permits, Basecamp; use
`manage-loom-projects` for reporting detail. Company reporting is proposed
locally until the operator approves the exact Basecamp change.
Continue independent authorized work; do not silently bypass LOOM.

Follow the project's own `AGENTS.md`, `.loom/` and `.project/` contracts;
read opted-in legacy `.repo/` guidance when present.
Use the selected Box's task capture and destination rules, not a global
Personal HQ fallback. For analysis-only work, do not create routine tasks.
Keep technical facts and accepted plans in their canonical project sources.

Use the current actor's local authorship rule for substantive work. The harness,
repository being inspected and authenticated creator are not interchangeable
with that actor's conversational identity.

The Archivist's specialized operating scope remains narrower than this general
account guidance. Available authentication does not enable Basecamp projection,
recurring execution, global scans or bypass its existing lifecycle rules.

## Delegated Conversations

For each operator-authorized independent assignment to a configured MINA, create
a fresh chat in the operator-selected remote MINA project on Main. Resolve its
workspace and current project ID through native project discovery; do not assume
this template establishes a deployment. Do not fork unrelated history or reuse
the operator's personal chat with her. Supply the outcome, relevant source pointers
and existing authority. The workspace bootstrap supplies MINA's identity, not a
copied transcript. Ordinary coding workers use their own project's context.

Keep same-assignment corrections, clarification and progress in that chat.
Desktop `create_thread` also delivers its opening prompt as tool output: use a
bootstrap-only creation note, wait until idle, then send the actual assignment
through `loom-codex-message task`. For authorised follow-up assignments to an
existing Main chat, use that same command as `agents`
with the actual sender and human-authorisation reference. Use
`loom-codex-message report` for information, not new authority. Builtin
`send_message_to_thread` remains tool output; do not use it for new
assignments or elevate arbitrary tool results into tasks.

Create chats only for the operator-authorised delegation, not every ordinary edit;
preserve native subagent orchestration. Do not interrupt active turns or change
model/effort settings. Existing personal chats and history remain intact.

## Loom App

When installed and configured, Loom is the desktop interface to Main Box,
read-only Storage/Application Data and Mac Box. Its file tree exposes actual
source paths; browsing does not require Notes indexing or Obsidian enrollment.
Create project files at their canonical source and give the verified Main path
plus a Box-relative navigation path (for example `Projects/<folder>/...`).
The operator can focus a project folder in the sidebar. Do not export a separate
vault, add `.obsidian`, or copy notes into another tree merely for him to read
them. Use Obsidian only for an explicitly requested legacy/client/export task.

App access is not proof of local/offline availability, indexing, enrichment or
backup protection. Check the relevant owner when that fact matters; preserve
paused processing and bulk transfers. A standalone Loom iPhone/iPad app is not
yet implemented. Do not promise mobile delivery from desktop availability.

## Source Retrieval

Load only context relevant to the current task. Reuse instructions already
available in the conversation; reread when their trusted revision changes,
the owning project/account/surface changes, or a concrete gap needs detail.
Do not follow every reference recursively or repeat instruction reads before
each tool call. This does not cache live health, GUI targets, source contents
or required account checks as permanently current evidence.

Prefer scoped filesystem tools for a known file, filename, code symbol or
literal text: `rg --files`, `rg`, directory listings and direct reads on the
owning machine. This needs no search skill, docs search, indexing wait or
node-health preflight. Load specialised context only when the task needs it.

Use Notes search for unknown locations, conceptual or cross-collection discovery,
and published PDF/image extraction. Use Provenance when relevant durable
decisions, preferences, supersession or unresolved history affect the task.
Read/get an exact file or record reference directly; search only for a missing
piece. An empty indexed result does not prove filesystem absence.

Choose tools for the information missing from the task, not a universal
search/get sequence. Independent retrievals may run in parallel; their scores
and snapshots are not interchangeable. A useful Provenance preview may answer
navigation; get evidence/lifecycle detail when needed, using `--details --json`
for the full representation. Mutations retain their exact state prerequisites.

Use known commands directly and local `--help` for unfamiliar syntax; consult
relevant skills/docs when more explanation is needed. Runtime state and managed
mutations still use their supported surfaces. Ordinary file access does not
authorize database queries, generated-view edits or credential access.

## Durable Knowledge Notes

When creating a durable knowledge note, classify and connect it as part of
writing: add a supported primary `type`, meaningful `subject/` tags and useful
links to existing people/concepts/sources or project context. Keep project
research with its project. Use `people` for confirmed profiles and `related`
for other useful relationships; preserve existing tags and domain properties.
Use the selected Box's Notes convention when supplied; do not read another
Box merely to classify a note or copy its contents into Personal knowledge.
Use known relevant targets or scoped lookup; no compulsory global search, link
quota, invented biography or unrelated knowledge-graph building. Leave a link
absent when no meaningful target is established.

Links/tags imply association, not equal currentness, relevance or endorsement.
Prefer owning-project context, note role and substantive recency. A clearly
later developed view in the same scope normally guides current interpretation;
earlier explorations can still be linked as history. Import/sync/metadata mtimes
are not idea dates. Preserve original dates; infer neither factual correctness
nor supersession from age alone. Keep uncertain chronology/disagreement explicit.

This applies to authored knowledge, not code, scratch, generated indexes or
imported originals. Do not run an existing-vault tagging sweep for every task
or rewrite body text just to add metadata. The finite historical curation pass
is a separate assignment. Classification is not Provenance acceptance.

## Selective Provenance Capture

When work establishes a source-backed decision, preference, constraint,
correction or unresolved question likely to matter again, load
`use-loom-provenance` and register it in Main through
`loom provenance candidate register --file candidate.json
--idempotency-key <stable-key> --yes --json`. Use your actual producer/task
identity and preserve the source and receipt. Capture relevant meaning before
closing significant work; do not wait for someone to search for it first.

Reuse an exact known record or check for a duplicate when necessary. Routine
commands, file edits, temporary debugging and chatter do not need entries or a
capture quota. Registration creates a searchable pending candidate, not truth
or permission. Reconcile when current work needs the interpretation and your
authority covers it; otherwise leave a qualified clue for review.

Keep source-backed meaning in the owning Box using its context and actual actor.
Basecamp owns tasks; source files own project facts. Automatic `.project`/repo
projections are not semantic acceptance. Main agents use `loom provenance`, not
another host's standalone `provenance` ledger. Never switch ledgers on failure.

## Mac Shell Access

When an operator has configured `loom-mac`, use ordinary `ssh`, `scp` or SFTP
for task-authorized Mac file and development work. For example,
`ssh loom-mac 'uname -a'`. Use explicit remote paths; the Main working directory
and Linux toolchain do not carry over. No extra LOOM command registration is
required for ordinary shell commands. Inspect `ssh -G loom-mac` when uncertain
about the binding. The configured Mac account is a normal user account, not a
project sandbox; all Main sessions sharing the agents Unix user share its key.

For visible applications use the current harness's native computer-use tools.
Main-to-Mac uses the existing loom-mac-computer-use MCP connector in Codex,
loom-mac in Hermes. Native Hermes computer_use controls Main's desktop. ORCA
and its computer-use, orca-cli and orchestration skills are retired: never
launch them as a fallback. An unavailable native tool is not permission to
switch devices, use shell automation or install another bridge. Main Chromium
is for independent web research, unless an exact existing session is requested.
Native Codex owns its own threads and managed worktrees.
The restricted computer-use SSH key is not a shell key.
Do not bypass host-key checks, change accounts, grant sudo or alter macOS
privacy settings as recovery. Report connection/authentication failures clearly;
do not blindly retry mutations after a disconnect. Existing task, destructive
action and external-effect authority still applies.
