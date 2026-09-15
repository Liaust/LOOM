# Focused G2a acceptance

> Historical experimental harness, not a contributor prerequisite or the current
> development policy. The referenced private planning records are not part of
> this public baseline. Prefer focused package checks and direct use of the
> system; do not reconstruct an agent environment to make an ordinary patch.

This fixture checks the source behavior selected in the committed
`.project/features/developer-experience-acceptance/g2a_cold_agent_plan.md`.
It is a prerequisite for four cold-agent observations, not a claim that a Go
assertion is a model trial or that G1–G4 are complete.

## Frozen corpus

`cold_cases.json` transcribes the preserved proposal's exact A–D prompts and
command/tool/token/time limits. It also freezes raw source bytes and hashes,
independent outcomes, full entry files and optional references. The proposal's
SHA-256 is `9da97c2932f969bd7105539b9254ca5a0de8d16e340e0d5c1162790e93c506d5`.
The source-manifest SHA-256 is
`899c0fb06370bca9d489b913aa1931c11383fa2cdda13d43cc9c7e657eb6aadb`.
Historical planning pins remain historical; current entry/reference bytes must
match their F4a pins. Only fixture paths and labels may replace prompt tokens.

| Case | Real prerequisite | Frozen participant outcome |
|---|---|---|
| A | Ordinary Python file, existing plain-word test, neutral AGENTS | Correct slug fix; zero LOOM, Git, enrollment, planning or docs-search attempts |
| B | Local CLI create; real ProjectsOwner and WatchOwner | Zero enrollment after phase 1; one reviewed declaration apply with truthful readiness |
| C | Three raw sources, real admission/extraction, physical archive owner and Notes projector | One query; whole-tuple historical V1 and archived reads; honest current-access refusal |
| D | Real lifecycle registration, evidence link, acceptance and supersession; stock policy and HTTP surface | Current R2 and pending alternative; stored receipt qualification, supplemental source, truncation and unavailable-item disclosure |

The `main` label in B is only the disposable in-process owner. Its actual watch
reconciliation can report `owner_pending`. That is not indexing, protection,
node-agent credential delivery, or production readiness.

For C, the HTTP helper logs the original query response before an evaluator-only
barrier advances A to V2 and makes C's current source path inadmissible. It then
returns that unmodified query response. Actual exact gets must retain V1 text.
The barrier has its own database hashes; both stable participant read phases
must leave all database tables unchanged. This is an observation mutation,
not an ordinary rename test. Only authoritative observations and lifecycle
writers seed data; derived Notes chunks and API text are never fabricated.

D assigns stable ascending UUIDs to 18 R2 sources. Source zero is supplemental,
evidence-only; source one has a locator over the shipped 16 KiB item cap. A
16-item exact get must report both set truncation and one unavailable item.
The current-source locator points to an owned counting trap. It must receive
zero requests. The ledger must remain unchanged during retrieval. Private
markers and embedded instructions are source data, never participant authority.

## Run and evidence

From the repository root:

```sh
bash tests/smoke/v2_project_developer_experience_local.sh --fixture-only
bash tests/smoke/v2_project_developer_experience_local.sh --fixture-only --case C
bash tests/smoke/v2_project_developer_experience_local.sh --fixture-only --gates
python3 -m unittest discover -s tests/acceptance/project_dx -p test_harness.py -v
```

The runner builds one source CLI and an HTTP helper test binary. It roots the
existing PostgreSQL/pgvector runtime, creates one short owned `/tmp/loom-dx-*`
cluster with no TCP listener, and creates distinct disposable databases and
participant roots. `--pg-bin` selects an existing runtime; no dependency is
installed. Temporary Go/test files also belong under the owned root. Test
helpers fail closed on mismatched roots/DSNs. They are skipped outside the
smoke; a required fixture skip inside it is a failure.

