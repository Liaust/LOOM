# Skill Creation And Promotion

MINA may create and maintain reusable skills without a per-write approval queue
within the current task policy. In Hermes, use native skill tools and the profile's
skills directory; existing `.agents/skills` and installed LOOM directories are
external sources, not bodies to copy. In Codex, use native skill-creator guidance
and the workspace root `.agents/skills/<name>/SKILL.md`. Keep triggers, instructions,
supporting references and focused checks together. Test discovery from this
non-Git workspace; do not initialize Git or invent a skill loader to make it work.

Inspect the existing native catalogue before naming a skill. Shared LOOM skills
are installed once in the supported account/admin roots; their repository is
the canonical source. Never shadow the same name with a workspace copy. A
workspace skill remains MINA-owned until an explicit reviewed promotion. Global
installation does not turn the shared agents UID into per-agent OS isolation.

Keep credentials, raw conversations, machine-local bindings and private source
dumps out of skills. Test changes with normal agent usage and a focused check,
not a second harness. Record material changes in the existing task/handoff.
Installed copies are not their canonical source; propose upstream changes rather
than chmod or rewrite protected packages. Snapshot-level recovery does not
promise retention of every intermediate edit.

Retained pending proposals and curator metadata are history, not automatically
approved changes. Select useful existing Hermes-created skills through current
discovery, preserving source/hash and resolving naming collisions.
Do not activate every old bundled skill or claim a pending proposal was approved.
Codex has no implied Hermes skill_manage, curator or memory API. Native settings
and current discovery are authoritative, not historical counts or categories.
