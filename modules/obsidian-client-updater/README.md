# LOOM Client Updates For Obsidian

An independent updater for the LOOM Notes overlay on desktop and iOS. It updates
**plugin code**, not Obsidian itself. Initial installation requires a closed-app
bootstrap; subsequent compatible target releases use the reviewed public feed.
Actual device installation and acceptance are separate from a source build.

## What Changes

The fixed target remains `obsidian-livesync`, upstream version `1.0.32`. A
separate `client-notes-*` release identity and increasing sequence distinguish
LOOM bundles without renaming any existing native database or vault appId.

Only `main.js`, `manifest.json`, `styles.css`, `loom-client-build.json` and
`loom-release.json` are replaceable. Device `data.json`, native IndexedDB,
vault content, pending intents and `loom-intent-backup-*.json` are never part
of an update or rollback. Nothing waits for Main to accept pending edits.

The shared [release contract](../client-updates/README.md) is reusable for later
app adapters. This first installer is deliberately only for the Notes client;
it cannot update itself, another plugin or arbitrary paths.

## Build

Use the pinned development dependencies, with no runtime Node dependency:

```sh
cd modules/obsidian-client-updater
npm ci --ignore-scripts --no-audit --no-fund
npm run check
npm test
npm run build
```

`main.js` and `manifest.json` are the updater bootstrap code. `main.js` and
`node_modules` are rebuildable and excluded from source history. The deployed
plugin uses public `requestUrl` and `Vault.adapter`, also on iPhone. Only the
small Obsidian plugin-manager reload adapter uses feature-checked internal
APIs, following the established [BRAT reload pattern](https://github.com/TfTHacker/obsidian42-brat/blob/main/src/features/BetaPlugins.ts).
Without a supported reload API, installation records restart-required instead
of claiming new code is running.

## One-Time Bootstrap

After explicit public distribution approval, build the target from the audited
public source, package it with `modules/client-updates/package_release.py`,
and review its exact `release.json`. An integration checkout or private source
commit is not permission to publish. Upload the seven package assets together
to its exact promoted `Liaust/LOOM` GitHub `client-notes-*` release. Never use
the stock upstream updater or generic server-release `latest` endpoint.

Close Obsidian on the Mac and phone. Use a **fresh** phone export; never replace
the phone with a previously exported vault over newer edits. Preserve all
existing target state, installing only the five verified target code files.
Install this updater under `.obsidian/plugins/loom-client-updater` and copy the
target package's verified outer `release.json` there as `adopted.json`. This
explicit bootstrap binding is required; the updater does not silently trust
a bundle that changed its own local build receipt.

Enable the updater in that vault. Its default is check/stage only. Enable
"Automatic compatible Notes client updates" in its settings to permit normal
compatible activation; bootstrap may set `autoUpdate: true` in this updater's
own `data.json` after approval. It uses no personal GitHub token.

The updater's bootstrap files are distributed separately under
`client-updater-*` releases in the same repository. Do not add those assets to a
`client-notes-*` release: that release has exactly the seven package assets.
The updater itself is not automatically updated by this first adapter.

A completed rollout must observe a subsequent promoted bundle through the real
feed on **both** Mac and iPhone, with actual loaded identity and ordinary note
acceptance. Compilation and mobile-shaped adapter tests are not substitutes
for that observation. Mac installation alone does not establish phone acceptance.

## Ordinary Updates

Checks happen on layout-ready and foreground/focus, at most once per six hours.
An explicit check bypasses the cooldown. No periodic idle timer is installed;
network failures leave the current client available and await a later real
event. GitHub discovery is bounded to three pages of 50 releases with cached
ETags, and at most ten component manifests per check. Only stable component
tags and their exact fixed asset URLs are accepted.

All downloads and byte counts/hashes are verified before touching target code.
The updater keeps one staged bundle and the exact previous code in its own
plugin directory, outside Notes synchronization. Checksums provide integrity
under the fixed repository/HTTPS trust boundary, not independent publisher
signatures. Independent signing is deferred.

The Notes handoff calls the public editor save path, drains already captured
work, checkpoints the journal/recovery exports, then awaits native shutdown.
Public Obsidian APIs do not fence typing, so an open text editor causes a safe
restart deferral. On the next open the target holds native startup while the
independent updater completes the staged replacement. Do not interrupt typing
to force plugin reload. A suspended/closed iPhone cannot update until Obsidian
runs again.

Commands in the Obsidian command palette:

- Check for Notes client updates.
- Install staged Notes client update.
- Show Notes client update status.
- Restore previous Notes client code (pause automatic updates).

Status reports installed, loaded and initialized separately. A correctly loaded
client with slow startup remains initialization-pending, not falsely failed or
rolled back; a later foreground event records initialization when observed.
A manual rollback
pauses automatic updates before code changes; re-enable explicitly when ready.

## Interrupted Installation

`install.json` is a bounded phase journal with previous/next release manifests.
Multi-file replacement is **not atomic**. Filesystem-adapter operations also do
not expose fsync or an application-consistency guarantee across forced power
loss. If Obsidian stops partway through replacement, target startup holds sync
independently of plugin load order. The updater verifies stored code and either
finishes a deferred install or restores the exact previous bundle. Partial code
that cannot load is also recoverable by the independent updater.

A malformed journal or damaged previous bundle is retained and requires review;
it is not deleted, bypassed or used to rebuild a database. If the target cannot
durably finish shutdown, code is not overwritten underneath active work. Keep
the updater directory, device settings and recovery exports for diagnosis.
Unknown locally modified target code refuses automatic replacement.

Rollback is code-only and restricted to the same Notes protocol and local
journal schema. A native or persistent-schema upgrade requires a separately
planned migration. No Main service, schedule, database table, enrollment or
credential change is needed for this updater.
