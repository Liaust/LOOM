# Repository Development Pack

Normal projects now start with project-wide `.project/` and a short `AGENTS.md`.
They do not need this legacy repository pack for project context or Provenance
search. Existing `.repo` sources remain supported and are not renamed or merged.

This optional, project-local pack contains source templates for a repository's
Git-tracked `.repo/` development state. A legacy LOOM project scaffold carries the
pack here; LOOM never installs or updates it inside a member repository without
an explicit, digest-reviewed local apply.

## Explicit Opt-In

The repository-leading agent may opt in only after it:

1. Confirms the repository's canonical owning membership in
   `.loom/contracts/repos.yaml` at the LOOM project root.
2. Runs `loom repo-state init` or `loom repo-state migration-plan` with the
   explicit repository path, this pack path, and complete repository identity.
3. Reviews the dry-run report, conflicts, Git posture, and printed plan digest.
4. Repeats the same inputs with `--apply --plan-digest <digest> --yes` only when
   the plan is accepted.
5. Reviews the complete unstaged Git diff for the correct repository ID,
   project backlink, role, portable paths, and absence of credentials or
   harness state, then runs repository quality gates and commits explicitly.

Existing repository-owned `.repo/` files are preserved. Apply may replace only
a LOOM-generated file whose embedded ownership marker and content digest still
match; manual edits become conflicts. The command never stages or commits.

## Boundaries

- `.loom/` remains the source for LOOM project identity, membership, and policy.
- `.repo/` declares one repository's development state; declarations are not
  accepted semantic truth.
- Git commits and handoffs are the portable integration boundary.
- Codex and ORCA retain their own live tasks, threads, worktrees, and cleanup.
- Runtime caches, credentials, absolute paths, session identifiers, private
  memory, generated indexes, and mutable harness state do not belong in
  `.repo/`.

Start with `templates/.repo/README.md`, then read the protocol files it names.
