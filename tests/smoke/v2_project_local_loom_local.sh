#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOOM_CMD="${LOOM_CMD:-go run ./cmd/loom}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v2-project-local.XXXXXX")"
pass_count=0

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

loom() {
  # LOOM_CMD may be an installed binary or a development command such as
  # "go run ./cmd/loom".
  # shellcheck disable=SC2086
  (cd "$ROOT" && $LOOM_CMD "$@")
}

assert_file() {
  local root="$1"
  local path="$2"
  [[ -f "$root/$path" ]] || fail "missing expected file: $root/$path"
}

assert_dir() {
  local root="$1"
  local path="$2"
  [[ -d "$root/$path" ]] || fail "missing expected directory: $root/$path"
}

assert_executable() {
  local root="$1"
  local path="$2"
  [[ -x "$root/$path" ]] || fail "missing expected executable: $root/$path"
}

assert_absent() {
  local root="$1"
  local path="$2"
  [[ ! -e "$root/$path" ]] || fail "unexpected path: $root/$path"
}

assert_contains() {
  local path="$1"
  local pattern="$2"
  grep -Fq -- "$pattern" "$path" || fail "$path does not contain: $pattern"
}

preset_facets() {
  case "$1" in
    minimal) printf '%s\n' 'notes backup_policy portal' ;;
    research) printf '%s\n' 'notes docs sync_policy backup_policy worker_policy portal' ;;
    automation) printf '%s\n' 'notes scripts schedules direct_events tests secrets sync_policy backup_policy worker_policy portal' ;;
    connector) printf '%s\n' 'notes scripts connectors docs tests secrets sync_policy backup_policy worker_policy portal' ;;
    module) printf '%s\n' 'notes repos connectors modules docs tests sync_policy backup_policy worker_policy portal' ;;
    *) fail "unknown preset: $1" ;;
  esac
}

preset_human_dirs() {
  case "$1" in
    minimal) printf '%s\n' 'notes' ;;
    research) printf '%s\n' 'notes docs' ;;
    automation) printf '%s\n' 'notes scripts schedules direct_events tests secrets' ;;
    connector) printf '%s\n' 'notes scripts connectors docs tests secrets' ;;
    module) printf '%s\n' 'notes repos connectors modules docs tests' ;;
    *) fail "unknown preset: $1" ;;
  esac
}

preset_singletons() {
  case "$1" in
    minimal) printf '%s\n' 'notes.yaml backup.yaml' ;;
    research) printf '%s\n' 'notes.yaml sync.yaml backup.yaml workers.yaml' ;;
    automation|connector) printf '%s\n' 'notes.yaml sync.yaml backup.yaml workers.yaml credentials.yaml' ;;
    module) printf '%s\n' 'notes.yaml repos.yaml sync.yaml backup.yaml workers.yaml' ;;
    *) fail "unknown preset: $1" ;;
  esac
}

assert_no_legacy_layout() {
  local root="$1"
  local path

  for path in \
    loom.project.yaml \
    notes/loom.notes.yaml \
    repos/loom.repos.yaml \
    policies/sync.yaml \
    policies/backup.yaml \
    policies/workers.yaml \
    policies/credentials.yaml \
    tests/validate_project.sh
  do
    assert_absent "$root" "$path"
  done
  assert_absent "$root" policies
}

assert_repository_development_pack() {
  local root="$1"
  local path
  local pack='.loom/agent-packs/repo-development'

  for path in \
    README.md \
    templates/.repo/repo.yaml \
    templates/.repo/README.md \
    templates/.repo/REPOSITORY.md \
    templates/.repo/STATE.md \
    templates/.repo/ROADMAP.md \
    templates/.repo/protocols/REPOSITORY_STATE_PROTOCOL.md \
    templates/.repo/protocols/WORKTREE_OWNERSHIP.md \
    templates/.repo/protocols/CODEX_WORKFLOW.md \
    templates/.repo/protocols/ORCA_WORKFLOW.md \
    templates/.repo/protocols/SLICE_AUTOPILOT_PROTOCOL.md \
    templates/.repo/protocols/CODE_REVIEW_PROTOCOL.md \
    templates/.repo/templates/feature/feature.yaml \
    templates/.repo/templates/feature/implementation_slices.md \
    templates/.repo/templates/feature/worktree_progress.md \
    templates/.repo/templates/feature/handoff.md
  do
    assert_file "$root" "$pack/$path"
  done

  assert_contains "$root/$pack/README.md" 'LOOM does not'
  assert_contains "$root/$pack/README.md" 'install, apply, register, or update it inside member'
  assert_contains "$root/$pack/templates/.repo/repo.yaml" 'schema_version: repo.state.v1'
  assert_contains "$root/$pack/templates/.repo/repo.yaml" 'slug: v2-smoke-'
  assert_contains "$root/$pack/templates/.repo/protocols/WORKTREE_OWNERSHIP.md" 'One harness owns each worktree'
  assert_contains "$root/$pack/templates/.repo/protocols/CODEX_WORKFLOW.md" 'Codex-native'
  assert_contains "$root/$pack/templates/.repo/protocols/CODEX_WORKFLOW.md" '`openai-docs`'
  assert_contains "$root/$pack/templates/.repo/protocols/ORCA_WORKFLOW.md" 'installed `orca-cli` skill'
  assert_contains "$root/AGENTS.md" "$pack/"
  assert_contains "$root/AGENTS.md" 'repository-leading agent'
  assert_contains "$root/.loom/agents/project.md" "$pack/"
  assert_contains "$root/.loom/agents/project.md" 'explicitly opt in'
  assert_absent "$root" .repo
  if [[ -d "$root/repos" ]] && find "$root/repos" -type d -name .repo -print -quit | grep -q .; then
    fail "repository development pack was installed into a member repository: $root/repos"
  fi
}

