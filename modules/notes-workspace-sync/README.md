# Native Notes workspace transport adapter

Runtime collection selections may set `path_prefix` to a clean relative folder
within the enrolled source root. This restricts enumeration and every source
operation, including both rename endpoints; it does not rebase relative file
paths. For example, root `Notes` plus prefix `Workspace Pilot` exposes only
`Notes/Workspace Pilot/...`. Omission preserves existing full-root selection.
Changing the prefix changes the effective enrollment generation. Source-owner
privacy, custody and active-registration checks still apply.

For an operator-reviewed expansion, a scope may name `previous_generation`: the
exact effective generation of its existing persisted mappings, not the old
enrollment token. Export carries forward only the same source collection, file
identity and client path, after checking that its bound native revision is the
sole current leaf. New mappings retain old mappings and advance the per-file
source sequence. Pending native edits stop that file's rebind; old-generation
intents do not gain write authority. Finalized retained journals may be inspected
under that same explicit predecessor while the source remains currently owned
and selected; unfinished operations keep current-generation admission. Keep the
predecessor while those retained journals need observation. Never reset replicas
or substitute a guessed generation.

W2a transport with C4a source-join IPC extensions. This is a small IPC entry point built with Self-hosted
LiveSync 1.0.32's native CLI. It replicates a native database and exposes exact
revisions. It never mirrors a source directory or writes canonical notes.

`internal/notesworkspace/` (owned by the other lane) must supply admitted source
identity, durable source bases, pending bytes, conflict custody and source-write
recovery. This adapter does not accept an incoming change as a canonical write.
A delivered revision must be mapped through its ancestry to a persisted source
base. Missing/ambiguous ancestry is held, never stamped with the newest source
hash. Native `_rev` and content SHA-256 are different identifiers.

## Build without vendoring upstream

`upstream-lock.json` fixes the source archive, root npm lock digest and Commonlib
integrity. `prepare.py` only downloads/checks/extracts that source into a new
owned build directory and substitutes this adapter's entry point. It refuses to
overwrite an existing directory. Upstream source/dependencies/build outputs stay
outside this repository. Python >=3.12 and Node >=22.12 are required; prefer
Main's pinned Node 22 runtime. The package uses upstream's exact npm lock.

From this repository root, the integrator can run these commands on Main under
its normal update authority (not executed there by this lane):

```sh
python3 modules/notes-workspace-sync/prepare.py /tmp/loom-notes-sync-build
cd /tmp/loom-notes-sync-build/obsidian-livesync-7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b
npm ci --ignore-scripts --no-audit --no-fund
npm rebuild leveldown --no-audit --no-fund
python3 /absolute/path/to/LOOM/modules/notes-workspace-client/apply.py "$PWD"
npm run build --workspace self-hosted-livesync-cli
```

The explicit rebuild runs the pinned LevelDB native-addon install step and may
need Python/make/C++ for the target host ABI. The initial install runs no lifecycle
scripts. Vite's output includes multiple `.cjs` chunks: keep **all** of
`src/apps/cli/dist/`, not only `index.cjs`. The native bundle externalizes PouchDB,
LevelDB, chokidar and other modules; keep the installed runtime dependency
closure accessible. The working build directory is a usable development package.
No standalone minimal Nix derivation or pruned redistributable closure is claimed
in this checkpoint. Future Nix packaging should retain the verified root lock,
licenses and target-ABI native modules; shared flake/service wiring is unmodified.

Run the focused actual-native test after building:

```sh
LOOM_SYNC_UPSTREAM=/tmp/loom-notes-sync-build/obsidian-livesync-7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b \
  node --test /absolute/path/to/LOOM/modules/notes-workspace-sync/native.test.mjs
```

The test uses an owned temporary LevelDB directory and cleans only that directory.
It starts the actual compiled entry point. It needs no CouchDB, network service,
credential or Obsidian vault. It tests native source-base siblings, exact reads,
ancestry/leaves, change resumption, publication idempotency, restart, ambiguous
receipts, logical and branch tombstones, and absence of filesystem reflection.

## Private configuration and invocation

```sh
node /path/to/built/src/apps/cli/dist/index.cjs \
  /path/to/owned/replica --settings /path/to/private/settings.json
```

The replica directory must already exist. Only this argument shape is accepted;
stock CLI commands and settings-write flags are not exposed. Use a dedicated
replica directory (never an original notes tree) and one owning process. Native
LevelDB enforces single-process ownership. The file format is the pinned CLI's
settings schema. Supply `remoteType:""` (CouchDB), `P2P_Enabled:false`, and explicit
`false` for `liveSync`, `syncOnStart`, `periodicReplication`, `syncOnSave`,
`syncOnEditorSave`, `syncOnFileOpen`, `syncAfterMerge`. The entry refuses other
modes so only an explicit IPC `sync` request initiates replication. Upstream
compatibility gates remain intact. Setup, reset, remote administration, native
conflict resolution, key generation and device onboarding are not IPC methods.

