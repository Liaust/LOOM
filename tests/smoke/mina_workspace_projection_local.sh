#!/usr/bin/env bash
# Portable instructions only. No live profile, auth, network or remote repository.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
mkdir -p "$repo/.loom-acceptance/mina-w4"
fixture=$(mktemp -d "$repo/.loom-acceptance/mina-w4/local-smoke.XXXXXX")
printf 'Retained disposable source-only smoke: %s\n' "$fixture"

# Explicit positive fixture census; do not walk a live workspace or copy Git metadata.
python3 - "$repo" "$fixture" <<'PY'
import pathlib, shutil, sys
repo, fixture = map(pathlib.Path, sys.argv[1:])
files = ['.hermes/SOUL.md', 'AGENTS.md', 'WORKFLOW.md', 'OPERATING-POLICY.md',
         'WORKSPACE-MAP.md', 'TOOLING.md', 'protocols/README.md', 'protocols/BASECAMP.md',
         'protocols/GITHUB.md', 'protocols/CREDENTIALS.md', 'protocols/EXTERNAL-MESSAGING.md',
         'protocols/LOOM-ROUTING.md', 'protocols/PROJECT-DELEGATION.md',
         'protocols/DEVICE-AND-TOOL-ROUTING.md', 'protocols/MAC-COMPUTER-USE.md',
         'protocols/PROVENANCE-AND-MEMORY.md', 'protocols/SESSION-RETRIEVAL.md',
         'protocols/SKILL-CREATION-AND-PROMOTION.md', 'handoffs/HANDOFF-TEMPLATE.md']
paths = ['ai-loom-pack/manifest.yaml', 'nix/files/mina/skins/mina-matrix-teal.yaml']
paths += ['ai-loom-pack/templates/mina/' + p for p in files]
for path in paths:
    dest = fixture / 'source' / path
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(repo / path, dest)
(fixture / 'source-inputs.txt').write_text('\n'.join(paths) + '\n')
PY

# All fixture Git writes are local, with isolated config and no user hooks.
local_git() {
  env -i PATH="$PATH" HOME=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    GIT_AUTHOR_NAME=Fixture GIT_AUTHOR_EMAIL=fixture@example.invalid \
    GIT_COMMITTER_NAME=Fixture GIT_COMMITTER_EMAIL=fixture@example.invalid \
    git -c core.hooksPath=/dev/null "$@"
}
local_git -C "$fixture/source" init --quiet
while IFS= read -r path; do local_git -C "$fixture/source" add -- "$path"; done < "$fixture/source-inputs.txt"
local_git -C "$fixture/source" commit --quiet -m 'Portable smoke source fixture'
(cd "$repo" && GOMAXPROCS=2 go build -p 2 -o "$fixture/loom" ./cmd/loom)
# Scaffold only the declared portable template, then preserve a user-edited protocol.
"$fixture/loom" --json agent pack scaffold-workspace --pack-dir "$repo/ai-loom-pack" \
  --template mina --path "$fixture/scaffold" --dry-run > "$fixture/scaffold-plan.json"
test ! -e "$fixture/scaffold"
"$fixture/loom" --json agent pack scaffold-workspace --pack-dir "$repo/ai-loom-pack" \
  --template mina --path "$fixture/scaffold" --yes > "$fixture/scaffold-apply.json"
python3 - "$repo" "$fixture" <<'PY_SCAFFOLD'
import hashlib,json,pathlib,sys
repo,f = map(pathlib.Path,sys.argv[1:]); root=f/'scaffold'
assert len([p for p in root.rglob('*') if p.is_file()]) == 23
assert {str(p.relative_to(root)) for p in root.rglob('*SOUL*')} == {'.hermes/SOUL.md'}
for name in ('.hermes/SOUL.md','protocols/DEVICE-AND-TOOL-ROUTING.md','protocols/MAC-COMPUTER-USE.md'):
    assert (root/name).read_bytes() == (repo/'ai-loom-pack/templates/mina'/name).read_bytes()
for name in ('DEVICE-AND-TOOL-ROUTING.md','MAC-COMPUTER-USE.md'):
    for discovery in ('AGENTS.md','TOOLING.md','protocols/README.md'):
        assert name in (root/discovery).read_text()
p=root/'protocols/MAC-COMPUTER-USE.md'; p.write_text(p.read_text()+'\nOperator-owned synthetic note.\n')
(f/'scaffold-before.json').write_text(json.dumps({str(p.relative_to(root)):[hashlib.sha256(p.read_bytes()).hexdigest(),p.stat().st_ino,p.stat().st_mtime_ns,p.stat().st_mode] for p in root.rglob('*') if p.is_file()},sort_keys=True))
PY_SCAFFOLD
"$fixture/loom" --json agent pack scaffold-workspace --pack-dir "$repo/ai-loom-pack" \
  --template mina --path "$fixture/scaffold" --yes > "$fixture/scaffold-replay.json"
python3 - "$fixture" <<'PY_REPLAY'
import hashlib,json,pathlib,sys
f=pathlib.Path(sys.argv[1]); root=f/'scaffold'
after={str(p.relative_to(root)):[hashlib.sha256(p.read_bytes()).hexdigest(),p.stat().st_ino,p.stat().st_mtime_ns,p.stat().st_mode] for p in root.rglob('*') if p.is_file()}
assert json.loads((f/'scaffold-before.json').read_text()) == after
PY_REPLAY
export_cli() {
  "$fixture/loom" --json agent pack export-workspace --template mina \
    --source-root "$fixture/source" --path "$1" "${@:2}"
}
export_cli "$fixture/first" > "$fixture/plan.json"
test ! -e "$fixture/first"
export_cli "$fixture/first" --dry-run --yes > "$fixture/dry-run.json"
test ! -e "$fixture/first"
cmp "$fixture/plan.json" "$fixture/dry-run.json"
export_cli "$fixture/first" --yes > "$fixture/apply.json"

