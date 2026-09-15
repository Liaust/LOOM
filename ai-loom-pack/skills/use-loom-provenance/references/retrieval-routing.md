# Retrieval Routing Reference

## Technical Objects

Use `loom object list` and `loom object inspect <object-ref>` for object
identity, relationships, versions, and technical state. Use index status for
extraction failures. Do not reinterpret an object failure as a semantic claim.

## Notes Source Material

Use `loom notes search <query>` when the question asks what a source says,
where wording appears, or which note contains material. Registered Notes,
Topics drafts, Library sources and explicitly declared project docs/research
share this engine. Documents and Imports are not implicitly Notes sources.
Use `--category topics`, `--category library` or `--category projects` when the
question supplies that context, and preserve project/owner filters.

Object metadata and search snippets are not an exact quotation. For a selected
passage, bind the knowledge object ID, version ID, chunk ID and source hash:

```text
loom notes passage get <chunk-id> --object <object-id> --version <version-id> --source-hash sha256:<digest>
```

Prefer the returned `passage_followup` tuple, including source hash and
`source_lifecycle`. Only when no tuple exists, `loom notes objects show
<object-id>` can supply a hash after verifying that its indexed version matches
the hit. A concurrent edit can make that combination unavailable: search again
only for a new current citation, never to replace an existing historical one.
For an existing citation, reuse its stored hash and IDs exactly. Check the
returned identities and structural heading/page locator before quoting.

Retained text may be historical; `current_source_context` is explicitly current
classification, not historical acceptance evidence. Topics remain drafts and
Library passages remain attributed claims, even when sources agree. Contradictory
sources should be quoted and attributed separately, not blended into a decision.
Metadata-only, needs-OCR and needs-image-description results have no passage to
invent. A refused read is unavailable evidence; do not bypass current privacy
through files, database queries or pipeline diagnostics. See reconciliation.md
for the separate reviewed candidate/record boundary.

## Qualified Provenance

Use `loom provenance search <query>` for accepted records and unresolved cases.
Pending candidates require `--include-pending` or the explicit
`pending_candidates` collection and remain separate. Follow compact IDs with:

```text
loom provenance record get <record-id>
loom provenance candidate get <candidate-id>
loom provenance case get <case-id>
```

## Project And Repository Navigation

On versions that expose `loom provenance project`, for what a project is doing,
use `loom provenance search <query> --collection
project_state`, or ordinary search with its separate typed collections. The
project need not contain a Git repository. Its `.project` purpose, state,
roadmap, map, feature metadata and decision-file references are observed source
declarations, not accepted lifecycle decisions. Work elsewhere in the project
is not crawled or automatically ingested into Notes.

Follow a result with `loom provenance project get <project-id> --snapshot
<snapshot-id>` to retrieve the captured context, relative document paths and
hashes. Omitting `--snapshot` means the latest stored observation, not an implicit
refresh or a live filesystem read. Check missing/partial/archived/unavailable
posture and truncation before making a claim. `loom project context <ref>` reads
bounded current source context beside technical readiness without mutation.

If the installed version lacks `project` or `project_state`, use the existing
project/repository commands below and ordinary project source files. Do not
claim automatic project context capture is installed just because this skill is.

Use `loom project list`, `loom project inspect <project-ref>`, and
`loom project repos list <project-ref>` for technical ownership, lifecycle,
registration, contracts, and observed repository state.

Use `loom provenance repo list --query <terms>` when incomplete user context
describes a repository by aliases, purpose, topics, role, or active focus. Load
the selected semantic card with `loom provenance repo get <repository-id>`.
Return to `loom project` only when the card exposes a technical navigation gap.

At every step, search one engine first, exact-get second, and expand
sequentially. Report collection, posture, currentness, truncation, and the
reason for any expansion.
