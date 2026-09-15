#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-full}"
[[ "$#" -le 1 && ( "$MODE" == full || "$MODE" == --offline-owner-only ) ]] || {
  printf 'Usage: %s [--offline-owner-only]\n' "$0" >&2; exit 1;
}
OWNER_KEY=box-owner
[[ "$MODE" != --offline-owner-only ]] || OWNER_KEY=macbook
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d /tmp/loom-box-citation-XXXXXX)"
PG_STARTED=false
PG_BIN=""
DAEMON_PID=""
AGENT_PID=""
PROJECTION_LOCK_PID=""
stop_process() {
  local pid="$1"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid"
    wait "$pid" || true
  fi
}
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  stop_process "$PROJECTION_LOCK_PID"
  stop_process "$AGENT_PID"
  stop_process "$DAEMON_PID"
  if [[ "$PG_STARTED" == true ]]; then
    if ! "$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -m fast -w stop >/dev/null; then
      printf 'FAIL: disposable PostgreSQL did not stop; retained %s\n' "$TMP_ROOT" >&2
      exit 1
    fi
  fi
  case "$TMP_ROOT" in
    /tmp/loom-box-citation-*)
      find -P "$TMP_ROOT" -type d -exec chmod u+w {} +
      rm -rf -- "$TMP_ROOT"
      [[ ! -e "$TMP_ROOT" ]] || exit 1
      ;;
    *) exit 1 ;;
  esac
  printf 'Disposable citation cluster and fixture root removed.\n'
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
[[ -n "$NIX" ]] || { printf 'Nix is required for pinned disposable PostgreSQL.\n' >&2; exit 1; }
PG_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build \
  --no-link --print-out-paths --impure --expr \
  'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])')"
PG_BIN="$PG_ROOT/bin"
[[ -x "$PG_BIN/pg_ctl" ]] || { printf 'Pinned PostgreSQL unavailable.\n' >&2; exit 1; }
PDF_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build \
  --no-link --print-out-paths --impure --expr \
  'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.poppler-utils')"
[[ -x "$PDF_ROOT/bin/pdfinfo" && -x "$PDF_ROOT/bin/pdftotext" ]] || { printf 'Pinned PDF tools unavailable.\n' >&2; exit 1; }
export PATH="$PDF_ROOT/bin:$PG_BIN:$PATH"
mkdir -m 0700 "$TMP_ROOT/socket" "$TMP_ROOT/home" "$TMP_ROOT/config"
"$PG_BIN/initdb" -D "$TMP_ROOT/postgres" --username=postgres --auth-local=trust \
  --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
"$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -l "$TMP_ROOT/postgres.log" \
  -o "-c listen_addresses='' -c unix_socket_directories='$TMP_ROOT/socket' -c unix_socket_permissions=0700" -w start >/dev/null
PG_STARTED=true
"$PG_BIN/createdb" -h "$TMP_ROOT/socket" -U postgres loom_box_citation
"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$TMP_ROOT/socket" -U postgres -d postgres \
  -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT' >/dev/null
"$PG_BIN/createdb" -h "$TMP_ROOT/socket" -U postgres -O loom_provenance loom_provenance
"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$TMP_ROOT/socket" -U postgres -d postgres \
  -c 'REVOKE CONNECT ON DATABASE loom_box_citation FROM PUBLIC; GRANT CONNECT ON DATABASE loom_box_citation TO postgres' >/dev/null

printf 'Checking the frozen Box contract, native retrieval and citations in disposable PostgreSQL.\n'
go test -count=1 -run BoxSourcesContract ./internal/knowledge
# Preserve the configured Go cache while isolating all LOOM CLI configuration.
GO_CACHE="$(go env GOCACHE)"
GO_MOD_CACHE="$(go env GOMODCACHE)"
go test -c -o "$TMP_ROOT/knowledge-http.test" ./internal/httpapi
go build -o "$TMP_ROOT/loom" ./cmd/loom
go build -o "$TMP_ROOT/loomd" ./cmd/loomd
go build -o "$TMP_ROOT/node-agent" ./cmd/loom-node-agent

# Separate runtime database and real processes: no derived knowledge seeding.
RUNTIME_ROOT="$(cd "$TMP_ROOT" && pwd -P)/runtime"
BOX_ROOT="$RUNTIME_ROOT/box"
SOCKET_PATH="$TMP_ROOT/loomd.sock"
mkdir -p "$RUNTIME_ROOT/home" "$RUNTIME_ROOT/config" "$RUNTIME_ROOT/agent"
HTTP_PORT="$(ruby -rsocket -e 's=TCPServer.new("127.0.0.1",0); puts s.addr[1]; s.close')"
"$PG_BIN/createdb" -h "$TMP_ROOT/socket" -U postgres loom_box_runtime
runtime_loom() {
  env -i PATH="$PATH" HOME="$RUNTIME_ROOT/home" XDG_CONFIG_HOME="$RUNTIME_ROOT/config" LOOM_NODE_ID="$OWNER_KEY" \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" \
    "$TMP_ROOT/loom" --socket "$SOCKET_PATH" --json "$@"
}
runtime_agent() {
  env -i PATH="$PATH" HOME="$RUNTIME_ROOT/home" XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    "$TMP_ROOT/node-agent" --config "$RUNTIME_ROOT/agent/config.json" \
    --state "$RUNTIME_ROOT/agent/state.json" --data-dir "$RUNTIME_ROOT/agent/data" --json "$@"
}
start_agent() {
  env -i PATH="$PATH" HOME="$RUNTIME_ROOT/home" XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    "$TMP_ROOT/node-agent" --config "$RUNTIME_ROOT/agent/config.json" \
    --state "$RUNTIME_ROOT/agent/state.json" --data-dir "$RUNTIME_ROOT/agent/data" --json serve \
    >>"$TMP_ROOT/runtime-agent.log" 2>&1 &
  AGENT_PID=$!
}
start_daemon() {
  env -i PATH="$PATH" HOME="$RUNTIME_ROOT/home" XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    LOOM_ENV=test LOOM_NODE_ID=main LOOM_NODE_KIND=main LOOM_NODE_ROLE=main LOOM_RUNTIME_CLASS=main_full \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" LOOM_OBJECT_STORE="$RUNTIME_ROOT/data/object-store" \
    LOOM_SERVICE_ROOT="$RUNTIME_ROOT/service" LOOM_STORAGE_ROOT="$RUNTIME_ROOT/storage" \
    LOOM_CANONICAL_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_IMPORTS_ROOT="$RUNTIME_ROOT/storage/imports" LOOM_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_ARCHIVE_ROOT="$RUNTIME_ROOT/storage/archive" LOOM_GENERATED_ROOT="$RUNTIME_ROOT/data/generated" \
    LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" LOOM_STORAGE_RETENTION_ROOT="$RUNTIME_ROOT/data/storage-retention" \
    LOOM_BOX_PATH="$RUNTIME_ROOT/main-box" LOOM_BOX_PROFILE=main \
    LOOM_SOCKET_PATH="$SOCKET_PATH" LOOM_HTTP_LISTEN_ADDR="127.0.0.1:$HTTP_PORT" \
    LOOM_DB_URL="postgresql://postgres@/loom_box_runtime?host=$TMP_ROOT/socket" \
    LOOM_PROVENANCE_DB_URL="postgresql://loom_provenance@/loom_provenance?host=$TMP_ROOT/socket" \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" LOOM_AUTO_MIGRATE=true LOOM_BOOTSTRAP_MODE=dev \
    "$TMP_ROOT/loomd" serve >>"$TMP_ROOT/runtime-daemon.log" 2>&1 &
  DAEMON_PID=$!
  for ((n=0; n<300; n++)); do
    if [[ -S "$SOCKET_PATH" ]] && runtime_loom health >"$TMP_ROOT/runtime-health.json" 2>"$TMP_ROOT/runtime-health-error"; then return; fi
    kill -0 "$DAEMON_PID" 2>/dev/null || break
    sleep 0.1
  done
  tail -40 "$TMP_ROOT/runtime-daemon.log" >&2
  cat "$TMP_ROOT/runtime-health.json" "$TMP_ROOT/runtime-health-error" >&2
  printf 'FAIL: disposable daemon did not become ready.\n' >&2
  exit 1
}
start_daemon
runtime_loom notes pipelines policy set --pdf-ocr=false --image-descriptions=false --embeddings=false --yes >"$TMP_ROOT/runtime-heavy-policy.json"
runtime_loom worker policy inspect main.knowledge_indexer >"$TMP_ROOT/runtime-tick-before.json"
runtime_agent init --main-url "http://127.0.0.1:$HTTP_PORT" --node-key "$OWNER_KEY" \
  --display-name 'Disposable Box owner' --kind workspace --role workspace --runtime-class workspace \
  --heartbeat-interval 5 --poll-interval 5 >"$TMP_ROOT/runtime-agent-init.json"
