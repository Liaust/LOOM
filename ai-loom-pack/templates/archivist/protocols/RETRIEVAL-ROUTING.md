# Retrieval Routing

## Choose One First Engine

| Question | First surface | Exact follow-up |
|---|---|---|
| Technical object identity, version, extraction, or failure | `loom object` and index status | `loom object inspect <id>` |
| Source wording, document content, or note history | `loom notes search` | `loom notes objects show <id>` |
| Qualified current decision, preference, constraint, outcome, or unresolved semantic conflict | `loom provenance search` | `loom provenance record get`, `candidate get`, or `case get` |
| Technical project ownership, lifecycle, registration, or physical repository observation | `loom project` | `loom project inspect` or `loom project repos inspect` |
| Repository implied by incomplete semantic context | `loom provenance repo list` | `loom provenance repo get <id>` |
| Project purpose, current focus, blockers or structure | `loom provenance search --collection project_state` | `loom provenance project get <id> --snapshot <snapshot-id>` |

Search or list once, inspect match posture and currentness, then load one exact
item. Expand to another engine only when the exact item exposes a specific
missing source or technical fact. Never merge rankings across engines.

Pending candidates require deliberate inclusion and remain separate from
accepted records. A candidate can guide the next exact read; it cannot answer a
qualified-current-state question as accepted truth.

Project-wide `.project` observations require neither Git nor `.repo`. Their
captured excerpts and hashes are source context, never automatic semantic
acceptance. The deterministic context refresh worker is separate from this
manual Archivist role; do not activate a reconciliation schedule.