python3 - "$fixture" <<'PY'
import hashlib, json, pathlib, sys
f = pathlib.Path(sys.argv[1]); manifest = json.loads((f/'first/export-manifest.json').read_text())
assert len(manifest['files']) == 22
assert manifest['pack_version'] == '0.6.3'
assert sum(p['origin'] == 'source' and p['path'].endswith('.md') for p in manifest['files']) == 19
for name in ('DEVICE-AND-TOOL-ROUTING.md', 'MAC-COMPUTER-USE.md'):
    assert (f/'first/protocols'/name).read_bytes() == (f/'source/ai-loom-pack/templates/mina/protocols'/name).read_bytes()
    for discovery in ('AGENTS.md', 'TOOLING.md', 'protocols/README.md'):
        assert name in (f/'first'/discovery).read_text()
assert {str(p.relative_to(f/'first')) for p in (f/'first').rglob('*SOUL*')} == {'.hermes/SOUL.md'}
assert json.loads((f/'apply.json').read_text())['applied'] is True
assert str(f) not in (f/'first/export-manifest.json').read_text()
allowed = {'export-manifest.json'} | {p['path'] for p in manifest['files']}
assert {str(p.relative_to(f/'first')) for p in (f/'first').rglob('*') if p.is_file()} == allowed
for entry in manifest['files']:
    path = f/'first'/entry['path']; data = path.read_bytes()
    assert len(data) == entry['bytes'] and hashlib.sha256(data).hexdigest() == entry['sha256']
    assert path.stat().st_nlink == 1
    if entry['origin'] == 'source':
        source = f/'source'/entry['source_path']
        assert data == source.read_bytes() and path.stat().st_ino != source.stat().st_ino
assert hashlib.sha256((f/'first/skins/mina-matrix-teal.yaml').read_bytes()).hexdigest() == '0175be4213c7deb52710dd993691d5f0c30bacd763d2a854381ca85a019095ef'
(f/'payloads.txt').write_text('\n'.join(sorted(allowed))+'\n')
PY
local_git -C "$fixture/first" init --quiet
while IFS= read -r path; do local_git -C "$fixture/first" add -- "$path"; done < "$fixture/payloads.txt"
local_git -C "$fixture/first" commit --quiet -m 'Reviewed portable source fixture'
mkdir "$fixture/reconstructed"
local_git -C "$fixture/first" checkout-index --all --prefix="$fixture/reconstructed/"
while IFS= read -r path; do cmp "$fixture/first/$path" "$fixture/reconstructed/$path"; done < "$fixture/payloads.txt"

printf '\nReviewed smoke revision two.\n' >> "$fixture/source/ai-loom-pack/templates/mina/WORKFLOW.md"
local_git -C "$fixture/source" add -- ai-loom-pack/templates/mina/WORKFLOW.md
local_git -C "$fixture/source" commit --quiet -m 'Second portable source fixture'
export_cli "$fixture/second" --yes > "$fixture/second.json"
# Real disposable Git diff of two explicit projections, with no publication.
while IFS= read -r path; do cp "$fixture/second/$path" "$fixture/reconstructed/$path"; done < "$fixture/payloads.txt"
local_git -C "$fixture/first" --work-tree="$fixture/reconstructed" diff --name-only > "$fixture/projection-diff.txt"
python3 - "$fixture" <<'PY'
import json, pathlib, sys
f = pathlib.Path(sys.argv[1]); a=json.loads((f/'apply.json').read_text()); b=json.loads((f/'second.json').read_text())
assert a['manifest']['export_id'] != b['manifest']['export_id'] and b['applied']
assert set((f/'projection-diff.txt').read_text().splitlines()) == {'WORKFLOW.md','export-manifest.json'}
PY
# Dirty existing destination and edited SOUL must retain bytes, inode and mtime.
printf '\nUser-owned local edit.\n' >> "$fixture/first/.hermes/SOUL.md"
printf 'Synthetic private content; never an export input.\n' > "$fixture/first/private.txt"
python3 - "$fixture/first" > "$fixture/before.json" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]); print(json.dumps({str(p.relative_to(root)):[hashlib.sha256(p.read_bytes()).hexdigest(),p.stat().st_ino,p.stat().st_mtime_ns,p.stat().st_mode] for p in root.rglob('*') if p.is_file()},sort_keys=True))
PY
if export_cli "$fixture/first" --yes > "$fixture/refused.json" 2> "$fixture/refusal.txt"; then
  printf 'ERROR: dirty destination accepted\n' >&2; exit 1
fi
python3 - "$fixture/first" > "$fixture/after.json" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]); print(json.dumps({str(p.relative_to(root)):[hashlib.sha256(p.read_bytes()).hexdigest(),p.stat().st_ino,p.stat().st_mtime_ns,p.stat().st_mode] for p in root.rglob('*') if p.is_file()},sort_keys=True))
PY
cmp "$fixture/before.json" "$fixture/after.json"
printf 'PASS: scaffold/replay, plan/dry-run/apply, 23-file census and two routing protocols, exact skin/SOUL, two revisions, local Git reconstruction/diff, dirty destination preservation.\n'
