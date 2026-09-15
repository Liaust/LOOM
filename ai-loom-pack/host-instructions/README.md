# Operator-Installed Host Instructions

These are host bootstrap sources, not project workspace templates or named-agent
identities. The operator installs `codex/AGENTS.md` into each already-approved
Codex home on Main, preserving any existing user instructions. It is not
automatically scaffolded into new repositories. The public template contains
identity placeholders, not a working account binding or credential values.
Resolve your own Codex profile's identity, account and account-scoped person IDs
and email, then replace all placeholders before installation. The shared
HQ/OPS/BET organization is an optional convention, not a LOOM dependency.

The companion `BASECAMP-ORGANIZATION.md` is copied from the exact section
between `BEGIN LOOM SHARED BASECAMP ORGANIZATION` and
`END LOOM SHARED BASECAMP ORGANIZATION` in
`templates/morathustra/protocols/BASECAMP.md`. That marked section is the sole
source for shared organizational rules. Do not copy the named identity sections
into a general-agent bootstrap or maintain a second edited organizational copy.
This gives each runtime a complete local instruction copy without a live
cross-workspace dependency or edits to the official Basecamp skill.

Publish only from a reviewed source checkpoint, refuse unexpected existing
content, and verify installed hashes. Existing sessions may need a new session
to load their bootstrap; do not interrupt them or restart services just to
refresh instructions. Authentication, credentials, defaults and schedules are
not changed by installing these files.
