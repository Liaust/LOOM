#!/usr/bin/env bash
set -euo pipefail

[[ "$#" -eq 0 ]] || { printf 'Usage: %s\n' "$0" >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d /tmp/loom-box-perf-XXXXXX)"
PG_STARTED=false
PG_BIN=""
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "$PG_STARTED" == true ]]; then
    if ! "$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -m fast -w stop >/dev/null; then
      printf 'FAIL: owned PostgreSQL did not stop; retained %s\n' "$TMP_ROOT" >&2
      exit 1
    fi
  fi
  case "$TMP_ROOT" in
    /tmp/loom-box-perf-*)
      find -P "$TMP_ROOT" -type d -exec chmod u+w {} +
      rm -rf -- "$TMP_ROOT"
      [[ ! -e "$TMP_ROOT" ]] || exit 1
      ;;
    *) exit 1 ;;
  esac
  printf 'Disposable performance cluster and fixture root removed.\n'
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$ROOT"

NIX="$(command -v nix || true)"
if [[ -z "$NIX" && -x /nix/var/nix/profiles/default/bin/nix ]]; then
  NIX=/nix/var/nix/profiles/default/bin/nix
fi
[[ -n "$NIX" ]] || { printf 'Pinned Nix dependencies are required.\n' >&2; exit 1; }
PG_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build \
  --no-link --print-out-paths --impure --expr \
  'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])')"
PDF_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build \
  --no-link --print-out-paths --impure --expr \
  'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.poppler-utils')"
PG_BIN="$PG_ROOT/bin"
[[ -x "$PG_BIN/pg_ctl" && -x "$PDF_ROOT/bin/pdftotext" ]] || exit 1
export PATH="$PDF_ROOT/bin:$PG_BIN:$PATH"
mkdir -m 0700 "$TMP_ROOT/socket" "$TMP_ROOT/home" "$TMP_ROOT/config"
"$PG_BIN/initdb" -D "$TMP_ROOT/postgres" --username=postgres --auth-local=trust \
  --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
"$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -l "$TMP_ROOT/postgres.log" \
  -o "-c listen_addresses='' -c unix_socket_directories='$TMP_ROOT/socket' -c unix_socket_permissions=0700" -w start >/dev/null
PG_STARTED=true

printf 'Building test-only performance harness; no daemon or watcher run.\n'
go test -count=1 -run '^TestBoxPerformance|BoxSourcesContract' ./internal/knowledge
go test -c -o "$TMP_ROOT/knowledge.test" ./internal/knowledge
go test -c -o "$TMP_ROOT/knowledge-http.test" ./internal/httpapi
go build -o "$TMP_ROOT/loom" ./cmd/loom

run_test() (
  cd "$ROOT/internal/knowledge"
  env -i PATH="$PATH" HOME="$TMP_ROOT/home" XDG_CONFIG_HOME="$TMP_ROOT/config" \
    LOOM_TEST_DB_URL="postgresql://postgres@/postgres?host=$TMP_ROOT/socket" \
    LOOM_BOX_SURFACE_HELPER="$TMP_ROOT/knowledge-http.test" LOOM_BOX_SURFACE_CLI="$TMP_ROOT/loom" \
    LOOM_BOX_PERFORMANCE="$1" "$TMP_ROOT/knowledge.test" -test.timeout=5m -test.count=1 -test.v -test.run="$2"
)
if ! run_test '' '^TestBox(SyncedMetadataObservation|SourceVersionSearchObservation)Postgres$' >"$TMP_ROOT/regressions.log" 2>&1; then
  tail -80 "$TMP_ROOT/regressions.log" >&2; exit 1
fi
printf 'PASS: current metadata and exact historical/current query regressions.\n'
for run in 1 2 3; do
  if ! run_test 1 '^TestBoxSourcesNativeRetrievalPostgres$' >"$TMP_ROOT/run-$run.log" 2>&1; then
    tail -80 "$TMP_ROOT/run-$run.log" >&2; exit 1
  fi
  printf 'PASS: reset process %s; frozen corpus before/after reconciliation.\n' "$run"
done

ruby -rjson -e '
  ARGV.each_with_index do |path,index|
    log=File.read(path)
    abort("missing successful native test") unless log.include?("--- PASS: TestBoxSourcesNativeRetrievalPostgres")
    queries=log.lines.map { |line| marker="BOX_PERFORMANCE_QUERIES "; JSON.parse(line.split(marker,2).last) if line.include?(marker) }.compact
    work=log.lines.map { |line| marker="BOX_PERFORMANCE_WORK "; JSON.parse(line.split(marker,2).last) if line.include?(marker) }.compact
    abort("incomplete query phases") unless queries.map { |q| q.fetch("phase") }==%w[before_reconciliation after_metadata_reconciliation]
    abort("incomplete work phases") unless work.map { |w| w.fetch("phase") }==%w[initial_extraction unchanged_replay metadata_replay content_replacement]
    queries.each do |phase|
      abort("incomplete samples") unless phase.fetch("warmups")==5 && phase.fetch("samples_per_query_surface")==50 && phase.fetch("queries").length==12
      phase.fetch("queries").each do |q|
        abort("unverified result") unless q.fetch("corpus_missing_count")==0
        %w[service http cli].each do |surface|
          values=q.fetch("#{surface}_p50_p95_max_ms")
          abort("invalid timing summary") unless values.length==3 && values.all? { |v| v.is_a?(Numeric) && v.finite? && v>0 } && values==values.sort
        end
      end
    end
    abort("reconciliation changed derived counts") unless queries[0].fetch("counts")==queries[1].fetch("counts")
    work.each do |w|
      if %w[unchanged_replay metadata_replay].include?(w.fetch("phase"))
        abort("replay did work") unless w.fetch("stage_jobs")==0 && w.fetch("before")==w.fetch("after")
      end
    end
    puts "BOX_PERFORMANCE_RESULT #{JSON.generate({"reset_process"=>index+1,"queries"=>queries,"work"=>work})}"
  end
' "$TMP_ROOT/run-1.log" "$TMP_ROOT/run-2.log" "$TMP_ROOT/run-3.log"

remaining="$("$PG_BIN/psql" -X -A -t -v ON_ERROR_STOP=1 -h "$TMP_ROOT/socket" -U postgres -d postgres \
  -c "SELECT count(*) FROM pg_database WHERE datname LIKE 'box_sources_%'")"
[[ "$remaining" == 0 ]] || { printf 'Disposable test databases remain.\n' >&2; exit 1; }
printf 'PASS: three reset processes, 12 queries, two phases, three surfaces; all owned test databases absent.\n'