Set `readChunksOnline:false` in the protected settings. The native default uses
on-demand chunk downloads: a finite sync can finish with metadata present but
content unavailable to this adapter's local-only exact reader. Eager chunk
replication is required for the bounded worker; do not enable implicit network
fetches during exact source validation to compensate for incomplete replication.

Supply configured native CouchDB/E2EE settings through protected runtime storage;
keep values out of Git, Nix store, logs and command arguments. Native diagnostic
streams and exception bodies are suppressed because they may contain private
configuration. Startup failures are nonzero exits; request failures have stable
codes. No unattended interactive recovery authority is granted: `sync` uses the
native finite replication role with `NO_INTERACTION`. No replication success is
reported on blocked, partial, cancelled or failed outcomes. Do not enable metadata
expiry or auto conflict resolution during initial integration.

## JSON lines protocol 1

One request/response per line, processed sequentially. End stdin to stop cleanly.
Maximum ordinary input line is 16 MiB; `publish.reference` alone permits a 128 MiB
line for base64 framing of an at-most-64 MiB reference. Malformed/oversized streams
must be fixed by the caller. Text payload is UTF-8, limited to 8 MiB per publication/read.
Control records retain their separate 1 MiB bound. No public
listener, authentication layer or generic remote shell is created. The local
service caller is trusted; LOOM source authorization is still mandatory.

```json
{"id":"hello","request":{"protocol":1,"method":"status"}}
```

Response envelope is `{"id":"hello","result":{...}}`. `id` is caller correlation;
`request.id` below is a native document ID. `status` returns
`{status:"ready",protocol:1,epoch:"...",sourceWrites:false}`. Save the opaque native
database epoch; the status also reports `upstreamRevision` and `upstreamVersion`.
Every other request must include the same `epoch`. A changed or
missing epoch returns `held/replica_epoch_mismatch`. Reconcile against LOOM's
journal after database replacement, never reuse a cursor blindly.

| Method | Request fields beyond `protocol`, `epoch`, `method` | Result |
| --- | --- | --- |
| `changes` | `since` explicit native sequence (0 initially), `limit` 1–128 (default 32) | `nextSequence`, changed file IDs with sequence and current leaf metadata; includes logical/branch tombstones. Cursor is transport evidence, not canonical acknowledgement. |
| `leaves` | `id` | Up to 64 current leaves, including deleted leaves. Refuses excess; does not silently truncate. |
| `read` | `id`, exact `revision` | Exact metadata and available UTF-8 bytes as `contentBase64` plus `sha256`; missing chunks/binary/deleted content is held. No implicit remote chunk fetch. |
| `publish` | `operationId`, relative `path`, exact `baseRevision`, `contentBase64` | Native `id`, created `revision`, unchanged supplied `baseRevision`, payload `sha256`. Calls `storeWithBaseRevision`, even when the supplied base has advanced. |
| `publish` (initial export) | `operationId`, relative `path`, `create:true`, `contentBase64`; omit `baseRevision` | Uses native `storeIndependentRevision` only if no document history exists. Explicit bootstrap export, not automatic source creation from a client path. |
| `publish.reference` | Same fields as `publish`, PDF/raster/SVG/Canvas/WAV/CSV/XLSX/DAT/JSON attachment path, binary base64 up to 64 MiB | Same exact-parent/receipt machinery, native binary chunks. Requires encryption/hooks; source export emits a non-writable binding. No incoming binary source mutation. |
| `sync` | none | `{status:"replication",outcome:"completed|blocked|partial|cancelled|failed",reason?:...}` from the native role, with no private error details. |
| `delete`, `rename` | none | Always `held/client_intent_not_proven`; stock intent is insufficient. |

Example publication after status and a canonical owner's base decision:

```json
{"id":"export-2","request":{"protocol":1,"method":"publish","epoch":"EPOCH_FROM_STATUS","operationId":"source-export-2","path":"personal/note.md","baseRevision":"1-EXACT_REVISION","contentBase64":"Qg=="}}
```

`publish` only allows non-hidden relative `.md`/`.txt` paths, no traversal or
platform-ambiguous separators. It verifies the supplied native base exists,
represents a nondeleted file and has the same path. It does not validate canonical
source identity: the caller must have journaled an authorized export first.
There is no latest-revision default and no inferred rename. Unmapped creations
or path changes arriving from devices must remain held by the source owner.

