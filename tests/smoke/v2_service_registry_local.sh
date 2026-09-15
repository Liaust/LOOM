#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v2-service-registry.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

cd "$ROOT"
go run ./cmd/loom project scaffold "Service Registry Smoke" \
  --slug service-registry-smoke --owner-node macbook --facets services \
  --directory "$TMP_DIR" >"$TMP_DIR/scaffold.out"

PROJECT_ROOT="$TMP_DIR/service-registry-smoke"
test -f "$PROJECT_ROOT/.loom/contracts/services/example.yaml"
go run ./cmd/loom project validate "$PROJECT_ROOT" >"$TMP_DIR/validate.out"
grep -Fq 'Project contract: ok' "$TMP_DIR/validate.out"
go run ./cmd/loom project plan "$PROJECT_ROOT" --json >"$TMP_DIR/plan.json"
grep -Fq 'would_register_service_provider' "$TMP_DIR/plan.json"
grep -Fq 'external' "$TMP_DIR/plan.json"

go test ./internal/serviceregistry -run 'TestRegisterProject|TestReconcileProject' -count=1
printf '[ok] project-local service registration is bounded and provisioning stays external\n'
