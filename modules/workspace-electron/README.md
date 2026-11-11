# Loom

Desktop Electron workspace in development: Files, Projects, Activity, Schedules
and System. Dark, keyboard-oriented UI with a persistent shallow file tree.

## Run

```sh
cd modules/workspace-electron
npm ci
npm start
```

Uses an operator-configured SSH connection, default alias `loom-main`, with
private Unix-socket forwarding. Configure an authorized network route to Main,
such as WireGuard, and the matching Files and native Notes endpoints. The source
supplies no host access, account setup, public listener or renderer credentials.
Run these commands from a clone of this repository with Node 24 or newer and the
Go toolchain from go.mod. The prestart/pretest steps build the local filesystem
helper; it is not a pre-existing maintainer binary. Connection settings are
operator-owned; an SSH alias in an example does not provide credentials.

## Files And Independent Local Notes

Finder file/folder drops onto a Box directory, and its Upload action, copy
originals to Main through one bounded resumable lane. Originals stay on the Mac.
Keep both is the default; there is no overwrite mode. Hidden/rebuildable paths,
links, special files and files above 2 GiB are excluded visibly. Empty folders
are retained. Uploads can pause/resume/cancel; restart leaves unfinished work
paused. Main stages checksummed chunks privately, verifies the complete content,
then creates the actual source file without replacing an existing entry. Upload
acceptance is not indexing, embedding, client delivery or backup protection.

Settings > Files & sync exposes current local copies, cache/pins and explicit
folder selections; it does not choose a new working set. Markdown metadata
refresh reads only current properties, aliases, inline tags and links on Main.
The explicit source-side refresh uses durable individual file/directory records,
not a periodic corpus scan. CLI: `loom files metadata start --path Notes`, then
`loom files metadata step <refresh-id>` until complete; status/latest/pause/cancel
are separate bounded operations. Paused progress survives restart. Missing files
are retired only after a confirmed absence against the pinned Box identity.

Box and Storage expose actual server paths, without requiring an index row.
Storage is read-only. Folder expansion is shallow/paged; annotations arrive
separately. Local/cache/remote availability is separate from indexing, embedding,
OCR and vision status. Unknown evidence is not a processing failure.

Opening a supported writable Box note checks out an exact source base into this
application's own working set. Obsidian is not required. Source-relative files
are stored under private app data `native-client/working-set/box/`; record-level
SQLite state/outbox lives alongside them. Existing Obsidian files, plugin state,
pending operations and recovery history are not imported or modified.

Native editing supports visible Box text, common source/configuration formats,
Canvas and Bases up to 512 KiB without requiring Notes enrollment. New filenames
without an extension default to `.md`; an explicit supported extension is kept.
Writable Markdown and source text open ready for typing, without an Edit gate.
Markdown uses live formatting over its exact source: headings, emphasis, lists,
tasks and links remain formatted while editing. Nearby syntax can reveal at the
caret. The optional source button does not control write permission. Source and
configuration files use a monospaced editor with line numbers. CodeMirror owns
the text, selection, undo and incremental Markdown parser; rendered HTML is never
serialized back over the source file. Accepted extension changes reconfigure the
same editor without replacing its draft/history. Changing a filename is not binary conversion.
Larger or non-text originals remain browsable/read-only. Each save
persists the local content and immutable operation before upload. Main's existing
Notes owner supplies exact-base conflict handling, recoverable path operations,
durable acceptance and idempotent replay. Offline edits survive restart. A local
save is not Main acceptance or backup coverage. Conflicts retain both versions;
they are never resolved by silently refreshing the submitted base. Typing
coalesces into an automatic local save after 750 ms, with draft preservation
after 350 ms. Explicit save remains available. A completed older save cannot
clear a newer dirty draft; closing flushes local work and retains save failures.
Local save is independent of network availability; Main acceptance follows the
ordinary durable outbox. This does not enable indexing or change download rules.

Local edits and subscribed Main directory changes trigger sync. Notifications
are shallow hints with current-source rechecks; reconnect refreshes the retained
working set. No periodic vault scan or database polling worker. Up to 64 parent
directories are live-subscribed; exceeding that reports an explicit warning,
not silent full-vault coverage. Local parent watches are bounded at 1024. The
working set admits up to 100,000 records / 256 MiB of text; existing and pending
content is never evicted to meet those bounds. Complete whole-vault watching and
ownership migration remain unfinished.

