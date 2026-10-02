# Roadmap

This is directional, not a release-date commitment. Version 0.9.0 establishes
the usable-system public beta; the next optimisation cycle can use 0.10.x.
Internal V1/V2 planning labels are not public semantic-version promises.

## Current Baseline

Projects/declarations, managed applications, capability schedules, native
Hermes schedule integration, qualified Provenance, selective Notes processing,
writable source-backed Notes sync, archive/recovery and agent/node integration
are implemented. See [current state](STATE.md) for their limits rather than
treating this list as a guarantee for every configuration.

## Next: Optimise What Exists

- Improve bulk imports so text/indexing/embeddings and asset delivery can make
  bounded progress concurrently without exhausting Main's compute or disk.
- Refine retrieval plans, lexical/semantic ranking and source-version lookups
  against real larger collections; expose useful latency and backlog diagnostics.
- Reduce unnecessary watcher/polling/full-scan work and client status noise;
  preserve event-driven ordinary editing and exact conflict/source checks.
- Make recovery status and service coverage easier to understand without
  conflating readable archives with application-consistent recovery.
- Assess better extraction and handwriting/equation quality only where useful;
  retain selective processing for expensive documents.

## Simplify Installation And Operation

- Guided CLI installation/configuration over existing primitives: hardware,
  network, identities, credentials, processing engines and recovery prerequisites.
- More consistent CLI/API/Portal prerequisite reporting, resumable actions and
  project forms; ordinary file edits should not require registration ceremony.
- Repeatable packaging and documented supported platform/dependency combinations,
  rather than assuming the maintainer's personal host configuration.
- Clearer agent workspace portability, connector setup and credential rotation.

## Deferred

Mobile/iOS desktop control, additional connectors and further installation
targets remain separate proposals. New features should solve an observed
workflow problem rather than introduce another control layer by default.
