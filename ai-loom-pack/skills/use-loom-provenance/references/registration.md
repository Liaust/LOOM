# Register A Pending Candidate On Main

Use `loom provenance candidate register` for capture. It uses the existing
authorized `POST /v1/provenance/candidates` local API and its idempotency contract,
not a second semantic writer or direct database access.

## Prepare Once

Reuse a known exact record or check relevant records/candidates for a duplicate
when necessary. Capture one
atomic claim with its scope, source and time. Use `decision`, `preference`,
`constraint`, `correction`, `outcome` or another accurate record kind. Keep
agent interpretation distinguishable from the user's actual statement.

Prepare a small JSON file using this shape, replacing all example content with
real task evidence. This example deliberately claims no resolver verification:

```json
{
  "candidates": [{
    "schema_version": "1.0",
    "claim": "The source states the preference being recorded.",
    "record_kind": "preference",
    "record_context": "Who this applies to, when, and why it matters again.",
    "domain": "user-preferences",
    "visibility": "private",
    "assertion_posture": "source_claim",
    "temporal_interpretation": {"interpretation": "Stated in the cited conversation; duration is not established."},
    "producer": {"producer_id": "mina", "producer_kind": "working_agent", "task_id": "actual-task-reference"},
    "sources": [{
      "kind": "conversation",
      "status": "unresolved",
      "verification_posture": "unverified",
      "resolver_name": "agent-submitted-evidence",
      "resolver_version": "1",
      "gap_reason": "Visible statement retained, but no independent source resolver was run.",
      "submitted": {"conversation_ref": "actual-conversation-reference", "excerpt": "Short exact visible statement."}
    }]
  }]
}
```

The coding agent uses its own producer ID/task reference, not `mina` or the
user's identity. The API authenticates the caller separately; producer labels
do not grant authority. Use only real identifiers. For project-specific meaning,
add `anchors.projects: ["<actual-project-id>"]` from `loom project inspect`.
Omit project anchors for a truly user-wide preference. Record ambiguity rather
than guessing project membership or extending a local preference globally.

Do not include credentials, private session dumps or another agent's memory.
For current conversation evidence, use only visible text and available message
references; do not crawl harness history to invent an exact reference. If no
stable message reference is exposed, say so in `gap_reason` and retain the actual
task context plus short excerpt in `submitted`.

The API stores submitted source receipts; it does not run a source resolver for
you. An unresolved source must not set `canonical_locator` or `content_digest`.
When an actual source read verifies bytes, use `status: resolved`, truthful
`resolver_name`/`resolver_version`, `canonical_locator`, `version_address` and
`content_digest`, `verification_posture: content_verified`, and no `gap_reason`.
Distinguish your read from an independent resolver. For Notes, preserve the exact
passage IDs/hash described in [reconciliation](reconciliation.md), not a search
snippet. A verified source still does not make its claim accepted truth.

## Submit And Verify

On Main, use the current agent account and configured LOOM backend. Keep request
files in the current task's private scratch area. Never use sudo, switch
identity or change ledger to make a refused request succeed.

```sh
loom provenance candidate register --file candidate.json \
  --idempotency-key <one-stable-key-for-this-exact-registration> --yes --json
```

Require command success AND `ok: true`. Inspect
`data.receipt.receipts[].candidate_id`, `effective_state`, `replayed` and
`source_results`. Verify the returned candidate, optionally its stored sources:

```sh
loom --json provenance candidate get <candidate-id> --sources
loom --json provenance search <distinctive-claim-terms> --include-pending
```

The routine get shows meaning and effective lifecycle without repeating the
submission payload. Use `--details --json` if the task needs that full stored
representation; registration receipts themselves keep their existing shape.
Source expansion reads stored evidence, not the live source. Preserve its
separate incomplete/truncated status rather than treating omitted detail as
verified or absent.

Keep the returned ID and honest pending/unverified posture in the normal task
handoff. There is no additional Markdown semantic ledger to maintain. A replay
may report a later effective lifecycle state: do not assume it is still pending.
Reuse the same key AND unchanged request/producer for a bounded retry after an
uncertain response. Changed content needs a new deliberate registration, not a
loop of fresh keys. A denied/unavailable response is not permission to alter
policy, choose another ledger or launch a worker; report its exact error.
