# Operator-Installed Host Instructions

These are operator-configured examples, not an installed account policy or a
grant of authority. They contain no live identity, account, mailbox or project
bindings. Review the selected policy and configure its non-secret binding from
authenticated evidence before enabling account work. Missing or placeholder
bindings hold account reads and writes; they never select a default account.

These are host bootstrap sources, not project workspace templates or named-agent
identities. The operator installs `codex/AGENTS.md` into each already-approved
Codex home on Main, preserving any existing user instructions. It is not
automatically scaffolded into new repositories. The global bootstrap and
`CODEX-IDENTITY.md` are account-neutral. `loom box context --json` derives the
selected Box's `.loom/agents/WORKFLOW.md`, including from nested Git checkouts.
Install reviewed `boxes/<key>/` policy sources at that directory. Personal's
`CODEX-ACCOUNT.md` describes the required Personal binding without supplying one.
For explicitly Personal tasks outside a Box, install that same Personal policy
bundle at `CODEX_HOME/box-policies/personal/`; never use it for Company fallback.

Personal's companion `BASECAMP-ORGANIZATION.md` is copied from the exact section
between `BEGIN LOOM SHARED BASECAMP ORGANIZATION` and
`END LOOM SHARED BASECAMP ORGANIZATION` in
`templates/morathustra/protocols/BASECAMP.md`. That marked section is the sole
source for shared organizational rules. Do not copy the named identity sections
into a general-agent bootstrap or maintain a second edited organizational copy.
Install it alongside Personal's policy, not as global Company instructions.
The organizational layout is optional; adopt it explicitly or use the operator's
own layout. Neither the layout nor this example authorizes account maintenance.
This gives each runtime a complete local instruction copy without a live
cross-workspace dependency or edits to the official Basecamp skill.

Publish only from a reviewed source checkpoint, refuse unexpected existing
content, and verify installed hashes. Existing sessions may need a new session
to load their bootstrap; do not interrupt them or restart services just to
refresh instructions. Merge only reviewed operational deltas in named workspaces;
never replace learned SOUL/USER/MEMORY, skills, settings or conversations from
templates. Keep private before-images outside captured native state. Authentication,
credentials, defaults and schedules are not changed by installing these files.

When configured, Main's Box-selected `basecamp`/`gh` launchers dispatch absolute native binaries,
using the existing trusted Box account-context mapping. Explicit cross-Box use
is `loom-box-tool --box <Box ID> <tool> ...`. No reduced command API or approval
classifier is introduced; Company Basecamp approval remains task policy.
Named `loom-mina-*` tools remain fixed Personal identities. Missing Company
bindings refuse instead of using Personal credentials. Shared Unix access is
not a security boundary; account sign-in and normal cloud proof remain separate.

The Company examples name no real company. `TYLER-BASECAMP.md` and `TYLER-MAIL.md`
describe optional named-agent policies, disabled until the operator configures
their exact scope and bindings. They supply no Personal exception or mail access
by themselves. A coding worker never becomes Tyler by reading those files.
