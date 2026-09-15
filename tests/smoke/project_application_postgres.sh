#!/usr/bin/env bash
set -euo pipefail
umask 077
checkout=${1:?checkout required}
pgbin=${LOOM_APPLICATION_PG_BIN:?set to an existing PostgreSQL bin directory; no installation is performed}
fixture=$(mktemp -d /tmp/lae2.XXXXXX)
started=0
cleanup() {
  result=$?
  trap - EXIT
  if [ "$started" = 1 ]; then
    if ! "$pgbin/pg_ctl" -D "$fixture/data" -m fast -w stop; then
      printf 'Owned cluster retained after stop failure: %s\n' "$fixture"
      exit 1
    fi
  fi
  rm -rf -- "$fixture"
  test ! -e "$fixture"
  printf 'Owned cluster stopped, removed and absence verified: %s\n' "$fixture"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$fixture/socket"
"$pgbin/initdb" -D "$fixture/data" -U loom_application_fixture --auth-local=trust --auth-host=reject --no-locale --encoding=UTF8 >"$fixture/initdb.log" 2>&1
started=1
"$pgbin/pg_ctl" -D "$fixture/data" -l "$fixture/postgres.log" -o "-c listen_addresses='' -c unix_socket_directories='$fixture/socket' -c unix_socket_permissions=0700" -w start
"$pgbin/createdb" -h "$fixture/socket" -U loom_application_fixture loom_application_test
cd "$checkout"
env -i PATH="$PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" \
  LOOM_SERVICE_REGISTRY_TEST_DB_URL="postgresql://loom_application_fixture@/loom_application_test?host=$fixture/socket&sslmode=disable" \
  go test -count=1 -v -run '^Test(ApplicationPersistedDispatchOutboxLostAckIntegration|ServiceRegistryPersistedSameNodeDispatchIntegration|ServiceRegistryPersistedRemoteDispatchIntegration)$' ./internal/nodeagent | tee "$fixture/test.log"
if rg -q '^--- SKIP:|warning: no tests to run' "$fixture/test.log"; then
  printf 'Required database acceptance skipped or selected no tests.\n'
  exit 1
fi
for name in ApplicationPersistedDispatchOutboxLostAckIntegration ServiceRegistryPersistedSameNodeDispatchIntegration ServiceRegistryPersistedRemoteDispatchIntegration; do
  rg -q "^--- PASS: Test$name " "$fixture/test.log"
done
