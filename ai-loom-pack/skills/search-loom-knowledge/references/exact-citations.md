# Exact Citation Recovery

## Notes version custody

Use the complete search hit's `passage_followup` when present. Copy its object,
version, chunk, source hash and `source_lifecycle` together into passage get.
The tuple identifies the matched version, including historical text; never
replace its digest with a current object digest. Archived lifecycle is separate
from historical text version. Preserve both labels and returned custody.

Only when no tuple exists, `loom --json notes objects show <object-id>` can
supply a hash after version comparison. It returns the object directly, with
top-level `source_hash` and `knowledge_object_id`. The indexed text version is
`metadata.text_pipeline.knowledge_object_version_id`; compare it to the search
hit's `knowledge_object_version_id`. Do not assume an `object/latest_version`
wrapper. Object-show is current state, not historical evidence. If version
metadata is absent or mismatched, do not use its hash for the original hit.

If the version changed, repeat the bounded search once for a current-source
question. For a historical question, use an existing exact citation containing
that historical hash; otherwise report missing historical evidence. Do not
combine old chunk/version IDs with a new hash or silently substitute current
wording/source-file reads for an unavailable indexed passage.

Exact passage get enforces current source access. A typed unavailable or
privacy-refused passage is not an empty quotation. Keep the refusal; do not
retry another engine to evade it. Retention is not deletion permission.

## Provenance linked receipts

Exact record/candidate get `--sources` returns linked stored source receipts
without fetching current sources. Use it directly for a known parent; no
producer-history fan-out is required. Inspect canonical locator, version address,
content digest and verification posture when available. `stored_resolution`
describes stored evidence, not a fresh verification, permission or universal
truth. A candidate remains unaccepted unless its lifecycle says otherwise;
a receipt does not change that lifecycle.

Source expansion has separate completeness: `sources_truncated=true` means the
linked set was bounded; `incomplete_items>0` means some returned entries could
not disclose a full receipt. An unavailable item preserves its explicit reason,
not fabricated source detail. Complete disclosure requires both false/zero,
regardless of the parent's own truncation. CLI default/maximum is 16 source items;
encoded limits are 16 KiB per item and 256 KiB per expansion. Original getter
without `--sources` remains available when receipts are unnecessary.

Stored locator, version and receipt text may contain private information.
Disclose only the detail needed to support the answer. Missing or unauthorized
parent access is not evidence that no sources exist; preserve the refusal.