runtime_loom node enrollment-token create --ttl-seconds 1800 >"$TMP_ROOT/runtime-token.json"
jq -er '.data.token_value' "$TMP_ROOT/runtime-token.json" >"$TMP_ROOT/enrollment-token"
chmod 0600 "$TMP_ROOT/enrollment-token" "$TMP_ROOT/runtime-token.json"
runtime_agent enroll --token-file "$TMP_ROOT/enrollment-token" >"$TMP_ROOT/runtime-enrollment.json"
ENROLLMENT="$(jq -er '.data.node_enrollment_request_id' "$TMP_ROOT/runtime-enrollment.json")"
runtime_loom node enrollment-request approve "$ENROLLMENT" >"$TMP_ROOT/runtime-approval.json"
chmod 0600 "$TMP_ROOT/runtime-approval.json"
runtime_agent credential import --from-file "$TMP_ROOT/runtime-approval.json" >"$TMP_ROOT/runtime-import.json"

if [[ "$MODE" == --offline-owner-only ]]; then
  runtime_loom box init --path "$BOX_ROOT" --profile workspace >"$TMP_ROOT/offline-box-init.json"
  ruby -ryaml -rjson -e '
    root,corpus=ARGV; path=File.join(root,".loom/box.yaml"); c=YAML.load_file(path)
    c.fetch("areas").each { |area,config| config["enabled"]=area=="notes" }
    %w[notes documents].each do |area|
      relative=c.fetch("policies").fetch(area)
      p=File.join(root,relative); policy=YAML.load_file(p)
      policy["enabled"]=area=="notes"; policy["backup"]={"enabled"=>false}
      File.write(p,YAML.dump(policy))
    end
    File.write(path,YAML.dump(c))
    corpus=JSON.parse(File.read(corpus)); source=corpus.fetch("sources").find { |s| s.fetch("key")=="offline" }
    owner=corpus.fetch("roots").find { |r| r.fetch("key")==source.fetch("root_key") }
    abort("frozen offline owner mismatch") unless owner.fetch("node_key")=="macbook" && !owner.fetch("owner_online")
    version=source.fetch("versions").find { |v| v.fetch("key")==source.fetch("current_version") }
    File.write(File.join(root,"Notes",source.fetch("relative_path")),"# #{version.fetch("passages").first.fetch("locator").fetch("heading")}\n\n#{version.fetch("fixture_text")}\n")
  ' "$BOX_ROOT" "$ROOT/internal/knowledge/testdata/box_sources/corpus.json"
  runtime_loom box watch-plan --path "$BOX_ROOT" --profile workspace >"$TMP_ROOT/offline-watch-plan.json"
  runtime_loom box watch-apply --path "$BOX_ROOT" --profile workspace \
    --idempotency-key box-slice5-offline-register >"$TMP_ROOT/offline-watch-registration.json"
  runtime_agent filesystem roots add --key loom_box --path "$BOX_ROOT" >"$TMP_ROOT/offline-safe-root.json"
  runtime_agent watched-roots apply-plan "$TMP_ROOT/offline-watch-plan.json" >"$TMP_ROOT/offline-watch-apply.json"
  OFFLINE_ROOT_KEY="$(jq -er '.data.applied[] | select(.root_relative_path=="Notes") | .root_key' "$TMP_ROOT/offline-watch-apply.json")"
  runtime_loom worker policy inspect main.realtime_expiry >"$TMP_ROOT/offline-policy-before.json"
  start_agent
  OFFLINE_OBJECT=""
  for ((n=0; n<90; n++)); do
    kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
    if runtime_agent watched-roots explain "$OFFLINE_ROOT_KEY" --path offline.md >"$TMP_ROOT/offline-owner-path.json" &&
      jq -e '.data.state | (.sync_status=="accepted" or .sync_status=="duplicate") and (.main_object_id | length>0)' "$TMP_ROOT/offline-owner-path.json" >/dev/null; then
      OFFLINE_OBJECT="$(jq -er '.data.state.main_object_id' "$TMP_ROOT/offline-owner-path.json")"
      break
    fi
    sleep 2
  done
  [[ -n "$OFFLINE_OBJECT" ]] || { printf 'FAIL: frozen offline source was not uploaded.\n' >&2; exit 1; }
  runtime_loom object inspect "$OFFLINE_OBJECT" >"$TMP_ROOT/offline-object-before.json"
  runtime_loom node health macbook >"$TMP_ROOT/offline-node-before.json"
  jq -e '.data.node.presence_state=="online"' "$TMP_ROOT/offline-node-before.json" >/dev/null
  stop_process "$AGENT_PID"
  AGENT_PID=""
  runtime_loom node health macbook >"$TMP_ROOT/offline-node-stopped.json"
  # Hide only this owned fixture pathname after its owner stops. Retain bytes;
  # no Main-side source crawl can now stand in for retained object custody.
  mv "$BOX_ROOT" "$RUNTIME_ROOT/offline-source-retained"
  [[ ! -e "$BOX_ROOT" ]]
  printf 'Observing retained frozen source after actual owner exit and natural expiry (bound: 180 seconds).\n'
  OFFLINE_READY=false
  for ((n=0; n<90; n++)); do
    kill -0 "$DAEMON_PID" || break
    runtime_loom node health macbook >"$TMP_ROOT/offline-node-after.json"
    if jq -e '.data.node.presence_state=="offline"' "$TMP_ROOT/offline-node-after.json" >/dev/null; then
      OFFLINE_READY=true
      break
    fi
    sleep 2
  done
  [[ "$OFFLINE_READY" == true ]] || { printf 'FAIL: retained source owner did not expire.\n' >&2; exit 1; }
  runtime_loom node inspect macbook >"$TMP_ROOT/offline-node-inspect.json"
  runtime_loom object inspect "$OFFLINE_OBJECT" >"$TMP_ROOT/offline-object-after.json"
  runtime_loom worker policy inspect main.realtime_expiry >"$TMP_ROOT/offline-policy-after.json"
  ruby -rjson -rdigest -e '
    root=ARGV.pop
    before,after,health_before,health,inspected,policy_before,policy_after=ARGV.map { |p| JSON.parse(File.read(p)).fetch("data") }
    node=health.fetch("node")
    abort("offline owner binding changed") unless node.fetch("node_key")=="macbook" &&
      node.fetch("node_id")==health_before.fetch("node").fetch("node_id") &&
      [node,inspected].all? { |n| n.fetch("presence_state")=="offline" } &&
      health.fetch("last_heartbeat").fetch("node_heartbeat_id")==health_before.fetch("last_heartbeat").fetch("node_heartbeat_id")
    abort("expiry policy changed") unless policy_before.fetch("policy_fingerprint")==policy_after.fetch("policy_fingerprint")
    %w[object latest_version blob file locations].each { |key| abort("retained #{key} changed") unless before.fetch(key)==after.fetch(key) }
    abort("retained source has another owner") unless after.fetch("file").fetch("source_node_id")==node.fetch("node_id")
    abort("live source pathname still exists") if File.exist?(File.join(root,"box"))
    blob=after.fetch("blob"); path=blob.fetch("storage_path")
    abort("blob outside owned object store") unless path.start_with?(File.join(root,"data/object-store")+"/")
    stat=File.lstat(path); abort("retained blob is not regular") unless stat.file? && !stat.symlink?
    source=File.join(root,"offline-source-retained/Notes/offline.md")
    hash="sha256:"+Digest::SHA256.file(path).hexdigest
    abort("retained source bytes differ") unless File.binread(path)==File.binread(source) &&
      hash==after.fetch("latest_version").fetch("content_hash")
    puts JSON.generate({source:"offline",node:node.fetch("node_id"),presence:"offline",
      object:after.fetch("object").fetch("object_id"),version:after.fetch("latest_version").fetch("object_version_id"),
      bytes:stat.size,hash:hash,live_path_absent:true,policy:policy_after.fetch("policy_fingerprint")})
  ' "$TMP_ROOT/offline-object-before.json" "$TMP_ROOT/offline-object-after.json" \
    "$TMP_ROOT/offline-node-stopped.json" "$TMP_ROOT/offline-node-after.json" "$TMP_ROOT/offline-node-inspect.json" \
    "$TMP_ROOT/offline-policy-before.json" "$TMP_ROOT/offline-policy-after.json" "$RUNTIME_ROOT"
  printf 'PASS: Objects reports retained source identity with its real owner offline; no live path substitution.\n'
  exit 0
