---
name: agent-browser
description: Browse and test websites using Main's local Chromium. Read pages, interact with forms, extract content and take screenshots. Use for agent-owned browser tasks, not for inspecting the operator's existing Mac browser tabs.
---

# Main Browser

Full Chromium runs headlessly on Main: it renders JavaScript and images and
can take screenshots, but does not open a window on the operator's Mac. Its sessions
do not inherit Safari/Chrome logins. Mac browser handoff remains separate.

In Hermes, use the native browser tools for normal navigation, snapshots,
clicks, typing and screenshots. They manage task-isolated sessions. General
coding agents can use the installed `agent-browser` CLI directly.

## CLI Workflow

Choose a distinct session name for the task and use it on every command:

```sh
agent-browser --session research-1 open https://example.com
agent-browser --session research-1 snapshot -i
agent-browser --session research-1 click @e1
agent-browser --session research-1 snapshot -i
agent-browser --session research-1 screenshot /tmp/research-1.png
agent-browser --session research-1 close
```

Use only refs returned by the latest snapshot. Re-snapshot after navigation or
DOM changes. `fill @e1 "text"` replaces an input; `press Enter` submits where
appropriate. `get text @e1`, `get url` and `get title` inspect the current page.
Snapshots provide structure; use screenshots when layout or visual appearance
matters. Save durable task artifacts to the task's approved output directory.

Close your own session when finished; do not close every agent's sessions.
For less common operations, `agent-browser skills get core --full` and command
`--help` provide the installed version's reference. These are reference tools,
not a substitute for this normally installed skill.

## Managed Environment And Accounts

Chromium and the driver are installed through Nix. Do not run npm installation,
`agent-browser install`, or lazy browser downloads to repair them. Report the
exact launch failure rather than disabling Chromium's sandbox or changing host
permissions. No headed desktop or external CDP endpoint is needed for local use.

Use approved CLI/API integrations for Basecamp and GitHub when they fit the
task. Browser installation does not authorize account login, credential access,
profile copying, publishing or other unrelated external effects. When human
authentication is needed, provide the exact URL and explain that this Main
session is separate from the user's Mac browser; do not claim they are shared.
