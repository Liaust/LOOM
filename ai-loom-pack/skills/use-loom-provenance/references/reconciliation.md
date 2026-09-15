# Reconciliation Reference

## Review Inputs

Begin with a deliberately included pending result, then exact-get the candidate.
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

Ordinary agents do not run the deterministic Archivist as part of capture.
Its bounded manual controls stay in the Archivist workspace's
`protocols/CANDIDATE-REVIEW.md`; installing this shared skill grants none of them.

## Stop Conditions

Stop with an investigation or handoff when identity, scope, source posture,
currentness, intended outcome, capability authority, or idempotency is unclear.
Never add a schedule, policy cadence, automation, event trigger, startup run, or
polling loop. Basecamp projection is deferred and is not part of this skill.
