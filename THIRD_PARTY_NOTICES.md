# Third-Party Notices

Apache-2.0 covers original LOOM work, not every upstream component that an
installation downloads. Existing file-level notices take precedence for those
files. The source release does not bundle a Nix store, dependency vendor tree,
model weights, or external application binaries.

## Material Included In This Source Tree

| Material | Origin and license |
| --- | --- |
| Geist and Geist Mono font files | Vercel and basement.studio; SIL Open Font License 1.1. Full notice is retained at [fonts/LICENSE.txt](internal/minidashboard/web/fonts/LICENSE.txt). |
| Hermes compatibility patches | Against NousResearch/hermes-agent, pinned in `flake.lock`; upstream MIT notice retained at [licenses/hermes-MIT.txt](licenses/hermes-MIT.txt). LOOM changes add the remote Mac adapter and centered skin layout. |
| ORCA folder bootstrap patch | Against stablyai/orca 1.4.191; upstream MIT notice retained at [licenses/orca-MIT.txt](licenses/orca-MIT.txt). LOOM changes expose the non-Git folder CLI path. |
| Cua contract integration | The adapter and selected compatibility fixtures refer to trycua/cua's pinned driver contract; upstream MIT notice retained at [licenses/cua-MIT.txt](licenses/cua-MIT.txt). |

Patch files contain upstream context as well as LOOM changes. They do not
relicense the upstream projects. Keep the upstream notices with redistributed
patched packages and preserve any additional notices those packages contain.

## Separately Resolved Dependencies

- **Go libraries:** exact modules/checksums are in `go.mod` and `go.sum`.
  Upstream licenses remain with the modules; a future binary distribution must
  include the notices applicable to its linked dependency set.
- **Nix/Nixpkgs:** `flake.lock` pins the package definitions and upstream inputs.
  The packages built from those definitions have their own licenses.
- **Hermes, ORCA, Basecamp CLI, agent-browser, Chromium and Cua Driver:** optional
  integrations are fetched or built independently under their upstream terms.
  Credentials, vendor services, model subscriptions and account permissions are
  not included in LOOM's license.
- **Proton Pass CLI:** the package definition fetches the official binary and
  identifies GPL-3.0-only. It is not copied into this source release or relicensed
  as Apache-2.0. Redistribution of a complete binary installation needs its own
  applicable source/notice compliance review.
- **PostgreSQL, pgvector, Borg, rclone, Caddy and processing tools:** independent
  engines selected by configuration; their upstream licenses continue to apply.

This inventory identifies the public source boundary. It is not a blanket
license grant for every optional tool, model, or future release artifact.
