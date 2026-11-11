# LOOM Public Source State

Source review: November 11, 2026. This is a reusable-source snapshot, not live
installation health, a signed desktop release or a stable 1.0 guarantee. The
published 0.9.0 beta and historical client releases retain their original meaning;
this source refresh does not rewrite their tags or compatibility contracts.

## Implemented Source

- Go daemon, CLI/terminal Portal, node agent and typed private APIs; PostgreSQL
  migrations through 00115. Existing migration bytes remain unchanged.
- Standalone Electron desktop: actual filesystem explorer, direct editing,
  Markdown/properties/wikilinks/tags, source highlighting, file operations,
  attachments, project focus, CSV, Canvas and supported Bases views.
- Application-owned local text copies, durable draft/outbox records, offline
  edits, exact-base source acceptance and Main-first recoverable path operations.
  Directory subscriptions and reconnect checks are bounded and event-driven.
- Local/cache/remote availability distinct from metadata/index/enrichment status;
  on-demand binary reads and explicitly selected offline working sets.
- Box contexts, project registration/resource declarations, active/passive/
  archived lifecycle, managed application preparation/build/start and supported
  archive/restore with explicit runtime reactivation.
- Metadata/categories/backlinks, bounded graph views and recorded-operation
  timelines; indexed lexical/semantic source search and selective enrichment.
- Qualified Provenance candidates/records, evidence, reconciliation, supersession
  and project context. Retrieval is not an authorization or truth service.
- Native Hermes conversational runtime/profile integration, agent-owned native
  schedules and LOOM-owned system jobs; independent native coding-agent dispatch.
  ORCA compatibility remains historical, not a current required interaction path.
- Storage custody, operational/native recovery, content-reference protection,
  bounded retention and Borg cloud history with exact selected-state evidence.

## What This Does Not Establish

- Fresh-machine installation is not a finished wizard. No maintainer accounts,
  actual host bindings, private agent memories or user corpus are included.
- The Electron app is available as source, not a signed/notarized downloadable
  product. Native updates need an explicitly configured signed channel; no
  operator credential or private repository is selected by default.
- The reference desktop is macOS and Main is NixOS. Package evaluation on another
  platform is not end-to-end platform acceptance. Mobile is planning, not a
  standalone shipping client. Old Obsidian plugins are legacy integrations.
- Editor support is bounded: writable text/structured files up to 512 KiB; larger
  originals remain browsable/read-only. Bases does not implement all Obsidian
  expressions. CSV does not execute formulas. Graph and timeline are bounded
  derived views, not complete knowledge or historical truth.
- A file can exist without indexing or enrichment. Source access, local save,
  Main acceptance, processing, device delivery and protection are distinct.
- Separate Boxes/native profiles establish context and workflow defaults, not
  operating-system isolation. External-service credentials and authority must be
  configured independently, and source templates grant no standing permissions.
- Native browser/desktop tooling requires its actual provider/session/permissions.
  No universal site-login, always-on microphone or wake-word reliability claim.
- Backup inclusion protects a specific selected capture. It does not prove
  unsent offline edits, all-Mac coverage or application-consistent recovery for
  every live service. Old recovery history and unknowns must be preserved.

## Verification Scope

Use the release/commit's checks and the operator's own installed diagnostics.
Public source tests do not certify a reader's machine or its cloud protection.
The candidate preparation preserves the existing public ancestry and excludes
private configuration/history rather than merging an operator repository.

See [README](../README.md), [desktop guide](../docs/user-guide/workspace.md),
[installation](../docs/operations/installation.md) and [roadmap](ROADMAP.md).
