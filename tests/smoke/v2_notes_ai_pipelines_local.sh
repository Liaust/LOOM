#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/loom-notes-ai-pipelines.XXXXXX")"
ACCEPTANCE_ROOT="$TMP_ROOT/.loom-acceptance/v2-notes-ai-pipelines"
PG_CONTAINER=""
cleanup() {
  if [[ -n "$PG_CONTAINER" ]]; then
    docker stop "$PG_CONTAINER" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

mkdir -p "$ACCEPTANCE_ROOT"
printf '# markdown fixture\n' >"$ACCEPTANCE_ROOT/note.md"
printf 'text fixture\n' >"$ACCEPTANCE_ROOT/note.txt"
printf '{"fixture":true}\n' >"$ACCEPTANCE_ROOT/data.json"
printf 'fake-pdf-contract-fixture\n' >"$ACCEPTANCE_ROOT/document.pdf"
printf 'fake-image-contract-fixture\n' >"$ACCEPTANCE_ROOT/image.png"

cd "$ROOT"
go test ./internal/knowledge -run 'PipelineBackfillPlanIncludesEveryFileClass|PipelineDefinition|PipelinePlan' -count=1
go test ./internal/knowledge -run 'TesseractOCRNormalizesFakeOutput|OllamaVisionRuntimeNormalizesDescription|OllamaEmbedRequestShapeAndResponseParsing' -count=1
go test ./internal/knowledge -run 'PipelineStageEmbedding|PDF|OCR|Vision|Consolidat' -count=1
go test ./internal/workers -run Resource -count=1
go test ./internal/workers/runtimes -run 'KnowledgeHeavy|KnowledgeEmbedder' -count=1

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  PG_CONTAINER="loom-notes-pipelines-${RANDOM}-$$"
  docker run --rm -d --name "$PG_CONTAINER" \
    -e POSTGRES_PASSWORD=loom -e POSTGRES_DB=loom_test \
    -p 127.0.0.1::5432 pgvector/pgvector:pg16 >/dev/null
  for _ in $(seq 1 30); do
    if docker exec "$PG_CONTAINER" pg_isready -U postgres -d loom_test >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  PG_PORT="$(docker port "$PG_CONTAINER" 5432/tcp | sed 's/.*://')"
  TEST_DB_URL="postgres://postgres:loom@127.0.0.1:${PG_PORT}/loom_test?sslmode=disable"
  LOOM_BOOTSTRAP_TEST_DB_URL="$TEST_DB_URL" go test ./internal/bootstrap -run '^TestEnsureProductionBootstrapIntegration$' -count=1
  LOOM_TEST_DB_URL="$TEST_DB_URL" go test ./internal/knowledge -run 'Postgres$' -count=1
else
  printf '[skip] Docker unavailable; Postgres transition regressions were not run\n'
fi

go run ./cmd/loom notes pipelines --help >"$ACCEPTANCE_ROOT/pipelines-help.txt"
go run ./cmd/loom notes pipelines backfill --help >"$ACCEPTANCE_ROOT/backfill-help.txt"
grep -Fq 'backfill' "$ACCEPTANCE_ROOT/pipelines-help.txt"
grep -Fq -- '--apply' "$ACCEPTANCE_ROOT/backfill-help.txt"
grep -Fq -- '--yes' "$ACCEPTANCE_ROOT/backfill-help.txt"

printf '[ok] unified Notes plans, fake heavy runtimes, resource admission, and CLI contracts passed\n'
