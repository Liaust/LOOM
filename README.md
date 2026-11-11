# LOOM

**A self-hosted workspace for files, knowledge, projects and AI agents.**

LOOM connects an ordinary filesystem to a desktop workspace and a persistent
server. Write a note, open a project, retrieve an earlier decision, run an
application or inspect a failed job without assembling a different system for
each task. Your files remain files; an index or agent conversation is not their
replacement.

**Public beta, under active development.** The source includes the Electron
desktop app and its server-side owners. It is not a hosted service, stable 1.0
API or turnkey appliance. A source build is not a configured installation.
Signed desktop distribution and a standalone mobile client are not available
from this source snapshot.

[Documentation](docs/README.md) · [Desktop guide](docs/user-guide/workspace.md) ·
[Installation](docs/operations/installation.md) · [Current state](.project/STATE.md) ·
[Roadmap](.project/ROADMAP.md) · [Changelog](CHANGELOG.md) · [Security](SECURITY.md)

## One Workspace, Ordinary Files

The desktop app exposes actual Box, Storage and application-data locations, not
another export of the same notes. Browse folders without first registering every
file in a knowledge index. Focus the explorer on one project without moving it.

- **Box** is writable work: notes, documents, topics and projects. Separate Boxes
  can keep different work contexts and agent defaults without pretending to be
  separate operating-system security boundaries.
- **Storage** is retained or archived material, browsed read-only. Project
  archive/restore has its own reviewed lifecycle; restoring files does not
  silently restart their services.
- **Application Data** exposes supported application-owned resources through
  their owner, rather than treating every database or archive as an editable note.

Opened working text has a durable local copy and pending-operation journal.
Explicit selections can keep a larger working set offline. Large attachments
can remain on Main and enter a bounded cache when opened. Local availability,
source acceptance, indexing and backup protection are separate facts.

Moves and other managed operations are Main-first. Offline requests remain
pending while local notes stay editable; reconnect replays the original request.
Conflicts and changed destinations preserve work rather than overwrite it.

## What You Can Work With

| Surface | Available in the source |
| --- | --- |
| Files and editing | Persistent folder tree, tabs/history, keyboard command palette, live Markdown, editable YAML properties, wikilinks, tags, source syntax highlighting and image/PDF previews. |
| Structured notes | CSV/TSV cell editing, JSON Canvas and a supported subset of Bases filters/formulas/views. Unknown fields are preserved; spreadsheet formulas and embedded code are not executed. |
| Knowledge navigation | Source metadata, backlinks, categories, bounded link graphs and recorded file timelines. Missing metadata does not make a file disappear from the filesystem. |
| Projects and applications | Ordinary project folders, optional repositories, portable development context, resource plan/apply/status, active/passive/archived lifecycle and managed application prerequisites. |
| Operations | Project views, activity, schedule controls and system status, with a CLI/API for deeper inspection. |
| Search and enrichment | Indexed source retrieval and citations; optional embeddings, OCR and image descriptions, including selective later enrichment. |
| Provenance | Qualified source-backed candidates, decisions, corrections, supersession and reconciliation. Searchable is not the same as accepted or authoritative. |
| Recovery | Local operational packages, source/recovery inventories, archive custody and deduplicated cloud history with explicit retention and coverage evidence. |

The [desktop guide](docs/user-guide/workspace.md) describes bounds and unsupported
cases. Graphs and Bases use known metadata, not a guarantee of complete corpus
coverage. Timelines show recorded operations, not every historical filesystem
change. Read-only origins remain read-only in structured editors.

## Agents Use The Same System

LOOM is **not an agent harness**. Native Hermes owns conversational profiles,
sessions, memory, skills, messaging and agent schedules. LOOM supplies the
filesystem, project/knowledge/provenance services, system operations and the
adapters that connect selected work. Codex and other coding agents keep their
own harnesses, project instructions and tools.

MINA is an optional example identity, not a required account or the system's
name. Native profiles can have distinct work contexts and conversations. Tools
and credentials require operator configuration; installing a skill grants no
access. Shared Unix accounts and Box policies are not hard per-agent isolation.
ORCA is retired from the current interaction model; retained compatibility code
does not make it a dependency of the shared agents account.

LOOM, an external task interface such as Basecamp, and Provenance have different
jobs: system/files/knowledge, executable task state, and qualified decisions.
There is no mandatory sequence of tool calls or requirement to use Basecamp.
Known files can be read directly; semantic search helps when the location or
relevant context is unknown.

## Build And Inspect

Use the Go version declared in `go.mod`:

```sh
git clone https://github.com/Liaust/LOOM.git
cd LOOM
go run ./cmd/loom --help
go run ./cmd/loom docs --docs-dir docs search "workspace"
go build ./cmd/...
```

For the desktop client, use Node.js 24 or newer and npm:

```sh
cd modules/workspace-electron
npm ci
npm test
npm start
```

The desktop uses a configured private SSH connection and a Unix-socket API on
Main. It does not create a server, expose a public listener or authenticate your
accounts. Follow [installation](docs/operations/installation.md) and the
[client README](modules/workspace-electron/README.md). Main's reusable Nix modules
are included; the reference hosts are synthetic evaluation examples, not a
configuration to apply unchanged to an existing machine.

## Ownership, Limits And Recovery

Main owns authoritative shared state and service execution. Clients own their
local working copies, drafts and pending edits. Network reachability does not
turn all server storage into a local disk. Select what to index, enrich and keep
offline; reading a folder should not launch a bulk processing campaign.

Sync is not backup. A successful cloud run protects its exact selected capture,
not unsent offline edits or every live application's state by implication.
Private credentials, host bindings, user notes, learned agent profiles and
operational archives are not distributed with this repository.

Installation and platform support remain hands-on. The reference topology is
NixOS Main plus a macOS desktop client; other package targets are not a universal
runtime-support claim. Voice/wake reliability, mobile delivery and signed native
update acceptance remain separate work. Development builds do not auto-update;
an operator-owned signed release channel must be configured explicitly.

## Development And License

See the [roadmap](.project/ROADMAP.md) and [contribution guide](CONTRIBUTING.md).
Focused fixes, useful examples and clearer installation workflows are welcome.
There is no support SLA or claim of universal search latency or recovery coverage.

Original LOOM work is [Apache-2.0](LICENSE). Dependencies, fonts and upstream
patches retain their own licenses; see [NOTICE](NOTICE) and
[third-party notices](THIRD_PARTY_NOTICES.md). LOOM is an independent project by
Leonardo Miranda, not an official Hermes, OpenAI or dependency-vendor product.