fi

# Exercise the shared owner-presence prerequisite before costly indexing.
# Use the product's published expiry and ordinary expiry worker, not a fake
# offline heartbeat, rewritten database timestamp or accelerated tick policy.
runtime_loom worker policy inspect main.realtime_expiry >"$TMP_ROOT/runtime-expiry-policy-before.json"
start_agent
OWNER_READY=false
for ((n=0; n<150; n++)); do
  kill -0 "$DAEMON_PID" 2>/dev/null && kill -0 "$AGENT_PID" 2>/dev/null || break
  runtime_loom node health box-owner >"$TMP_ROOT/runtime-owner-health.json"
  if jq -e '.data.node.presence_state=="online" and .data.last_heartbeat.reported_status=="ok" and .data.heartbeat_age_seconds<=10' "$TMP_ROOT/runtime-owner-health.json" >/dev/null; then
    OWNER_READY=true
    break
  fi
  sleep 0.2
done
[[ "$OWNER_READY" == true ]] || { printf 'FAIL: disposable owner did not send a live heartbeat.\n' >&2; exit 1; }
OWNER_ID="$(jq -er '.data.node.node_id' "$TMP_ROOT/runtime-owner-health.json")"
OWNER_PID="$AGENT_PID"
stop_process "$AGENT_PID"
AGENT_PID=""
if kill -0 "$OWNER_PID" 2>/dev/null; then
  printf 'FAIL: disposable owner process did not stop.\n' >&2
  exit 1
fi
runtime_loom node health "$OWNER_ID" >"$TMP_ROOT/runtime-owner-stopped.json"
runtime_loom realtime presence inspect "$OWNER_ID" >"$TMP_ROOT/runtime-owner-presence-before.json"
ruby -rjson -rtime -e '
  h,p=ARGV.map { |f| JSON.parse(File.read(f)).fetch("data") }
  b=h.fetch("last_heartbeat")
  abort("owner/presence heartbeat mismatch") unless p.fetch("subject_kind")=="node" &&
    p.fetch("subject_ref")==h.fetch("node").fetch("node_id") && p.fetch("node_id")==p.fetch("subject_ref") &&
    p.fetch("source_kind")=="heartbeat" && p.fetch("source_ref")==b.fetch("node_heartbeat_id") &&
    Time.iso8601(p.fetch("last_seen_at"))==Time.iso8601(b.fetch("received_at")) &&
    Time.iso8601(p.fetch("expires_at"))>Time.now && p.fetch("state")=="online"
' "$TMP_ROOT/runtime-owner-stopped.json" "$TMP_ROOT/runtime-owner-presence-before.json"
printf 'Observing stopped owner through existing presence expiry and normal worker cadence (bound: 180 seconds).\n'
OWNER_EXPIRED=false
for ((n=0; n<90; n++)); do
  kill -0 "$DAEMON_PID" 2>/dev/null || break
  runtime_loom realtime presence inspect "$OWNER_ID" >"$TMP_ROOT/runtime-owner-presence-after.json"
  if jq -e '.data.state=="stale" and .data.source_kind=="expiry_worker"' "$TMP_ROOT/runtime-owner-presence-after.json" >/dev/null; then
    OWNER_EXPIRED=true
    break
  fi
  sleep 2
done
runtime_loom node health "$OWNER_ID" >"$TMP_ROOT/runtime-owner-health-after.json"
runtime_loom node inspect "$OWNER_ID" >"$TMP_ROOT/runtime-owner-inspect-after.json"
runtime_loom node list >"$TMP_ROOT/runtime-owner-list-after.json"
runtime_loom worker inspect main.realtime_expiry >"$TMP_ROOT/runtime-expiry-worker.json"
runtime_loom worker policy inspect main.realtime_expiry >"$TMP_ROOT/runtime-expiry-policy-after.json"
ruby -rjson -rtime -e '
  before,after,health,inspected,listed,worker,policy_before,policy_after=ARGV.map { |f| JSON.parse(File.read(f)).fetch("data") }
  node=health.fetch("node"); id=node.fetch("node_id"); heartbeat=health.fetch("last_heartbeat")
  original=before.fetch("last_heartbeat"); row=listed.find { |v| v.fetch("node_id")==id } or abort("owner missing from list")
  abort("heartbeat changed after owner exit") unless heartbeat.fetch("node_heartbeat_id")==original.fetch("node_heartbeat_id") &&
    heartbeat.fetch("received_at")==original.fetch("received_at")
  abort("expiry policy changed") unless policy_before.fetch("policy_fingerprint")==policy_after.fetch("policy_fingerprint")
  abort("expired presence belongs to a different observation") unless after.fetch("subject_ref")==id &&
    after.fetch("node_id")==id && Time.iso8601(after.fetch("last_seen_at"))==Time.iso8601(heartbeat.fetch("received_at"))
  puts JSON.pretty_generate({"owner_presence_observation"=>{
    "node_id"=>id,"node_key"=>node.fetch("node_key"),"owner_process_stopped"=>true,
    "last_heartbeat_id"=>heartbeat.fetch("node_heartbeat_id"),"last_received_at"=>heartbeat.fetch("received_at"),
    "heartbeat_age_seconds"=>health.fetch("heartbeat_age_seconds"),"last_reported_presence"=>heartbeat.fetch("presence_state"),
    "node_health_presence"=>node.fetch("presence_state"),"node_inspect_presence"=>inspected.fetch("presence_state"),
    "node_list_presence"=>row.fetch("presence_state"),"realtime_presence_id"=>after.fetch("presence_id"),
    "realtime_state"=>after.fetch("state"),"realtime_source"=>after.fetch("source_kind"),
    "expires_at"=>after.fetch("expires_at"),"realtime_updated_at"=>after.fetch("updated_at"),
    "expiry_policy_fingerprint"=>policy_after.fetch("policy_fingerprint"),"expiry_worker_health"=>worker.fetch("health"),
    "expiry_last_run"=>worker.fetch("last_run")}})
  abort("SLICE5_BLOCKER: stopped owner presence did not expire naturally") unless after.fetch("state")=="stale" &&
    after.fetch("source_kind")=="expiry_worker" && Time.iso8601(after.fetch("expires_at"))<=Time.now
  abort("SLICE5_BLOCKER: node surfaces do not report offline after the same owners realtime presence expired") unless
    [node,inspected,row].all? { |v| v.fetch("presence_state")=="offline" }
