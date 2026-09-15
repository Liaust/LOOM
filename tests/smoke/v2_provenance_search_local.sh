#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-provenance-search.XXXXXX")"
PG_DATA="$TMP_ROOT/postgres"
PG_SOCKET="$(mktemp -d /tmp/loom-provenance-search-pg.XXXXXX)"
RUNTIME_ROOT="$TMP_ROOT/runtime"
SOCKET_PATH="$RUNTIME_ROOT/run/loomd.sock"
LOOM_BIN="$TMP_ROOT/bin/loom"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() {
  printf '[provenance-search] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID" >/dev/null 2>&1 || true
    wait "$DAEMON_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$POSTGRES_STARTED" == true ]]; then
    "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null 2>&1 || true
  fi
  case "$TMP_ROOT" in
    */loom-provenance-search.*)
      find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$PG_SOCKET" in
    /tmp/loom-provenance-search-pg.*)
      find "$PG_SOCKET" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

stop_daemon() {
  local status=0
  local remaining=100
  if [[ -z "$DAEMON_PID" ]]; then
    return
  fi
  if kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID"
    while kill -0 "$DAEMON_PID" >/dev/null 2>&1 && [[ "$remaining" -gt 0 ]]; do
      sleep 0.1
      remaining=$((remaining - 1))
    done
    [[ "$remaining" -gt 0 ]] || fail 'loomd did not stop within ten seconds'
  fi
  wait "$DAEMON_PID" || status=$?
  DAEMON_PID=""
  [[ "$status" -eq 0 ]] || fail "loomd exited with status $status"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

resolve_nix() {
  if command -v nix >/dev/null 2>&1; then
    command -v nix
    return
  fi
  if [[ -x /nix/var/nix/profiles/default/bin/nix ]]; then
    printf '%s\n' /nix/var/nix/profiles/default/bin/nix
    return
  fi
  fail 'Nix is required to obtain the pinned disposable PostgreSQL 17 + pgvector toolchain'
}

resolve_postgres() {
  local nix_command
  local postgres_root
  nix_command="$(resolve_nix)"
  postgres_root="$(
    cd "$ROOT"
    "$nix_command" --extra-experimental-features 'nix-command flakes' \
      build --no-link --print-out-paths --impure --expr \
      'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])' \
      | tail -n 1
  )"
  [[ -x "$postgres_root/bin/initdb" ]] || fail 'Nix did not return a PostgreSQL server toolchain'
  PG_BIN="$postgres_root/bin"
  export PATH="$PG_BIN:$PATH"
}

start_postgres() {
  mkdir -p "$PG_SOCKET"
  "$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust \
    --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
  printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\n" \
    "$PG_SOCKET" >>"$PG_DATA/postgresql.conf"
  if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$TMP_ROOT/postgres.log" -w start >/dev/null; then
    sed -n '1,240p' "$TMP_ROOT/postgres.log" >&2
    fail 'smoke-owned PostgreSQL did not start'
  fi
  POSTGRES_STARTED=true

  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres loom_main
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;' >/dev/null
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'REVOKE CONNECT ON DATABASE loom_main FROM PUBLIC; GRANT CONNECT ON DATABASE loom_main TO postgres;' >/dev/null
}

start_daemon() {
  local auto_migrate="${1:-true}"
  local log_path="${2:-$TMP_ROOT/loomd.log}"
  local remaining=300
  env \
    HOME="$RUNTIME_ROOT/home" \
    XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    LOOM_ENV=test \
    LOOM_NODE_ID=main \
    LOOM_NODE_KIND=main \
    LOOM_NODE_ROLE=main \
    LOOM_RUNTIME_CLASS=main_full \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" \
    LOOM_OBJECT_STORE="$RUNTIME_ROOT/data/object-store" \
    LOOM_SERVICE_ROOT="$RUNTIME_ROOT/service" \
    LOOM_STORAGE_ROOT="$RUNTIME_ROOT/storage" \
    LOOM_CANONICAL_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_IMPORTS_ROOT="$RUNTIME_ROOT/storage/imports" \
    LOOM_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_ARCHIVE_ROOT="$RUNTIME_ROOT/storage/archive" \
    LOOM_GENERATED_ROOT="$RUNTIME_ROOT/data/generated" \
    LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" \
    LOOM_STORAGE_RETENTION_ROOT="$RUNTIME_ROOT/data/storage-retention" \
    LOOM_BOX_PATH="$RUNTIME_ROOT/box" \
    LOOM_SOCKET_PATH="$SOCKET_PATH" \
    LOOM_DB_URL="$MAIN_DB_URL" \
    LOOM_PROVENANCE_DB_URL="$PROVENANCE_DB_URL" \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_AUTO_MIGRATE="$auto_migrate" \
    LOOM_BOOTSTRAP_MODE=dev \
    "$LOOMD_BIN" serve >"$log_path" 2>&1 &
  DAEMON_PID=$!

  while [[ "$remaining" -gt 0 ]]; do
    if [[ -S "$SOCKET_PATH" ]]; then
      return
    fi
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$log_path" >&2
      fail 'loomd exited before creating its disposable Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$log_path" >&2
  fail 'loomd did not create its disposable Unix socket within thirty seconds'
}

request_to_file() {
  local output_path="$1"
  local status
  shift
  status="$(curl --silent --show-error --output "$output_path" --write-out '%{http_code}' \
    --unix-socket "$SOCKET_PATH" "$@")"
  if [[ "$status" != 200 ]]; then
    sed -n '1,240p' "$output_path" >&2
    fail "unexpected HTTP status $status for ${*: -1}"
  fi
}

