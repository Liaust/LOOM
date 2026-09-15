# Repository State Protocol

## Task size and authority

`.repo/` is optional portable development context. Ordinary edits do not require
a feature lifecycle, new planning files or installation. Follow the repository's
actual instructions and relevant existing source. Reading source does not register
resources, refresh projections or accept semantic claims.

The owning project's `.project/` is the default project-wide context. Preserve
it and this existing repository state separately; no automatic rename or
merge is implied. A component worktree outside its parent project uses relevant
parent context from the coordinator's handoff rather than a copied control tree.

The owning project's `.loom/project.yaml` (or legacy `.loom/contracts/repos.yaml`) owns
project identity and repository membership. `.repo/repo.yaml` declares identity
and backlink; Git observations and declarations are not accepted semantic context.
The integrator owns repo.yaml, STATE, ROADMAP, releases, cross-feature decisions
and integration status. Workers honor their assigned scope.

## Planned feature lifecycle

When a repository uses the feature lifecycle, committed `status: ready` scope
permits its worker to start. The worker records `in_progress`, checkpoints,
validation and handoff, then finishes at `review`. The integrator advances to
`integrated`; required acceptance advances to `validated`. Use future/, initiatives/,
features/, decisions/, releases/ and integrations/ for their corresponding state.

## Portable state and explicit opt-in

Keep `.repo/` reviewable Git source with relative slash-separated paths. No
credentials, absolute paths, session or conversation IDs, worktree locations,
caches, locks, logs, generated indexes, private memory or harness state. Repository source wins over conflicting chat;
report the mismatch. Initialization, migration and pack updates need explicit review.

`loom repo-state validate --path <repository>` is read-only. Supply all four
membership flags together when proving owner/repository matching locally.
`loom repo-state init --path <repository> --pack <project-pack> ...` and
`loom repo-state migration-plan --path <repository> --pack <project-pack> ...`
produce dry-run plans. Apply uses the same paths/identity inputs, exact reviewed
`--plan-digest`, `--apply` and `--yes`. Unrelated dirty Git state blocks apply.
Preserve repository-owned files; only unchanged LOOM-generated files with a
matching embedded digest may be replaced. Apply creates only reported files; it never removes `.project/`,
never stages or commits, manages worktrees or starts workers. The native harness
retains review and cleanup ownership.