' "$TMP_ROOT/runtime-owner-stopped.json" "$TMP_ROOT/runtime-owner-presence-after.json" \
  "$TMP_ROOT/runtime-owner-health-after.json" "$TMP_ROOT/runtime-owner-inspect-after.json" \
  "$TMP_ROOT/runtime-owner-list-after.json" "$TMP_ROOT/runtime-expiry-worker.json" \
  "$TMP_ROOT/runtime-expiry-policy-before.json" "$TMP_ROOT/runtime-expiry-policy-after.json"
[[ "$OWNER_EXPIRED" == true ]] || exit 1
printf 'PASS: stopped-owner presence prerequisite; frozen source offline/retained-data acceptance remains separate.\n'
runtime_loom box init --path "$BOX_ROOT" --profile workspace >"$TMP_ROOT/runtime-box-init.json"

# The compiler accepts text/sync opt-in without raw backup. Do not enable
# backup merely to bypass a missing Box sync-to-knowledge admission route.
# These policies belong only to this synthetic Box, never Main.
ruby -ryaml -rfileutils -e '
  root=ARGV.fetch(0); contract_path=File.join(root,".loom/box.yaml")
  c=YAML.load_file(contract_path)
  c.fetch("areas").fetch("documents")["enabled"]=false
  document=File.join(root,c.fetch("policies").fetch("documents"))
  d=YAML.load_file(document); d["enabled"]=false; File.write(document,YAML.dump(d))
  %w[topics library].each do |area|
    c.fetch("areas")[area]={"path"=>area.capitalize,"enabled"=>true}
    FileUtils.mkdir_p(File.join(root,area.capitalize))
    c.fetch("policies")[area]=".loom/policies/#{area}.watch.yaml"
    p={"schema_version"=>"loom.box.watch_policy.v0.6", "area"=>area,
      "path"=>c.fetch("areas").fetch(area).fetch("path"),"mode"=>"watched_root","enabled"=>true,
      "sync"=>{"enabled"=>true},"backup"=>{"enabled"=>false},"text"=>{"enabled"=>true}}
    File.write(File.join(root,c["policies"][area]),YAML.dump(p))
  end
  notes=File.join(root,c.fetch("policies").fetch("notes"))
  p=YAML.load_file(notes); p["backup"]={"enabled"=>false}; File.write(notes,YAML.dump(p))
  File.write(contract_path,YAML.dump(c))
' "$BOX_ROOT"
runtime_loom box watch-plan --path "$BOX_ROOT" --profile workspace >"$TMP_ROOT/runtime-watch-plan.json"
runtime_loom box watch-apply --path "$BOX_ROOT" --profile workspace \
  --idempotency-key box-slice5-test-register >"$TMP_ROOT/runtime-watch-registration.json"
runtime_agent filesystem roots add --key loom_box --path "$BOX_ROOT" >"$TMP_ROOT/runtime-safe-root.json"
runtime_agent watched-roots apply-plan "$TMP_ROOT/runtime-watch-plan.json" >"$TMP_ROOT/runtime-watch-apply.json"

# Preserve the frozen source bytes and headings. Real watcher/upload owns IDs.
ruby -rjson -rfileutils -e '
  root,corpus=ARGV; c=JSON.parse(File.read(corpus))
  %w[note topic library-b].each do |key|
    s=c.fetch("sources").find { |x| x.fetch("key")==key } or abort("missing source #{key}")
    version_key=key=="note" ? "note-v1" : s.fetch("current_version")
    v=s.fetch("versions").find { |x| x.fetch("key")==version_key } or abort("missing version")
    path=File.join(root,{"note"=>"Notes","topic"=>"Topics","library-b"=>"Library"}.fetch(key),s.fetch("relative_path"))
    heading=v.fetch("passages").first.fetch("locator").fetch("heading")
    FileUtils.mkdir_p(File.dirname(path)); File.write(path,"# #{heading}\n\n#{v.fetch("fixture_text")}\n")
  end
' "$BOX_ROOT" "$ROOT/internal/knowledge/testdata/box_sources/corpus.json"

# An unrelated missing project must remain pending without stopping real Box
# uploads. Queue it through the supported fixture command, never a DB insert.
printf 'Unrelated disposable missing-project upload.\n' >"$RUNTIME_ROOT/orphan.md"
runtime_agent sync create-test-object --path "$RUNTIME_ROOT/orphan.md" \
  --project box-slice5-missing-project --index-policy none \
  >"$TMP_ROOT/runtime-orphan-created.json"
runtime_agent sync status >"$TMP_ROOT/runtime-sync-before.json"
NOTES_ROOT_KEY="$(jq -er '.data.applied[] | select(.root_relative_path=="Notes") | .root_key' "$TMP_ROOT/runtime-watch-apply.json")"
start_agent
printf 'Observing valid uploads beside the retained missing-project request (bound: 240 seconds).\n'
UPLOAD_READY=false
for ((n=0; n<120; n++)); do
  kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
  runtime_agent sync status >"$TMP_ROOT/runtime-sync.json"
  runtime_agent watched-roots status "$NOTES_ROOT_KEY" >"$TMP_ROOT/runtime-watch-status.json"
  if jq -e '.data.counts | .accepted==5 and .pending==1 and .failed==0 and .conflicted==0' "$TMP_ROOT/runtime-sync.json" >/dev/null &&
    jq -e '.data.roots | length==1 and .[0].health.status=="degraded"' "$TMP_ROOT/runtime-watch-status.json" >/dev/null; then
    UPLOAD_READY=true
    break
  fi
  sleep 2
done
if [[ "$UPLOAD_READY" != true ]]; then
  cat "$TMP_ROOT/runtime-sync.json" "$TMP_ROOT/runtime-watch-status.json" >&2
  printf 'SLICE5_BLOCKER: valid uploads or honest unrelated-upload degradation did not converge.\n' >&2
  exit 1
fi
assert_orphan_retained() {
  runtime_agent sync status >"$TMP_ROOT/runtime-sync.json"
  ruby -rjson -rdigest -e '
    created,status,root=ARGV
    original=JSON.parse(File.read(created)).fetch("data")
    current=JSON.parse(File.read(status)).fetch("data")
    paths=current.fetch("paths")
    %w[objects outbox].each do |key|
      abort("queue diagnostic escaped owned fixture") unless File.expand_path(paths.fetch(key)).start_with?(root+"/")
    end
    objects=JSON.parse(File.read(paths.fetch("objects")))
    outbox=JSON.parse(File.read(paths.fetch("outbox")))
    object=objects.find { |o| o.fetch("local_object_id")==original.fetch("local_object_id") } or abort("orphan object discarded")
    item=outbox.find { |o| o.fetch("local_ref")==original.fetch("local_object_id") } or abort("orphan outbox discarded")
    %w[local_object_id local_version_id project_ref hash_uri size_bytes source_path].each do |key|
      abort("orphan identity rewritten") unless object.fetch(key)==original.fetch(key)
    end
    abort("orphan no longer pending") unless object.fetch("sync_status")=="pending" && item.fetch("status")=="pending"
    abort("orphan not actually attempted") unless item["last_attempt_at"]
    abort("orphan falsely committed") if object["main_object_id"] || object["main_version_id"]
    abort("orphan source bytes changed") unless "sha256:"+Digest::SHA256.file(object.fetch("source_path")).hexdigest==original.fetch("hash_uri")
    puts JSON.generate({orphan_id:object.fetch("local_object_id"),outbox_id:item.fetch("local_outbox_id"),
      status:item.fetch("status"),last_attempt_at:item.fetch("last_attempt_at"),counts:current.fetch("counts")})
  ' "$TMP_ROOT/runtime-orphan-created.json" "$TMP_ROOT/runtime-sync.json" "$RUNTIME_ROOT"
}
assert_orphan_retained
printf 'PASS: five valid uploads accepted; unrelated missing-project request remains pending and degradation visible.\n'