assert_json_equal() {
  local first="$1"
  local second="$2"
  local label="$3"
  if ! diff -u <(jq -S '.data' "$first") <(jq -S '.data' "$second") >"$TMP_ROOT/json-diff.out"; then
    sed -n '1,240p' "$TMP_ROOT/json-diff.out" >&2
    fail "$label differs between API and CLI"
  fi
}

loom() {
  env \
    HOME="$RUNTIME_ROOT/home" \
    XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    LOOM_ENV=test \
    LOOM_NODE_ID=main \
    LOOM_NODE_KIND=main \
    LOOM_NODE_ROLE=main \
    LOOM_RUNTIME_CLASS=main_full \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" \
    LOOM_OBJECT_STORE="$RUNTIME_ROOT/data/object-store" \
    LOOM_SERVICE_ROOT="$RUNTIME_ROOT/service" \
    LOOM_STORAGE_ROOT="$RUNTIME_ROOT/storage" \
    LOOM_CANONICAL_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_IMPORTS_ROOT="$RUNTIME_ROOT/storage/imports" \
    LOOM_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_ARCHIVE_ROOT="$RUNTIME_ROOT/storage/archive" \
    LOOM_GENERATED_ROOT="$RUNTIME_ROOT/data/generated" \
    LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" \
    LOOM_STORAGE_RETENTION_ROOT="$RUNTIME_ROOT/data/storage-retention" \
    LOOM_BOX_PATH="$RUNTIME_ROOT/box" \
    LOOM_SOCKET_PATH="$SOCKET_PATH" \
    LOOM_DB_URL="$MAIN_DB_URL" \
    LOOM_PROVENANCE_DB_URL="$PROVENANCE_DB_URL" \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_BOOTSTRAP_MODE=dev \
    "$LOOM_BIN" --socket "$SOCKET_PATH" --json "$@"
}

seed_frozen_corpus() {
  local overlay_source="$TMP_ROOT/slice6_seed_test.go"
  local overlay_config="$TMP_ROOT/slice6_overlay.json"
  local overlay_target="$ROOT/internal/provenance/slice6_seed_test.go"

  sed 's/^    //' >"$overlay_source" <<'EOF'
    package provenance

    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "sort"
      "testing"
      "time"

      "github.com/jackc/pgx/v5/pgxpool"
    )

    func TestSliceSixSeedFrozenCorpus(t *testing.T) {
      databaseURL := os.Getenv("LOOM_SLICE6_DB_URL")
      if databaseURL == "" {
        t.Fatal("LOOM_SLICE6_DB_URL is required")
      }
      pool, err := pgxpool.New(context.Background(), databaseURL)
      if err != nil {
        t.Fatal(err)
      }
      defer pool.Close()
      store, err := NewStore(pool)
      if err != nil {
        t.Fatal(err)
      }
      corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
      seedSliceTwoSearchCorpus(t, store, corpus)
      seedSearchRepositoryProjectionCorpus(t, store, corpus)
    }

    func TestSliceSixLatency(t *testing.T) {
      run := os.Getenv("LOOM_SLICE6_LATENCY_RUN")
      pool, store, _ := migratedStore(t, "slice6_latency_"+run)
      defer pool.Close()
      corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
      seedSliceTwoSearchCorpus(t, store, corpus)
      seedSearchRepositoryProjectionCorpus(t, store, corpus)
      service, err := NewService(store)
      if err != nil {
        t.Fatal(err)
      }
      api := &FoundationAPI{store: store, service: service}
      contract := loadSearchContract[searchQueryContract](t, "queries.json")
      limits := loadSearchContract[searchLimitsContract](t, "limits.json")
      for _, query := range contract.Queries {
        if query.ExpectedFirstEngine != "provenance" {
          continue
        }
        measure := func() time.Duration {
          started := time.Now()
          var encoded []byte
          var queryErr error
          switch query.Request.Surface {
          case "search":
            request := SearchRequest{Query: query.Query, IncludePending: query.Request.IncludePending}
            request.Project = query.Request.Filters["project"]
            request.Repository = query.Request.Filters["repository"]
            if collection := query.Request.Filters["collection"]; collection != "" {
              request.Collections = []SearchCollection{SearchCollection(collection)}
            }
            response, err := api.Search(context.Background(), request)
            queryErr = err
            if err == nil {
              encoded, queryErr = json.Marshal(response)
            }
          case "repo_list":
            request := RepositoryProjectionListRequest{Query: query.Query, Project: query.Request.Filters["project"], Topic: query.Request.Filters["topic"]}
            response, err := api.ListRepositoryProjections(context.Background(), request)
            queryErr = err
            if err == nil {
              encoded, queryErr = json.Marshal(response)
            }
          default:
            t.Fatalf("provenance query %s has unsupported surface %s", query.QueryID, query.Request.Surface)
          }
          elapsed := time.Since(started)
          if queryErr != nil {
            t.Fatalf("query %s: %v", query.QueryID, queryErr)
          }
          if len(encoded) == 0 {
            t.Fatalf("query %s encoded an empty response", query.QueryID)
          }
          return elapsed
        }
        for index := 0; index < limits.LatencyMeasurement.WarmupIterationsPerQuery; index++ {
          measure()
        }
        durations := make([]time.Duration, 0, limits.LatencyMeasurement.MeasuredIterationsPerQuery)
        for index := 0; index < limits.LatencyMeasurement.MeasuredIterationsPerQuery; index++ {
          elapsed := measure()
          if elapsed > time.Duration(limits.LatencyMeasurement.PerQueryTimeoutMilliseconds)*time.Millisecond {
            t.Fatalf("query %s exceeded individual timeout: %s", query.QueryID, elapsed)
          }
          durations = append(durations, elapsed)
        }
        sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
        percentile := func(value float64) time.Duration {
          index := int(float64(len(durations))*value+0.999999) - 1
          if index < 0 {
            index = 0
          }
          return durations[index]
        }
        p50, p95 := percentile(0.50), percentile(0.95)
        if p95 > time.Duration(limits.LatencyMeasurement.LocalP95BudgetMilliseconds)*time.Millisecond {
          t.Fatalf("query %s p95 exceeded local budget: %s", query.QueryID, p95)
        }
        t.Logf("SLICE6_LATENCY run=%s query=%s samples=%d p50_ms=%.3f p95_ms=%.3f max_ms=%.3f", run, query.QueryID, len(durations), float64(p50)/float64(time.Millisecond), float64(p95)/float64(time.Millisecond), float64(durations[len(durations)-1])/float64(time.Millisecond))
      }
      fmt.Printf("SLICE6_LATENCY_COMPLETE run=%s\n", run)
    }
