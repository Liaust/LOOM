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

## Workspace Navigation And Freshness

`loom notes passage locate` accepts the exact same citation tuple as passage get
and resolves it to the currently enrolled workspace binding. Historical wording
can remain readable while its former file is not a current editable target.
Preserve the returned status; do not fabricate a path for a stale or archived hit.

These are different observations:

- Obsidian local save/local search works on device files, including offline edits.
- Native publication/upload does not prove that Main applied an edit. Wait for
  the matching source acknowledgement; a conflict/hold retains its own bytes.
- `binding_recorded_device_ack_unknown` means the server knows the binding but
  has not proved this particular device received it.
- Lexical and semantic indexing have separate publication versions. A saved and
  synchronized file can legitimately be waiting for either search index.

Normal source discovery/admission is automatic. Do not force global reconcile,
reindex, reset a replica or run a worker merely because a new edit is not yet
searchable. Inspect the exact file's pipeline when needed. Configured scan,
admission, quiet windows and model work all contribute latency.

The legacy generated Notes projection is read-only. The enrolled writable
workspace is a separate exact-base source adapter; raw projection edits do not
write back. PDF/image bindings are references, not binary write-back permission.

Recovery must preserve pending client intent, unresolved variants, source
journals, SQL bindings, native local receipts and keys, not just indexed text.
Do not delete/rebuild these stores to clear a hold. Use the operator handoff for
an actual recovery; search success is not proof of backup coverage.

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