# Check metadata propagation before waiting for the full native pipeline.
# Explain reads persisted scanner state; it does not reconcile or queue work.
runtime_agent watched-roots explain "$NOTES_ROOT_KEY" --path cadence.md >"$TMP_ROOT/runtime-path-before.json"
CORE_OBJECT="$(jq -er '.data.state.main_object_id' "$TMP_ROOT/runtime-path-before.json")"
runtime_loom object inspect "$CORE_OBJECT" >"$TMP_ROOT/runtime-core-before.json"
ruby -rjson -rtime -rdigest -e '
  path,before=ARGV; b=JSON.parse(File.read(before)).fetch("data")
  stat=File.lstat(path); abort("source is not a regular file") unless stat.file?
  t=Time.at(Time.now.to_r.floor(6)).utc
  abort("metadata edit did not advance time") unless t>stat.mtime
  File.utime(stat.atime,t,path)
  puts JSON.generate({path:path,source_hash:"sha256:"+Digest::SHA256.file(path).hexdigest,
    original_modified_at:stat.mtime.utc.iso8601(6),modified_at:File.mtime(path).utc.iso8601(6),
    object_id:b.fetch("object").fetch("object_id"),version_id:b.fetch("latest_version").fetch("object_version_id")})
' "$BOX_ROOT/Notes/cadence.md" "$TMP_ROOT/runtime-core-before.json" >"$TMP_ROOT/runtime-metadata-edit.json"
printf 'Observing metadata-only edit through two ordinary owner scans (bound: 180 seconds).\n'
METADATA_READY=false
printf '[]\n' >"$TMP_ROOT/runtime-metadata-scans.json"
for ((n=0; n<90; n++)); do
  kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
  runtime_agent watched-roots explain "$NOTES_ROOT_KEY" --path cadence.md >"$TMP_ROOT/runtime-path-after.json"
  runtime_agent watched-roots status "$NOTES_ROOT_KEY" >"$TMP_ROOT/runtime-watch-status.json"
  runtime_loom object inspect "$CORE_OBJECT" >"$TMP_ROOT/runtime-core-after.json"
  # Persist only the harness observations, not runtime state. Two completed
  # post-edit scans rule out a read while an ordinary upload is still pending.
  if ruby -rjson -rtime -e '
    edit,path,root,core,receipt=ARGV
    e=JSON.parse(File.read(edit)); p=JSON.parse(File.read(path)).fetch("data").fetch("state")
    r=JSON.parse(File.read(root)).fetch("data").fetch("roots").fetch(0)
    c=JSON.parse(File.read(core)).fetch("data"); cp=r.fetch("checkpoint",{})
    observations=File.exist?(receipt) ? JSON.parse(File.read(receipt)) : []
    changed=Time.iso8601(e.fetch("modified_at"))
    if Time.iso8601(p.fetch("modified_at"))==changed && cp["last_finished_at"] &&
      Time.iso8601(cp.fetch("last_started_at"))>changed &&
      Time.iso8601(cp.fetch("last_finished_at"))>=Time.iso8601(cp.fetch("last_started_at")) &&
      !observations.any? { |o| o.fetch("run_id")==cp.fetch("last_run_id") }
      observations << {"run_id"=>cp.fetch("last_run_id"),"finished_at"=>cp.fetch("last_finished_at"),
        "owner_modified_at"=>p.fetch("modified_at"),"owner_hash"=>p.fetch("content_hash_uri"),
        "main_modified_at"=>c.fetch("file").fetch("source_mtime"),"watch_health"=>r.fetch("health").fetch("status")}
      File.write(receipt,JSON.generate(observations))
    end
    exit(observations.length>=2 ? 0 : 1)
  ' "$TMP_ROOT/runtime-metadata-edit.json" "$TMP_ROOT/runtime-path-after.json" \
    "$TMP_ROOT/runtime-watch-status.json" "$TMP_ROOT/runtime-core-after.json" "$TMP_ROOT/runtime-metadata-scans.json"; then
    METADATA_READY=true
    break
  fi
  sleep 2
done
runtime_agent sync status >"$TMP_ROOT/runtime-metadata-sync.json"
ruby -rjson -rtime -e '
  edit,before,after,scans,path,sync=ARGV.map { |f| JSON.parse(File.read(f)) }
  b=before.fetch("data"); a=after.fetch("data")
  owner=path.fetch("data").fetch("state"); status=sync.fetch("data")
  outbox_path=status.fetch("paths").fetch("outbox")
  abort("outbox outside owned runtime") unless outbox_path.start_with?(File.dirname(edit.fetch("path")).sub(%r{/box/Notes$},"/agent/"))
  items=JSON.parse(File.read(outbox_path)).select { |i| i.fetch("item_kind")=="object_metadata" && i.fetch("payload_json").fetch("object_id")==edit.fetch("object_id") }
  puts JSON.pretty_generate({metadata_edit:edit,completed_scans:scans,
    main_object:a.fetch("object").fetch("object_id"),main_version:a.fetch("latest_version").fetch("object_version_id"),
    main_modified_at:a.fetch("file").fetch("source_mtime")})
  abort("SLICE5_BLOCKER: owner metadata was not observed by two completed ordinary scans") unless scans.length>=2
  abort("metadata edit changed owner content hash") unless scans.all? { |s| s.fetch("owner_hash")==edit.fetch("source_hash") }
  abort("metadata edit changed content identity") unless a.fetch("object").fetch("object_id")==edit.fetch("object_id") &&
    a.fetch("latest_version").fetch("object_version_id")==edit.fetch("version_id") &&
    a.fetch("latest_version").fetch("content_hash")==b.fetch("latest_version").fetch("content_hash")
  abort("SLICE5_BLOCKER: current object metadata did not follow the real owner metadata-only edit") unless
    Time.iso8601(a.fetch("file").fetch("source_mtime"))==Time.iso8601(edit.fetch("modified_at"))
  abort("SLICE5_BLOCKER: metadata acknowledgement did not converge on owner") unless items.length==1 &&
    %w[accepted duplicate].include?(items.first.fetch("status")) &&
    items.first.fetch("local_sequence")==owner.fetch("last_synced_metadata_sequence",0) &&
    Time.iso8601(owner.fetch("last_synced_modified_at"))==Time.iso8601(edit.fetch("modified_at")) &&
    owner.fetch("main_object_id")==edit.fetch("object_id") && owner.fetch("main_version_id")==edit.fetch("version_id")
  puts JSON.generate({metadata_outbox:items.first.fetch("local_outbox_id"),status:items.first.fetch("status"),ack_sequence:owner.fetch("last_synced_metadata_sequence")})
' "$TMP_ROOT/runtime-metadata-edit.json" "$TMP_ROOT/runtime-core-before.json" \
  "$TMP_ROOT/runtime-core-after.json" "$TMP_ROOT/runtime-metadata-scans.json" \
  "$TMP_ROOT/runtime-path-after.json" "$TMP_ROOT/runtime-metadata-sync.json"
[[ "$METADATA_READY" == true ]] || exit 1
printf 'PASS: real owner metadata edit retained content identity and updated current source metadata.\n'

