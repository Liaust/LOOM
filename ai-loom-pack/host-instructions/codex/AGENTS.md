# Main Coding-Agent Basecamp Instructions

This is an operator-configured template. Replace every angle-bracket identity
placeholder with values verified for your own Basecamp account before installing
it. Unconfigured placeholders authorize no Basecamp access.

These instructions apply to identity-neutral coding tasks on LOOM Main. They
do not bootstrap a named agent or transfer that agent's identity, memory or
permissions. A separately and explicitly bootstrapped named agent follows its
own trusted actor binding. Changing directory, inspecting a named workspace
or receiving delegated work does not change an ordinary task's actor.

Before Basecamp work, load the installed official `basecamp` skill and read
`BASECAMP-ORGANIZATION.md` in the active `CODEX_HOME` alongside this file.
When `CODEX_HOME` is unset, use `$HOME/.codex/BASECAMP-ORGANIZATION.md`.
That companion contains the shared HQ/OPS/BET, Inbox, Backlog, VISION and
multi-tool rules. Missing instructions or tooling are a configuration gap to
report, not a reason to improvise a new organization or use another account.

## Exact Codex Identity

Use literal `basecamp --profile codex` on every Basecamp command. Never change
the shared default, export `BASECAMP_PROFILE`, use a profile variable or alias,
or fall back to another authenticated identity. Account visibility and skill
installation grant no new action authority. All sessions share the agents Unix
user; profiles are not per-agent OS isolation.

Before the first Basecamp task access, after an authentication/account change,
and before writes when identity evidence may have changed, run:

```sh
basecamp --profile codex me --agent
```

Require successful structured output with global `identity.id` exactly
`<CODEX_IDENTITY_ID>` and exactly one current account, whose `id` is
`<BASECAMP_ACCOUNT_ID>`. The authenticated email is `<CODEX_EMAIL>`.
Missing fields, a parse error,
wrong identity/account or authentication failure means stop before task reads
or writes. Do not rebind expected IDs or authenticate as an incidental repair.
New recordings must have account-scoped `creator.id` equal to `<CODEX_PERSON_ID>`,
not the global identity ID. For an existing recording, its original creator
does not identify the current updater; verify event actor when available.

## Task Capture And Scope

Follow the project's own `AGENTS.md`, `.loom/`, `.project/` and any legacy `.repo/` contracts.
Resolve one approved active Basecamp project from trusted mapping, the task or
live project discovery; use unique active HQ only when no narrower destination
is clear. A connection to one account does not authorize routing other company work
there. Search the destination before writing.

Capture accepted future work, genuine blockers, operator-only input and changed
or completed existing tasks. Do not create duplicates, rhetorical possibilities
or immediate steps already completed this turn. For analysis-only work, capture
only an accepted follow-up or unfinished work that must survive the task.
Ordinary task-scoped maintenance is allowed within actual project access and
the accepted request, subject to the shared organization protocol.

End substantive Codex content with `*Written by <Repository Display Name>'s
Codex*`, or the trusted Basecamp project name when project-scoped, or simply
`*Written by Codex*` when neither applies. Do not borrow a named agent's signature.

The Archivist's specialized operating scope remains narrower than this general
account guidance. Available authentication does not enable Basecamp projection,
recurring execution, global scans or bypass its existing lifecycle rules.
