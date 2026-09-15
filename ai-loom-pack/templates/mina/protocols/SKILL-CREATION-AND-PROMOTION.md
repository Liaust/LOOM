# Skill Creation And Promotion

MINA may propose, create and maintain reusable skills through native
Hermes tools. Inspect existing skills first and use `skill_manage` with
`action="create"`, a unique name and `category="created"`. The destination is
`.hermes/skills/created/<name>/SKILL.md`, a category within the supported native
`.hermes/skills` store. Pinned Hermes has no `skills.create_dir` setting. Do not
invent another root, move or copy bundled skills, introduce symlinks, or patch
upstream to change this layout.

Keep reusable instructions grounded in tested work: include clear triggers,
constraints and checks. Do not copy private memory, credentials, runtime state
or machine-local paths into a skill. Supporting files and later edits use the
same native skill manager and task-level operating policy.

## Creation And Review

Maintain native/local skills without a per-write approval queue. The accepted
MINA settings are `skills.write_approval=false`, `memory.write_approval=false`
and `skills.guard_agent_created=false`. This removes the heuristic scan of
MINA's own edits, not external/hub installation scanning. Follow
`OPERATING-POLICY.md` for task authority; use native tools rather than a second
skill store. Verify the actual saved content after a successful edit.

Existing pending proposals are not automatically applied by changing settings.
Inspect their exact native diff and approve/reject only the intended proposal
under the accepted task. A response with `staged=true` still means pending,
not saved. Do not replay ambiguous writes blindly. If a review surface omits
the patch, obtain the exact native proposal through an operator-supported
surface rather than guessing from its summary.

Native skill copies belong to the writable profile. A read-only local copy is
a custody fault to report, not proof that all skill maintenance is prohibited.
The operator may repair that native copy; this never permits chmod or edits in
`skills/installed`. Recovery provides snapshot-level recovery, not a guarantee
that every intermediate skill or memory revision is retained.

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

The inherited accepted baseline specifies these conservative native curator
settings. Deployment must verify effective state; this portable source does
not activate maintenance or overwrite current configuration:

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

The effective native selection is authoritative. Historical counts and unused
skill lists are not a curated set, a ceiling, or permission to restore obsolete
choices. Preserve current selections when publishing protocols. Native creation
does not require adding a Nix catalogue entry; changing selection does not
transfer ownership. Follow `TOOLING.md` for managed configuration changes.
