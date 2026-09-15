---
title: "Getting Started"
description: "Use a configured LOOM installation and choose the right interface and information source."
audience: [user, operator, agent]
tags: [loom, user-guide]
status: draft
verified_at: "2026-09-15"
source_scope: ["internal/loomcli/root.go", "internal/loomcli/enter.go", "internal/loomcli/project_context.go"]
related: ["[[External Agents And AI LOOM Pack]]", "[[Projects]]", "[[Canonical Paths]]", "[[Automation]]", "[[Notes And Search]]", "[[Diagnostics And Support]]", "[[Command Safety]]", "[[ORCA Main Operations]]"]
aliases: ["Start With LOOM"]
---

# Getting Started

## First, Know Which System You Are Using

This page assumes a configured installation. For a new machine, read
[Installation](../operations/installation.md). A source checkout or CLI binary
does not imply that a daemon, database, identity, network or agent is ready.

Main owns global coordination, policy, discovery, knowledge and archive
services. Workspace nodes own their live local files. Confirm the selected
context before interpreting output:

```sh
loom version
loom status
loom health
```

`version` identifies the CLI. `status` gives an operational summary;
`health` checks the selected backend. Use `loom --json status` for structured
output. Open the terminal Portal with `loom enter`.

## Work With A Project

Projects are ordinary directories. New projects start with `notes/`, `repos/`,
`.loom/` declarations and `.project/` development context. They do not
automatically create Git repositories or enroll every file in a search engine.

Edit code and documents normally. Declare only the resources that LOOM should
manage, then use the project plan/apply/status workflow. An existing project
can explain its development context without starting deployment:

```sh
loom project context <project-ref-or-path>
loom project status <project-ref-or-path>
```

See [Projects](projects.md) for creation, declared membership, application
prerequisites, archival and compatibility with legacy layouts. A Git worktree
is an implementation checkout, not a new canonical project or deployment.

## Optional Agent Interface

If the operator configured Hermes with the MINA template, open that workspace
through ORCA or a terminal on Main and run `hermes --tui` with its configured
`HERMES_HOME`. The same profile may also serve a configured Discord or other
gateway. TUI and gateway conversations remain distinct sessions, even when
their shared profile makes earlier context retrievable.

MINA is an optional persona, not a requirement for the CLI or a built-in model.
Her source template grants neither credentials nor access to a desktop.
Read [Agent integration](agents.md) for tools, accounts and native runtime ownership.

## Ask The Right Source

| Question | First route |
| --- | --- |
| Where is the current file or version? | Objects and technical inspection |
| What does a source document say? | Notes search, then the exact cited passage |
| What was decided, constrained, superseded or left unresolved? | Provenance search, then exact record/candidate/case |
| Which project/repository should I work in? | Project registry or qualified project/repository discovery |
| What was said in an agent conversation? | The harness's session/history tools |

Notes results remain source material. Provenance candidates remain pending
until reconciled. Neither indexing nor backup automatically accepts a decision.
Check enrollment and processing state when a search is empty.

## Background Work And Recovery

LOOM schedules target capabilities; workers have their own policy and run
history. A scheduled next occurrence is not proof of successful execution.
Inspect the actual run, coverage and failure before retrying. Model-driven
Hermes automation is a different runtime until a LOOM adapter explicitly owns
it. See [Automation](automation.md).

Daily timing is operator configuration, not a universal timezone or installed
default. Recovery coverage must match the data you care about; a cloud archive
listing alone is not a completed restore proof.

## When A Route Is Unavailable

Preserve the command, selected context and bounded error. Do not change
permissions or switch machines merely to bypass a refusal. Main Chromium,
ORCA's shared browser, paired Mac computer use and SSH are separate routes.

Restoring project files can deliberately leave runtime services inactive.
A successful copy or restore is not permission to restart an application.
See [[Command Safety]] and [[Diagnostics And Support]] for operations.