EOF
  jq -n --arg target "$overlay_target" --arg source "$overlay_source" \
    '{Replace: {($target): $source}}' >"$overlay_config"
  (
    cd "$ROOT"
    LOOM_SLICE6_DB_URL="$PROVENANCE_DB_URL" \
      go test -count=1 -overlay "$overlay_config" -run '^TestSliceSixSeedFrozenCorpus$' ./internal/provenance
  )
}

require_command go
require_command curl
require_command jq
resolve_postgres

query_contract="$ROOT/internal/provenance/testdata/search/queries.json"
jq -e '
  .queries[] |
  select(.query_id == "cross-project-atlas-ambiguity") |
  .request.surface == "search" and
  any(.expected.collections[]; .collection == "repo_state") and
  any(.expected.collections[]; .collection == "unresolved_cases")
' "$query_contract" >/dev/null || fail 'frozen cross-project ambiguity contract is missing'

mkdir -p \
  "$TMP_ROOT/bin" \
  "$RUNTIME_ROOT/home" \
  "$RUNTIME_ROOT/config" \
  "$RUNTIME_ROOT/run" \
  "$RUNTIME_ROOT/storage/imports" \
  "$RUNTIME_ROOT/storage/backups" \
  "$RUNTIME_ROOT/storage/archive" \
  "$RUNTIME_ROOT/data/object-store" \
  "$RUNTIME_ROOT/data/box-state" \
  "$RUNTIME_ROOT/data/generated" \
  "$RUNTIME_ROOT/data/storage-retention" \
  "$RUNTIME_ROOT/box/Documents"

log 'starting smoke-owned PostgreSQL 17 + pgvector and isolated LOOM roots'
start_postgres
MAIN_DB_URL="postgresql://postgres@/loom_main?host=$PG_SOCKET"
PROVENANCE_DB_URL="postgresql://loom_provenance@/loom_provenance?host=$PG_SOCKET"
ADMIN_DB_URL="postgresql://postgres@/postgres?host=$PG_SOCKET"

log 'building and booting real CLI/daemon binaries only inside disposable paths'
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom && go build -o "$LOOMD_BIN" ./cmd/loomd)
start_daemon
seed_frozen_corpus

search_collection_ids() {
  local response="$1"
  local collection="$2"
  case "$collection" in
    accepted_records) jq -c '[.data.accepted_records.items[].record_id]' "$response" ;;
    pending_candidates) jq -c '[.data.pending_candidates.items[].candidate_id]' "$response" ;;
    unresolved_cases) jq -c '[.data.unresolved_cases.items[].case_id]' "$response" ;;
    repo_state) jq -c '[.data.repo_state.items[].repository_id]' "$response" ;;
    *) fail "unknown search collection $collection" ;;
  esac
}

