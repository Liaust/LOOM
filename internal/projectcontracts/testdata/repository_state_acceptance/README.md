# Repository State Acceptance Fixtures

These fixtures are copied only into disposable roots by
`tests/smoke/v2_project_repository_state_local.sh`.

- `v03-*` and `v04-compat-*` encode equivalent watched-root policy without
  inferring repository members.
- `v04-empty`, `v04-one`, `v04-many`, and `v04-remote` cover explicit member
  cardinality and observation posture.
- `v04-duplicate` is intentionally invalid inside one source contract.
- `v04-cross-conflict` is valid source that intentionally reuses an owning
  repository ID from `v04-one`; persistence must reject the second owner.
- `repo-identity-*` are copied under disposable member `.repo/` roots.

The `__PROJECT_ID__` and `__REPOSITORY_ID__` markers are rendered only after a
disposable runtime has generated or selected the identity being tested.
