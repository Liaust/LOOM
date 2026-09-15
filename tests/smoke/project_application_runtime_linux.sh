#!/usr/bin/env bash
set -euo pipefail
# This executes an already-provisioned, integrator-owned fixture. It never
# installs a container/VM/Nix, creates host units or selects a production host.
fixture=${1:?exact integrator-owned /run/loom-application-acceptance-* fixture required}
case "$fixture" in /run/loom-application-acceptance-*) ;; *) printf 'Refusing unscoped fixture root\n' >&2; exit 1 ;; esac
[ "$(uname -s)" = Linux ]
[ "$(id -u)" = 0 ]
[ -f "$fixture/runtime-fixture.json" ]
[ -x "$fixture/serviceregistry.test" ]
# A missing actual runtime is an error, never a mock or cross-build pass.
LOOM_APPLICATION_LINUX_FIXTURE="$fixture" "$fixture/serviceregistry.test" \
  -test.v -test.count=1 -test.run '^TestApplicationLinuxRenderedUnitsFixture$' | tee "$fixture/runtime-gate.log"
if rg -q '^--- SKIP:|warning: no tests to run' "$fixture/runtime-gate.log"; then exit 1; fi
rg -q '^--- PASS: TestApplicationLinuxRenderedUnitsFixture ' "$fixture/runtime-gate.log"
