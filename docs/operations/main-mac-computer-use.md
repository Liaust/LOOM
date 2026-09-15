---
title: "Main-To-Mac Computer Use"
description: "Use MINA's native Mac desktop connection and recover from runtime turnover without replaying input."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - operations
  - agents
  - troubleshooting
status: draft
verified_at: "2026-09-11"
source_scope:
  - "ai-loom-pack/templates/mina/protocols/MAC-COMPUTER-USE.md"
  - "nix/files/hermes-mac-computer-use/bridge.py"
  - "nix/files/hermes-mac-computer-use/mac_endpoint.py"
  - "nix/modules/loom-morathustra.nix"
  - "scripts/loom-mac-computer-use-setup"
related:
  - "[[ORCA Main Operations]]"
  - "[[Main And Mac Operations]]"
  - "[[Updating And Rebuilding]]"
  - "[[External Agents And AI LOOM Pack]]"
---
# Main-To-Mac Computer Use

## Use The Existing Connection

On a configured installation, MINA runs on Main. Her selected Hermes profile exposes native
`computer_use` for the logged-in Mac desktop. Open the existing MINA folder
in ORCA, run `hermes --tui`, and ask for a particular Mac application or
window. The Discord gateway shares the profile but has an independent session.

The development installation has exercised this path through TUI and Discord
gateway sessions. That does not establish pairing, permissions, or every
application action on another machine. The public source includes no working
Mac binding or credential; the operator must configure those separately.

## Choose The Surface

| Need | Owner and route |
|---|---|
| Main project file or command | Main terminal or supported LOOM capability |
| Authorized development command on Mac | Separately configured Mac SSH |
| Fresh web research | Native Hermes browser on Main Chromium |
| Already shared ORCA page | ORCA browser tools for the exact page and owning host |
| Existing Safari, Chrome, Finder or other Mac app | Native Hermes `computer_use` |
| Objects, Notes, Provenance or project contracts | The owning LOOM/repository surface |

Main Chromium is not the operator's Safari session; an ORCA shared page is not
the entire Mac desktop. If "this page" is ambiguous, clarify the surface before
inspecting unrelated windows. A GUI refusal does not authorize a shell bypass.

## Data Flow

Hermes launches the fixed Main bridge. It connects over noninteractive SSH
to the restricted Mac endpoint, which speaks MCP to Cua Driver in the logged-in
user's desktop session. Captures return native images and accessibility
elements through the same route. Cua owns input delivery and macOS consent.

The endpoint key grants that command only: no general shell, PTY or forwarding.
The public binding is `/etc/loom-mac-computer-use/binding.json`; the protected
key remains outside the workspace. Do not read or export its bytes.

Only the selected profile enables this route. Other profiles default off and
have inherited Mac routing variables removed. ORCA's rootless namespace and the
gateway must expose the approved binding with trusted ownership. Translated
nobody ownership is not a reason to weaken admission. Ending one connector
session does not stop the shared Cua app.

## Target And Verify

Resolve the exact app/PID/window ID through the native schema, then capture.
Element indices belong to that session's fresh capture. Generation is returned
metadata, not a tool argument. Observe the same target after acting.

Browser background typing is best-effort. Inspect the field after an ineffective
attempt. With foreground interaction authorized, use `delivery_mode: foreground`
and normally omit `bring_to_front`. That separate persistent activation can
fail exact-window verification around overlays/popups. Do not repeatedly force
activation or conclude that Cua must be minimized.

Scroll with a fresh page/scroll-area element or coordinates, not direction
alone. Verify text before Return and page position after scrolling. Transport
success is not visual proof when the driver reports an unverifiable effect.
Remote `set_value` remains refused. The portable Mac protocol supplies exact
examples without granting new task authority.

## Restart And Availability

Complete v1 bindings retain original PID/socket-inode observations. Admission
compares stable host, user, peer UID, executable/hash, socket path, permission
mode and policy hashes. Ordinary PID/inode turnover therefore does not require
republishing the binding.

Verified turnover creates a fresh session/generation and clears old capture
handles. Input based on stale state is refused before dispatch; capture again.
Turnover during an input makes its outcome unknown. Nothing queues or replays it.
Changed executable, user, host or policy still requires operator review.

The connector does not start a stopped driver, unlock Mac, grant consent or
replace credentials. Once available, a bounded read-only recheck can reconnect.

| Result | Response |
|---|---|
| `mac_unreachable` | Report unreachable; sleep/offline are possible causes, not proven diagnoses. |
| `mac_auth_failed` / `mac_host_identity_mismatch` | Review the exact credential or stable identity issue; no alternate account/key. |
| `mac_permission_denied` / `mac_desktop_unavailable` | Resolve actual consent or desktop availability with the operator. |
| `mac_driver_unavailable` | Driver stopped, missing or incompatible; do not relabel it offline. |
| `mac_target_stale` / `mac_busy` | Reobserve after availability; never queue stale input. |
| `mac_action_outcome_unknown` | An effect may have occurred. Inspect and reconcile before another action. |
| `mac_protocol_error` or absent delivery | Preserve uncertainty; missing evidence is not proof of no dispatch. |
| `mac_policy_refused` / `mac_output_limit` | Respect the boundary or narrow the inspection. |

Read code, phase, delivery and next_action together. A timeout or local cleanup
cannot prove remote rollback. Never blindly repeat input or submission after
possible delivery. Keep screenshots, private titles and credentials out of
portable instructions and public receipts.

## Validation And Maintenance

Pinned native fixtures exercise fake SSH/MCP, targeting, interruption,
session isolation and runtime turnover without a VM or production data.
The comprehensive local entry point is:

```bash
bash tests/smoke/v2_main_mac_computer_use_local.sh
```

It requires exact cached package outputs. If garbage-collected, report that
packaged-runtime gate unavailable rather than rebuilding a large unrelated
closure or silently switching versions. Smaller source/native tests are
separate evidence, not proof that the complete packaged smoke passed.

Before live changes read [[Updating And Rebuilding]] and the machine runbooks.
Preserve active sessions, SSH trust, permissions and profile state. Publish
only reviewed connector/instruction changes with retained prior bytes and
current health/recovery checks. Rollback restores code/config, not stale
conversations or memories. Do not reset TCC, uninstall shared Cua or clean
unrelated storage to repair a connection.
