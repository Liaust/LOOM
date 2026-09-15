---
title: "ORCA Main Operations"
description: "Configure ORCA's optional Main runtime and distinguish its sessions, agent launch environment and device access."
audience: [operator, developer, agent]
tags: [loom, operations, agents, orca]
status: draft
verified_at: "2026-09-15"
source_scope: ["nix/modules/loom-orca.nix", "nix/modules/loom-morathustra.nix", "nix/packages/orca-headless.nix", "nix/files/morathustra-account-tool.sh"]
related: ["[[Main And Mac Operations]]", "[[External Agents And AI LOOM Pack]]", "[[Main-To-Mac Computer Use]]", "[[Updating And Rebuilding]]"]
aliases: ["ORCA On Main"]
---

# ORCA Main Operations

## Optional Workspace And Terminal Interface

ORCA is an external application, not LOOM's agent reasoning engine. Its
headless service lets paired clients use Main-owned folders, repositories,
terminals and coding tasks. The reference package is pinned in
`nix/packages/orca-headless.nix`, including a narrow non-Git folder CLI patch.
The upstream version and available command syntax must be checked when updating.

The public configuration is an evaluation example. Pair your own client and
configure private access before using it. A repository checkout does not come
with a Main connection, mobile credential or workspace registration.

## Runtime And Paths

The reference service runs as the dedicated `agents` user and uses the
private WireGuard endpoint. LOOM's `loomd` is a separate service/user. The
ORCA service, Hermes gateway and their persistent state have separate owners
even when they share the same Unix agent account.

| Path or component | Purpose |
| --- | --- |
| `/srv/loom/agents/mina` | Optional MINA workspace |
| `/srv/loom/agents/mina/.hermes` | Native Hermes profile and one SOUL |
| `/srv/loom/box` | Main project and working-file sources |
| `/run/loom/loomd.sock` | Local LOOM API access under configured permissions |
| `/etc/loom-mina-accounts.json` | Immutable non-secret operator account binding |
| `/var/lib/loom-mina-auth` | Protected named auth/config store, not portable source |
| `/etc/loom-mac-computer-use/binding.json` | Operator-verified public Mac pairing metadata |

Historical Morathustra identifiers remain for explicit compatibility and
retained recovery. They are not instructions to create a second active runtime.

## Launch Hermes In The Correct Context

Open the intended Main folder in ORCA and use `hermes --tui`. The configured
agent shell selects `HERMES_HOME` and the packaged executable. A gateway using
the same profile still runs a distinct conversation/session.

A direct SSH shell, ORCA terminal and systemd gateway can differ in environment,
namespace and filesystem visibility. Verify the actual failing context rather
than treating a successful SSH probe as proof of every path. End/reopen a
session when it retains a pre-update environment; do not overwrite its state.

Hermes owns its native sessions, memory, skills, cron and messaging adapters.
LOOM owns the release wiring and its own service interfaces. The reference
keeps managed skill sources separate from Hermes-native created skills.
Read [[External Agents And AI LOOM Pack]] for distribution and role boundaries.

## Accounts

The optional `loom-mina-gh` and `loom-mina-basecamp` wrappers select installed
native tools and isolated child configuration. They discard ambient token/host
overrides and pin the intended profile/endpoint. The Basecamp account ID comes
from explicit `loom.mina.externalAccountBinding`, not a maintainer constant.
See [Nix configuration](../../nix/README.md).

Supply your own verified public identities and OAuth grants. The general
`codex` Basecamp profile is independently configured; it is not MINA's actor.
Source protocols are templates and do not authenticate either account.
Do not publish protected auth stores or put tokens in ordinary workspace files.

## Browser, Desktop And Shell

- Hermes browser tools operate configured Main Chromium.
- ORCA browser tools operate the shared ORCA page.
- Hermes native computer use can route through LOOM's paired Mac bridge.
- Ordinary SSH grants shell access separately.

Mac tools require the correct bridge environment in both TUI and gateway.
The pinned public binding supports the ORCA rootless namespace without trusting
arbitrary unmapped file owners. Review the configured hash and visibility if
admission fails; do not replace identity checks with a world-writable binding.

The desktop route cannot unlock a Mac or grant its own macOS permissions.
Offline, stale-target and unknown-delivery outcomes must remain explicit.
See [[Main-To-Mac Computer Use]] for action/reconnect semantics.

## Updates And Diagnostics

Update the pinned source/package and compatibility patch through the existing
LOOM/Nix path. An upstream self-updater cannot replace Nix-owned files safely.
Inspect service status, bounded logs, selected executable/version and current
pairing. Logs can contain sensitive connection information; redact before sharing.

A restart changes process/session state and can interrupt work. Drain only the
affected work and preserve rollback information. Neither a rendered Nix unit nor
a healthy process proves model authentication, external accounts or desktop input.
Test the specific route through an ordinary authorized session after deployment.