printf 'Observing real owner upload and unattended Notes processing at the default cadence (bound: 900 seconds).\n'
RUNTIME_READY=false
ADMISSION_BLOCKED=false
for ((n=0; n<450; n++)); do
  kill -0 "$DAEMON_PID" 2>/dev/null && kill -0 "$AGENT_PID" 2>/dev/null || break
  runtime_loom notes objects list --node box-owner >"$TMP_ROOT/runtime-objects.json"
  if jq -e '[.[] | select(.relative_path=="cadence.md" or .relative_path=="atlas/ideas.md" or .relative_path=="cadence/source-b.md")] | length==3 and all(.[]; .processing_state=="indexed")' "$TMP_ROOT/runtime-objects.json" >/dev/null; then
    RUNTIME_READY=true
    break
  fi
  if ((n % 15 == 0)); then
    runtime_loom notes roots list --node box-owner >"$TMP_ROOT/runtime-roots.json"
    runtime_loom worker inspect main.knowledge_indexer >"$TMP_ROOT/runtime-worker.json"
    runtime_agent sync status >"$TMP_ROOT/runtime-sync.json"
    # Stop early only on two completed ordinary ticks after accepted upload,
    # with all roots active and no source admission at either tick.
    if ruby -rjson -rtime -e '
      roots,sync,worker,objects=ARGV.map { |p| JSON.parse(File.read(p)) }
      exit 1 unless roots.map { |r| r.fetch("root_kind") }.sort==%w[box_library box_notes box_topics]
      exit 1 unless roots.all? { |r| r.fetch("status")=="active" } && objects.empty?
      s=sync.fetch("data"); counts=s.fetch("counts")
      exit 1 unless counts.fetch("accepted")==6 && counts.fetch("pending")==1 && counts.fetch("failed")==0
      uploaded=s.fetch("cursors").map { |c| Time.iso8601(c["last_success_at"]) if c["last_success_at"] }.compact.max
      exit 1 unless uploaded
      runs=worker.fetch("data").fetch("recent_runs").select do |r|
        r.fetch("run_status")=="succeeded" && r.fetch("trigger_kind")=="supervisor_tick" &&
          Time.iso8601(r.fetch("started_at"))>uploaded &&
          r.fetch("counters_json").fetch("synced_observed")==0 &&
          r.fetch("counters_json").fetch("catalog_observed")==0
      end
      exit(runs.length>=2 ? 0 : 1)
      ' "$TMP_ROOT/runtime-roots.json" "$TMP_ROOT/runtime-sync.json" \
      "$TMP_ROOT/runtime-worker.json" "$TMP_ROOT/runtime-objects.json"; then
      ADMISSION_BLOCKED=true
      break
    fi
  fi
  sleep 2
done
runtime_loom notes roots list >"$TMP_ROOT/runtime-roots.json"
runtime_loom notes pipelines status >"$TMP_ROOT/runtime-pipelines.json"
runtime_loom worker inspect main.knowledge_indexer >"$TMP_ROOT/runtime-worker.json"
runtime_agent sync status >"$TMP_ROOT/runtime-sync.json"
if [[ "$RUNTIME_READY" != true ]]; then
  printf 'SLICE5_BLOCKER: unattended registered Box source pipeline did not converge.\n' >&2
  printf 'Post-upload zero-admission gate: %s\n' "$ADMISSION_BLOCKED" >&2
  jq '.' "$TMP_ROOT/runtime-objects.json" "$TMP_ROOT/runtime-roots.json" "$TMP_ROOT/runtime-sync.json" >&2
  jq '{ok,data:{health:.data.health,last_run:.data.last_run}}' "$TMP_ROOT/runtime-worker.json" >&2
  exit 1
fi
printf 'PASS: three real owner-uploaded source categories indexed without manual reconciliation.\n'
assert_orphan_retained
ruby -rjson -rtime -e '
  edit,objects=ARGV.map { |p| JSON.parse(File.read(p)) }
  note=objects.find { |o| o.fetch("relative_path")=="cadence.md" } or abort("note missing")
  abort("Notes did not admit current source mtime") unless Time.iso8601(note.fetch("source_modified_at"))==Time.iso8601(edit.fetch("modified_at"))
' "$TMP_ROOT/runtime-metadata-edit.json" "$TMP_ROOT/runtime-objects.json"

# Indexed is a lexical milestone, not pipeline finalization. All runtime
# observations below are read-only; only the fixture processes/lock are changed.
printf 'Waiting for unattended finalization and generated Notes (bound: 180 seconds).\n'
FINALIZED=false
for ((n=0; n<90; n++)); do
  kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
  runtime_loom notes pipelines status >"$TMP_ROOT/runtime-pipelines.json"
  runtime_loom notes projection status >"$TMP_ROOT/runtime-projection.json"
  runtime_loom worker inspect main.knowledge_indexer >"$TMP_ROOT/runtime-worker.json"
  if jq -e '.counts.complete==5 and (.current|length)==0' "$TMP_ROOT/runtime-pipelines.json" >/dev/null &&
    jq -e '.exists and .read_only and (.raw_writes_supported|not) and .counts.materialized==5 and ((.findings // [])|length)==0' "$TMP_ROOT/runtime-projection.json" >/dev/null &&
    jq -e '.data.last_run.run_status=="succeeded"' "$TMP_ROOT/runtime-worker.json" >/dev/null; then
    FINALIZED=true
    break
  fi
  sleep 2
done
if [[ "$FINALIZED" != true ]]; then
  printf 'SLICE5_BLOCKER: indexing did not converge to unattended finalization/projection.\n' >&2
  cat "$TMP_ROOT/runtime-pipelines.json" "$TMP_ROOT/runtime-projection.json" >&2
  jq '{data:{health:.data.health,last_run:.data.last_run}}' "$TMP_ROOT/runtime-worker.json" >&2
  exit 1
fi
PROJECTION_ROOT="$(jq -er '.projection_root' "$TMP_ROOT/runtime-projection.json")"
[[ "$PROJECTION_ROOT" == "$RUNTIME_ROOT/data/generated/notes" ]] || exit 1
ruby -rjson -rdigest -e '
  root,box,objects_path=ARGV
  objects=JSON.parse(File.read(objects_path))
  manifest=JSON.parse(File.read(File.join(root,".loom/manifest.json")))
  paths={"cadence.md"=>"Notes", "atlas/ideas.md"=>"Topics", "cadence/source-b.md"=>"Library"}
  paths.each do |rel,area|
    matches=objects.select { |o| o.fetch("relative_path")==rel }
    abort("source identity count mismatch") unless matches.length==1
    id=matches.first.fetch("knowledge_object_id")
    entries=manifest.fetch("entries").select { |e| e.fetch("knowledge_object_id")==id }
    abort("projection identity count mismatch") unless entries.length==1
    e=entries.first; path=File.join(root,e.fetch("projected_path"))
    abort("projection escaped owned root") unless File.expand_path(path).start_with?(root+"/")
    stat=File.lstat(path); source=File.join(box,area,rel)
    abort("projection type/mode/status mismatch") unless stat.file? && stat.mode & 0777 == 0444 && e.fetch("status")=="materialized"
    abort("projection bytes differ") unless File.binread(path)==File.binread(source)
    puts JSON.generate({source: "#{area}/#{rel}", object: id, bytes: stat.size,
      sha256: Digest::SHA256.file(path).hexdigest, projected_path: e.fetch("projected_path")})
  end
' "$PROJECTION_ROOT" "$BOX_ROOT" "$TMP_ROOT/runtime-objects.json"

