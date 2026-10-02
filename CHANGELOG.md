# Changelog

## 0.9.0 - Public Beta

The first usable-system source baseline, following the September developer
preview. This is not a stable 1.0 API or a turnkey installation. The tag and
GitHub release identify the published commit; development snapshots are not
additional release guarantees.

### Added

- A source-backed writable Notes workspace spanning declared collections and
  projects, with pinned encrypted LiveSync transport and Mac/iPhone clients.
- Durable edit intent, offline reconnect, event-driven source reflection,
  explicit text conflict review/resolution, and exact citation-to-vault navigation.
- Project Notes refresh/processing policies and one-version selective OCR,
  image description and embedding requests, including bounded folder previews.
- Native Hermes schedule observation in CLI/Portal and project-owned Hermes
  declarations. Hermes retains its scheduler; LOOM capabilities remain a separate
  route for project coding-agent jobs.
- General authorized agent SSH to the Mac, separate from the paired desktop
  computer-use integration and ORCA's shared-browser surface.
- Matching Notes recovery cohorts and optional bounded client vault captures.

### Improved

- Indexed lexical candidate retrieval, batched search and embedding work,
  source-version lookup, queue draining and idle worker/scanner behavior.
- Recoverable archive lifecycle, watcher retries, schedule delivery recovery,
  and update restart behavior.
- Application-data Notes enrollment, source-specific attachment processing,
  provenance reconciliation guidance and agent-visible errors.

### Compatibility And Known Limits

- PostgreSQL migrations now extend through 00083. Follow the migration/update
  path; do not mix newer sources with an older schema or assume SQL downgrades.
- The Notes plugin is a pinned overlay on LiveSync 1.0.32. Stock clients and
  unreviewed upstream updates are not interchangeable with the paired build.
- Text edits are writable; binary attachments are source-owned references.
  Reference snapshots are limited to 64 MiB, independently of Notes intake limits.
- Handwriting/equation recognition is optional. OCR, vision and embeddings are
  selectable processing, not requirements for every imported attachment.
- Large imports and searches have no latency SLA. Further throughput, ranking
  and resource-pressure optimisation is planned for the next cycle.
- Cloud inclusion and application-consistent recovery are different claims.
  Client vault captures do not capture a live native browser database or a
  phone's unsent offline edits.
- Installation is manual; example host bindings are synthetic. Legacy Portal
  forms do not yet have complete parity with file-first project workflows.

See [current state](.project/STATE.md), [roadmap](.project/ROADMAP.md), and
[recovery guidance](docs/operations/backup-restore-and-drills.md).

## public-preview-2026-09-15

Initial clean-history, privacy-reviewed source release: coordination, projects,
managed applications, storage/recovery, Notes search, qualified Provenance,
optional agent integrations, licensing and public documentation.
