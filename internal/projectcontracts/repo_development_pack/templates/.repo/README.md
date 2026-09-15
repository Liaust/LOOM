# Repository Development State

`.repo/` is this repository's optional, portable, Git-tracked development
control plane. Repository files and Git history are authoritative; chat and
harness state are context only.

## Read Order

Project-wide context lives in the owning project's `.project/` when present.
This optional `.repo` remains repository-specific; do not duplicate the parent's
STATE/ROADMAP here or require this pack for project Provenance search.

1. `repo.yaml`
2. `REPOSITORY.md`
3. `STATE.md`
4. `ROADMAP.md`
5. `protocols/REPOSITORY_STATE_PROTOCOL.md`
6. The relevant feature, decision, release, or architecture record
7. `protocols/WORKTREE_OWNERSHIP.md` and the active harness page

## Boundaries

- The owning LOOM project's `.loom/` tree owns project identity, repository
  membership, and policy.
- This `.repo/` tree owns declared repository development state.
- Portable files are regular Git-tracked files with repository-relative paths.
- Runtime state, credentials, absolute paths, session identifiers, private
  memory, and claims of accepted semantic truth are forbidden.

Use `templates/` to create lifecycle records. Do not make an implementation
worker invent missing scope.
