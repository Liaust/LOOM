# LOOM

**A self-hosted coordination layer for your machines, projects, knowledge, and AI agents.**

LOOM connects ordinary files and repositories to persistent services: search,
qualified project context, background jobs, application deployment, and recovery.
The aim is to give people and agents one understandable system to work with,
without making an agent harness the owner of everything.

**Developer preview.** This is an actively developed, working personal system,
not a finished appliance. The first public source baseline exposes the code,
documentation, limitations, and roadmap. Installation is still hands-on;
interfaces and migrations can change. There is no hosted LOOM service or
one-command installer yet.

[Documentation](docs/README.md) · [Current State](.project/STATE.md) ·
[Roadmap](.project/ROADMAP.md) · [Contributing](CONTRIBUTING.md) ·
[Security](SECURITY.md)

## Why LOOM Exists

An agent can write code or answer a question. It still needs somewhere to keep
work, discover the right project, retrieve an earlier decision, run a service,
and know whether the result is backed up. Connecting those pieces independently
usually leaves a collection of scripts and hidden assumptions.

LOOM makes those relationships explicit. Projects remain normal folders. An
agent edits files normally and declares the resources that need system support.
LOOM can then plan, apply, inspect, and recover that declared work. Technical
state, source material, and interpreted decisions stay distinct rather than
being flattened into one opaque "memory" database.

## What It Provides

| Area | What is implemented |
| --- | --- |
| Projects | Minimal folders, explicit resource declarations, development context, resumable plan/apply/status operations, and repository discovery. |
| Execution | Typed capabilities exposed by providers, routed jobs, permission policy, durable events, schedules, and background workers. |
| Files and recovery | Box workspaces, node synchronization, storage catalogs, physical archive/restore workflows, operational database packages, and Borg cloud history. |
| Knowledge | Notes ingestion and source citations, lexical/semantic retrieval infrastructure, and separate technical object inspection. |
| Provenance | Source-backed candidates, accepted records, reconciliation cases, supersession, and project/repository state projections. |
| Applications | A managed application path that composes host grants, storage, credential references, startup readiness, and optional public HTTPS. |
| Interfaces | CLI, terminal Portal, HTTP API, node agent, and a small status dashboard. |
| Agent integration | Portable skills and protocols, optional Hermes/MINA runtime configuration, ORCA integration, and an operator-paired Mac computer-use bridge. |

These are implemented surfaces, not a claim that every combination has completed
real-world acceptance. See [current state](.project/STATE.md) for the remaining
gaps, especially installation, full document-processing coverage, and larger
project lifecycle tests.

## How The Pieces Fit

**LOOM is not an LLM harness.** It can serve agents, but does not replace their
reasoning loop, model provider, conversation interface, or native tools.

| Component | Owns |
| --- | --- |
| **LOOM** | Project/resource declarations, coordination, capability policy, technical state, knowledge/provenance services, storage and recovery integrations. |
| **Hermes** | Its agent loop, sessions, conversational memory, tools, skill runtime, and messaging gateway. Native Hermes schedules are not automatically LOOM schedules. |
| **MINA** | An optional example persona, protocols, and configuration for a Hermes agent. Neither MINA nor her external accounts are required for LOOM's core. |
| **ORCA** | Its desktop/remote workspace experience, terminals, coding sessions, and worktrees. |
| **Codex and other coding agents** | Work performed inside their own harness and project checkout, using the project's instructions and declared LOOM interfaces. |
| **PostgreSQL, Borg, Caddy and other dependencies** | The underlying database, archive, ingress, and supporting engines. LOOM configures and integrates them; it does not claim to implement them. |

The Main node coordinates the system and owns global knowledge and archive
services. Other nodes own their local files and expose configured capabilities.
The reference deployment uses NixOS on Main and a macOS workspace node. A private
network and an optional edge proxy connect them; public exposure is an explicit
operator configuration, not a default discovery mechanism.

### Where Agents Connect

The harness runs the agent. Project instructions and LOOM skills tell it which
interfaces to use; the agent calls the CLI or an authorized API integration.
Those calls reach `loomd`, which handles the relevant project, search, job or
capability operation and returns structured results and errors. Skills are
instructions, not the transport or an additional execution engine.

Ordinary code edits and Git work remain in the agent's own workspace. They do
not need a LOOM operation for every action. Conversely, starting Hermes through
LOOM does not automatically put every shell command, native memory write or
Hermes cron job under LOOM's event and policy model. See
[agent and system boundaries](docs/concepts/modules-connectors-and-agents.md).

> **Visual placeholder: system map.** Planned code-rendered map of nodes,
> projects, capabilities, storage, knowledge, and agent runtimes.

> **Visual placeholder: terminal Portal.** A real screenshot of the public
> preview, using non-private example data.

> **Visual placeholder: network.** Planned code-rendered Main/workspace/edge
> topology. No generated decorative illustration is required.

## Start With The Source

You can inspect the CLI and documentation without installing services. With
[Go](https://go.dev/dl/) matching `go.mod` (currently 1.25.7 or newer):

```sh
git clone https://github.com/Liaust/LOOM.git
cd LOOM
go run ./cmd/loom --help
go run ./cmd/loom docs --docs-dir docs search "projects"
go build -o ./bin/loom ./cmd/loom
```

Most operational commands require a configured daemon, database, node identity,
and authorized connection. Building the CLI does not create them. Start with
the [installation guide](docs/operations/installation.md) and
[Nix configuration notes](nix/README.md). Do not apply the example hardware
configuration to an existing machine unchanged.

On an already configured installation, the everyday entry points are:

```sh
loom status
loom health
loom enter
loom project context <project-ref-or-path>
```

A new project has `.loom/` for resource declarations and `.project/` for portable
development state, alongside ordinary `notes/` and `repos/` folders. Git is an
explicit choice, not a side effect of project creation. Legacy `.repo/` contracts
remain supported for existing repositories. See the [project guide](docs/user-guide/projects.md).

## Search Is Not One Thing

- **Objects** answer technical questions about identity, files, versions, and
  processing state.
- **Notes** find source material and return citations. Enrolling a source does
  not turn its contents into accepted decisions.
- **Provenance** retrieves qualified decisions, constraints, preferences, and
  unresolved cases with their sources and lifecycle. A pending candidate is
  searchable, but is not an accepted fact or permission to act.
- **Hermes memory** supports the agent's conversational continuity. It does not
  replace LOOM's source-backed reconciliation system.

Optional embeddings, OCR, vision, and model calls depend on configured workers
and providers. "Local-first" describes ownership and deployment, not a promise
that every inference or third-party service runs offline.

## Development And Roadmap

The immediate priority is to make the existing system easier to install,
understand, and use through real projects. Next come guided CLI setup, clearer
prerequisite reporting, broader document-pipeline acceptance, project lifecycle
coverage, and a deliberate model-driven automation integration. The website,
rendered documentation, and diagrams follow this source release.

The [roadmap](.project/ROADMAP.md) separates current work from deferred ideas and
does not promise release dates. Contributions, bug reports, documentation fixes,
and focused design discussions are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License And Credits

Original LOOM work is licensed under [Apache-2.0](LICENSE). Third-party code,
fonts, patches, and separately fetched packages retain their own licenses;
see [NOTICE](NOTICE) and [third-party notices](THIRD_PARTY_NOTICES.md).
LOOM is an independent project by Leonardo Miranda. It is not an official
Hermes, ORCA, OpenAI, or dependency-vendor product.