validate_search_query() {
  local query_json="$1"
  local query_id query include_pending project repository collection expected_ids actual_ids
  local api_path cli_path request_path contamination_id contamination_collection exact_collection exact_id
  query_id="$(jq -er '.query_id' <<<"$query_json")"
  query="$(jq -er '.query' <<<"$query_json")"
  include_pending="$(jq -r '.request.include_pending' <<<"$query_json")"
  project="$(jq -r '.request.filters.project // empty' <<<"$query_json")"
  repository="$(jq -r '.request.filters.repository // empty' <<<"$query_json")"
  collection="$(jq -r '.request.filters.collection // empty' <<<"$query_json")"
  api_path="$TMP_ROOT/query-$query_id-api.json"
  cli_path="$TMP_ROOT/query-$query_id-cli.json"
  request_path="$TMP_ROOT/query-$query_id-request.json"

  jq -n --arg query "$query" --argjson include_pending "$include_pending" \
    --arg project "$project" --arg repository "$repository" --arg collection "$collection" '
      {query: $query, include_pending: $include_pending}
      + (if $project == "" then {} else {project: $project} end)
      + (if $repository == "" then {} else {repository: $repository} end)
      + (if $collection == "" then {} else {collections: [$collection]} end)
    ' >"$request_path"
  request_to_file "$api_path" -H 'Content-Type: application/json' \
    --data-binary "@$request_path" http://loom/v1/provenance/search

  local cli_args=(provenance search "$query")
  [[ "$include_pending" == true ]] && cli_args+=(--include-pending)
  [[ -z "$project" ]] || cli_args+=(--project "$project")
  [[ -z "$repository" ]] || cli_args+=(--repo "$repository")
  [[ -z "$collection" ]] || cli_args+=(--collection "$collection")
  loom "${cli_args[@]}" >"$cli_path"
  assert_json_equal "$api_path" "$cli_path" "frozen search $query_id"

  for collection in accepted_records pending_candidates unresolved_cases repo_state; do
    expected_ids="$(jq -c --arg collection "$collection" '[.expected.collections[] | select(.collection == $collection) | .ids[]]' <<<"$query_json")"
    actual_ids="$(search_collection_ids "$api_path" "$collection")"
    [[ "$actual_ids" == "$expected_ids" ]] || {
      jq . "$api_path" >&2
      fail "frozen search $query_id $collection IDs were $actual_ids, expected $expected_ids"
    }
  done

  while IFS=$'\t' read -r contamination_id contamination_collection; do
    [[ -n "$contamination_id" ]] || continue
    if [[ -n "$contamination_collection" ]]; then
      actual_ids="$(search_collection_ids "$api_path" "$contamination_collection")"
      ! jq -e --arg id "$contamination_id" 'index($id) != null' <<<"$actual_ids" >/dev/null \
        || fail "frozen search $query_id contaminated $contamination_collection with $contamination_id"
    else
      ! jq -e --arg id "$contamination_id" '.. | scalars | select(tostring == $id)' "$api_path" >/dev/null \
        || fail "frozen search $query_id leaked prohibited result $contamination_id"
    fi
  done < <(jq -r '.expected.prohibited_contamination[]? | [.id, (.collection // "")] | @tsv' <<<"$query_json")

  jq -e '
    .ok == true and .data.schema_version == "loom.provenance.search.v1" and
    .data.returned == ([.data.accepted_records.items[], .data.pending_candidates.items[], .data.unresolved_cases.items[], .data.repo_state.items[]] | length) and
    .data.returned <= 8 and
    ([.data.accepted_records.items, .data.pending_candidates.items, .data.unresolved_cases.items, .data.repo_state.items] | all(length <= 8)) and
    ([.data.accepted_records.items[], .data.pending_candidates.items[], .data.unresolved_cases.items[], .data.repo_state.items[]] |
      all(
        (.match.summary.text | length) > 0 and
        (.match.match_explanation.text | length) > 0 and
        (.match.match_explanation.text | length) <= 280 and
        (.match.source.kind | length) > 0 and
        (.match.source.source_reference_ids | length) <= 2 and
        (.match.freshness.currentness | length) > 0 and
        (.match.assertion_posture | length) > 0 and
        (.exact_get.id | length) > 0 and
        (.exact_get.path | startswith("/v1/provenance/")) and
        ((tojson | length) <= 800)
      )) and
    ((.data | tojson | length) <= 6000)
  ' "$api_path" >/dev/null || {
    jq . "$api_path" >&2
    fail "frozen search $query_id violated posture, citation, result, or context bounds"
  }
  if [[ "$include_pending" == false ]]; then
    jq -e '.data.pending_candidates.items | length == 0' "$api_path" >/dev/null \
      || fail "frozen search $query_id mixed pending candidates into default results"
  fi
  jq -e '(.expected.currentness.posture | length) > 0 and (.expected.currentness.explanation | length) > 0' <<<"$query_json" >/dev/null \
    || fail "frozen search $query_id lacks an expected final-answer posture"

  exact_collection="$(jq -er '.expected.exact_get.collection' <<<"$query_json")"
  exact_id="$(jq -er '.expected.exact_get.id' <<<"$query_json")"
  validate_exact_get "$query_id" "$exact_collection" "$exact_id"
  pass "frozen search $query_id passed API/CLI, posture, contamination, citation, exact-get, and bounds checks"
}

validate_repo_query() {
  local query_json="$1"
  local query_id query project topic expected_ids actual_ids api_path cli_path contamination_id exact_id
  query_id="$(jq -er '.query_id' <<<"$query_json")"
  query="$(jq -er '.query' <<<"$query_json")"
  project="$(jq -r '.request.filters.project // empty' <<<"$query_json")"
  topic="$(jq -r '.request.filters.topic // empty' <<<"$query_json")"
  api_path="$TMP_ROOT/query-$query_id-api.json"
  cli_path="$TMP_ROOT/query-$query_id-cli.json"
  local curl_args=(--get --data-urlencode "query=$query")
  [[ -z "$project" ]] || curl_args+=(--data-urlencode "project=$project")
  [[ -z "$topic" ]] || curl_args+=(--data-urlencode "topic=$topic")
  curl_args+=(http://loom/v1/provenance/repos)
  request_to_file "$api_path" "${curl_args[@]}"
  local cli_args=(provenance repo list --query "$query")
  [[ -z "$project" ]] || cli_args+=(--project "$project")
  [[ -z "$topic" ]] || cli_args+=(--topic "$topic")
  loom "${cli_args[@]}" >"$cli_path"
  assert_json_equal "$api_path" "$cli_path" "frozen repository finder $query_id"

  expected_ids="$(jq -c '[.expected.collections[] | select(.collection == "repo_state") | .ids[]]' <<<"$query_json")"
  actual_ids="$(jq -c '[.data.items[].repository_id]' "$api_path")"
  [[ "$actual_ids" == "$expected_ids" ]] || {
    jq . "$api_path" >&2
    fail "frozen repository finder $query_id IDs were $actual_ids, expected $expected_ids"
  }
  while IFS= read -r contamination_id; do
    [[ -n "$contamination_id" ]] || continue
    ! jq -e --arg id "$contamination_id" '.data.items | any(.repository_id == $id)' "$api_path" >/dev/null \
      || fail "frozen repository finder $query_id leaked prohibited repository $contamination_id"
  done < <(jq -r '.expected.prohibited_contamination[]?.id' <<<"$query_json")
  jq -e '
    .ok == true and .data.returned == (.data.items | length) and .data.returned <= 8 and
    (.data.items | all(
      (.repository_id | length) > 0 and (.name | length) > 0 and
      (.owning_project.project_id | length) > 0 and (.purpose | length) > 0 and
      (.portable_navigation_ref | length) > 0 and (.tracking_status | length) > 0 and
      (.accepted_context | length) <= 4 and
      (has("project_root") or has("repository_path") or has("facet_path") or has("registry_recorded_at") or has("contract_schema_version") or has("membership_source_digest") or has("git_command") or has("raw_observation") | not)
    ))
  ' "$api_path" >/dev/null || {
    jq . "$api_path" >&2
    fail "frozen repository finder $query_id violated card or context bounds"
  }
  exact_id="$(jq -er '.expected.exact_get.id' <<<"$query_json")"
  validate_exact_get "$query_id" repo_state "$exact_id"
  pass "frozen repository finder $query_id passed API/CLI, stale-contamination, exact-get, and context checks"
}

