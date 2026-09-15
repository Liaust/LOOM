# LOOM Public Preview State

Snapshot: 2026-09-15. This describes implementation and known development use,
not the health of any reader's installation. The system is still in active
development and has not completed a general-purpose installer acceptance pass.

## Implemented And Used In Development

- Core daemon, PostgreSQL migrations, CLI/Portal, node agent, typed capability
  routing, jobs, policies, durable events, schedules, and background workers.
- Minimal projects with .loom declarations and project-level .project context;
  compatibility for repository-level .repo; source-qualified metadata projection.
- Project plan/apply/status, managed application prerequisites, credential
  delivery, storage grants, startup readiness, and optional public HTTPS.
  A real WebDAV project has reached working deployment and device use.
- Box/storage catalogs, physical project/workspace archiving and inactive
  restore, Borg cloud history, database recovery packages, and strict recovery
  compatibility across historical Provenance schema heads.
- Notes source ingestion and exact citations; technical object search; typed
  Provenance search, candidates, accepted records, and reconciliation cases.
  A real coding-agent candidate registration and retrieval have been exercised.
- Optional Hermes/MINA sessions and Discord gateway, portable skills/protocols,
  and paired Mac computer use through both terminal and gateway sessions.

## Important Limits

- Installation is manual and operator-specific. Nix examples need real hardware,
  network, credential and account configuration. No one-command installer,
  automatic discovery wizard, or supported binary distribution is promised.
- Basic ingestion is not proof of every OCR, PDF vision, local-model, or vector
  embedding path. Broader real-document queue/processing/search checks remain.
- Project state projection does not automatically accept semantic decisions.
  Registered candidates remain pending until separately reconciled.
- Working application deployment is not proof of fresh application-data backup
  and restore coverage. That must be checked for the selected deployment.
- Larger project archive/restore/reactivation workflows need further real-use
  acceptance. Restoring files does not imply restarting an application.
- General agent SSH to a Mac is separate from paired computer use and remains
  an integration task. Mobile computer control is deferred.
- LOOM capability schedules exist. A unified LOOM-owned adapter for recurring
  model-driven Hermes tasks is deferred; native Hermes cron is a separate owner.
- Historical docs and compatibility surfaces are still being reconciled. Draft
  pages are useful explanations, not a promise of universal support.

## Release Posture

This is a clean-history source baseline. Private machine state and historical
operator logs are excluded. Public example configuration contains synthetic
identities, documentation addresses and disk labels, not a ready deployment.
The README, documentation entry points and roadmap are the current public
orientation. Test skip conditions and platform limits must remain visible.
