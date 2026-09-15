#!/usr/bin/env bash
# Compact, owned Borg/PostgreSQL acceptance. Clean only reviewed fixture payload.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BORG="${LOOM_TEST_BORG_BINARY:?Set LOOM_TEST_BORG_BINARY to the pinned local Borg 1.4.3 binary}"
PG_BIN="${LOOM_TEST_PG_BIN:?Set LOOM_TEST_PG_BIN to a local PostgreSQL 17 bin directory}"
[[ "$("$BORG" --version)" == 'borg 1.4.3' ]] || exit 1
[[ "$("$PG_BIN/postgres" --version)" == *' 17.'* ]] || exit 1
# Cleanup requires safe ancestors: keep payload custody beneath the worktree,
# never waive that gate for a globally writable temporary-directory ancestor.
mkdir -p "$ROOT/.loom-acceptance"
BASE="$(mktemp -d "$ROOT/.loom-acceptance/loom-fetch-failure-XXXXXXXX")"
BASE="$(cd "$BASE" && pwd -P)"
ACCEPTANCE="$BASE/.loom-acceptance"
mkdir -m 700 "$ACCEPTANCE" "$BASE/home" "$BASE/go-tmp" "$BASE/traps"
# Socket custody is separate from the caller's possibly long TMPDIR. Only an
# unchanged, empty directory created by this fixture can be removed on exit.
socket_custody() {
 python3 -I - "$ACCEPTANCE" "$1" <<'PY_SOCKET'
import json, os, pathlib, secrets, stat, sys
receipt = pathlib.Path(sys.argv[1]) / 'socket-root.json'
action = sys.argv[2]
parent = os.path.realpath('/tmp')
flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
with_parent = os.open(parent, flags)
try:
    parent_stat = os.fstat(with_parent)
    if action == 'create':
        name = 'lff-' + secrets.token_hex(8)
        os.mkdir(name, 0o700, dir_fd=with_parent)
        held = os.open(name, flags, dir_fd=with_parent)
        try:
            info = os.fstat(held)
            data = dict(path=os.path.join(parent, name), parent=parent,
                        parent_dev=parent_stat.st_dev, parent_ino=parent_stat.st_ino,
                        dev=info.st_dev, ino=info.st_ino, uid=info.st_uid)
            assert info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700
            assert len(os.fsencode(data['path'] + '/.s.PGSQL.5432')) < 104
            with open(os.open(receipt, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), 'w') as out:
                json.dump(data, out)
                out.flush()
                os.fsync(out.fileno())
            os.fsync(with_parent)
            print(data['path'])
        finally:
            os.close(held)
    else:
        with open(os.open(receipt, os.O_RDONLY | os.O_NOFOLLOW), 'r') as source:
            assert stat.S_ISREG(os.fstat(source.fileno()).st_mode)
            data = json.loads(source.read(4097))
        name = os.path.basename(data['path'])
        assert len(name) == 20 and name.startswith('lff-') and all(c in '0123456789abcdef' for c in name[4:])
        assert data['path'] == os.path.join(parent, name) and data['parent'] == parent
        assert (parent_stat.st_dev, parent_stat.st_ino) == (data['parent_dev'], data['parent_ino'])
        held = os.open(name, flags, dir_fd=with_parent)
        try:
            info = os.fstat(held)
            named = os.stat(name, dir_fd=with_parent, follow_symlinks=False)
            expected = (data['dev'], data['ino'], data['uid'])
            assert (info.st_dev, info.st_ino, info.st_uid) == expected
            assert (named.st_dev, named.st_ino, named.st_uid) == expected
            assert info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700
            assert len(os.fsencode(data['path'] + '/.s.PGSQL.5432')) < 104
            if action == 'cleanup':
                assert not os.listdir(held), 'socket directory is not empty; preserve it'
                # No recursive removal or unlink. A substituted/nonempty tree
                # is refused, and rmdir itself cannot remove any file payload.
                named = os.stat(name, dir_fd=with_parent, follow_symlinks=False)
                assert (named.st_dev, named.st_ino, named.st_uid) == expected
                os.rmdir(name, dir_fd=with_parent)
                os.fsync(with_parent)
                (receipt.parent / 'socket-cleanup.json').write_text(json.dumps(dict(path=data['path'], dev=data['dev'], ino=data['ino'], removed_empty_directory=True)))
            else:
                assert action == 'verify'
        finally:
            os.close(held)
finally:
    os.close(with_parent)
PY_SOCKET
}
PG_STARTED=false
SOCKET_ROOT=""
finish() {
 local status=$?
 trap - EXIT
 local socket_cleanup=true
 if [[ "$PG_STARTED" == true ]]; then
  if ! "$PG_BIN/pg_ctl" -D "$ACCEPTANCE/postgres" -m fast -w stop >>"$ACCEPTANCE/postgres-stop.log" 2>&1; then status=1; socket_cleanup=false; fi
 fi
 if [[ -n "$SOCKET_ROOT" && "$socket_cleanup" == true ]]; then
  if ! socket_custody cleanup >>"$ACCEPTANCE/socket-cleanup.log" 2>&1; then status=1; fi
 fi
 printf 'Retained compact acceptance: %s\n' "$ACCEPTANCE"
 exit "$status"
}
trap finish EXIT
SOCKET_ROOT="$(socket_custody create)"
socket_custody verify
"$PG_BIN/initdb" -D "$ACCEPTANCE/postgres" -U postgres --auth-local=trust --auth-host=reject --encoding=UTF8 --no-locale >"$ACCEPTANCE/initdb.log"
printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\nlog_statement = 'all'\n" "$SOCKET_ROOT" >>"$ACCEPTANCE/postgres/postgresql.conf"
"$PG_BIN/pg_ctl" -D "$ACCEPTANCE/postgres" -l "$ACCEPTANCE/postgres.log" -w start >"$ACCEPTANCE/postgres-start.log"
PG_STARTED=true
# Mutation baseline includes existing active fixture databases, which must survive.
"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$SOCKET_ROOT" -U postgres -d postgres -c 'CREATE DATABASE loom_active_fixture' >"$ACCEPTANCE/pg-setup.log"
"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$SOCKET_ROOT" -U postgres -d postgres -c 'CREATE DATABASE loom_provenance_active_fixture' >>"$ACCEPTANCE/pg-setup.log"
printf '#!/bin/sh\nprintf called >> %q\nexit 99\n' "$ACCEPTANCE/pg-restore-called" >"$BASE/traps/pg_restore"
chmod 700 "$BASE/traps/pg_restore"
GO="$(command -v go)"
# No inherited LOOM, PG, Borg, credentials, HOME or service configuration.
run_test() {
 env -i PATH="$BASE/traps:$(dirname "$GO"):/usr/bin:/bin:/usr/sbin:/sbin" \
 HOME="$BASE/home" TMPDIR="$BASE/go-tmp" USER="$(id -un)" LOGNAME="$(id -un)" \
 GOCACHE="$(go env GOCACHE)" GOPATH="$(go env GOPATH)" \
 LOOM_TEST_FETCH_FAILURE_ROOT="$ACCEPTANCE" LOOM_TEST_BORG_BINARY="$BORG" \
 "$GO" test -count=1 -v -run "$1" "$2"
}
cd "$ROOT"
run_test '^TestPrepareCloudFetchFailureFixture$' ./internal/cloudstorage >"$ACCEPTANCE/prepare.log" 2>&1
run_test '^TestCloudFetchFailureDisposableAcceptance$' ./internal/httpapi >"$ACCEPTANCE/acceptance.log" 2>&1
[[ ! -e "$ACCEPTANCE/pg-restore-called" ]]
# PostgreSQL logs are synthetic and must contain no mutation after setup.
python3 - "$ACCEPTANCE" <<'PY'
import json, pathlib, sys
r=pathlib.Path(sys.argv[1])
rows=json.loads((r/'acceptance.json').read_text())
assert len(rows)==2 and all(x['database_catalog_unchanged'] and x['authority_connections']==0 and x['original_failure_preserved'] and x['cleanup_borg_commands']==0 for x in rows)
assert rows[0]['cleanup_status']=='completed' and rows[0]['cleanup_replay_unchanged']
assert rows[1]['external_hardlink_refused'] and rows[1]['external_refusal_payload_retained']
assert rows[1]['cleanup_status']=='completed' and rows[1]['internal_hardlink_cleaned'] and rows[1]['cleanup_replay_unchanged']
log=(r/'postgres.log').read_text()
assert log.count('statement: CREATE DATABASE')==2
assert 'DROP DATABASE' not in log and 'pg_restore' not in log
print('PASS: two bounded failure cases, durable receipts, reviewed payload cleanup, unchanged exact replay, external hardlink refusal and internal hardlink cleanup; zero restore/database mutation; all evidence retained')
PY