validate_exact_get() {
  local query_id="$1"
  local collection="$2"
  local id="$3"
  local resource api_resource api_path cli_path identity_filter
  case "$collection" in
    accepted_records) resource=record; api_resource=records; identity_filter='.data.record.record_id' ;;
    pending_candidates) resource=candidate; api_resource=candidates; identity_filter='.data.candidate.candidate_id' ;;
    unresolved_cases) resource=case; api_resource=cases; identity_filter='.data.case.case_id' ;;
    repo_state) resource=repo; api_resource=repos; identity_filter='.data.repository_id' ;;
    *) fail "unsupported exact-get collection $collection for $query_id" ;;
  esac
  api_path="$TMP_ROOT/exact-$query_id-api.json"
  cli_path="$TMP_ROOT/exact-$query_id-cli.json"
  if [[ "$resource" == repo ]]; then
    request_to_file "$api_path" "http://loom/v1/provenance/$api_resource/$id"
    loom provenance repo get "$id" >"$cli_path"
  else
    request_to_file "$api_path" "http://loom/v1/provenance/$api_resource/$id?limit=16"
    loom provenance "$resource" get "$id" --limit 16 >"$cli_path"
  fi
  assert_json_equal "$api_path" "$cli_path" "exact get for $query_id"
  jq -e --arg id "$id" "$identity_filter == \$id" "$api_path" >/dev/null \
    || fail "exact get for $query_id returned the wrong identity"
  case "$resource" in
    record)
      jq -e '(.data.source_reference_ids | length) <= 16 and (.data.producer_history | length) <= 16 and (.data.representation_events | length) <= 16' "$api_path" >/dev/null ;;
    candidate)
      jq -e '(.data.source_reference_ids | length) <= 16 and (.data.lineage | length) <= 16 and (.data.events | length) <= 16' "$api_path" >/dev/null ;;
    case)
      jq -e '(.data.members | length) <= 16 and (.data.events | length) <= 16' "$api_path" >/dev/null ;;
  esac || fail "exact get for $query_id exceeded bounded lifecycle context"
}

log 'measuring frozen first-tool routing and exercising every provenance query through real API and CLI paths'
routing_total=0
routing_correct=0
provenance_queries=0
while IFS= read -r query_json; do
  routing_total=$((routing_total + 1))
  engine="$(jq -er '.expected_first_engine' <<<"$query_json")"
  surface="$(jq -er '.request.surface' <<<"$query_json")"
  exact_command="$(jq -er '.expected.exact_get.command' <<<"$query_json")"
  case "$engine:$surface:$exact_command" in
    provenance:search:'loom provenance '*) routing_correct=$((routing_correct + 1)); provenance_queries=$((provenance_queries + 1)); validate_search_query "$query_json" ;;
    provenance:repo_list:'loom provenance repo get '*) routing_correct=$((routing_correct + 1)); provenance_queries=$((provenance_queries + 1)); validate_repo_query "$query_json" ;;
    notes:notes_search:'loom notes get '*) routing_correct=$((routing_correct + 1)) ;;
    objects:objects_search:'loom objects get '*) routing_correct=$((routing_correct + 1)) ;;
    project_registry:project_inspect:'loom project inspect '*) routing_correct=$((routing_correct + 1)) ;;
    *) fail "frozen routing contract is inconsistent for $(jq -r '.query_id' <<<"$query_json")" ;;
  esac
done < <(jq -c '.queries[]' "$query_contract")
[[ "$routing_total" -eq 14 && "$routing_correct" -eq 14 && "$provenance_queries" -eq 11 ]] \
  || fail "first-tool measurement was $routing_correct/$routing_total with $provenance_queries provenance queries"
pass 'first-tool correctness was 14/14; all 11 provenance-routed queries passed real API and CLI evaluation'

log 'running three independent in-process latency measurements against reset frozen corpora'
for latency_run in 1 2 3; do
  latency_log="$TMP_ROOT/latency-$latency_run.log"
  (
    cd "$ROOT"
    LOOM_PROVENANCE_TEST_DB_URL="$ADMIN_DB_URL" LOOM_SLICE6_LATENCY_RUN="$latency_run" \
      go test -count=1 -overlay "$TMP_ROOT/slice6_overlay.json" -run '^TestSliceSixLatency$' -v ./internal/provenance
  ) >"$latency_log"
  grep '^    slice6_seed_test.go:.*SLICE6_LATENCY ' "$latency_log" || grep 'SLICE6_LATENCY ' "$latency_log"
  grep -q "SLICE6_LATENCY_COMPLETE run=$latency_run" "$latency_log" \
    || fail "latency process run $latency_run did not complete"
