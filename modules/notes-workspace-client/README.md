# LOOM LiveSync client intent overlay

## Resolving text conflicts

Use the **LOOM: review and resolve note conflicts** command or click the LOOM status indicator.
The comparison shows the device draft, current source, and original base when
its native revision is still available. Choose the source, the device draft,
write a merged result, or leave it for later. A changed comparison must be
refreshed. The choice is pending until Main accepts its exact source base.
Edits made while a choice is pending remain retained for another review.

Source acceptance creates a new edit and a receipt linking the old conflicts;
it does not rewrite those historical receipts. Exact reviewed losing native
revisions are retained in the private journal before ordinary revision
tombstones converge the file. Unknown competing text remains held, not lost.
There is no vault reset, purge, timestamp winner or automatic AI merge.
Rename/delete conflicts and reference-only PDF/image edits still need operator
attention; these controls resolve writable text notes.

Agents use the same source-checked workflow:

```sh
loom notes conflicts list
loom notes conflicts show <intent-id>
loom notes conflicts resolve <intent-id> --review <review-token> --choice source --yes
loom notes conflicts resolve <intent-id> --review <review-token> --choice device --yes
loom notes conflicts resolve <intent-id> --review <review-token> --choice merge --file merged.md --yes
```

`show` supplies the review token and competing text. `resolve` stages the choice
in the existing journal; the ordinary workspace worker publishes it and performs
the exact-base write. A pending response is not completion. Repeat the same
request to retrieve its receipt; a different source requires a fresh comparison.

This is a pinned extension of Self-hosted LiveSync, not a replacement sync
engine. C4 integration and device installation are separate. Never install this
build into an existing live vault as part of building it.

Upstream: https://github.com/vrtmrz/obsidian-livesync at
`7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b` (plugin 1.0.32).
The upstream archive SHA-256 is
`d427a25cac9a124610fb25d0747dc3dc329a7a486475841ba032652859b04403`.
Use its exact package-lock (checked by apply.py), `npm ci --ignore-scripts`, then
`python3 /absolute/path/to/this/apply.py /absolute/owned/upstream/build`.
Commonlib is the lockfile's npm 0.1.29 artifact, not an asserted Git revision.
Its published integrity is
`sha512-PeBUUQNSuyS+ISPIvwK3rzaritHi0XDfNlFMOqaRpIbxGAWbWK0YDtE1Htbde3r8Lr3ZdTNjpY+Nmr//OxzgTA==`.
The manifest records original/applied target and overlay hashes. Unknown edits
fail closed. Reapplication checks the previous receipt and preserved originals.

Run focused checks inside the patched build:

```sh
npm run test:unit -- src/loomClient/control.unit.spec.ts
```

Control JSON lives in ordinary native text-note chunks at
`LOOM-Control-v1/<kind>/<immutable-id>.md`. Native encryption must be enabled;
no sensitive custom PouchDB metadata is introduced. The path namespace carries
only kind and opaque ID. Stock clients must not be connected to this pilot DB:
they lack the reserved-namespace reflection guard. Source admission and Notes
index exclusion remain C4 responsibilities.

`src/protocol.ts` is the strict v1 wire schema. Binding `collectionRoot` is a
source-declared relative collection folder; it is not an arbitrary host path or
client authority. Identifiers and base tokens are opaque URL-safe strings.
Exact native revisions and SHA-256 digests accompany every binding. Payloads
are capped at 1 MiB to match the smaller transport boundary.

PDF, PNG, JPEG, GIF and WebP references use native binary chunks and exact-hash
non-writable bindings, up to the separate reference snapshot limit of 64 MiB. Their original
relative paths are preserved for note links/embeds. Source changes may refresh an
unchanged local reference; local changes or missing previously bound references
are held rather than overwritten or sent back. This is reference delivery, not
binary editing. Files above 64 MiB are not yet supported by this snapshot path.
Text and control-message limits are unchanged. Reference snapshots are still
buffered and retained in LOOM; native transport chunking is not streaming at
every layer.

## Build and package

Obtain the pinned GitHub source archive in an owned build directory, verify its
archive checksum above, extract it, and install only the reviewed lockfile with
`npm ci --ignore-scripts`. Run:

```sh
python3 /absolute/path/to/this/build.py /absolute/owned/upstream/build
```

The recipe applies the overlay, runs upstream TypeScript and the three focused
Vitest specs, then runs `npx --no-install vite build --mode original`. It refuses
all `.env*` files and a nonempty `PATHS_TEST_INSTALL`. Native Vite's copy plugin
has no destination. Outputs are `main.js`, `manifest.json`, `styles.css`, and a
SHA-256 receipt `loom-client-build.json`. Plugin ID/version remain native;
manifest name and status display visibly say `LOOM intent v1`. Keep the upstream
MIT notice (`LICENSE.upstream`) with any distributed bundle. No upstream fork,
publication, installation, or updater registration is part of this module.

## Runtime behavior

