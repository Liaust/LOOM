---
title: "External Agents And AI LOOM Pack"
description: "Understand optional agent runtimes, portable instructions, account bindings and device access."
audience: [user, operator, agent]
tags: [loom, user-guide, agents]
status: draft
verified_at: "2026-11-11"
source_scope: ["ai-loom-pack/manifest.yaml", "ai-loom-pack/catalogue.yaml", "internal/agentpack", "nix/modules/loom-hermes-platform.nix", "nix/modules/loom-mina-tools.nix"]
related: ["[[Getting Started]]", "[[Projects]]", "[[Modules Connectors And Agents]]", "[[Documentation CLI]]", "[[Canonical Paths]]", "[[Automation]]", "[[ORCA Main Operations]]", "[[Main-To-Mac Computer Use]]"]
aliases: ["AI LOOM Pack", "Morathustra Workspace", "Archivist Workspace"]
---

# External Agents And AI LOOM Pack

## LOOM Serves Agents; It Is Not Their Harness

LOOM supplies projects, capabilities, jobs, technical Objects, Notes, Provenance,
and storage/recovery interfaces. Hermes owns its model loop, conversations,
native memory, tools, messaging gateway and native agent schedules. Codex and
other coding agents retain their own harnesses and repository instructions.
ORCA is retired from the current interaction model; historical integration code
does not make it a prerequisite for the shared agents account.

MINA is an optional Hermes persona and protocol template. The older Morathustra
name remains in compatibility code and historical recovery formats. It is not
a second required agent. Installing a source template does not initialize,
authenticate, launch or migrate a live profile.

## One Profile, Distinct Sessions

The operator chooses the workspace and native profile locations. MINA's current
portable template has one canonical personality source, root `SOUL.md`; its
configured native profile references that source. Runtime state is separate
from portable instructions. Installation or replay must preserve learned SOUL,
USER/MEMORY, skills, settings, sessions and pending work rather than replacing
them from a template. On a configured host, `hermes --profile <profile> chat`
uses the selected profile. A separately enabled messaging
gateway can use that same profile, but each conversation keeps its own session.

The older Morathustra compatibility template used `.hermes/SOUL.md`. That is a
legacy layout, not a second active personality or a migration instruction.
Preserve historical names, conversations and recovery history during any
separately authorized transition.

Shared history is not permission to disclose a private terminal conversation
to a messaging channel. Retrieve relevant prior context through native Hermes
session tools. Do not confuse conversational memory with Provenance records.

A private Git repository may hold reviewed portable instruction sources. It
must not mirror the live profile: tokens, session databases, memory, logs,
recovery packages and generated runtime files stay outside that source export.
The public LOOM checkout contains no ready agent account or private workspace.

## Skills And Protocols

`ai-loom-pack/` contains release-matched instruction sources. Its manifest and
catalogue describe discovery and recommended roles, not an authorization system.
The destination harness performs actual installation and discovery.

LOOM-managed instructions are exposed through Hermes `skills.external_dirs`
from the protected `skills/installed` tree. Native and agent-created skills
remain in Hermes' own skill storage. Managed source is updated through the
repository; native writes follow the configured Hermes policy. Categories
organize discovery, and are not separate grants or a fixed skill-count limit.

Shared retrieval and Provenance instructions are available to project agents
and MINA; Archivist adds a narrower review role. Inspect the current manifest
and the running harness rather than inferring installation from a historical
skill count. A skill does not authenticate an account or accept a decision.

```sh
loom agent pack status --pack-dir ai-loom-pack
loom agent pack validate --pack-dir ai-loom-pack
```

These are local source inspection commands. To preview a new template in an
explicit destination whose parent already exists:

```sh
loom agent pack scaffold-workspace --pack-dir ai-loom-pack \
  --template mina --path /path/to/review/mina --dry-run
```

Review the result before applying. Existing edited files are preserved;
conflicts are reported, not overwritten. This is not a live profile migrator.

## Account Configuration

Named wrappers such as `loom-mina-gh` and
`loom-mina-basecamp --profile mina` bind a protected operator-provisioned auth
store and the selected account. Enabling them requires the explicit non-secret
`externalAccountBinding` documented in [Nix configuration](../../nix/README.md).
Synthetic IDs in the example host are not credentials and must not be reused.

The general coding-agent `codex` Basecamp profile, when configured, is distinct
from MINA's named identity. Public contributors do not need either profile.
Configure the actual account/project mapping and verify it before task use;
missing or ambiguous bindings hold account reads and writes, not unrelated local
coding. Do not fall back to another profile, Personal defaults or ambient tokens.
An agent's identity, selected Box and authenticated service actor are separate;
changing project directories does not bootstrap a named identity.

The [host policy examples](../../ai-loom-pack/host-instructions/README.md) and
Basecamp/GitHub protocols require explicit operator adoption and verified
bindings. They grant no standing Personal authority, Company access, mailbox
access or publication rights. Company Basecamp writes, including completion
and remote drafts, need explicit approval of their destination and changes.
An ordinary worker does not inherit a named agent's accounts or private memory.
LOOM core does not require Basecamp, a private workspace repository or any
maintainer account. The optional task organization is an example, not live state.

## Device Routes

| Intent | Route and owner |
| --- | --- |
| Agent-owned web research | The active harness's native browser tools; Hermes uses `browser_exec`, with its configured CLI fallback only for a concrete limitation or CLI-only workflow |
| Main desktop applications | Native Hermes `computer_use` on the configured desktop; inspect the actual profile/session rather than assuming physical-login access |
| A Codex app or browser surface | Native computer-use/browser tools exposed by that Codex session |
| The user's Safari, TextEdit or another Mac application | The operator-configured `loom-mac` MCP connector in Hermes, or `loom-mac-computer-use` in Codex, targeting that Mac |
| Remote shell work | Separately authorized SSH, not desktop control |

Mac computer use requires pairing, reachability, macOS permissions and the
correct runtime environment. Source availability is not proof of a configured
connector or live session. An offline target is unavailable; do not silently
switch devices, substitute shell automation or launch ORCA. Its old integration
is documented only as [legacy operations](../operations/orca-main.md), not a
current shared-browser route.
See [Main-to-Mac computer use](../operations/main-mac-computer-use.md).

## Archivist And Provenance

Objects describe technical state. Notes return source material and citations.
Provenance returns qualified records, pending candidates and unresolved cases.
Project `.project/` state is a portable source, with rebuildable projections;
neither projection nor indexing automatically accepts semantic assertions.

The optional Archivist workspace supplies review instructions. Separately,
`main.provenance_archivist` inside `loomd` is a deterministic bounded worker
using existing lifecycle operations. That worker is not a continuously reasoning
agent. Its manual behavior does not enable recurring model-driven execution.
Hermes native cron and LOOM capability schedules remain distinct owners.
Optional model-driven reconciliation needs separately authorized scope and
activation. The public protocols supply no live job ID or standing lifecycle
write authority. Preserve existing pauses; never start a second timer from an
instruction file or treat a pending candidate as permission.

## Diagnose The Missing Layer

Distinguish missing implementation, installation, harness discovery, network,
account binding, policy and task authorization. A valid pack cannot prove a
live tool works; a working login cannot prove a requested action is permitted.
Report the exact failed layer and relevant error without exposing credentials.