done
pass 'all frozen provenance queries met p95 and individual latency budgets in three independent reset runs'

log 'resetting only the smoke-owned provenance database for manual Archivist evaluation'
stop_daemon
"$PG_BIN/dropdb" -h "$PG_SOCKET" -U postgres loom_provenance
"$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance
start_daemon true "$TMP_ROOT/loomd-archivist.log"

loom worker inspect main.provenance_archivist >"$TMP_ROOT/archivist-inspect.json"
loom worker policy inspect main.provenance_archivist >"$TMP_ROOT/archivist-policy.json"
jq -e '
  .ok == true and
  .data.instance.worker_key == "main.provenance_archivist" and
  .data.instance.worker_kind == "provenance_archivist" and
  .data.instance.enabled == true and .data.instance.paused == false and
  .data.instance.tick_policy_json.mode == "manual" and
  .data.instance.next_run_after == null and
  .data.instance.metadata.manual_only == true and
  .data.instance.metadata.scheduling_enabled == false and
  .data.kind.metadata.scheduling_enabled == false
' "$TMP_ROOT/archivist-inspect.json" >/dev/null \
  || fail 'Archivist worker was not registered as enabled, manual-only, and unscheduled'
jq -e '
  .ok == true and .data.worker_key == "main.provenance_archivist" and
  .data.policy.mode == "manual" and .data.next_run_after == null
' "$TMP_ROOT/archivist-policy.json" >/dev/null \
  || fail 'Archivist normalized policy was not manual with no next run'
pass 'Archivist worker and policy are manual-only with scheduling disabled'

jq -n '{
  candidates: [{
    schema_version: "1.0",
    claim: "Disposable Slice 6 source-gap candidate.",
    record_kind: "decision",
    record_context: "Disposable integrated semantic evaluation only.",
    sources: [{
      source_reference_id: "00000000-0000-4000-8000-000000006001",
      kind: "codex_current_thread",
      status: "unresolved",
      verification_posture: "unverified",
      resolver_name: "disposable-smoke",
      resolver_version: "1.0",
      gap_reason: "The disposable source intentionally remains unresolved.",
      submitted: {kind: "codex_current_thread", evidence: {excerpt: "disposable only"}}
    }],
    domain: "provenance-search-slice-6",
    visibility: "private",
    temporal_interpretation: {interpretation: "Applies only to this disposable smoke."},
    assertion_posture: "source_claim",
    producer: {producer_id: "slice-6-smoke", producer_kind: "working_agent", task_id: "disposable-slice-6"},
    anchors: {entities: ["slice6.disposable.source_gap"], projects: ["LOOM"]}
  }]
}' >"$TMP_ROOT/archivist-register.json"
request_to_file "$TMP_ROOT/archivist-register-result.json" \
  -H 'Content-Type: application/json' -H 'X-Loom-Idempotency-Key: slice6-archivist-register' \
  --data-binary "@$TMP_ROOT/archivist-register.json" http://loom/v1/provenance/candidates
ARCHIVIST_CANDIDATE_ID="$(jq -er '.data.receipt.receipts[0].candidate_id' "$TMP_ROOT/archivist-register-result.json")"

loom worker run main.provenance_archivist --once --reason slice6-manual \
  --idempotency-key slice6-archivist-manual >"$TMP_ROOT/archivist-run-first.json"
jq -e --arg candidate "$ARCHIVIST_CANDIDATE_ID" '
  .ok == true and .data.run.run_status == "succeeded" and .data.run.trigger_kind == "manual" and
  .data.run.idempotency_key == "slice6-archivist-manual" and
  .data.run.result_summary_json.schema_version == "loom.provenance.archivist.result.v1" and
  .data.run.result_summary_json.cycle_complete == true and
  .data.run.result_summary_json.selected == 1 and
  .data.run.result_summary_json.examined == 1 and
  .data.run.result_summary_json.deferred == 1 and
  .data.run.result_summary_json.cases_created == 1 and
  .data.run.result_summary_json.outcomes[0].candidate_id == $candidate and
  .data.run.result_summary_json.outcomes[0].outcome == "defer" and
  .data.run.result_summary_json.outcomes[0].reason_code == "agent_interpretation" and
  .data.run.counters_json.selected == 1 and .data.run.counters_json.exact_reads >= 1 and
  .data.run.resource_usage_json.candidate_limit == 10 and .data.run.resource_usage_json.evidence_limit == 8 and
  .data.health.health_status == "healthy" and
  .data.worker.instance.next_run_after == null and
  .data.worker.instance.tick_policy_json.mode == "manual"
' "$TMP_ROOT/archivist-run-first.json" >/dev/null || {
  jq . "$TMP_ROOT/archivist-run-first.json" >&2
  fail 'first manual Archivist cycle violated its frozen result, resource, health, or scheduling posture'
}
ARCHIVIST_RUN_ID="$(jq -er '.data.run.worker_run_id' "$TMP_ROOT/archivist-run-first.json")"
ARCHIVIST_CASE_ID="$(jq -er '.data.run.result_summary_json.outcomes[0].case_id' "$TMP_ROOT/archivist-run-first.json")"

loom worker run main.provenance_archivist --once --reason slice6-manual \
  --idempotency-key slice6-archivist-manual >"$TMP_ROOT/archivist-run-replay.json"
