# Roadmap

The order below is directional, not a release-date commitment. Working code,
remaining acceptance, and possible future integrations are different things.

## Public Developer Preview

- Publish a clean, privacy-audited source baseline with licensing, contributor
  guidance, clear dependency ownership, and an honest implementation snapshot.
- Reconcile the documentation around current projects, search and agent use.
- Add code-rendered system/network maps and real Portal visuals after the
  source/docs baseline; build the public documentation site separately.

## Make The Existing System Easier To Use

- Add guided CLI installation/configuration over existing setup primitives,
  including hardware, identities, credentials, network and recovery prerequisites.
- Keep project work file-first: ordinary edits should not require repeated
  documentation searches or registration ceremonies.
- Improve actionable errors, prerequisite summaries, resumable operations,
  context discovery and the consistency of CLI/API/Portal workflows.
- Bring remaining Portal project forms into line with file-first CLI creation
  and declaration operations; do not make legacy facet forms the onboarding path.
- Replace remaining maintainer-specific assumptions with documented operator
  configuration and make supported platform/dependency combinations explicit.

## Exercise Complete Workflows

- Continue a real project through development, deployment, state projection,
  candidate registration, reconciliation, retrieval and later changes.
- Verify Notes queues, chunking, embeddings, OCR and PDF processing against
  representative real documents; distinguish disabled workers from failures.
- Verify fresh application-data backup/recovery and larger archive/inactive
  restore/reactivation scenarios without creating a replica testing platform.
- Check explicit project schedules and automation lifecycle through actual use.

## Extend Deliberately

- General, authorized agent SSH between nodes, separate from desktop control.
- A LOOM-owned bounded agentic-run adapter with project-specific declarations,
  identity, run history, concurrency and failure reporting. Hermes native jobs
  must not silently become a second canonical scheduling system.
- Clearer agent workspace portability, connector setup and credential rotation.
- Additional installation targets and public release packaging after the
  documented reference configuration has a repeatable setup path.

## Deferred Ideas

Mobile/iOS computer control, further connectors, and broader autonomy remain
ideas, not implemented commitments. New capabilities should solve an observed
workflow problem rather than add another control layer by default.
