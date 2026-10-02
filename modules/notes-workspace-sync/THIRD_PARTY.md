# Upstream notices

This directory contains a LOOM adapter and a verified-source preparation recipe,
not a vendored copy of Self-hosted LiveSync or its dependencies.

- Self-hosted LiveSync 1.0.32, commit
  `7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b`, is MIT licensed:
  https://github.com/vrtmrz/obsidian-livesync/blob/7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b/LICENSE
- `@vrtmrz/livesync-commonlib` 0.1.29 is MIT licensed. The pinned npm tarball
  includes its LICENSE. Integrity is in `upstream-lock.json`.
- PouchDB's LevelDB adapter is Apache-2.0; LevelDB dependencies have their own
  notices. The upstream npm lock and installed packages retain license metadata.

Keep upstream LICENSE and installed dependency notices with any packaged build.
`prepare.py` retains upstream's LICENSE and the exact dependency lock. A future
pruned/Nix package must carry these forward rather than distributing just the
compiled entry point without notices. No transitive license audit is claimed.
