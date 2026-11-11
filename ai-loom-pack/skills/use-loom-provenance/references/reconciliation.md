# Reconciliation Reference

## Review Inputs

Use effective review work or a relevant known candidate/case. Before a lifecycle
write, read the exact candidate and related state needed by that operation.
Use `--details --json` for the full lifecycle representation; a routine view or
search preview is not the complete mutation input. Add `--sources` when stored
receipts are needed, preserving their separate completeness flags.
Check verification posture, actor and producer distinction, entity and project
scope, time, ambiguity, lineage, relationship hints, source gaps, truncation,
accepted competitors, and unresolved cases.

Agreement or repetition is not acceptance. Agent interpretations, inferred
preferences, ambiguous scope, incomplete sources, and competing evidence must
remain pending or unresolved unless a supported reviewed operation establishes
another outcome.

## Box Source Citations

Indexing a file does not register or accept a Provenance assertion. Register
only a deliberately selected, source-grounded candidate through the supported
candidate surface. A draft, attributed claim, or agent paraphrase is not a
confirmed user decision. Keep conflicting proposals pending or in a reviewed
resolution case until an authorized lifecycle operation resolves them.

For Notes evidence, preserve `knowledge_object_id`, `knowledge_object_version_id`,
`knowledge_chunk_id`, `source_hash`, heading/page `passage_locator`, source
category/posture, and owner/project context in the source submitted/details
payload. Pin the exact Notes passage URL as `canonical_locator`, the knowledge
version as `version_address`, and the source hash as `content_digest`. A copied
locator is not verification; report only the verification actually performed.

Source edits neither rewrite that citation nor supersede an accepted record.
Follow the original passage through the version-bound Notes read. Current
privacy, removal or unavailable retention can block it: report that gap while
preserving the original identity. Do not substitute a current passage or delete
semantic history. Record source context at citation time separately from the
passage response's current source context. Historical archived-workspace reads
remain dependent on Notes archive lifecycle support, not inferred path prefixes.

## Shared Capture Is Not Worker Control

Use [registration](registration.md) for ordinary pending capture. When the task
depends on a candidate, inspect its exact evidence and related records now.
Keep unresolved conflicts explicit; do not turn agreement into acceptance.

Acceptance, supersession and other lifecycle changes use the separately
authorized `POST /v1/provenance/operations` surface with reviewed operation
input. They are not a required follow-up to every registration. Never call
database or lifecycle-store internals. After an authorized operation, exact-get
the candidate and resulting records, relationships or cases.

The equivalent CLI is `loom provenance operations apply --file review.json
--idempotency-key <stable-batch-key> --yes --json`. Save the exact request and
operation UUIDs for retries. For an explicitly intended one-candidate acceptance,
`loom provenance review prepare <candidate-id> --producer-file reviewer.json
--out review.json` builds the saved batch from exact current receipts, preserving
original capture and your actual reviewer context. The producer file contains
your actual `producer_id`, `producer_kind` and `task_id` (and `run_id` if known).
Preparation performs no acceptance or source fetch; review the file before the
returned explicit apply command. Optional `--supersedes <accepted-record-id>`
requires an explicit `--relationship-context`; no relationship is inferred.
Use `loom provenance relationship get <relationship-id>` for its exact direction,
evidence and effective state; `--details` exposes the full bounded lifecycle.
`loom provenance review list --limit 10 --json`
selects bounded effective review work; completed states are not pending merely
because their immutable initial state says so. Respect pagination/truncation and
deferrals. Unchanged human-blocked cases should not be repeatedly re-reviewed;
new durable evidence, clarification or an explicit due time can make work useful.
Exact-get when the interpretation needs missing evidence, and always obtain the
state required for a mutation; the list is not acceptance or an authorization
grant. `loom provenance candidate list --limit 10 --json`
pages all candidate lifecycle states using `--after-time` and `--after-id` from
the preceding page; exact-get and skip already resolved items. These surfaces
do not themselves grant reconciliation authority to an ordinary capture agent.

Ordinary agents do not run the deterministic Archivist as part of capture.
Its bounded manual controls stay in the Archivist workspace's
`protocols/CANDIDATE-REVIEW.md`; installing this shared skill grants none of them.

## Stop Conditions

Stop with an investigation or handoff when identity, scope, source posture,
currentness, intended outcome, capability authority, or idempotency is unclear.
Never add a schedule, policy cadence, automation, event trigger, startup run, or
polling loop. Basecamp projection is deferred and is not part of this skill.