jq -e --arg run "$ARCHIVIST_RUN_ID" --arg case_id "$ARCHIVIST_CASE_ID" '
  .ok == true and .data.run.worker_run_id == $run and
  .data.run.result_summary_json.outcomes[0].case_id == $case_id
' "$TMP_ROOT/archivist-run-replay.json" >/dev/null \
  || fail 'manual Archivist run did not deterministically replay its supported idempotency key'
loom worker runs main.provenance_archivist --limit 16 >"$TMP_ROOT/archivist-runs-after-replay.json"
jq -e --arg run "$ARCHIVIST_RUN_ID" '
  [.data[] | select(.worker_run_id == $run)] | length == 1
' "$TMP_ROOT/archivist-runs-after-replay.json" >/dev/null \
  || fail 'manual Archivist idempotency replay created duplicate durable worker runs'
pass 'manual Archivist run deterministically replayed without duplicate worker or lifecycle state'

loom worker run main.provenance_archivist --once --reason slice6-second-manual \
  --idempotency-key slice6-archivist-second >"$TMP_ROOT/archivist-run-second.json"
jq -e --arg case_id "$ARCHIVIST_CASE_ID" '
  .ok == true and .data.run.run_status == "succeeded" and
  .data.run.result_summary_json.cycle_complete == true and
  .data.run.result_summary_json.selected == 1 and
  .data.run.result_summary_json.deferred == 1 and
  .data.run.result_summary_json.cases_updated == 1 and
  .data.run.result_summary_json.outcomes[0].case_id == $case_id and
  .data.worker.instance.next_run_after == null
' "$TMP_ROOT/archivist-run-second.json" >/dev/null || {
  jq . "$TMP_ROOT/archivist-run-second.json" >&2
  fail 'second manual Archivist cycle did not update the deterministic case without scheduling'
}

request_to_file "$TMP_ROOT/archivist-candidate.json" \
  "http://loom/v1/provenance/candidates/$ARCHIVIST_CANDIDATE_ID?limit=16"
request_to_file "$TMP_ROOT/archivist-case.json" \
  "http://loom/v1/provenance/cases/$ARCHIVIST_CASE_ID?limit=16"
loom provenance candidate get "$ARCHIVIST_CANDIDATE_ID" --limit 16 >"$TMP_ROOT/archivist-candidate-cli.json"
loom provenance case get "$ARCHIVIST_CASE_ID" --limit 16 >"$TMP_ROOT/archivist-case-cli.json"
assert_json_equal "$TMP_ROOT/archivist-candidate.json" "$TMP_ROOT/archivist-candidate-cli.json" 'manual Archivist candidate exact get'
assert_json_equal "$TMP_ROOT/archivist-case.json" "$TMP_ROOT/archivist-case-cli.json" 'manual Archivist case exact get'
jq -e --arg candidate "$ARCHIVIST_CANDIDATE_ID" '
  .data.candidate.candidate_id == $candidate and .data.effective_state == "pending" and
  (.data.latest_deferral | length) > 0 and (.data.events | length) <= 16
' "$TMP_ROOT/archivist-candidate.json" >/dev/null \
  || fail 'manual Archivist candidate lost its pending/deferred lifecycle posture'
jq -e --arg case_id "$ARCHIVIST_CASE_ID" --arg candidate "$ARCHIVIST_CANDIDATE_ID" '
  .data.case.case_id == $case_id and .data.effective_state == "open" and
  any(.data.members[]; .candidate_id == $candidate) and
  (.data.members | length) <= 16 and (.data.events | length) <= 16
' "$TMP_ROOT/archivist-case.json" >/dev/null \
  || fail 'manual Archivist resolution case lost its deterministic member or bounded lifecycle'
pass 'two manual cycles preserved candidate/record separation and one deterministic open case'

log 'running the real crash-after-commit and retry contract on another disposable database'
(
  cd "$ROOT"
  LOOM_PROVENANCE_TEST_DB_URL="$ADMIN_DB_URL" \
    go test -count=1 -run '^TestArchivistCrashAfterCommitDeterministicReplayPostgres$' ./internal/provenance
)
pass 'crash-after-commit retry replayed deterministic Archivist lifecycle state'

log 'restarting the disposable daemon without migration writes'
stop_daemon
start_daemon false "$TMP_ROOT/loomd-archivist-restart.log"
request_to_file "$TMP_ROOT/archivist-health-after-restart.json" http://loom/v1/provenance/health
request_to_file "$TMP_ROOT/archivist-case-after-restart.json" \
  "http://loom/v1/provenance/cases/$ARCHIVIST_CASE_ID?limit=16"
loom worker inspect main.provenance_archivist >"$TMP_ROOT/archivist-inspect-after-restart.json"
jq -e '.data.state == "ready" and .data.code == "ready" and .data.database == "loom_provenance"' \
  "$TMP_ROOT/archivist-health-after-restart.json" >/dev/null \
  || fail 'provenance service was not ready after disposable daemon restart'
jq -e --arg case_id "$ARCHIVIST_CASE_ID" '.data.case.case_id == $case_id and .data.effective_state == "open"' \
  "$TMP_ROOT/archivist-case-after-restart.json" >/dev/null \
  || fail 'Archivist case did not survive disposable daemon restart'
jq -e --arg run "$ARCHIVIST_RUN_ID" '
  .data.instance.tick_policy_json.mode == "manual" and .data.instance.next_run_after == null and
  any(.data.recent_runs[]; .worker_run_id == $run)
' "$TMP_ROOT/archivist-inspect-after-restart.json" >/dev/null \
  || fail 'Archivist manual policy or run evidence did not survive daemon restart'
pass 'daemon restart preserved provenance and worker state without enabling a schedule'