snapshot_runtime() {
  local label="$1"
  # Exact smoke-owned DB only, SELECT-only durable identity/work accounting.
  "$PG_BIN/psql" -X -qAt -v ON_ERROR_STOP=1 -h "$TMP_ROOT/socket" -U postgres -d loom_box_runtime \
    -c "SELECT json_build_object(
      'runs', (SELECT json_agg(x ORDER BY knowledge_pipeline_run_id) FROM
        (SELECT knowledge_pipeline_run_id,knowledge_object_id,knowledge_object_version_id,generation,status,source_hash,completed_at FROM knowledge.pipeline_runs) x),
      'stages', (SELECT json_agg(x ORDER BY knowledge_pipeline_stage_run_id) FROM
        (SELECT knowledge_pipeline_stage_run_id,status,attempt_count,output_artifact_count,completed_at FROM knowledge.pipeline_stage_runs) x))" \
    >"$TMP_ROOT/$label-work.json"
  ruby -rjson -rdigest -rfind -e '
    root=ARGV.fetch(0); entries=[]
    Find.find(root) do |path|
      s=File.lstat(path); abort("non-regular projection entry") unless s.file? || s.directory?
      mode=s.mode & 0777; abort("projection not sealed") unless mode==(s.directory? ? 0555 : 0444)
      entries << {path: path.delete_prefix(root),type: s.ftype,device: s.dev,inode: s.ino,
        mode: mode,mtime: [s.mtime.to_i,s.mtime.nsec],size: s.file? ? s.size : nil,
        sha256: s.file? ? Digest::SHA256.file(path).hexdigest : nil}
    end
    puts JSON.generate(entries.sort_by { |e| e.fetch(:path) })
  ' "$PROJECTION_ROOT" >"$TMP_ROOT/$label-view.json"
}
wait_for_new_run() {
  local previous="$1" expected="$2" ready=false
  for ((n=0; n<90; n++)); do
    kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
    runtime_loom worker inspect main.knowledge_indexer >"$TMP_ROOT/runtime-worker.json"
    if jq -e --arg previous "$previous" --arg expected "$expected" \
      '.data.last_run | .worker_run_id!=$previous and .trigger_kind=="supervisor_tick" and .run_status==$expected' \
      "$TMP_ROOT/runtime-worker.json" >/dev/null; then ready=true; break; fi
    if jq -e --arg previous "$previous" --arg expected "$expected" \
      '.data.last_run | .worker_run_id!=$previous and (.run_status=="succeeded" or .run_status=="failed" or .run_status=="cancelled") and .run_status!=$expected' \
      "$TMP_ROOT/runtime-worker.json" >/dev/null; then break; fi
    sleep 2
  done
  if [[ "$ready" != true ]]; then
    printf 'SLICE5_BLOCKER: expected next ordinary worker result %s.\n' "$expected" >&2
    jq '{data:{health:.data.health,last_run:.data.last_run}}' "$TMP_ROOT/runtime-worker.json" >&2
    exit 1
  fi
  jq '{data:{health:.data.health,last_run:.data.last_run}}' "$TMP_ROOT/runtime-worker.json"
}
snapshot_runtime before-restart
printf 'PASS: all five pipelines finalized and three frozen generated copies match exactly.\n'
PREVIOUS_RUN="$(jq -er '.data.last_run.worker_run_id' "$TMP_ROOT/runtime-worker.json")"
stop_process "$DAEMON_PID"; DAEMON_PID=""
start_daemon
printf 'Observing the next ordinary tick after disposable daemon restart.\n'
wait_for_new_run "$PREVIOUS_RUN" succeeded
snapshot_runtime after-restart
cmp "$TMP_ROOT/before-restart-work.json" "$TMP_ROOT/after-restart-work.json"
cmp "$TMP_ROOT/before-restart-view.json" "$TMP_ROOT/after-restart-view.json"
printf 'PASS: restart preserved exact pipeline/stage attempts and generated inode/mode/mtime/bytes.\n'

# Contend only the existing directory lock. No source, database, generated
# file or product setting is modified to manufacture a projection failure.
PREVIOUS_RUN="$(jq -er '.data.last_run.worker_run_id' "$TMP_ROOT/runtime-worker.json")"
ruby -e '
  File.open(ARGV.fetch(0),File::RDONLY) do |f|
    abort("fixture could not claim projection lock") unless f.flock(File::LOCK_EX|File::LOCK_NB)
    File.write(ARGV.fetch(1),"ready\n")
    sleep 200
  end
' "$PROJECTION_ROOT" "$TMP_ROOT/projection-lock-ready" >"$TMP_ROOT/projection-lock.log" 2>&1 &
PROJECTION_LOCK_PID=$!
for ((n=0; n<50; n++)); do
  [[ -f "$TMP_ROOT/projection-lock-ready" ]] && break
  kill -0 "$PROJECTION_LOCK_PID" || break
  sleep 0.1
done
[[ -f "$TMP_ROOT/projection-lock-ready" ]] || { cat "$TMP_ROOT/projection-lock.log" >&2; exit 1; }
printf 'Observing a projection-lock refusal on one ordinary tick.\n'
wait_for_new_run "$PREVIOUS_RUN" failed
jq -e '.data.last_run.error_json | tostring | contains("Notes projection is busy")' "$TMP_ROOT/runtime-worker.json" >/dev/null
snapshot_runtime during-failure
cmp "$TMP_ROOT/before-restart-work.json" "$TMP_ROOT/during-failure-work.json"
cmp "$TMP_ROOT/before-restart-view.json" "$TMP_ROOT/during-failure-view.json"
PREVIOUS_RUN="$(jq -er '.data.last_run.worker_run_id' "$TMP_ROOT/runtime-worker.json")"
stop_process "$PROJECTION_LOCK_PID"; PROJECTION_LOCK_PID=""
printf 'Observing recovery after releasing the fixture lock, without a manual retry.\n'
wait_for_new_run "$PREVIOUS_RUN" succeeded
snapshot_runtime after-recovery
cmp "$TMP_ROOT/before-restart-work.json" "$TMP_ROOT/after-recovery-work.json"
cmp "$TMP_ROOT/before-restart-view.json" "$TMP_ROOT/after-recovery-view.json"
runtime_loom worker policy inspect main.knowledge_indexer >"$TMP_ROOT/runtime-tick-after.json"
runtime_loom notes pipelines status >"$TMP_ROOT/runtime-pipelines-after.json"
ruby -rjson -e '
  before,after,initial,final=ARGV.map { |p| JSON.parse(File.read(p)) }
  abort("ordinary cadence changed") unless before.fetch("data").fetch("policy_fingerprint")==after.fetch("data").fetch("policy_fingerprint")
  abort("heavy policy changed") unless initial.fetch("policy")==final.fetch("policy")
  abort("pipeline finality changed") unless final.fetch("counts")=={"complete"=>5} && final.fetch("current").empty?
' "$TMP_ROOT/runtime-tick-before.json" "$TMP_ROOT/runtime-tick-after.json" \
  "$TMP_ROOT/runtime-pipelines.json" "$TMP_ROOT/runtime-pipelines-after.json"
printf 'PASS: projection failure remained truthful; natural recovery preserved data, work and policies.\n'

# Replace one actual owner source with the exact frozen v2. No manual queue,
# admission, job dispatch or source-version insertion is used.
runtime_loom notes search '"daily snapshots"' --node box-owner --path cadence.md --mode lexical >"$TMP_ROOT/content-old-search.json"
jq -e '.results | length==1 and .[0].knowledge_chunk_id!=""' "$TMP_ROOT/content-old-search.json" >/dev/null
OLD_CHUNK="$(jq -er '.results[0].knowledge_chunk_id' "$TMP_ROOT/content-old-search.json")"
OLD_OBJECT="$(jq -er '.results[0].knowledge_object_id' "$TMP_ROOT/content-old-search.json")"
OLD_VERSION="$(jq -er '.results[0].knowledge_object_version_id' "$TMP_ROOT/content-old-search.json")"
OLD_HASH="$(jq -er --arg id "$OLD_OBJECT" '.[] | select(.knowledge_object_id==$id) | .source_hash' "$TMP_ROOT/runtime-objects.json")"
runtime_loom notes search '"source-to-search latency"' --node box-owner --path cadence.md --mode lexical >"$TMP_ROOT/content-before-new-search.json"
jq -e '.result_count==0 and (.results|length)==0' "$TMP_ROOT/content-before-new-search.json" >/dev/null
ruby -rjson -rtime -rdigest -e '
  root,corpus=ARGV; s=JSON.parse(File.read(corpus)).fetch("sources").find { |x| x.fetch("key")=="note" }
  v=s.fetch("versions").find { |x| x.fetch("key")=="note-v2" }
  path=File.join(root,"Notes",s.fetch("relative_path"))
  File.write(path,"# #{v.fetch("passages").first.fetch("locator").fetch("heading")}\n\n#{v.fetch("fixture_text")}\n")
  puts JSON.generate({edited_at:File.mtime(path).utc.iso8601(6),source_hash:"sha256:"+Digest::SHA256.file(path).hexdigest})