validate_and_plan() {
  local root="$1"
  local label="$2"

  loom project validate "$root" >"$TMP_DIR/$label.validate.out"
  assert_contains "$TMP_DIR/$label.validate.out" 'Project contract: ok'
  assert_contains "$TMP_DIR/$label.validate.out" 'Layout: canonical'
  loom project plan "$root" >"$TMP_DIR/$label.plan.out"
  assert_contains "$TMP_DIR/$label.plan.out" 'Project plan:'
}

assert_canonical_scaffold() {
  local preset="$1"
  local root="$2"
  local facet
  local path

  assert_file "$root" .loom/project.yaml
  assert_file "$root" .loom/.gitignore
  assert_contains "$root/.loom/.gitignore" 'state/'
  assert_contains "$root/.loom/.gitignore" 'tmp/'
  assert_file "$root" .loom/agents/project.md
  assert_executable "$root" .loom/tools/validate-project.sh
  for path in note.md dated-file.yaml dataset.yaml; do
    assert_file "$root" ".loom/templates/$path"
    assert_contains "$root/.loom/templates/$path" '{{printf "%q" .CreatedAt}}'
    assert_contains "$root/.loom/templates/$path" '{{printf "%q" .UpdatedAt}}'
  done
  assert_file "$root" README.md
  assert_file "$root" AGENTS.md

  assert_repository_development_pack "$root"

  for facet in $(preset_facets "$preset"); do
    assert_file "$root" ".loom/agents/surfaces/$facet.md"
  done
  for path in $(preset_singletons "$preset"); do
    assert_file "$root" ".loom/contracts/$path"
  done
  for path in $(preset_human_dirs "$preset"); do
    assert_dir "$root" "$path"
    assert_absent "$root" "$path/README.md"
    assert_absent "$root" "$path/AGENTS.md"
  done
  assert_no_legacy_layout "$root"
  validate_and_plan "$root" "preset-$preset"
}

log 'scaffolding every canonical preset'
for preset in minimal research automation connector module; do
  slug="v2-smoke-$preset"
  root="$TMP_DIR/$slug"
  loom project scaffold "V2 Smoke $preset" \
    --owner-node main \
    --preset "$preset" \
    --slug "$slug" \
    --directory "$TMP_DIR" >"$TMP_DIR/$preset.scaffold.out"
  assert_contains "$TMP_DIR/$preset.scaffold.out" 'Project scaffold: created'
  assert_canonical_scaffold "$preset" "$root"
  pass "$preset preset uses canonical project-local layout"
done

log 'checking explicit facets'
explicit_root="$TMP_DIR/explicit-facets"
loom project scaffold 'Explicit Facets' \
  --owner-node main \
  --facets notes,repos,docs,tests \
  --directory "$TMP_DIR" >"$TMP_DIR/explicit.scaffold.out"
for facet in notes repos docs tests; do
  assert_file "$explicit_root" ".loom/agents/surfaces/$facet.md"
done
for path in notes.yaml repos.yaml; do
  assert_file "$explicit_root" ".loom/contracts/$path"
done
for path in notes repos docs tests; do
  assert_dir "$explicit_root" "$path"
  assert_absent "$explicit_root" "$path/README.md"
  assert_absent "$explicit_root" "$path/AGENTS.md"
done
assert_absent "$explicit_root" .loom/contracts/backup.yaml
assert_no_legacy_layout "$explicit_root"
validate_and_plan "$explicit_root" explicit
pass 'explicit facets override the preset with canonical files only'

