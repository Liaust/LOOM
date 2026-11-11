# LOOM Roadmap

Reviewed November 11, 2026. Direction, not release dates or a support commitment.
Read [current state](STATE.md) before treating a historical plan as unfinished.

## Current Priorities

1. Make the existing desktop/filesystem workflow easier to install and operate:
   clear prerequisites, source-matched documentation and useful failure recovery.
2. Finish signed macOS distribution and observe a real signed update/relaunch.
   Development packaging is not this acceptance gate.
3. Refine explicit working-set, cache/on-demand and indexing/enrichment choices.
   Preserve drafts and pending operations; do not infer permission for a bulk
   download or model-processing campaign from ordinary file browsing.
4. Continue agent workflow improvements through actual tasks: precise context,
   compact tool results, source-grounded decisions and visible ownership of
   scheduled work. Keep native harness state replaceable and separate from LOOM.

## Deliberately Separate Work

- Standalone iPhone/iPad client and its private Main transport, offline durability
  and iOS background-operation limits. Desktop source is not a mobile client.
- Voice, calls and wake-word activation with real device acceptance. Presence of
  an integration is not a reliability claim.
- Broader installation targets and service adapters, richer Bases/Canvas/file
  support and additional archive/history views as concrete workflows require.
- A rendered documentation site. GitHub documentation and the Observatory guide
  already have distinct roles and should not become competing source catalogs.

## Principles

Keep ordinary files and project structure understandable without a knowledge
index. Add metadata and system configuration where an actual owner needs them,
not an alternate placement contract for every file. Preserve project identity,
user content and retained history across operations. Use proven database,
editor, graph, archive and networking engines rather than rebuilding them.

Contributions should simplify a real workflow. No replica deployment, benchmark
platform or broad test matrix is required for every patch. State which focused
checks ran and which installation/device/protection claims remain unverified.