' "$BOX_ROOT" "$ROOT/internal/knowledge/testdata/box_sources/corpus.json" >"$TMP_ROOT/content-edit.json"
printf 'Observing frozen v1-to-v2 content replacement at the ordinary cadence (bound: 900 seconds).\n'
CONTENT_READY=false
for ((n=0; n<450; n++)); do
  kill -0 "$DAEMON_PID" && kill -0 "$AGENT_PID" || break
  runtime_loom notes search '"source-to-search latency"' --node box-owner --path cadence.md --mode lexical >"$TMP_ROOT/content-new-search.json"
  runtime_loom notes pipelines status >"$TMP_ROOT/content-pipelines.json"
  NEW_VERSION_READY=false
  if jq -e --arg object "$OLD_OBJECT" --arg old "$OLD_VERSION" \
    '.result_count==1 and (.results|length)==1 and (.results[0] | .knowledge_object_id==$object and .knowledge_object_version_id!=$old and (.knowledge_object_version_id|type=="string" and length>0) and (.knowledge_chunk_id|type=="string" and length>0) and (.metadata_only!=true))' \
    "$TMP_ROOT/content-new-search.json" >/dev/null; then NEW_VERSION_READY=true; fi
  if [[ ! -e "$TMP_ROOT/content-first-search.json" && "$NEW_VERSION_READY" == true ]]; then
    ruby -rjson -rtime -e 'puts JSON.generate({observed_at:Time.now.utc.iso8601(6)})' >"$TMP_ROOT/content-first-search.json"
  fi
  if [[ "$NEW_VERSION_READY" == true ]] &&
    jq -e '.counts=={"complete":5,"stale":1} and (.current|length)==0' "$TMP_ROOT/content-pipelines.json" >/dev/null; then
    CONTENT_READY=true; break
  fi
  sleep 2
done
[[ "$CONTENT_READY" == true ]] || { cat "$TMP_ROOT/content-new-search.json" "$TMP_ROOT/content-pipelines.json" >&2; exit 1; }
ruby -rjson -rtime -e '
  edit,found=ARGV.map { |p| JSON.parse(File.read(p)) }
  elapsed=Time.iso8601(found.fetch("observed_at"))-Time.iso8601(edit.fetch("edited_at"))
  abort("invalid source-to-search observation") unless elapsed>=0 && elapsed<900
  puts JSON.generate({source_edit:edit.fetch("edited_at"),first_search_observed:found.fetch("observed_at"),
    source_to_search_seconds:elapsed,observation_poll_seconds:2,includes_ordinary_queue_waits:true})
' "$TMP_ROOT/content-edit.json" "$TMP_ROOT/content-first-search.json"
runtime_loom notes search '"daily snapshots"' --node box-owner --path cadence.md --mode lexical >"$TMP_ROOT/content-obsolete-search.json"
if ! jq -e '.result_count==0 and (.results|length)==0' "$TMP_ROOT/content-obsolete-search.json" >/dev/null; then
  cat "$TMP_ROOT/content-obsolete-search.json" >&2
  printf 'FAIL: obsolete exact source phrase remained in current search.\n' >&2
  exit 1
fi
# Bind exact historical retrieval to the old search result and retained source
# version; the hash comes from the prior current Notes object, never latest.
runtime_loom notes passage get "$OLD_CHUNK" --object "$OLD_OBJECT" --version "$OLD_VERSION" --source-hash "$OLD_HASH" >"$TMP_ROOT/content-historical.json"
NEW_CHUNK="$(jq -er '.results[0].knowledge_chunk_id' "$TMP_ROOT/content-new-search.json")"
NEW_VERSION="$(jq -er '.results[0].knowledge_object_version_id' "$TMP_ROOT/content-new-search.json")"
NEW_HASH="$(jq -er '.source_hash' "$TMP_ROOT/content-edit.json")"
runtime_loom notes passage get "$NEW_CHUNK" --object "$OLD_OBJECT" --version "$NEW_VERSION" --source-hash "$NEW_HASH" >"$TMP_ROOT/content-current.json"
ruby -rjson -e '
  old,current,historical,passage=ARGV.map { |p| JSON.parse(File.read(p)) }
  a=old.fetch("results").first; b=current.fetch("results").first
  abort("content edit changed object identity") unless a.fetch("knowledge_object_id")==b.fetch("knowledge_object_id")
  abort("content edit reused old version") if a.fetch("knowledge_object_version_id")==b.fetch("knowledge_object_version_id")
  abort("historical citation lost") unless historical.fetch("historical") && historical.fetch("knowledge_chunk_id")==a.fetch("knowledge_chunk_id") && historical.fetch("text").include?("daily snapshots at 03:15")
  abort("current citation mismatch") unless !passage.fetch("historical") && passage.fetch("knowledge_chunk_id")==b.fetch("knowledge_chunk_id") && passage.fetch("text").include?("source-to-search latency")
  puts JSON.generate({content_object:a.fetch("knowledge_object_id"),old_version:a.fetch("knowledge_object_version_id"),new_version:b.fetch("knowledge_object_version_id"),historical:true})
' "$TMP_ROOT/content-old-search.json" "$TMP_ROOT/content-new-search.json" "$TMP_ROOT/content-historical.json" "$TMP_ROOT/content-current.json"
assert_orphan_retained
printf 'PASS: ordinary source replacement produced one new version; old content left current search but exact historical citation survived.\n'
stop_process "$AGENT_PID"; AGENT_PID=""
stop_process "$DAEMON_PID"; DAEMON_PID=""
"$PG_BIN/dropdb" -h "$TMP_ROOT/socket" -U postgres loom_box_runtime
"$PG_BIN/dropdb" -h "$TMP_ROOT/socket" -U postgres loom_provenance
"$PG_BIN/createdb" -h "$TMP_ROOT/socket" -U postgres -O loom_provenance loom_provenance

env HOME="$TMP_ROOT/home" XDG_CONFIG_HOME="$TMP_ROOT/config" \
  GOCACHE="$GO_CACHE" GOMODCACHE="$GO_MOD_CACHE" \
  LOOM_TEST_DB_URL="postgresql://postgres@/postgres?host=$TMP_ROOT/socket" \
  LOOM_BOX_SURFACE_HELPER="$TMP_ROOT/knowledge-http.test" LOOM_BOX_SURFACE_CLI="$TMP_ROOT/loom" \
  go test -count=1 -v -run '^TestBox(SourceVersionSearchObservation|SourcesNativeRetrieval)Postgres$' ./internal/knowledge
env HOME="$TMP_ROOT/home" XDG_CONFIG_HOME="$TMP_ROOT/config" \
  GOCACHE="$GO_CACHE" GOMODCACHE="$GO_MOD_CACHE" \
  LOOM_BOX_CITATION_FIXTURE_ROOT="$TMP_ROOT" \
  LOOM_BOX_CITATION_MAIN_URL="postgresql://postgres@/loom_box_citation?host=$TMP_ROOT/socket" \
  LOOM_BOX_CITATION_PROVENANCE_URL="postgresql://loom_provenance@/loom_provenance?host=$TMP_ROOT/socket" \
  go test -count=1 -v -run '^TestBoxSourceCitationHistoricalExactGetPostgres$' ./internal/provenance

go test -count=1 -run 'BoxSourceRoutingAndCitationProtocol|NotesPassage' \
  ./internal/agentpack ./internal/knowledge ./internal/httpapi ./internal/localclient ./internal/loomcli
"$TMP_ROOT/loom" agent pack validate --pack-dir ai-loom-pack
"$TMP_ROOT/loom" --json docs --docs-dir docs status
printf 'PASS: Slice 4 native retrieval, historical citations, privacy, reviewed semantics and routing.\n'
printf 'PASS: unattended capture/finalization/projection, restart and projection-failure recovery.\n'
printf 'Frozen offline-source acceptance: run this same script with --offline-owner-only. Archive lifecycle remains a separate dependency.\n'