log 'checking dry-run and force behavior'
loom project scaffold 'Dry Run Project' \
  --owner-node main \
  --directory "$TMP_DIR" \
  --dry-run >"$TMP_DIR/dry-run.out"
assert_contains "$TMP_DIR/dry-run.out" 'Project scaffold: planned'
assert_absent "$TMP_DIR" dry-run-project
pass 'scaffold dry-run writes nothing'

force_root="$TMP_DIR/force-project"
loom project scaffold 'Force Project' \
  --owner-node main \
  --slug force-project \
  --directory "$TMP_DIR" >"$TMP_DIR/force-first.out"
printf '%s\n' 'operator-modified generated README' >"$force_root/README.md"
set +e
loom project scaffold 'Force Project' \
  --owner-node main \
  --slug force-project \
  --directory "$TMP_DIR" >"$TMP_DIR/force-refused.out" 2>"$TMP_DIR/force-refused.err"
force_status=$?
set -e
[[ "$force_status" -ne 0 ]] || fail 'existing scaffold unexpectedly succeeded without --force'
assert_contains "$force_root/README.md" 'operator-modified generated README'
loom project scaffold 'Force Project' \
  --owner-node main \
  --slug force-project \
  --directory "$TMP_DIR" \
  --force >"$TMP_DIR/force-applied.out"
if grep -Fq 'operator-modified generated README' "$force_root/README.md"; then
  fail '--force did not replace the generated README'
fi
assert_file "$force_root" .loom/project.yaml
assert_absent "$force_root" loom.project.yaml
pass 'force is explicit and rewrites generated canonical files'

log 'adding a facet to an existing canonical project'
facet_root="$TMP_DIR/facet-target"
loom project scaffold 'Facet Target' \
  --owner-node main \
  --slug facet-target \
  --facets docs \
  --directory "$TMP_DIR" >"$TMP_DIR/facet-scaffold.out"
loom project facet add "$facet_root" notes >"$TMP_DIR/facet-add.out"
assert_contains "$TMP_DIR/facet-add.out" 'Project facets: updated'
assert_file "$facet_root" .loom/project.yaml
assert_contains "$facet_root/.loom/project.yaml" 'notes: true'
assert_file "$facet_root" .loom/contracts/notes.yaml
assert_file "$facet_root" .loom/agents/surfaces/notes.md
assert_dir "$facet_root" notes
assert_no_legacy_layout "$facet_root"
validate_and_plan "$facet_root" facet-added
pass 'facet addition resolves and rewrites canonical contract paths'

log 'migrating a deliberate legacy fixture'
legacy_root="$TMP_DIR/legacy-project"
mkdir -p "$legacy_root/notes"
cat >"$legacy_root/loom.project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-project
  name: Legacy Project
  owner_node: main
  status: active
facets:
  notes: true
YAML
cat >"$legacy_root/notes/loom.notes.yaml" <<'YAML'
kind: loom.notes
schema_version: notes.contract.v0.3
notes:
  status: draft
  sync: true
  index: true
  backup: false
  root_key: notes
  path: .
  include:
    - "**/*.md"
  exclude:
    - ".obsidian/**"
YAML

loom project validate "$legacy_root" >"$TMP_DIR/legacy.validate.out"
assert_contains "$TMP_DIR/legacy.validate.out" 'Project contract: ok'
assert_contains "$TMP_DIR/legacy.validate.out" 'Layout: legacy'
assert_contains "$TMP_DIR/legacy.validate.out" 'contract.layout_legacy'
loom project migrate-layout "$legacy_root" --dry-run >"$TMP_DIR/legacy.dry-run.out"
assert_contains "$TMP_DIR/legacy.dry-run.out" 'Project layout migration: planned'
assert_contains "$TMP_DIR/legacy.dry-run.out" 'Layout: legacy'
assert_absent "$legacy_root" .loom/project.yaml
assert_file "$legacy_root" loom.project.yaml
assert_file "$legacy_root" notes/loom.notes.yaml
pass 'legacy validation works and migration dry-run writes nothing'