An explicit device-local `loomLocalVaultName` in plugin `data.json` pins the
existing native database and device-key namespace before native startup. Set
it to the old vault name before a visible folder rename, and preserve the
Obsidian registry appId and database suffix. Without this pin, native behavior
is unchanged. This is not a new device identity or a remote configuration;
another device keeps its own old name. Rename only with Obsidian closed and a
recovery copy of the vault and closed native profile. Never reset or rebuild a
database to perform a cosmetic rename.

The plugin's existing original vault callbacks synchronously snapshot binding
and operation class before the native event queue. Asynchronous reads must stay
stable or the operation is held. The extension journals bytes before publishing;
a filesystem save and journal transaction are not one atomic action. Same-file
operations retain predecessor IDs, including descendants of a new create's
explicit absent base. `intentDigest` hashes the exact UTF-8 JSON returned by the
codec, not a re-ordered JSON representation.

The native file-handler gate covers live writes, offline/rebuild writes, manual
resolution and conflict-preservation writes. The offline scanner's direct local
delete is also held. Native force/conflict preferences cannot bypass reflection
checks while the LOOM hook is active. Source-export reflection requires exact
revision/hash and ordered source bindings within the same file/enrollment;
legacy bindings use proven native ancestry. The optional `sourceSequence` is
allocated by Main under the replica lock, not inferred from timestamps or
native revision numbers. A legacy current head is re-exported once with its
sequence; old controls remain unchanged. An old binding replay cannot roll it
back. Remote tombstones are held for source lifecycle
resolution rather than becoming client-originated deletes.

Native conflict resolution is held before merge/write/delete when the LOOM
hook is present. A refused revision deletion returns false, never a successful
merge: otherwise native resolution repeatedly queues the unchanged conflict.
The hold is `native_conflict_requires_resolution`; revisions are preserved.
The LOOM comparison controls below provide explicit resolution; the generic
native resolver does not silently select a winner. Plugin unload stops the LOOM change feed and drains active work before
native database shutdown. Already captured edits remain in the journal.

The publisher uses existing native path locks and explicit-base APIs. Rename
has an independent absent target revision plus an exact-base logical deletion;
only a complete publication pair is eligible for C4 admission. A write/receipt
crash gap stays held with its bytes; it never guesses ancestry from content.
Conflicting, held, unacknowledged and predecessor-needed payloads remain in the
journal. An applied source acknowledgement must match intent digest, publication
ID, exact revision and binding before retiring payload bytes. Metadata is
retained for replay. `uploaded` is the internal state for native **local**
publication; the UI explicitly says it is waiting for source. It is not proof
of network delivery or canonical application.

The journal is `loom-client-intent-v1:<vault appId>` in IndexedDB, separate from
native DB rebuilds, with a 64 MiB total bound and 1 MiB per payload. It awaits
transaction completion with strict durability requested. Journal/quota failures
hold further writes and never reset/drop pending data. Browser profile/storage
loss is outside that guarantee. Commands show source acknowledgement/holds and
retry incomplete delivery; they do not override ambiguous native-write gaps.
Native local changes are revisited in bounded batches for late chunks/receipts.

The installed overlay also writes alternating `loom-intent-backup-0.json` and
`loom-intent-backup-1.json` inside its plugin directory after each durable journal
save. Each contains the vault identity and full journal, including unresolved
payloads, bases and acknowledgements. These files are private recovery data,
not Notes sources, and must not enter Git. One stable vault capture can protect
them while Obsidian is open; do not copy live IndexedDB/LevelDB as a substitute.
The exports are never automatically imported on a new device. If native local
receipts are lost, preserve these payloads and reconcile against Main; do not
blindly replay or clone a live device identity. Full same-client restoration can
instead use a closed-app native-profile backup. An offline Mac cannot send its
new unsynced edits to a Main/cloud backup until it reconnects.

Creation needs an already materialized writable binding declaring the collection
folder. An empty/unbound collection holds until C4 establishes its enrollment.
Folder rename expands at most 256 known descendants into individual intents;
it is not atomic and does not invent unknown descendants. Case/path collisions,
unknown deletes and moves outside that source-declared folder stay held.

## Acceptance boundary

C1-C3 are local implementation/build checks, not installed-device acceptance.
C4 must implement operation-level source admission, reserved-control exclusion
from Notes ingestion, source acknowledgement writing, live membership/lifecycle
rechecks, and direct tiny Mac/iPad scenarios. Only patched clients with native
encryption may share the pilot DB. Stock native writes are not canonical source
permission. Do not enable plugin auto-updates for an installed pilot: a native
upstream update must first be reviewed against this pinned overlay. Rollback
stops write-back and preserves journals/source/conflicts, not a DB downgrade.

The optional commonlib hooks preserve native behavior when absent. Only the
marked plugin composition installs `PathService.loomControlPath` for control
selection and the file-handler `loomIntent` hook for reflection suppression.
Hookless hosts still apply ordinary native selection rules and can reflect these
paths; they must not be connected to the LOOM pilot DB.

If an ordinary edit is superseded before its verification read, a later ordinary
edit may reuse the captured exact base and last durable predecessor. The skipped
edit never entered the journal. Existing queued/published operations are neither
removed nor rebased. Missing payloads, rename/delete captures and other genuine
ambiguities do not use this recovery path.
