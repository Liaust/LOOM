---
title: "External Agents And AI LOOM Pack"
description: "Understand optional agent runtimes, portable instructions, account bindings and device access."
audience: [user, operator, agent]
tags: [loom, user-guide, agents]
status: draft
verified_at: "2026-09-15"
source_scope: ["ai-loom-pack/manifest.yaml", "ai-loom-pack/catalogue.yaml", "internal/agentpack", "nix/modules/loom-morathustra.nix"]
related: ["[[Getting Started]]", "[[Projects]]", "[[Modules Connectors And Agents]]", "[[Documentation CLI]]", "[[Canonical Paths]]", "[[Automation]]", "[[ORCA Main Operations]]", "[[Main-To-Mac Computer Use]]"]
aliases: ["AI LOOM Pack", "Morathustra Workspace", "Archivist Workspace"]
---

# External Agents And AI LOOM Pack

## LOOM Serves Agents; It Is Not Their Harness

LOOM supplies projects, capabilities, jobs, technical Objects, Notes, Provenance,
and storage/recovery interfaces. Hermes owns its model loop, conversations,
native memory, tools and messaging gateway. ORCA owns its workspace, terminal
and coding-session interface. Codex and other coding agents retain their own
harnesses and repository instructions.

MINA is an optional Hermes persona and protocol template. The older Morathustra
name remains in compatibility code and historical recovery formats. It is not
a second required agent. Installing a source template does not initialize,
authenticate, launch or migrate a live profile.

## One Profile, Distinct Sessions

The reference MINA workspace is `/srv/loom/agents/mina`, with Hermes state under
`.hermes/` and one personality source at `.hermes/SOUL.md`. On a configured
host, `hermes --tui` uses the selected profile. A separately enabled messaging
gateway can use that same profile, but each conversation keeps its own session.

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
do not fall back to someone else's token or infer authorization from a skill.

Basecamp/GitHub protocols include an example task organization. Operators may
adapt that organization to their own account. LOOM core does not require the
maintainer's Basecamp structure, GitHub repositories, or email accounts.

## Device Routes

| Intent | Route and owner |
| --- | --- |
| Agent-owned web research | Hermes browser tools using configured Main Chromium |
| An ORCA-controlled shared page | ORCA's shared browser integration |
| The user's Safari, TextEdit or another Mac application | Explicitly paired Mac computer use via LOOM's bridge and the configured Mac driver |
| Remote shell work | Separately authorized SSH, not desktop control |

Mac computer use has been exercised through terminal and gateway sessions on
the development installation. Your machine still needs pairing, reachability,
macOS permissions and the correct runtime environment. An offline Mac must be
reported as unavailable; do not silently switch to Main's browser.
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

## Diagnose The Missing Layer

Distinguish missing implementation, installation, harness discovery, network,
account binding, policy and task authorization. A valid pack cannot prove a
live tool works; a working login cannot prove a requested action is permitted.
Report the exact failed layer and relevant error without exposing credentials.