loom project migrate-layout "$legacy_root" --apply --yes >"$TMP_DIR/legacy.apply.out"
assert_contains "$TMP_DIR/legacy.apply.out" 'Project layout migration: applied'
assert_contains "$TMP_DIR/legacy.apply.out" 'Layout: legacy -> canonical'
assert_file "$legacy_root" .loom/project.yaml
assert_file "$legacy_root" .loom/contracts/notes.yaml
assert_absent "$legacy_root" loom.project.yaml
assert_absent "$legacy_root" notes/loom.notes.yaml
loom project validate "$legacy_root" >"$TMP_DIR/migrated.validate.out"
assert_contains "$TMP_DIR/migrated.validate.out" 'Layout: canonical'
loom project migrate-layout "$legacy_root" --dry-run >"$TMP_DIR/legacy.idempotent.out"
assert_contains "$TMP_DIR/legacy.idempotent.out" 'Layout: canonical'
assert_contains "$TMP_DIR/legacy.idempotent.out" 'Actions: 0'
pass 'legacy migration relocates contracts and is idempotent'

log 'rejecting divergent canonical and legacy contracts'
conflict_root="$TMP_DIR/conflict-project"
mkdir -p "$conflict_root/.loom"
cat >"$conflict_root/.loom/project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: conflict-project
  name: Canonical Definition
  owner_node: main
YAML
cat >"$conflict_root/loom.project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: conflict-project
  name: Divergent Legacy Definition
  owner_node: main
YAML
cp "$conflict_root/.loom/project.yaml" "$TMP_DIR/conflict-canonical.before"
cp "$conflict_root/loom.project.yaml" "$TMP_DIR/conflict-legacy.before"

set +e
loom project validate "$conflict_root" >"$TMP_DIR/conflict.validate.out" 2>"$TMP_DIR/conflict.validate.err"
validate_status=$?
loom project migrate-layout "$conflict_root" --dry-run >"$TMP_DIR/conflict.migrate.out" 2>"$TMP_DIR/conflict.migrate.err"
migrate_status=$?
set -e
[[ "$validate_status" -ne 0 ]] || fail 'divergent contract validation unexpectedly succeeded'
[[ "$migrate_status" -ne 0 ]] || fail 'divergent contract migration unexpectedly succeeded'
assert_contains "$TMP_DIR/conflict.validate.out" 'contract.layout_conflict'
cmp -s "$TMP_DIR/conflict-canonical.before" "$conflict_root/.loom/project.yaml" || fail 'canonical conflict file changed'
cmp -s "$TMP_DIR/conflict-legacy.before" "$conflict_root/loom.project.yaml" || fail 'legacy conflict file changed'
pass 'divergent layouts fail closed without changing either contract'

log 'exporting deterministic human, portable, and archival project tar files'
printf '%s\n' 'ignored.txt' >"$facet_root/.loomignore"
printf '%s\n' 'ignored content' >"$facet_root/ignored.txt"
mkdir -p "$facet_root/.loom/state" "$facet_root/.loom/tmp" "$facet_root/.loom-acceptance"
printf '%s\n' 'runtime state' >"$facet_root/.loom/state/cache"
printf '%s\n' 'runtime temp' >"$facet_root/.loom/tmp/work"
printf '%s\n' 'acceptance artifact' >"$facet_root/.loom-acceptance/probe"

portable_tar="$TMP_DIR/facet-portable.tar"
loom project export "$facet_root" --mode portable --out "$portable_tar" >"$TMP_DIR/export-portable.out"
tar -tf "$portable_tar" >"$TMP_DIR/export-portable.list"
assert_contains "$TMP_DIR/export-portable.list" '.loom/project.yaml'
assert_contains "$TMP_DIR/export-portable.list" '.loom/agents/project.md'
assert_contains "$TMP_DIR/export-portable.list" '.loomignore'
if grep -Eq '^(.loom/state|.loom/tmp|ignored.txt)(/|$)' "$TMP_DIR/export-portable.list"; then
  fail 'portable export included ignored or runtime-only content'
fi
pass 'portable project export preserves canonical controls through shared policy'

human_tar="$TMP_DIR/facet-human.tar"
loom project export "$facet_root" --mode human --out "$human_tar" >"$TMP_DIR/export-human.out"
tar -tf "$human_tar" >"$TMP_DIR/export-human.list"
assert_contains "$TMP_DIR/export-human.list" 'README.md'
if grep -Eq '^(.loom|.loom-acceptance|AGENTS.md)(/|$)' "$TMP_DIR/export-human.list"; then
  fail 'human export included LOOM control material or managed root AGENTS.md'
fi
pass 'human project export omits LOOM control material'

archival_tar="$TMP_DIR/facet-archival.tar"
loom project export "$facet_root" --mode archival --out "$archival_tar" >"$TMP_DIR/export-archival.out"
tar -tf "$archival_tar" >"$TMP_DIR/export-archival.list"
assert_contains "$TMP_DIR/export-archival.list" '.loom/export/registrations.json'
pass 'archival project export adds bounded registration references'

log "completed $pass_count checks"
