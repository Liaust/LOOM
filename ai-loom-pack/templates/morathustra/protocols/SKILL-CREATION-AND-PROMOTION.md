# Skill Creation And Promotion

Morathustra may propose, create and maintain reusable skills through native
Hermes tools. Inspect existing skills first and use `skill_manage` with
`action="create"`, a unique name and `category="created"`. The destination is
`.hermes/skills/created/<name>/SKILL.md`, a category within the supported native
`.hermes/skills` store. Pinned Hermes has no `skills.create_dir` setting. Do not
invent another root, move or copy bundled skills, introduce symlinks, or patch
upstream to change this layout.

Keep reusable instructions grounded in tested work: include clear triggers,
constraints and checks. Do not copy private memory, credentials, runtime state
or machine-local paths into a skill. Supporting files and later edits use the
same native skill manager and approval policy.

## Creation And Review

Keep `skills.write_approval=true` and `memory.write_approval=true`.
`skills.guard_agent_created=true` enables heuristic review of agent-created
skill content; it can miss hazards or flag harmless text and is not a security
guarantee or a replacement for permission checks.

A successful response with `staged=true` means a proposal is pending, not that
the skill has been saved. Use the native `/skills pending` and
`/skills diff <id>` review surfaces. the operator can accept with
`/skills approve <id>` or discard with `/skills reject <id>`. Never approve
your own pending changes on the operator's behalf or replay the write through a
raw file edit to bypass approval. After approval, re-read the actual skill
through supported tools before reporting completion. If a surface cannot show
the full diff, move review to a supported TUI or dashboard surface; do not
replace it with a guess from the proposal summary.

## Separate Owners

`skills/installed` is the distinct LOOM-managed external set exposed through
`skills.external_dirs`. It is not a creation destination. Never edit, delete,
rename, chmod, replace or symlink installed files or their parent directories.
Do not shadow a LOOM-installed skill name with a native skill as a way around
review. Native lookup searches local skills before external directories; check
both owners before choosing a name. If lookup ownership is ambiguous, stop.

To promote a created skill, prepare a sanitized source proposal in the owning
repository with triggers, constraints, evidence and tests. A project worker
reviews and validates the pack and catalogue. An authorized operator deploys
the accepted source into the installed set. No self-promotion or in-place
mutation of an installed copy is allowed. Read-only modes owned by the same
user are not a security sandbox. Actual OS/service custody of the installed
set and its parents remains independently authoritative for gateway and TUI.

## Conservative Native Curator

The following conservative settings are an optional configuration example,
not a claim about a live profile or authorization to enable maintenance:

- `prune_builtins=false` and `consolidate=false`.
- `interval_hours=168` and `min_idle_hours=2`.
- `stale_after_days=30` and `archive_after_days=90`.
- `archive_ttl_days=0` and `backup.enabled=true`, `backup.keep=5`.

It may mark or recoverably archive eligible curator-owned skills under those
limits; it does not run the LLM consolidation pass. Native pre-run backups keep
five regular snapshots. Do not force a curator run, request consolidation,
bulk-adopt skills or issue an irreversible archive purge.

The configured `min_idle_hours=2` is not an enforced gateway idle guard in the
pinned runtime: its hourly housekeeping tick passes
`idle_for_seconds=float("inf")` to `maybe_run_curator`. The weekly interval and
ownership gates still apply, but do not promise two hours of gateway inactivity
before maintenance. This upstream limitation is not permission to force a run.

Skill selection re-reads config through an mtime/size cache, and the gateway's
hourly curator tick reads current settings. A restart is not needed merely to
reload these settings. Preserve active sessions; if an existing TUI catalogue
still needs refresh, use a new session when convenient rather than interrupting
current work.

Foreground-created skills are user-owned even when written by an agent and
placed in `created`. Category alone does not transfer curator ownership.
the operator must explicitly opt an eligible individual skill in through the
supported `hermes curator adopt <name>` flow. Review its current activity first:
adoption does not reset the inactivity clock. Never forge an ownership marker
or edit usage metadata directly. Bundled, hub-installed and external skills
keep their existing owners; `prune_builtins=false` preserves all bundled skills.

## Skill Selection

Inspect the actual installed catalogue and the operator's selected skills.
Change selection with `skills.disabled` only when authorized, not deletion.
This template supplies no private inventory or live count. ORCA's legacy skills
are retired, not an availability fallback; do not disable native Hermes tools
merely because a retired client once exposed similarly named skills.
Do not infer a new-skill approval requirement from it; the native write-approval
and installed/created ownership rules above still apply. Keep platform-hidden
skills untouched. Changing selection does not transfer
ownership. Follow `TOOLING.md` for managed Nix settings; the baseline is a
provisioning template, not permission to overwrite live profile configuration.