log 'dumping and restoring only the disposable provenance database'
stop_daemon
"$PG_BIN/pg_dump" -Fc -h "$PG_SOCKET" -U loom_provenance -d loom_provenance \
  -f "$TMP_ROOT/loom-provenance.slice6.dump"
[[ -s "$TMP_ROOT/loom-provenance.slice6.dump" ]] || fail 'disposable provenance dump is empty'
"$PG_BIN/dropdb" -h "$PG_SOCKET" -U postgres loom_provenance
"$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance
"$PG_BIN/pg_restore" -h "$PG_SOCKET" -U loom_provenance -d loom_provenance \
  --no-owner --no-acl "$TMP_ROOT/loom-provenance.slice6.dump"
start_daemon false "$TMP_ROOT/loomd-archivist-restored.log"
request_to_file "$TMP_ROOT/archivist-candidate-restored.json" \
  "http://loom/v1/provenance/candidates/$ARCHIVIST_CANDIDATE_ID?limit=16"
request_to_file "$TMP_ROOT/archivist-case-restored.json" \
  "http://loom/v1/provenance/cases/$ARCHIVIST_CASE_ID?limit=16"
jq -e --arg candidate "$ARCHIVIST_CANDIDATE_ID" '
  .data.candidate.candidate_id == $candidate and .data.effective_state == "pending" and (.data.latest_deferral | length) > 0
' "$TMP_ROOT/archivist-candidate-restored.json" >/dev/null \
  || fail 'disposable restore lost candidate lifecycle state'
jq -e --arg case_id "$ARCHIVIST_CASE_ID" --arg candidate "$ARCHIVIST_CANDIDATE_ID" '
  .data.case.case_id == $case_id and .data.effective_state == "open" and any(.data.members[]; .candidate_id == $candidate)
' "$TMP_ROOT/archivist-case-restored.json" >/dev/null \
  || fail 'disposable restore lost resolution-case lifecycle state'
pass 'disposable pg_dump/pg_restore preserved exact provenance lifecycle state'

log 'scaffolding and scanning a disposable portable Archivist workspace'
ARCHIVIST_WORKSPACE="$TMP_ROOT/workspace/archivist"
mkdir -p "$(dirname "$ARCHIVIST_WORKSPACE")"
loom agent pack scaffold-workspace --pack-dir "$ROOT/ai-loom-pack" --template archivist \
  --path "$ARCHIVIST_WORKSPACE" --dry-run >"$TMP_ROOT/archivist-workspace-plan.json"
jq -e --arg path "$ARCHIVIST_WORKSPACE" '
  .template == "archivist" and .path == $path and .conflicts == 0 and (.actions | length) > 0 and
  ([.actions[].relative_path | select(. == "memory" or startswith("memory/"))] | length) == 0
' "$TMP_ROOT/archivist-workspace-plan.json" >/dev/null \
  || fail 'Archivist workspace dry-run was not portable and conflict-free'
loom agent pack scaffold-workspace --pack-dir "$ROOT/ai-loom-pack" --template archivist \
  --path "$ARCHIVIST_WORKSPACE" --yes >"$TMP_ROOT/archivist-workspace-apply.json"
jq -e '.template == "archivist" and .applied == true' "$TMP_ROOT/archivist-workspace-apply.json" >/dev/null \
  || fail 'Archivist disposable workspace did not apply'
[[ ! -e "$ARCHIVIST_WORKSPACE/memory" ]] || fail 'Archivist workspace created private memory'
[[ -f "$ARCHIVIST_WORKSPACE/ARCHIVIST.md" && -f "$ARCHIVIST_WORKSPACE/protocols/CANDIDATE-REVIEW.md" ]] \
  || fail 'Archivist workspace omitted required portable sources'
non_regular="$(find "$ARCHIVIST_WORKSPACE" ! -type d ! -type f -print -quit)"
[[ -z "$non_regular" ]] || fail "Archivist workspace contains a non-regular path: $non_regular"
for forbidden_directory in memory private-memory runtime state cache caches logs sessions generated-indexes; do
  ! find "$ARCHIVIST_WORKSPACE" -type d -iname "$forbidden_directory" -print -quit | grep -q . \
    || fail "Archivist workspace contains forbidden directory $forbidden_directory"
done
for forbidden_marker in \
  '/srv/' '/Users/' '/home/' '/var/lib/' 'postgres://' 'postgresql://' \
  'LOOM_PROVENANCE_DB_URL' 'pass://' 'BEGIN PRIVATE KEY' 'password=' 'token=' \
  '.loom/state' 'checkpoint.json' "$TMP_ROOT" "$PG_SOCKET" "$MAIN_DB_URL" "$PROVENANCE_DB_URL"
do
  ! grep -R -F -- "$forbidden_marker" "$ARCHIVIST_WORKSPACE" >/dev/null \
    || fail "Archivist workspace leaked forbidden runtime marker: $forbidden_marker"
done
loom agent pack scaffold-workspace --pack-dir "$ROOT/ai-loom-pack" --template archivist \
  --path "$ARCHIVIST_WORKSPACE" --yes >"$TMP_ROOT/archivist-workspace-replay.json"
jq -e '.template == "archivist" and .applied == true and all(.actions[]; .kind != "conflict-non-directory")' \
  "$TMP_ROOT/archivist-workspace-replay.json" >/dev/null \
  || fail 'Archivist workspace replay was not preserving and conflict-free'
pass 'portable Archivist workspace contains no host paths, credentials, database locators, runtime state, or private memory'

stop_daemon
log "passed checks: $PASS_COUNT"