Folder actions and the command palette offer explicit offline download preview,
start, progress, pause and resume. A record-level SQLite traversal retains its
page and entry cursors. Restart pauses the operation until resumed; a changed or
expired Main cursor also pauses instead of silently restarting the whole tree.
Each operation is bounded to 256 MiB / 100,000 entries, one download at a time.
Text files retain existing source permissions, other requested assets are pinned
read-only copies. Download does not enroll or index files. Completion describes
the observed inventory, not future additions or device-wide backup coverage.
Saved directory pages support offline navigation (bounded to 8192 pages).

Relative links, wikilinks, headings and local image attachments navigate through
the same physical roots and preview owner. Ambiguous downloaded titles require
an explicit choice. Rendered links navigate by click when there is no active
text selection. Remote image embeds are links, never tracking-pixel fetches.
CSV/TSV has a virtualized cell editor with display-only filtering/sorting,
explicit delimiter conversion, undo and preserved string values/newlines.
Larger tables parse in a bundled local worker; at most 55 rows and 20
columns are rendered together. File-size/write limits still apply.
JSON Canvas supports text/file/link/group cards, connections, drag/resize,
pan/zoom, fit-to-content, removal and undo. New cards avoid covering existing
cards; armed connections can be cancelled with Escape. Unknown format fields
and IDs are preserved. Both
formats retain source mode, including read-only originals; neither executes
embedded code or spreadsheet formulas.

Bases supports table/cards/grouping, common filters, formulas and sorting using
parsed YAML and a restricted expression interpreter, never JavaScript evaluation.
Visual configuration preserves unrelated YAML. Unsupported expressions/views
fail visibly. Results use Main-known metadata plus local edits, not just the
downloaded working set, but are explicitly not a complete filesystem inventory.

## Focus, Knowledge And History

The Explorer's focus action makes one actual folder the sidebar root, without
moving it or changing registration, enrollment, indexing or offline selection.
Global/focused expansion and scroll are separate. Search and creation follow
the explicit focus. The regular tree remains the physical Box/Storage tree.

Knowledge navigation uses existing type/subject/people properties and tags;
Subjects, People, Reading, Concepts, History and saved Bases lead to real paths.
Main owns the derived metadata catalog. Queries are bounded and cached in the
existing private client database (16 MiB); pending local notes overlay those
hints. Folder scope is explicit, with an All known files view for unclassified
content. An initial read examines at most 10,000 catalog candidates, including
missing old paths; Load more retains its cursor and continues in bounded windows
up to 10,000 present files per snapshot. Loaded depth survives requested
refreshes. Cache age alone never restarts the query; source invalidation or
explicit refresh does. Source observations, older indexed hints and names-only
entries remain distinct. Missing metadata is unknown, not a missing file or
enrichment request. No new crawler, embedding work or periodic graph worker is
started. Bases infer a query scope only from mandatory literal folder filters,
never from an unrelated Explorer focus.

Graph views offer local one/two-hop links, the focused scope and a grouped
folder overview. Sigma/Graphology render at most 400 nodes/2,000 edges, with
explicit unresolved/ambiguous counts. Relatedness is not authority, recency or
agreement. Graph resources are released on navigation.

Timeline reads existing Box source receipts/path history, including grouped
populated-folder moves. It does not provide complete historical filesystem,
Git, semantic milestone or Storage custody history. Recorded dates are immutable
receipt creation, not authorship or final acceptance. Recent local pending
operations remain distinct. Retained UTF-8 pairs can be compared read-only, up
to 256 KiB each; binaries and missing revisions remain metadata-only. There is
no restore action or new snapshot store. Timeline and graph views never turn on
indexing or bulk downloads. Timeline filters, loaded receipts and the selected
day survive Back/Forward navigation. Inclusive date ranges follow local calendar
days across daylight-saving changes; invalid ranges leave their controls usable.