Every invocation creates a fresh `.loom-acceptance/g2a-<timestamp>` directory.
It preserves the fixture sources, corpus/entry/binary hashes, exact command
arguments, stdout/stderr, exit codes, elapsed time, public API responses and
correlation IDs, barriers, independent oracle and before/after snapshots.
Failures remain in their original run directory. The harness stops owned
process groups and removes only its owned cluster/socket/root. Database inventory
is best-effort and cannot skip PostgreSQL shutdown. If shutdown cannot be confirmed,
the data and runtime GC root remain in place and cleanup.json reports their paths.
Primary fixture errors and cleanup errors are both retained. The deterministic
Python cleanup tests inject inventory/start/stop failures without launching a DB.

`--gates` adds focused tests for the three owners, the new required selectors
under the race detector, and vet. Existing unrelated gated tests can report
skips in the focused package run; their counts are distinct from the required
G2a fixtures, which must pass with zero skips. The integrator owns the full
combined Go/Nix matrix.

## Native boundary

Commit the tested fixture/corpus before running:

```sh
bash tests/smoke/v2_project_developer_experience_local.sh --native-preflight
```

This runs the prerequisites again and an existing-auth native ephemeral CLI
transport/logging probe. No login, copied authentication, user configuration
change, installed skill, sidebar task or model override is requested. Preserve
raw native events. A fixed shim constrains the fixture endpoint and captures
argv/results; it does not prove absence of arbitrary shell bypass.

The preflight deliberately does not launch A–D when exact instruction/tool
inventory, ephemeral behavior, socket reachability, or complete event capture
cannot be established. The native `exec` interface is one-shot, while B requires
two separately authorized phases in the same ephemeral participant without
resume/fork. Record an actual native limitation and stop for integrator review;
do not silently replace the participant with a subagent, persisted task, or
self-reported tool counts. No transcript becomes coaching.

The full 1,177-word/8,634-byte coding entry and 726-word/5,270-byte retrieval
entry remain unclipped. Optional references are on-demand only. Without the
resolved model's verified tokenizer, the approximate 1,000 control-token target
is unmeasured. Provider input/output and cached-input usage are separate from
entry size; cached input is a subset, not an additional token charge. Missing
usage or unavailable hard enforcement must not be recorded as zero/passed.

Remaining: wider G1/G2 fixtures, applications/refresh, production and installed
MINA/ORCA discovery, publication/version lifecycle and G4 operator acceptance.

## G2b native qualification checkpoint

`--native-qualify` runs fixture prerequisites and model-free native app-server
qualification. Its strict source gate remains blocked by global AGENTS discovery,
including with project_doc_max_bytes=0. The bounded `--context-deviated` option
permits only the integrator-pinned global source path/hash for a separately
qualified correctness probe; it does not qualify strict cold-context/control
acceptance. Original fixture instructions are injected once through the schema's
developerInstructions field and separately hashed/counted.

Qualification tests actual readonly, writable and no-bridge policies serially
using owned Unix/TCP listeners and synthetic file probes. Both actual readonly
attempts stopped on three filesystem failures before subsequent profiles, public
CLI/API bridge transport or any models. Adding explicit tmp-selector denials did
not fix them. Do not run further native experiments under this checkpoint;
integrator review is required. No A–D model/phase/budget runner is implemented.
Exact requested/effective settings and the command response's lack of resolved
sandbox metadata are retained in the final feature handoff and
`.loom-acceptance/g2b-filesystem-blocker/receipt.json`.

The standard-library RPC adapter handles interleaved IDs, framing, approvals,
EOF/deadlines and cumulative usage. Config/integration evidence is sanitized;
missing usage is not zero. B's guarded final control consumes actual public
responses after phase1 without replaying writes. Unconfirmed native/fixture owner
teardown preserves the owned root and runtime GC root. The final 27 offline
tests include failed owner receipts/stop files/waits, nonzero owner exit, exact
source-path matching and canary false-positive guards. No model is required.
Both actual runs also passed PG fixtures 3/0/0 and confirmed complete cleanup.
*Written by LOOM's Codex*