Leaf evidence includes `id`, `revision`, `path`, `ancestors`,
`ancestryAvailable`, `ancestryCompleteToRoot`, `logicalDeleted`, `branchDeleted`,
`kind`, and `disposition`. Ancestors are nearest-first and can be pruned. A current
root with no known LOOM mapping is not a proven base. `available` bytes mean local
transport content only; they do not mean safe canonical publication. Changes are
PouchDB change pages with current leaves, not an audit history of every edit.
Changed system/chunk records are skipped while the cursor still advances; content
may need a later explicit exact-revision retry after chunks arrive.

The caller commits a batch's pending bytes or durable retry records before saving
`nextSequence`. It must not advance on error. Handle page resumption even if a
page returns no file changes. Missing revisions, `content_unavailable`,
`base_unavailable`, epoch mismatch and deleted leaves require durable holds.
Native exceptions are reduced to `revision_unavailable` or
`native_operation_failed`; validation errors use stable codes. The caller owns
bounded retries and exposes unresolved states to the user.

## Publication and recovery ownership

`operationId` is a caller-generated `[A-Za-z0-9_-]` token, 1–128 characters.
The adapter writes an unreplicated native `_local/loom-publication-*` receipt
before publishing. It records a request fingerprint, then the returned revision.
Reusing the same ID/request returns the same result; a different request is an
error. If a crash falls between the native content write and completion receipt,
retry returns `held/publication_outcome_unknown`, without duplicating the write.
This is deliberately a hold, not an exactly-once transaction claim. No automatic
repair API guesses which matching revision to use; the integrator must reconcile
that held export through exact leaves/bytes and the source journal.

These receipts are local transport deduplication only. They are not replicated,
not a canonical pending journal, and not preserved by resetting the replica.
LOOM must durably retain operation IDs, bases, variants and returned mappings.
After an epoch change, consult that source custody before retrying an export.
Do not equate transport publication or `sync` completion with device delivery,
canonical source acceptance, source-write crash safety or Notes indexing.

## Integration boundary

The paired source journal, client intent/base extension and worker are implemented
in `internal/notesworkspace`, `internal/notesworkspacesync`, and the
[client overlay](../notes-workspace-client/README.md). The Nix module packages the
headless transport for the Linux reference deployment. Mac and iPhone client
editing, offline reconnect and source-checked text resolution have been exercised
in development; this is not a guarantee for every platform or corpus.

Reference-only authorization and membership withdrawal belong to the source
owner. Native replication does not confer permission to write canonical data.
See [the Notes guide](../../docs/user-guide/notes-and-search.md) for the complete
workflow and [recovery guidance](../../docs/operations/backup-restore-and-drills.md)
for the distinction between transport state and a recoverable source cohort.

## C4a bridge IPC

The build now shares `protocol.ts` and `control.ts` from the pinned client overlay.
After installing the exact lockfile, apply that overlay before building this CLI.
C4a refuses encrypted control methods unless the optional commonlib hook is
present. `overlay.py OWNED_UPSTREAM` refreshes only this module and shared codec
copies in an existing verified checkout; reuse it instead of redownloading deps.
Then run `npm run check --workspace self-hosted-livesync-cli` and the build.
The headless adapter installs the optional namespace hook and suppresses all
filesystem reflection: replica storage is never a mirror of canonical notes.

Additional protocol-1 calls (same epoch fence):

| Method | Request | Result |
| --- | --- | --- |
| `status` | unchanged | Also `encryptionEnabled` and `clientIntentVersion` (1 only with hooks). |
| `read.path` | exact `path`, `revision` | Resolves native document ID using native path service, returns existing exact-read evidence, including logical deletion metadata. Reserved paths refused here. |
| `control.read` | native `id`, exact `revision` | Same contentBase64/SHA/ancestry envelope, only for a valid reserved native control with exactly one immutable root leaf; missing chunks/collisions held. |
| `control.put` | UTF-8 strict control JSON encoded as `contentBase64` | `published`, exact native `revision`, reserved `path`; uses ordinary native note chunks/encryption and immutable collision checks. |

`changes` classifies reserved records as `kind:"control"`. Ordinary native files
remain insufficient source intent. Input **and output** lines are bounded to
2 MiB; decrease the changes page size if a leaf-heavy page exceeds that bound.
Transport SHA strings omit the `sha256:` prefix; the client wire fields include
it. Control evidence preserves the exact encoded intent bytes for digest joins.

`internal/notesworkspacesync` consumes these calls. It alone joins source-issued
binding maps, exact native parents/payloads, intent/publication pairs and durable
source outcomes. Incoming binding/ack documents never create server authority.
Missing native chunks/receipt parts are retried through persisted observations;
stock writes remain inspectable holds. No new sync engine or network service was
introduced. See the C4a runtime handoff in the client-intent feature.