Text creation, native note rename and recoverable source removal use that owner.
New move/removal requests are Main-first: the durable request appears as pending,
but the Mac working path changes only after Main's exact acceptance receipt.
Offline requests retain their current local path and pending destination until
reconnection; downloaded notes can still be edited while a move is pending.
Those edits follow the accepted move's new path and base. A lost response replays
the original request, not an inferred delete/create or a second upload.
Accepted moves preserve the local inode and retire the old working path. Removed
local content goes to private recovery. A conflicting local destination is never
overwritten: Activity distinguishes Main acceptance from local recovery needed.
Open tabs and drafts follow actual placement, not the unconfirmed request.
Older already-saved optimistic operations retain their original semantics.
Remove retains Main's source recovery journal, not the macOS Trash. Populated
folder rename/move has a durable Main-derived preview and explicit confirmation.
It renames the original same-filesystem directory, retains descendant identities
and relocates journal custody without rewriting original edit receipts. Only
already-local working files follow on the Mac; remote assets are not downloaded.
Pending local edits must finish before confirmation; edits during the brief
handover stay as editor drafts. Mounts, symlinks, collisions and registered
source/project-root relocation remain refused. Cross-device moves and recursive
removal are not implemented. Previous Obsidian aliases are not new app placement rules. The old local
vault is hidden recovery-only, not a competing synchronization root.

## On-Demand Data

Small assets share a complete cached download; larger files use range requests.
The 1 GiB/4096-entry cache is separate from writable local files and drafts.
Explicitly pinned files and active readers cannot be evicted. A complete cached
file is limited to 256 MiB; larger originals remain stream-only. Offline copies
remain qualified until their source can be checked.

## Other Sections

Projects use registered state, declarations and real physical roots. Completed
archives navigate Storage from the retained matching receipt, without expensive
live repository observations. Legacy active projects remain in the Working
filter without writing an invented work-state field.

Activity combines bounded job/indexing inventories with the native edit journal.
LOOM schedule mutations use supported owner APIs and create paused schedules;
Hermes inventory is read-only until its native mutation adapter exists. System
metrics are requested on entry/refresh, never background-sampled by this app.
Protection loads separately and remains evidence-qualified.

This file workspace is not an agent harness. Hermes owns its conversations,
profiles and agentic routines; Codex owns its native coding sessions and managed
worktrees. LOOM owns coordination, capabilities and system jobs. ORCA is retired,
not a desktop or lifecycle fallback. Opening a Box changes file context, not an
existing conversation's identity, saved job context or external account grant.
Preserve native learned state, pending edits and recovery history independently
of app updates. Missing bindings do not select another account automatically.

## Boundaries And Checks

Sandboxed renderer, fixed validated IPC, sanitized Markdown, opaque previews,
no arbitrary shell/API access from documents. HTTP(S) external links require
an explicit click. No phone work, signed distribution, raw database editor or
live Obsidian owner handover is included.

## Local macOS Bundle

`npm run package:mac` stages an allowlisted local application under the repository's
`.loom-acceptance/workspace-desktop-v1/packages/`. It includes pinned production
dependencies and fonts, not user files, outboxes, credentials or development
tooling. The display name is Loom; private app data remains **LOOM
Files**, preserving the current native journal, working set, drafts and cache.
The local bundle is not notarized or a public release. Signed automatic updates
require a separately configured, verified release channel; local packaging does
not establish one.
`LOOM_WORKSPACE_UPDATE_REPOSITORY=owner/repository` selects an operator-owned
GitHub release feed. It is empty by default. Only an installed, signed build
with a valid configured channel can offer an update; merely setting this value
does not sign a package or authorize access to a private repository. The app
validates exact asset origin/repository and signature expectations before an
explicit install/relaunch, and preserves its separate user-data directory.
Close the old instance gracefully before opening a new build. Never replace or
merge the private data folder during an update.
Packaging builds a small native no-replace directory-move helper. For an
unpackaged development session, build it first with
`go build -o .loom-acceptance/workspace-desktop-v1/loom-workspace-fs ./cmd/loom-workspace-fs`
from the repository root.

Respect the installation's actual Notes-processing and legacy delivery policy;
this source does not establish a live pause, activation or acceptance receipt.
Existing indexed searches may be stale. Opening a folder or starting this app
does not resume paused workers or start a bulk download. Historical Obsidian
state remains recovery data, not a second active file owner.
Focused checks: `npm test`, `npm run check`, and the affected existing Go tests.
Native desktop acceptance and remaining owner operations are tracked separately
from source implementation and deployment.
