package projectcontracts

import "embed"

// repositoryDevelopmentPackTemplates is copied only into a new LOOM project's
// project-local agent-pack directory. It is never installed into a repository.
//
//go:embed all:repo_development_pack
var repositoryDevelopmentPackTemplates embed.FS

const rootContractTemplate = `kind: loom.project
schema_version: project.contract.v0.4

project:
  id: {{.ProjectID}}
  slug: {{.Slug}}
  name: {{printf "%q" .Name}}
  description: ""
  owner_node: {{.OwnerNode}}
  status: draft

facets:
{{- range .Facets}}
  {{.}}: true
{{- end}}

provider_defaults:
  scripts_provider: project
  workflows_provider: project
{{if or .HasSyncPolicy .HasBackupPolicy .HasWorkerPolicy .HasSecrets}}
policies:
{{- if .HasSyncPolicy}}
  sync: .loom/contracts/sync.yaml
{{- end}}
{{- if .HasBackupPolicy}}
  backup: .loom/contracts/backup.yaml
{{- end}}
{{- if .HasWorkerPolicy}}
  workers: .loom/contracts/workers.yaml
{{- end}}
{{- if .HasSecrets}}
  credentials: .loom/contracts/credentials.yaml
{{- end}}
{{end}}
portal:
  display_group: projects
  summary: ""

metadata:
  scaffolded_by: loom
  scaffold_preset: {{.Preset}}
`

const loomGitignoreTemplate = `state/
tmp/
`

const noteContentTemplate = `---
title: {{printf "%q" .Title}}
created_at: {{printf "%q" .CreatedAt}}
updated_at: {{printf "%q" .UpdatedAt}}
---

# {{.Title}}

LOOM indexing reads these timestamps but never rewrites source frontmatter.
Update ` + "`updated_at`" + ` when the note changes materially.
`

const datedFileManifestTemplate = `file:
  path: {{printf "%q" .Path}}
created_at: {{printf "%q" .CreatedAt}}
updated_at: {{printf "%q" .UpdatedAt}}
metadata: {}
`

const datasetManifestTemplate = `dataset:
  key: {{printf "%q" .DatasetKey}}
  title: {{printf "%q" .Title}}
created_at: {{printf "%q" .CreatedAt}}
updated_at: {{printf "%q" .UpdatedAt}}
sources: []
`

const conciseRootReadmeTemplate = `# {{.Name}}

This is a LOOM project. Human-authored work stays in the visible project
folders; portable LOOM metadata and guidance live under ` + "`" + `.loom/` + "`" + `.

- Project slug: ` + "`{{.Slug}}`" + `
- Owner node: ` + "`{{.OwnerNode}}`" + `
- Preset: ` + "`{{.Preset}}`" + `
- Enabled facets: ` + "`{{.FacetList}}`" + `

Start with:

` + "```sh" + `
loom project validate .
loom project plan .
` + "```" + `

See ` + "`.loom/project.yaml`" + ` for project identity and ` + "`AGENTS.md`" + ` for the
agent entry point. Registration and activation remain explicit operations.
`

const conciseRootAgentsTemplate = `# Project Agent Entry Point

<!-- loom-managed: project-agent-entry/v1 -->

Your default working boundary is this project root.

Before changing content or contracts, read ` + "`.loom/project.yaml`" + `,
` + "`.loom/agents/project.md`" + `, and the relevant files under
` + "`.loom/agents/surfaces/`" + `. Use ` + "`search-loom-docs`" + ` for authoritative current
guidance and ` + "`manage-loom-projects`" + ` for project lifecycle work.

Edit canonical project sources only. Never edit generated ` + "`loom-storage`" + `
views. Validate with ` + "`loom project validate .`" + ` and review
` + "`loom project plan .`" + ` before registration or activation. Escalate node,
storage, backup, credential, or LOOM-core defects with evidence instead of
broadening the project boundary.

Skills provide instructions; they do not grant authority. Proton Pass is the
credential authority. Keep credential values out of project files. The current
` + "`.loom/contracts/credentials.yaml`" + ` schema uses logical references bound to
environment variables or absolute files; direct Proton bindings are deferred.
Do not store host-specific absolute paths, private agent memory, or identity
instructions here.

The optional repository-development pack lives at
` + "`.loom/agent-packs/repo-development/`" + `. It is reference and template material,
not an installed repository control plane. Only a repository-leading agent may
explicitly opt a repository in by creating and reviewing its own Git-tracked ` + "`.repo/`" + ` commit.
LOOM scaffolding, validation, and planning never copy, update, or apply the pack
inside repositories.
`

const projectAgentsTemplate = `# {{.Name}} Project Guidance

<!-- loom-managed: project-guidance/v1 -->

This project is identified by ` + "`.loom/project.yaml`" + ` as ` + "`{{.Slug}}`" + ` and is owned
by the ` + "`{{.OwnerNode}}`" + ` node. Work with project-relative paths so the same tree
can be used from a workspace node, main, or an agent-managed checkout.

- Lifecycle state: ` + "`draft`" + `
- Enabled facets: ` + "`{{.FacetList}}`" + `

Use this loop for contract changes:

` + "```sh" + `
loom project validate .
loom project plan .
` + "```" + `

Registration, activation, watched-root application, and other runtime mutations
are separate explicit operations. Package-local manifests such as
` + "`scripts/<key>/loom.script.yaml`" + ` stay with their packages. Keep human content
in the enabled visible work surfaces and portable LOOM control material under
` + "`.loom/`" + `. Proton Pass owns credential values. The current credential
contract accepts logical references with environment-variable or absolute-file
sources; direct Proton bindings remain deferred.

This project carries an optional repository-development pack at
` + "`.loom/agent-packs/repo-development/`" + `. The pack is not installed into any
member repository. A repository-leading agent must explicitly opt in, verify
the authoritative membership and owner backlink, review the complete
` + "`.repo/`" + ` diff, and commit it in that repository. Never apply or upgrade the
pack automatically.
`

const surfaceAgentsTemplate = `# {{.Facet}} Surface Guidance

<!-- loom-managed: project-surface-guidance/v1 -->

This guidance applies to the ` + "`{{.Facet}}`" + ` facet of {{.Project}}.

- Keep authored paths project-relative.
- Treat source files and contracts as authoritative over backend projections.
- Preserve package-local manifests beside their code, fixtures, and examples.
- Never store secret values in the project.
- Run ` + "`loom project validate .`" + ` after contract changes.
`

const serviceRegistrationContractTemplate = `kind: service_registration
schema_version: loom.service.v0.1

service:
  key: example
  name: "Example service"
  description: "An already-provisioned service registered for inspection."
  target_node: {{.OwnerNode}}
  class: project

runtime:
  manager: launchd
  unit: com.example.service

operations:
  status: true
  logs: true

health:
  kind: manager
`

const rootReadmeTemplate = `# {{.Name}}

This is a LOOM project scaffold. A LOOM project is a folder that describes
something the user is working on: notes, code, scripts, automations,
connectors, modules, datasets, documentation, or any combination of those.

The folder is the source of truth. LOOM reads the contracts in this tree,
validates them, registers declared intent in the backend, and activates specific
runtime surfaces only when asked.

## Project Identity

- Project slug: ` + "`{{.Slug}}`" + `
- Owner node: ` + "`{{.OwnerNode}}`" + `
- Project provider: ` + "`{{.ProjectProvider}}`" + `
- Preset: ` + "`{{.Preset}}`" + `
- Facets: ` + "`{{.FacetList}}`" + `

## First Commands

Validate local contracts:

` + "```sh" + `
loom project validate .
` + "```" + `

Preview what LOOM would register:

` + "```sh" + `
loom project plan .
` + "```" + `

Register declared project intent:

` + "```sh" + `
loom project register .
` + "```" + `

Nothing becomes active just because a file exists. Validation, registration,
activation, and watched-root application are explicit lifecycle steps.

## Capability URLs

Capabilities use this address form:

` + "```text" + `
node@provider.endpoint
` + "```" + `

The example project script capability is:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

Project-owned scripts use the project provider by default. Connectors and
modules declare their own provider namespaces.

## Activation Order

Use this order when enabling runtime behavior:

1. Validate and plan the project.
2. Register the project.
3. Activate capabilities first, usually ` + "`scripts`" + ` or ` + "`connectors`" + `.
4. Activate automations that target those capabilities, such as ` + "`schedules`" + ` and
   ` + "`direct-events`" + `.
5. Apply watched-root policy only when the owner node should use the sync,
   backup, and indexing plan.

Example:

` + "```sh" + `
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet connectors --project-root .
loom project activate {{.Slug}} --facet schedules --project-root .
loom project activate {{.Slug}} --facet direct-events --project-root .
loom project apply-watch-policy {{.Slug}} --project-root .
` + "```" + `

## Facets

- ` + "`notes/`" + `: markdown project knowledge that can be synced and indexed
- ` + "`repos/`" + `: project-associated code and repository references
- ` + "`scripts/`" + `: executable packages that can expose project capabilities
- ` + "`workflows/`" + `: executable workflow packages and design-only workflow intent
- ` + "`connectors/`" + `: provider namespaces and grouped capability endpoints
- ` + "`schedules/`" + `: timer-based automations that call one capability
- ` + "`direct_events/`" + `: external notifications mapped into one capability call
- ` + "`modules/`" + `: larger LOOM expansion packages
- ` + "`datasets/`" + `: datasets, samples, fixtures, or external data references
- ` + "`docs/`" + `: operational documentation for humans and agents
- ` + "`secrets/`" + `: credential references only, never secret values
- ` + "`policies/`" + `: sync, backup, worker, and credential policy
- ` + "`tests/`" + `: validation and smoke-test helpers

Read facet-specific ` + "`AGENTS.md`" + ` files before changing a facet. Start with
` + "`docs/contracts.md`" + ` when creating or modifying LOOM contracts.
`

const rootAgentsTemplate = `# AGENTS

This folder is a LOOM project source tree.

LOOM is a local/distributed automation substrate. It lets a user describe work
as a project folder, then turns the folder's declarative contracts into
backend-visible project state, capabilities, schedules, direct-event endpoints,
connectors, module registrations, and watched-root policies.

From an agent or developer perspective, this project folder is the source of
truth. The backend is derived from it through explicit lifecycle commands.

## Project Mental Model

A LOOM project is anything the user is working on: notes, research, scripts,
automations, connectors, modules, datasets, documentation, or a mix of those.

The root contract is ` + "`" + `loom.project.yaml` + "`" + `. It defines:

- project identity, including slug, name, owner node, and status
- enabled facets, such as ` + "`" + `notes` + "`" + `, ` + "`" + `scripts` + "`" + `, ` + "`" + `direct_events` + "`" + `, and ` + "`" + `connectors` + "`" + `
- provider defaults for project-owned scripts and workflows
- policy files for sync, backup, workers, and credential references
- portal metadata used by the human terminal interface

Each facet folder can contain its own contracts. LOOM reads these contracts
during validation and registration. LOOM does not create backend runtime state
just because files exist.

## Lifecycle

Use this loop when developing the project:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom project doctor {{.Slug}} --project-root .
loom project diff . --project {{.Slug}}
` + "`" + `` + "`" + `` + "`" + `

Registration records declared intent in the backend. Activation creates or
updates supported runtime surfaces from that intent.

For example:

- registering records that this project declares a script capability
- activating ` + "`" + `scripts` + "`" + ` creates the provider, capability endpoint, endpoint
  version, and runtime binding
- activating ` + "`" + `schedules` + "`" + ` registers paused schedule automation targeting one
  capability
- activating ` + "`" + `direct-events` + "`" + ` registers a private direct-event endpoint and
  automation mapping targeting one capability
- applying watch policy records desired watched roots for sync, backup, and
  indexing, but the owner node-agent still applies local filesystem watchers

Deactivation disables project-owned runtime surfaces while preserving history.
It does not delete jobs, events, invocations, object metadata, or project rows.

## Capability URLs

LOOM capabilities use this address form:

` + "`" + `` + "`" + `` + "`" + `text
node@provider.capability
` + "`" + `` + "`" + `` + "`" + `

In this template:

` + "`" + `` + "`" + `` + "`" + `text
{{.ExampleCapability}}
` + "`" + `` + "`" + `` + "`" + `

means the capability runs from the ` + "`" + `{{.OwnerNode}}` + "`" + ` node, belongs to the project provider
` + "`" + `{{.Slug}}` + "`" + `, and exposes the capability endpoint ` + "`" + `hello_world` + "`" + `.

Project-owned scripts and executable workflows use ` + "`" + `{{.ProjectProvider}}` + "`" + `
by default. Connectors and modules declare their own provider namespaces in
their own contracts.

Do not change capability URLs casually. Schedules, direct events, workflows,
docs, tests, and user habits can depend on them.

## Facet Map

- ` + "`" + `notes/` + "`" + `: durable markdown knowledge; can be synced, indexed, and backed up
- ` + "`" + `repos/` + "`" + `: project-associated repository or code references
- ` + "`" + `scripts/` + "`" + `: executable script packages; can expose project capabilities
- ` + "`" + `workflows/` + "`" + `: executable workflow packages and design-only workflow intent
- ` + "`" + `connectors/` + "`" + `: grouped provider namespaces and capability endpoints
- ` + "`" + `schedules/` + "`" + `: time-based automations that call one capability
- ` + "`" + `direct_events/` + "`" + `: external notifications mapped into one capability call
- ` + "`" + `modules/` + "`" + `: larger LOOM expansion packages and module registration contracts
- ` + "`" + `datasets/` + "`" + `: project datasets or dataset references
- ` + "`" + `docs/` + "`" + `: project documentation for humans and agents
- ` + "`" + `secrets/` + "`" + `: secret references only, never secret values
- ` + "`" + `policies/` + "`" + `: sync, backup, worker, and credential reference policy
- ` + "`" + `tests/` + "`" + `: validation and smoke scripts for this project

Read the facet-specific ` + "`" + `AGENTS.md` + "`" + ` file before editing a facet.

## Safe Editing Rules

- Treat ` + "`" + `loom.project.yaml` + "`" + ` as the root project contract.
- Edit source contracts and source files; do not invent backend IDs by hand.
- Keep contract names, keys, slugs, provider keys, and endpoint names stable
  unless all dependent targets are updated.
- Store credential names and references only; never store secret values.
- Do not assume validation, planning, or registration starts background work.
- Do not assume scaffolded schedules, direct events, scripts, connectors, or
  modules are active.
- Prefer small, deterministic examples that can be validated and smoke-tested.
- Run ` + "`" + `loom project validate .` + "`" + ` after changing contracts.
- Run ` + "`" + `loom project plan .` + "`" + ` to inspect the read-only registration plan before
  registration or activation.

## Common Development Paths

To expose a new script as a capability:

1. Create ` + "`" + `scripts/<script_key>/loom.script.yaml` + "`" + `.
2. Add an executable entrypoint inside the script package.
3. Add ` + "`" + `scripts/<script_key>/loom.exposure.yaml` + "`" + `.
4. Set ` + "`" + `expose.enabled: true` + "`" + `.
5. Validate and plan.
6. Register the project.
7. Activate the ` + "`" + `scripts` + "`" + ` facet.

To add an automation:

1. Expose or identify the target capability.
2. Add a schedule or direct-event contract.
3. Set the target to exactly one capability URL.
4. Add deterministic example input or payload fixtures.
5. Validate and plan.
6. Register the project.
7. Activate the target capability facet first, then activate the automation
   facet.

To add sync or backup behavior:

1. Update notes/repos/policy contracts.
2. Run ` + "`" + `loom project watch-plan .` + "`" + `.
3. Register the project.
4. Run ` + "`" + `loom project apply-watch-policy {{.Slug}} --project-root .` + "`" + `.
5. Check ` + "`" + `loom project sync-status {{.Slug}}` + "`" + `.
6. Check ` + "`" + `loom project backup-status {{.Slug}}` + "`" + `.

If something is unclear, prefer adding documentation or examples before adding
new runtime behavior.
`

const notesReadmeTemplate = `# Notes

Use this folder for durable markdown knowledge about the project.

Notes are meant to survive beyond a single terminal session or chat. They can
become synced, indexed, and queryable project knowledge when the owner node
applies the project watch policy.

## What Belongs Here

- research notes
- decisions and rationale
- implementation outlines
- operating notes
- source summaries
- project status notes
- explanations of scripts, connectors, schedules, direct events, or modules

Prefer small named markdown files over one large scratchpad.

## Notes Contract

` + "`loom.notes.yaml`" + ` declares notes sync/index intent.

In this template:

` + "```yaml" + `
notes:
  sync: true
  index: true
  backup: true
` + "```" + `

That means notes are intended to become synchronized/indexed knowledge while
the full notes folder is preserved as backup material. Markdown/text files are
eligible for the text pipeline; attachments are backed up without becoming text
objects by default.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project watch-plan .
loom project register .
loom project apply-watch-policy {{.Slug}} --project-root .
loom project sync-status {{.Slug}}
` + "```" + `

Validation and registration do not scan the folder by themselves. The owner
node applies watched-root desired state after the explicit apply step.

## Writing Rules

- Use markdown.
- Keep headings descriptive.
- Include source links or references when useful.
- Include capability URLs when documenting LOOM capabilities.
- Include schedule keys or direct-event keys when documenting automations.
- Do not store secrets, API keys, private payloads, or one-time codes.
- Do not put generated artifacts here unless the user wants them indexed.

## Notes Versus Docs

Use ` + "`notes/`" + ` for durable project knowledge.

Use ` + "`docs/`" + ` for operational instructions, contract authoring guides, runbooks,
and troubleshooting material.
`

const notesAgentsTemplate = `# Notes Agents

This folder contains durable project knowledge.

LOOM treats notes as source material that can become searchable, synchronized,
and backed up. Notes are not just scratch text. They are part of the project's
long-term context for humans, agents, and later database/object search.

## What Belongs Here

Use markdown files for:

- project decisions and rationale
- research notes
- implementation outlines
- external source summaries
- operational runbooks
- design notes for scripts, connectors, schedules, direct events, and modules
- status notes that should survive beyond a single chat session

Prefer small, named files over one large scratchpad. A future user or agent
should be able to find the relevant note by filename and heading.

## Notes Contract

` + "`" + `loom.notes.yaml` + "`" + ` declares how this notes folder should participate in LOOM.

In this template it declares that notes should be:

- synced as selected files
- indexed as markdown text
- treated as project knowledge
- included and excluded through explicit glob rules

The notes contract does not start filesystem watching by itself. LOOM compiles
notes and policy intent into watched-root desired state. The owner node-agent
then applies that watched-root policy explicitly.

Useful commands from the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project watch-plan .
loom project apply-watch-policy {{.Slug}} --project-root .
loom project sync-status {{.Slug}}
` + "`" + `` + "`" + `` + "`" + `

## Relationship To Sync, Indexing, And Backup

Notes are usually the most important synced content in a project.

When sync is enabled, the owner node-agent scans the configured notes root and
reports matching markdown/text files to main. When indexing is enabled,
markdown/text content can be turned into searchable objects. Non-text
attachments in notes are still backup material, but they do not enter the text
pipeline by default.

Do not assume every file in the project is synced. The contract and policies
decide what LOOM should track.

## Editing Rules

- Use markdown for durable notes.
- Keep headings descriptive.
- Keep links and source references explicit.
- Do not store secret values, API keys, access tokens, private credentials, or
  one-time codes.
- If a note describes a capability, include its capability URL when known.
- If a note describes an automation, include the schedule key, direct-event key,
  or target capability URL when known.
- If you change sync/index intent, update ` + "`" + `loom.notes.yaml` + "`" + ` or the project
  policy files and run validation.

## Common Mistakes

- Do not use notes as a hidden configuration surface. Runtime behavior belongs
  in contracts such as ` + "`" + `loom.script.yaml` + "`" + `, ` + "`" + `loom.exposure.yaml` + "`" + `,
  ` + "`" + `loom.schedule.yaml` + "`" + `, or ` + "`" + `loom.direct_event.yaml` + "`" + `.
- Do not place large generated artifacts here unless the user explicitly wants
  them synced and indexed.
- Do not move or rename important notes without considering search, references,
  and backup history.
- Do not assume validation means the node-agent has already scanned or synced
  the folder.
`

const notesContractTemplate = `kind: loom.notes
schema_version: notes.contract.v0.3

notes:
  status: draft
  sync: true
  index: true
  backup: {{if .HasBackupPolicy}}true{{else}}false{{end}}
  root_key: notes
  path: .
  include:
    - "**/*"
  exclude: []
`

const scriptsReadmeTemplate = `# Scripts

Scripts are project-owned executable packages. They are the simplest way to add
new LOOM capabilities from a project folder.

The basic path is:

` + "```text" + `
script package -> script manifest -> exposure contract -> capability URL -> runtime binding
` + "```" + `

This template includes ` + "`hello_world`" + ` as a minimal script-backed capability:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

## Folder Shape

Each script package should usually look like this:

` + "```text" + `
scripts/<script_key>/
  loom.script.yaml
  loom.exposure.yaml
  run.sh
  README.md
  examples/input.example.json
` + "```" + `

` + "`loom.script.yaml`" + ` describes how to run the package. ` + "`loom.exposure.yaml`" + `
describes whether LOOM should expose it as a capability during activation.

## Creating A Script Capability

1. Create ` + "`scripts/<script_key>/`" + `.
2. Add ` + "`loom.script.yaml`" + ` with a stable ` + "`id`" + `, entrypoint, runtime, inputs,
   outputs, and execution policy.
3. Add an executable entrypoint such as ` + "`run.sh`" + `.
4. Add deterministic examples.
5. Add ` + "`loom.exposure.yaml`" + ` when the script should be callable through LOOM.
6. Validate and plan.
7. Register the project.
8. Activate the ` + "`scripts`" + ` facet.

## Capability URL Rules

Project script capabilities use the project provider by default:

` + "```text" + `
{{.ProjectProvider}}
` + "```" + `

If ` + "`loom.exposure.yaml`" + ` sets:

` + "```yaml" + `
expose:
  provider: project
  endpoint: hello_world
` + "```" + `

then the capability URL is:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

Do not change script IDs or exposure endpoints casually. Schedules, direct
events, docs, tests, portal actions, and users can depend on them.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom capability inspect {{.ExampleCapability}}
loom capability call {{.ExampleCapability}} --input '{}' --wait
` + "```" + `

Calling ` + "`run.sh`" + ` directly only tests the local script. Calling the capability
tests LOOM's provider, policy, routing, job, and runtime-binding path.

## Safety Rules

- Prefer JSON input and JSON output.
- Keep shell scripts executable.
- Keep paths package-relative.
- Do not store credentials or machine-local absolute paths.
- Declare risk level and side effects honestly in ` + "`loom.exposure.yaml`" + `.
- Keep examples small and deterministic.
`

const scriptsAgentsTemplate = `# Scripts Agents

This folder contains executable script packages.

Scripts are the simplest way for a project to expose new LOOM capabilities.
Each script package describes how to run code. An optional exposure contract
describes whether that script should become a callable project capability.

Use this facet when the thing being built can be reduced to one executable entry
point with structured input and structured output. More complex systems can still
start here by wrapping their first useful operation as a script-backed
capability.

## Script Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
script package -> script manifest -> exposure contract -> capability URL -> runtime binding
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `loom.script.yaml` + "`" + ` defines the executable package. ` + "`" + `loom.exposure.yaml` + "`" + ` defines
how LOOM should expose that package as a capability.

In this template, the example exposure creates:

` + "`" + `` + "`" + `` + "`" + `text
{{.ExampleCapability}}
` + "`" + `` + "`" + `` + "`" + `

after the project is registered and the ` + "`" + `scripts` + "`" + ` facet is activated.

The project name becomes the provider namespace for scripts. A script capability
is therefore addressed as:

` + "`" + `` + "`" + `` + "`" + `text
<owner-node>@<project-slug>.<endpoint>
` + "`" + `` + "`" + `` + "`" + `

The owner node is the node that owns the project. The provider is the project
slug. The endpoint comes from ` + "`" + `loom.exposure.yaml` + "`" + `.

## Package Shape

Each script package should usually look like this:

` + "`" + `` + "`" + `` + "`" + `text
scripts/<script_key>/
  loom.script.yaml
  loom.exposure.yaml
  run.sh
  README.md
  examples/input.example.json
` + "`" + `` + "`" + `` + "`" + `

The manifest should define:

- ` + "`" + `kind` + "`" + `
- ` + "`" + `id` + "`" + `
- ` + "`" + `name` + "`" + `
- ` + "`" + `version` + "`" + `
- ` + "`" + `entrypoint` + "`" + `
- ` + "`" + `runtime` + "`" + `
- ` + "`" + `inputs` + "`" + `
- ` + "`" + `outputs` + "`" + `
- ` + "`" + `execution` + "`" + `
- optional usage docs and metadata

Entrypoints must be package-relative. Shell entrypoints should be executable.

Keep one script package focused on one callable behavior. If a package starts to
grow multiple unrelated modes, split it into multiple script packages so each
capability keeps a clear URL, schema, risk level, and test path.

## Exposure Contract

Add ` + "`" + `loom.exposure.yaml` + "`" + ` only when the script should become a LOOM capability.

Important fields:

- ` + "`" + `expose.enabled` + "`" + `: whether activation should create the capability surface
- ` + "`" + `expose.provider` + "`" + `: usually ` + "`" + `project` + "`" + ` for project-owned scripts
- ` + "`" + `expose.endpoint` + "`" + `: capability endpoint name
- ` + "`" + `capability.form` + "`" + `: usually ` + "`" + `job` + "`" + ` for script-backed execution
- ` + "`" + `capability.risk_level` + "`" + `: expected risk of the operation
- ` + "`" + `capability.execution_authorization_level` + "`" + `: required authorization level
- ` + "`" + `execution.default_mode` + "`" + `: whether calls wait for start or completion

If ` + "`" + `expose.provider: project` + "`" + `, the provider resolves to the project provider:

` + "`" + `` + "`" + `` + "`" + `text
{{.ProjectProvider}}
` + "`" + `` + "`" + `` + "`" + `

Do not change provider or endpoint names casually. Schedules, direct events,
workflows, docs, tests, and users may target the resulting URL.

## Runtime Binding

Registration records project intent. Activation creates the backend runtime
binding that tells LOOM how to run the capability after normal policy, routing,
authorization, audit, job, and event handling have accepted the call.

That means a script should not bypass LOOM by assuming it will be called directly
forever. The script's entrypoint is the inner execution shell. The outer shell is
still LOOM: capability URL parsing, policy, invocation records, logs, jobs, and
result capture.

## Runtime Behavior

Scripts should read structured input and write structured output when practical.
For shell scripts, prefer JSON output on stdout.

Keep examples deterministic. A script used in validation or smoke tests should
not depend on network access, external credentials, or user-specific paths
unless the contract clearly says so.

Use credentials by reference only. Do not put secret values in scripts,
examples, manifests, or docs.

Good script behavior:

- accepts JSON input from the mechanism declared by the manifest
- validates required fields before doing side effects
- prints machine-readable JSON output on success
- returns a non-zero exit code on failure
- writes diagnostic text clearly enough for job logs
- keeps side effects within the declared capability intent

Avoid hidden runtime dependencies. If the script needs a binary, environment
variable, credential reference, working directory, or network service, document
that in the package README and contract metadata.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom capability inspect {{.ExampleCapability}}
` + "`" + `` + "`" + `` + "`" + `

Validation checks local contract shape. Registration records declared intent.
Activation creates the provider, capability endpoint, endpoint version, script
registration, and runtime binding.

After activation, smoke-test exposed scripts through LOOM rather than by calling
the entrypoint directly:

` + "`" + `` + "`" + `` + "`" + `sh
loom capability call {{.ExampleCapability}} --input '{}' --wait
` + "`" + `` + "`" + `` + "`" + `

Directly running ` + "`" + `./run.sh` + "`" + ` is still useful while developing the script, but it
does not test the LOOM provider/capability/runtime-binding path.

## Agent Workflow

When adding a script:

1. Create ` + "`" + `scripts/<script_key>/` + "`" + `.
2. Add ` + "`" + `loom.script.yaml` + "`" + ` with a stable script id, version, entrypoint, runtime,
   input contract, output contract, and execution settings.
3. Add a minimal README explaining purpose, inputs, outputs, examples, and
   side effects.
4. Add deterministic example input under ` + "`" + `examples/` + "`" + `.
5. Add ` + "`" + `loom.exposure.yaml` + "`" + ` only when the script should be callable through
   LOOM.
6. Validate the project, register it if needed, activate the ` + "`" + `scripts` + "`" + ` facet,
   and call the resulting capability URL.

## Common Mistakes

- Do not create backend IDs by hand.
- Do not expose a script before its input/output expectations are documented.
- Do not use a capability URL in schedules or direct events until the exposure
  contract declares that URL.
- Do not assume ` + "`" + `loom project register .` + "`" + ` activates the script.
- Do not store credentials in examples or shell files.
- Do not let generated files, caches, or temporary outputs become part of the
  script package unless the user explicitly wants them tracked.
- Do not change ` + "`" + `expose.endpoint` + "`" + ` after other contracts have started depending
  on the capability URL.
- Do not make scripts depend on the current terminal directory; use package
  relative paths and declared runtime configuration.
`

const scriptPackageReadmeTemplate = `# Hello World Script

This is the minimal project-owned script package in the template.

It exists to show the full path from local executable code to a LOOM capability:

` + "```text" + `
loom.script.yaml -> loom.exposure.yaml -> {{.ExampleCapability}}
` + "```" + `

## Files

- ` + "`loom.script.yaml`" + `: describes how to run the script package.
- ` + "`loom.exposure.yaml`" + `: declares the LOOM capability surface.
- ` + "`run.sh`" + `: executable entrypoint.
- ` + "`examples/input.example.json`" + `: small example input fixture.

## Capability Exposure

This scaffold enables the example exposure:

` + "```yaml" + `
expose:
  enabled: true
  provider: project
  endpoint: hello_world
` + "```" + `

Because ` + "`provider: project`" + `, the resulting capability URL is:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom capability inspect {{.ExampleCapability}}
loom capability call {{.ExampleCapability}} --input '{}' --wait
` + "```" + `

Keep the script id and exposure endpoint stable unless all references are
updated.
`

const scriptManifestTemplate = `kind: loom.script
id: hello_world
name: Hello World
version: 0.1.0
description: Minimal scaffolded script package.

entrypoint:
  command:
    - ./run.sh

runtime:
  shell: bash

inputs: {}
outputs: {}

execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only

artifacts: []
usage_documents:
  - path: README.md
metadata:
  scaffolded_by: loom
`

const scriptRunTemplate = `#!/usr/bin/env bash
set -euo pipefail
printf '{"message":"hello from LOOM project script"}\n'
`

const scriptExposureTemplate = `kind: loom.script_exposure
schema_version: script.exposure.v0.3

expose:
  enabled: true
  provider: project
  endpoint: hello_world
  display_name: Hello World
  description: Minimal scaffolded script capability.

capability:
  class_namespace: project
  class_name: scaffolded_script
  form: job
  risk_level: low
  execution_authorization_level: 1
  requires_approval: false
  side_effects: []
  input_schema:
    type: object
  output_schema:
    type: object

execution:
  default_mode: wait_until_started
  wait_timeout_seconds: 120

credentials:
  required: []
`

const workflowsReadmeTemplate = `# Workflows

Use this folder for executable workflow packages.

A workflow is a larger executable package that owns its orchestration logic. It
may call scripts, connectors, agents, or other LOOM capabilities, but those
nested calls should still go through normal LOOM capability URLs.

LOOM should not micro-manage workflow internals. LOOM validates the package,
registers the callable surface, runs one workflow job, and captures logs,
outputs, and artifacts.

## Folder Shape

Each workflow should live under:

` + "```text" + `
workflows/<workflow_key>/
  loom.workflow.yaml
  run.sh
  README.md
  examples/input.example.json
` + "```" + `

The scaffolded workflow exposes:

` + "```text" + `
{{.OwnerNode}}@{{.Slug}}.example_workflow
` + "```" + `

## Implementation Kinds

- ` + "`workflow`" + `: executable workflow package. The entrypoint is run as one
  workflow job.
- ` + "`script`" + `: compatibility shim that points at an existing script package.
- ` + "`placeholder`" + `: design-only intent. It validates with a warning and is
  not callable.

Use ` + "`workflow.contract.v0.3.1`" + ` for executable workflow packages.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project workflows list .
loom project register .
loom project activate {{.Slug}} --facet workflows --project-root .
loom capability inspect {{.OwnerNode}}@{{.Slug}}.example_workflow
` + "```" + `

Runtime execution lands in a later v0.3.1 part. This first contract slice makes
the executable workflow package validate, plan, and scaffold correctly.

## Safety Rules

- Keep the workflow entrypoint deterministic and package-relative.
- Use full capability URLs for nested LOOM calls.
- Document side effects, approval expectations, retries, and idempotency.
- Do not call providers, scripts, or shell commands through hidden lanes when a
  LOOM capability URL exists.
- Keep placeholder workflows draft-only.
`

const workflowsAgentsTemplate = `# Workflow Agents

This folder contains executable workflow packages.

A workflow is a category of executable package, not a second hidden automation
engine. The workflow process owns orchestration. LOOM owns the outer lifecycle:
contract validation, package registration, capability exposure, routing, policy,
job execution, logs, outputs, and artifacts.

## Mental Model

The intended path is:

` + "`" + `` + "`" + `` + "`" + `text
workflow package -> workflow contract -> capability URL -> workflow_run job
` + "`" + `` + "`" + `` + "`" + `

Inside the entrypoint, call other LOOM capabilities through normal CLI/API
surfaces:

` + "`" + `` + "`" + `` + "`" + `sh
loom capability call {{.ExampleCapability}} --input '{}' --wait
` + "`" + `` + "`" + `` + "`" + `

Those nested calls produce their own policy decisions, routes, capability call
records, jobs, logs, and artifacts. Do not bypass that by hard-coding direct
backend access unless the workflow contract explicitly owns that lower-level
integration.

## Contract Shape

Each workflow should live under:

` + "`" + `` + "`" + `` + "`" + `text
workflows/<workflow_key>/
  loom.workflow.yaml
  run.sh
  README.md
  examples/input.example.json
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `loom.workflow.yaml` + "`" + ` should describe:

- workflow id, name, version, and status
- implementation kind
- entrypoint command
- execution timeout and filesystem/network expectations
- expose/provider/endpoint fields
- capability risk, authorization, side effects, and schemas
- optional usage documents and artifact declarations
- optional ` + "`" + `steps` + "`" + ` as documentation metadata

In v0.3.1, ` + "`" + `steps` + "`" + ` are not runtime instructions. They are a readable map
of what the executable is expected to orchestrate.

## Implementation Kinds

- ` + "`" + `workflow` + "`" + `: executable package in this folder.
- ` + "`" + `script` + "`" + `: compatibility shim to a script package.
- ` + "`" + `placeholder` + "`" + `: design-only intent.

Use ` + "`" + `workflow` + "`" + ` for new executable workflows. Use ` + "`" + `placeholder` + "`" + ` only
when the user wants to document an intended workflow without exposing a
capability yet.

## Runtime Rules

- The workflow entrypoint runs once per workflow capability call.
- It should read JSON input and write a result file compatible with script jobs.
- It may call other capabilities, including project scripts and connector
  capabilities.
- Nested capability calls must use stable URLs like
  ` + "`" + `{{.ExampleCapability}}` + "`" + `.
- The workflow should return non-zero on failure.
- Secrets must be referenced by name or environment policy, never written into
  contracts or examples.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project workflows list .
loom project register .
loom project activate {{.Slug}} --facet workflows --project-root .
loom project workflows list {{.Slug}}
` + "`" + `` + "`" + `` + "`" + `

Part 1 of v0.3.1 validates and plans executable workflow contracts. Runtime
registration and execution arrive in the following implementation parts.

## Common Mistakes

- Do not build a workflow DSL inside ` + "`" + `steps` + "`" + `.
- Do not assume LOOM executes each step individually in v0.3.1.
- Do not make placeholder workflows active.
- Do not hide nested LOOM actions behind direct database writes.
- Do not change provider or endpoint names without updating schedules, direct
  events, docs, tests, and portal references.
`

const workflowReadmeTemplate = `# Example Workflow

This package is an executable LOOM workflow.

It is intentionally small: the entrypoint writes a normal LOOM result file and
can later be expanded to call other capabilities through ` + "`loom capability call`" + `.

## Capability URL

After the project is registered and the workflows facet is activated, this
workflow is intended to expose:

` + "```text" + `
{{.OwnerNode}}@{{.Slug}}.example_workflow
` + "```" + `

## Direct Execution

From this folder:

` + "```sh" + `
./run.sh
` + "```" + `

Direct execution only checks the local executable. LOOM execution adds provider
registration, policy, routing, job records, logs, outputs, and artifacts.

## Editing Rules

- Keep ` + "`workflow.id`" + ` aligned with the folder name.
- Keep ` + "`entrypoint.command`" + ` package-relative.
- Keep the result file JSON-compatible.
- Use stable capability URLs for nested LOOM calls.
- Document side effects before raising risk or authorization level.
`

const workflowRunTemplate = `#!/usr/bin/env bash
set -euo pipefail

result_file="${LOOM_RESULT_FILE:-}"
if [ -z "$result_file" ]; then
  printf '{"status":"ok","outputs":{"message":"hello from LOOM project workflow"},"artifacts":[]}\n'
  exit 0
fi

cat > "$result_file" <<'JSON'
{
  "status": "ok",
  "outputs": {
    "message": "hello from LOOM project workflow"
  },
  "artifacts": []
}
JSON
`

const workflowContractTemplate = `kind: loom.workflow
schema_version: workflow.contract.v0.3.1

workflow:
  id: example_workflow
  name: Example Workflow
  description: Minimal scaffolded executable workflow package.
  status: active
  version: 0.1.0

implementation:
  kind: workflow

entrypoint:
  command:
    - ./run.sh

runtime:
  shell: bash

execution:
  timeout_seconds: 60
  network: false
  filesystem:
    mode: read_only

expose:
  enabled: true
  provider: project
  endpoint: example_workflow
  display_name: Example Workflow
  description: Minimal scaffolded workflow capability.

capability:
  class_namespace: project.workflow
  class_name: example_workflow
  form: job
  risk_level: medium
  execution_authorization_level: 2
  requires_approval: false
  side_effects: []
  input_schema:
    type: object
  output_schema:
    type: object

credentials:
  required: []

inputs:
  schema:
    type: object
outputs:
  schema:
    type: object

artifacts: []
usage_documents:
  - path: README.md
    target: endpoint

steps: []
metadata:
  scaffolded_by: loom
`

const connectorsReadmeTemplate = `# Connectors

Connectors define reusable provider namespaces and grouped capabilities.

Use a connector when the project is wrapping an external service, local
application, command surface, API, or larger subsystem. Unlike project scripts,
connectors do not use the project provider by default. Each connector declares
its own provider key.

The example connector in this template exposes:

` + "```text" + `
{{.OwnerNode}}@example_connector.ping
` + "```" + `

## Folder Shape

A connector package usually looks like this:

` + "```text" + `
connectors/<connector_key>/
  loom.connector.yaml
  README.md
  AGENTS.md
  capabilities/
  examples/
  scripts/
` + "```" + `

` + "`loom.connector.yaml`" + ` is authoritative. Smaller files under ` + "`capabilities/`" + ` can
help with authoring, but they must not contradict the root connector contract.

## Connector Contracts

A connector contract declares:

- provider key, display name, description, version, and status
- connector runtime family
- capability endpoints
- input and output schemas
- risk level and side effects
- runtime binding for each endpoint
- usage documents
- credential references when needed

For script-backed endpoints, keep runtime code under:

` + "```text" + `
connectors/<connector_key>/scripts/<endpoint>/
` + "```" + `

## Provider Namespace Rules

Connector provider keys become part of capability URLs:

` + "```text" + `
{{.OwnerNode}}@provider_key.endpoint
` + "```" + `

Keep provider keys distinct from:

- the project provider ` + "`{{.ProjectProvider}}`" + `
- other connector providers
- module providers
- existing backend providers

Treat provider keys and endpoint names as stable public interfaces.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet connectors --project-root .
loom providers inspect {{.OwnerNode}}@example_connector
loom capability inspect {{.OwnerNode}}@example_connector.ping
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "```" + `

## Safety Rules

- Do not store API tokens, passwords, OAuth secrets, cookies, or private keys.
- Use credential references and auth profiles.
- Prefer specific endpoint names, such as ` + "`note.create`" + `, over vague names such
  as ` + "`run`" + `.
- Add deterministic smoke paths for safe endpoints.
- Declare side effects and risk levels honestly.
`

const connectorsAgentsTemplate = `# Connector Agents

This folder contains connector packages.

A connector is a grouped provider namespace. It is how a project declares a
reusable set of related capabilities, usually wrapping an external tool,
service, local application, API, or command surface.

Use this facet when the project is creating a provider, not just one project
script. A connector should feel like "LOOM can now talk to this system through a
small set of named operations."

## Connector Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
connector contract -> provider namespace -> capability endpoints -> runtime bindings
` + "`" + `` + "`" + `` + "`" + `

Unlike project scripts, connectors do not use the project provider by default.
Each connector declares its own provider key in ` + "`" + `loom.connector.yaml` + "`" + `.

For example:

` + "`" + `` + "`" + `` + "`" + `text
{{.OwnerNode}}@example_connector.ping
` + "`" + `` + "`" + `` + "`" + `

means the capability runs from ` + "`" + `{{.OwnerNode}}` + "`" + `, belongs to provider
` + "`" + `example_connector` + "`" + `, and exposes endpoint ` + "`" + `ping` + "`" + `.

The provider key is part of the public capability URL. Treat it like a stable
interface name. Users, schedules, direct events, workflows, tests, and agents may
depend on it.

## Package Shape

A connector package usually looks like this:

` + "`" + `` + "`" + `` + "`" + `text
connectors/<connector_key>/
  loom.connector.yaml
  README.md
  AGENTS.md
  examples/
  capabilities/
  scripts/
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `loom.connector.yaml` + "`" + ` is the authoritative connector contract. Smaller files
under ` + "`" + `capabilities/` + "`" + ` can be used as authoring aids, but the root connector
contract is what LOOM validates and activates.

Keep connector-local runtime code inside the connector package unless there is a
specific reason to share it through the project ` + "`" + `scripts/` + "`" + ` facet. For
script-backed connector endpoints, the default shape is:

` + "`" + `` + "`" + `` + "`" + `text
connectors/<connector_key>/scripts/<endpoint>/loom.script.yaml
` + "`" + `` + "`" + `` + "`" + `

## Connector Contract

The connector contract should declare:

- provider key, display name, description, type, version, and status
- connector runtime kind
- capability endpoints
- input and output schemas
- risk level and side effects
- runtime implementation for each endpoint
- usage documents
- credential references when needed

Supported runtime declarations depend on the current LOOM runtime substrate.
For v0.3, script-backed connectors are the scaffold default, and command/HTTP
runtime connectors are also supported when declared with valid runtime config.

Script-backed endpoint runtimes should point to normal LOOM script packages
under:

` + "`" + `` + "`" + `` + "`" + `text
connectors/<connector_key>/scripts/<endpoint>/
` + "`" + `` + "`" + `` + "`" + `

Each capability endpoint should be understandable without reading the
implementation. At minimum, the contract or README should make clear:

- what external/local system is touched
- what input fields are required
- what output shape is returned
- whether the operation is read-only or side-effecting
- what credentials or local tools are required
- how to smoke-test the endpoint after activation

## Provider Namespace Rules

Connector provider keys must be distinct from:

- the project script provider
- other connector providers in the project
- module provider declarations
- existing backend providers that are not owned by this project

Provider and endpoint names become capability URLs. Treat them as stable public
interfaces for users and agents.

Do not use the project slug as a connector provider key. Project-owned scripts
already occupy that namespace. For this template, ` + "`" + `{{.Slug}}` + "`" + ` is the
project provider, while ` + "`" + `example_connector` + "`" + ` is a separate connector provider.

## Runtime Binding

Activation creates the backend records that connect a connector endpoint to its
declared runtime implementation. The capability call still travels through the
normal LOOM outer shell: URL parsing, policy, routing, audit, jobs, invocation
records, and result capture. The connector runtime is only the inner execution
step.

This means connector code should not assume it is a standalone CLI forever. It
should accept the declared input, produce the declared output, and let LOOM own
authorization, observability, and lifecycle state.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet connectors --project-root .
loom providers inspect {{.OwnerNode}}@example_connector
loom capability inspect {{.OwnerNode}}@example_connector.ping
` + "`" + `` + "`" + `` + "`" + `

Registration records connector intent. Activation creates provider records,
capability classes/endpoints, endpoint versions, runtime bindings, and usage
documents where supported.

After activation, smoke-test a safe endpoint through the capability path:

` + "`" + `` + "`" + `` + "`" + `sh
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "`" + `` + "`" + `` + "`" + `

## Agent Workflow

When adding a connector:

1. Create ` + "`" + `connectors/<connector_key>/` + "`" + `.
2. Choose a stable provider key that does not collide with the project provider,
   another connector, or a module.
3. Add ` + "`" + `loom.connector.yaml` + "`" + ` with provider metadata, runtime config, endpoint
   specs, schemas, risk, authorization level, and usage docs.
4. Add connector-local scripts or runtime files referenced by the contract.
5. Add a README with installation, credentials, examples, and smoke-test notes.
6. Validate the project, register it if needed, activate the ` + "`" + `connectors` + "`" + ` facet,
   inspect the provider, and call a safe endpoint.

## Security And Credentials

Keep credentials as references only. Do not store API tokens, passwords, OAuth
secrets, private keys, or session cookies in connector folders.

If a connector needs credentials, describe the required credential reference in
the connector contract or policy docs. The actual secret must live outside the
project source tree.

## Common Mistakes

- Do not use ` + "`" + `project` + "`" + ` as a connector provider key.
- Do not duplicate provider keys or endpoint names.
- Do not hide runtime behavior outside the declared runtime config.
- Do not assume a connector exists in the backend before activation.
- Do not change provider keys casually; doing so changes every capability URL
  under that connector.
- Do not claim an external integration works until the connector has a
  deterministic local validation or smoke path.
- Do not declare broad, vague endpoints such as ` + "`" + `run` + "`" + ` or ` + "`" + `do_action` + "`" + ` when the
  connector can expose specific operations with clear schemas.
`

const connectorReadmeTemplate = `# Example Connector

This connector demonstrates how a project can declare a provider namespace and
one or more capability endpoints.

The connector provider is:

` + "```text" + `
{{.OwnerNode}}@example_connector
` + "```" + `

The example endpoint is:

` + "```text" + `
{{.OwnerNode}}@example_connector.ping
` + "```" + `

## Files

- ` + "`loom.connector.yaml`" + `: authoritative connector contract.
- ` + "`capabilities/ping.yaml`" + `: lightweight authoring aid for the ` + "`ping`" + ` endpoint.
- ` + "`scripts/ping/loom.script.yaml`" + `: script package backing the endpoint.
- ` + "`scripts/ping/run.sh`" + `: endpoint entrypoint.
- ` + "`examples/input.example.json`" + `: example input fixture.

## How The Connector Works

` + "`loom.connector.yaml`" + ` declares provider metadata, runtime defaults,
endpoint contracts, and usage docs. The ` + "`ping`" + ` endpoint is script-backed:

` + "```yaml" + `
runtime:
  kind: script
  script: scripts/ping
` + "```" + `

After connector activation, LOOM owns provider lookup, capability URL parsing,
policy, routing, jobs, audit, and result capture. The script is only the inner
execution step.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet connectors --project-root .
loom providers inspect {{.OwnerNode}}@example_connector
loom capability inspect {{.OwnerNode}}@example_connector.ping
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "```" + `

Keep provider keys and endpoint names stable once other contracts reference
them. Do not store API tokens or credentials in this folder.
`

const connectorAgentsTemplate = `# Example Connector Agents

This folder is a concrete connector package.

The parent ` + "`" + `connectors/AGENTS.md` + "`" + ` explains connector rules generally. This file
applies those rules to ` + "`" + `example_connector` + "`" + `, the scaffolded connector provider
included with the everything template.

## Connector Mental Model

This connector declares one provider:

` + "`" + `` + "`" + `` + "`" + `text
{{.OwnerNode}}@example_connector
` + "`" + `` + "`" + `` + "`" + `

and one endpoint:

` + "`" + `` + "`" + `` + "`" + `text
{{.OwnerNode}}@example_connector.ping
` + "`" + `` + "`" + `` + "`" + `

The endpoint is script-backed. LOOM owns the provider/capability records and
runtime binding after connector activation. The local script under
` + "`" + `scripts/ping/` + "`" + ` is only the inner execution step.

## Package Shape

This package uses:

` + "`" + `` + "`" + `` + "`" + `text
connectors/example_connector/
  loom.connector.yaml
  README.md
  AGENTS.md
  capabilities/ping.yaml
  examples/input.example.json
  scripts/ping/loom.script.yaml
  scripts/ping/run.sh
  scripts/ping/README.md
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `loom.connector.yaml` + "`" + ` is authoritative. ` + "`" + `capabilities/ping.yaml` + "`" + ` is an authoring
aid and should not contradict the root connector contract.

## Editing Rules

- Keep provider key ` + "`" + `example_connector` + "`" + ` stable unless every dependent URL is
  updated.
- Keep endpoint names lowercase and dot-separated.
- Keep script-backed endpoint packages under ` + "`" + `scripts/<endpoint>/` + "`" + `.
- Keep examples under ` + "`" + `examples/` + "`" + `.
- Do not store API tokens, credentials, session cookies, or private payloads.
- Prefer deterministic scripts that can be smoke-tested without network access.
- Run ` + "`" + `loom project validate .` + "`" + ` from the project root after connector changes.

## Adding An Endpoint

When adding another endpoint to this connector:

1. Add a new capability entry to ` + "`" + `loom.connector.yaml` + "`" + `.
2. Choose a specific endpoint name such as ` + "`" + `note.create` + "`" + `, not a vague name such
   as ` + "`" + `run` + "`" + `.
3. Define input and output schemas.
4. Declare risk level, side effects, authorization level if needed, and runtime.
5. Add ` + "`" + `scripts/<endpoint>/loom.script.yaml` + "`" + ` and an entrypoint if the endpoint
   is script-backed.
6. Add example input.
7. Update README usage docs.
8. Validate the project and call the safe endpoint after activation.

## Runtime Rules

For script-backed endpoints:

- entrypoints should be package-relative
- shell scripts should be executable
- stdout should contain structured JSON when practical
- failures should exit non-zero
- scripts should not depend on the current terminal directory
- hidden tools, environment variables, or credentials must be documented

This example ` + "`" + `ping` + "`" + ` endpoint is intentionally low-risk and returns:

` + "`" + `` + "`" + `` + "`" + `json
{"message":"pong"}
` + "`" + `` + "`" + `` + "`" + `

Use it as a shape example, not as proof that all connector operations should be
this small.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet connectors --project-root .
loom providers inspect {{.OwnerNode}}@example_connector
loom capability inspect {{.OwnerNode}}@example_connector.ping
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "`" + `` + "`" + `` + "`" + `

Activation creates the backend provider, capability endpoint/version, runtime
binding, and usage document records where supported.

## Common Mistakes

- Do not edit ` + "`" + `capabilities/ping.yaml` + "`" + ` and forget to update
  ` + "`" + `loom.connector.yaml` + "`" + `.
- Do not use ` + "`" + `project` + "`" + ` or the project slug as this connector provider key.
- Do not change endpoint names casually; they are part of capability URLs.
- Do not store real external-service tokens in examples.
- Do not declare a connector endpoint without a smoke-test path.
- Do not claim an endpoint is safe/read-only if it has external side effects.
`

const connectorContractTemplate = `kind: loom.connector
schema_version: connector.contract.v0.3

provider:
  key: example_connector
  display_name: Example Connector
  description: Scaffolded connector provider.
  type: connector
  version: 0.1.0
  status: draft

runtime:
  kind: script
  base_dir: scripts

capabilities:
  - endpoint: ping
    display_name: Ping
    description: Minimal connector capability.
    form: job
    risk_level: low
    side_effects: []
    input_schema:
      type: object
    output_schema:
      type: object
    runtime:
      kind: script
      script: scripts/ping
      default_mode: wait_for_completion
      wait_timeout_seconds: 30

usage_documents:
  - path: README.md
    target: provider
`

const connectorScriptManifestTemplate = `kind: loom.script
id: ping
name: Connector Ping
version: 0.1.0
description: Minimal script package backing the example connector ping endpoint.

entrypoint:
  command:
    - ./run.sh

runtime:
  shell: bash

inputs: {}
outputs: {}

execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only

artifacts: []
usage_documents:
  - path: README.md
metadata:
  scaffolded_by: loom
  connector_endpoint: ping
`

const connectorScriptReadmeTemplate = `# Connector Ping Script

This script package backs the connector endpoint:

` + "```text" + `
{{.OwnerNode}}@example_connector.ping
` + "```" + `

It is intentionally minimal and low-risk. It returns a JSON ` + "`pong`" + ` response and
does not require network access or credentials.

## Files

- ` + "`loom.script.yaml`" + `: script package manifest.
- ` + "`run.sh`" + `: executable entrypoint.
- ` + "`README.md`" + `: endpoint implementation notes.

## Direct Execution

From this folder:

` + "```sh" + `
./run.sh
` + "```" + `

Expected output:

` + "```json" + `
{"message":"pong"}
` + "```" + `

Direct execution only tests the local script. It does not test LOOM provider
registration, capability routing, policy, jobs, or runtime binding.

## LOOM Execution

After activating the connector facet from the project root:

` + "```sh" + `
loom project activate {{.Slug}} --facet connectors --project-root .
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "```" + `

## Editing Rules

- Keep output JSON-friendly.
- Keep failures non-zero and clear.
- Do not add hidden credentials.
- Do not depend on the current terminal directory.
- Document any runtime dependency before using it.
`

const connectorCapabilityTemplate = `endpoint: ping
display_name: Ping
description: Minimal connector capability declaration.
`

const connectorPingTemplate = `#!/usr/bin/env bash
set -euo pipefail
printf '{"message":"pong"}\n'
`

const schedulesReadmeTemplate = `# Schedules

Schedules are time-based automations. A LOOM schedule calls exactly one
capability on a timer.

If an automation needs multiple steps, first wrap those steps behind one script,
connector, or workflow capability. Then schedule that one
capability.

The scaffolded schedule targets:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

## Folder Shape

Each schedule should live under:

` + "```text" + `
schedules/<schedule_key>/
  loom.schedule.yaml
  input.example.json
  README.md
` + "```" + `

` + "`loom.schedule.yaml`" + ` defines the schedule. The input file must be a JSON object
that matches what the target capability expects.

## Schedule Contract

A schedule contract declares:

- schedule key and display name
- target capability URL
- input file
- run-as actor
- timing kind, expression, and timezone
- misfire behavior
- concurrency behavior
- approval policy
- timeout and retry behavior

The schedule owns when the call happens. The target capability owns what work is
performed.

## Activation Order

Activate the target capability before activating the schedule:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet schedules --project-root .
loom schedules list
` + "```" + `

After activation, inspect or manually fire only safe schedules:

` + "```sh" + `
loom schedules inspect <schedule-ref>
loom schedules fire <schedule-ref>
` + "```" + `

## Policy Choices

Choose schedule policy deliberately:

- ` + "`misfire.policy`" + ` controls what happens when LOOM missed a fire time.
- ` + "`concurrency.policy`" + ` controls overlapping runs of the same schedule.
- ` + "`approval.policy`" + ` controls whether human approval is required.
- ` + "`retry.max_attempts`" + ` controls repeated attempts after failure.

The scaffold uses conservative defaults: mark missed runs, allow parallel runs,
no approval, one attempt.

## Safety Rules

- Do not target placeholder workflows.
- Do not schedule high-risk side effects without approval policy.
- Do not use schedules as hidden multi-step workflows.
- Do not set active runtime behavior before the target capability has been
  smoke-tested.
- Keep schedule keys stable once users or docs refer to them.
`

const schedulesAgentsTemplate = `# Schedule Agents

This folder contains time-based automation contracts.

A LOOM schedule is not an arbitrary script runner. It is a declarative trigger
that calls exactly one capability at a configured time or interval. If a timed
automation needs to do several things, reduce that behavior into one capability
first, usually a script-backed project capability or connector capability, and
make the schedule target that one URL.

## Schedule Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
schedule contract -> validated target capability -> backend schedule -> one capability call per fire
` + "`" + `` + "`" + `` + "`" + `

The schedule owns timing, misfire behavior, concurrency behavior, approval
policy, timeout, retry, and static input. The target capability owns the actual
work.

In this template, the scaffolded schedule targets:

` + "`" + `` + "`" + `` + "`" + `text
{{.ExampleCapability}}
` + "`" + `` + "`" + `` + "`" + `

That capability must exist before the schedule can be useful at runtime. Usually
that means activating the ` + "`" + `scripts` + "`" + ` facet before activating ` + "`" + `schedules` + "`" + `.

## Package Shape

Each schedule should live under:

` + "`" + `` + "`" + `` + "`" + `text
schedules/<schedule_key>/
  loom.schedule.yaml
  input.example.json
  README.md
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `loom.schedule.yaml` + "`" + ` is the schedule contract. The input file should be a JSON
object and should match what the target capability expects.

## Contract Responsibilities

The schedule contract should define:

- stable schedule key and display name
- lifecycle status: ` + "`" + `draft` + "`" + `, ` + "`" + `active` + "`" + `, ` + "`" + `paused` + "`" + `, or ` + "`" + `disabled` + "`" + `
- target capability URL
- static input file, when needed
- actor/run-as policy
- timing kind, expression, and timezone
- misfire behavior for missed runs
- concurrency policy for overlapping fires
- approval behavior
- timeout and retry limits

Keep scaffolded schedules in ` + "`" + `draft` + "`" + ` until the target capability has been
activated and smoke-tested.

## Timing And Runtime Rules

Use supported schedule timing only. In the current project contract substrate,
interval schedules are the practical scaffold path. Timezone names must be valid
IANA timezone names such as ` + "`" + `UTC` + "`" + `, ` + "`" + `Europe/Amsterdam` + "`" + `, or ` + "`" + `America/New_York` + "`" + `.

Choose misfire and concurrency policies deliberately:

- ` + "`" + `mark_missed` + "`" + ` is safest when late work may be harmful or confusing.
- limited late execution is appropriate only when the work remains useful after
  delay.
- ` + "`" + `allow_parallel` + "`" + ` is useful for independent runs but unsafe for shared mutable
  resources.
- queued or skipped overlap behavior should be preferred when repeated runs can
  conflict with each other.

Do not use schedules to bypass approval. If the target action should require
human approval, express that in the schedule approval profile or capability
policy rather than hiding it in script code.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet schedules --project-root .
loom schedules list
` + "`" + `` + "`" + `` + "`" + `

Validation checks local contract shape and target URL syntax. Registration
records project intent. Activation creates or updates backend schedule state.

After activation, inspect the schedule and fire it manually only when the target
capability is safe to run:

` + "`" + `` + "`" + `` + "`" + `sh
loom schedules inspect <schedule-ref>
loom schedules fire <schedule-ref>
` + "`" + `` + "`" + `` + "`" + `

## Agent Workflow

When adding a schedule:

1. Identify or create the one target capability.
2. Smoke-test the target capability directly.
3. Create ` + "`" + `schedules/<schedule_key>/loom.schedule.yaml` + "`" + `.
4. Add an input example JSON object that matches the target capability schema.
5. Choose timing, misfire, concurrency, approval, timeout, and retry policies.
6. Validate and plan the project.
7. Activate the target capability facet first, then activate schedules.

## Common Mistakes

- Do not point a schedule at a placeholder workflow.
- Do not target a capability URL that is only planned but not exposed.
- Do not use a JSON array or scalar as the input file; use a JSON object.
- Do not use schedule keys as backend IDs. LOOM derives backend identities.
- Do not set a schedule active before the target capability is safe to run.
- Do not hide multi-step behavior in the schedule; wrap it as one capability.
`

const scheduleContractTemplate = `kind: loom.schedule
schema_version: schedule.contract.v0.3

schedule:
  key: example_schedule
  display_name: Example Schedule
  description: Draft scaffolded schedule.
  status: draft

target:
  capability: {{.ExampleCapability}}
  input_file: input.example.json
  run_as: owner

timing:
  kind: interval
  expression: 24h
  timezone: UTC

misfire:
  policy: mark_missed
  lateness_window_seconds: 0

concurrency:
  policy: allow_parallel

approval:
  policy: none

timeout:
  seconds: 60

retry:
  max_attempts: 1
`

const directEventsReadmeTemplate = `# Direct Events

Direct events let external or adjacent systems notify LOOM that something
happened. LOOM receives the payload, authenticates the endpoint, maps the
payload into capability input, and calls exactly one target capability.

If an event should perform multiple operations, wrap that behavior behind one
script, connector, or workflow capability first.

The scaffolded direct event targets:

` + "```text" + `
{{.ExampleCapability}}
` + "```" + `

## Folder Shape

Each direct-event package should live under:

` + "```text" + `
direct_events/<event_key>/
  loom.direct_event.yaml
  README.md
  examples/payload.json
  examples/expected_mapped_input.json
` + "```" + `

The payload fixture should look like what the external system can actually send.
The expected mapped input fixture should match what LOOM passes to the target
capability.

## Direct Event Contract

` + "`loom.direct_event.yaml`" + ` declares:

- event key and display name
- integration key and auth level
- endpoint slug, event type, and status
- target capability URL
- auth profile references
- mapping from payload fields into capability input
- required mapped fields
- idempotency strategy
- response behavior
- raw payload storage policy
- example fixture paths

## Mapping

The mapping layer adapts external payloads to LOOM capability input.

Example:

` + "```yaml" + `
mapping:
  fields:
    message:
      source: body
      expr: $.message
` + "```" + `

This reads ` + "`message`" + ` from the JSON request body and creates a capability input
field called ` + "`message`" + `.

Validation compares the mapped result against
` + "`examples/expected_mapped_input.json`" + `.

## Activation Order

Activate the target capability before activating direct events:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet direct-events --project-root .
` + "```" + `

Use preview/ingest commands for smoke testing when available:

` + "```sh" + `
loom direct-events preview <endpoint-ref> --body-file examples/payload.json
loom direct-events ingest <endpoint-ref> --body-file examples/payload.json
` + "```" + `

## Safety Rules

- Do not store webhook secrets or tokens in YAML.
- Use private-network auth or existing auth profile references.
- Use idempotency when the sender may retry delivery.
- Do not target placeholder workflows.
- Do not assume external systems send LOOM-shaped payloads.
- Keep raw payload storage policy intentional.
`

const directEventsAgentsTemplate = `# Direct Event Agents

This folder contains direct-event automation contracts.

A direct event is how an external or adjacent system notifies LOOM that
something happened. LOOM receives the event, authenticates it, stores the
payload according to policy, maps the external payload into LOOM capability
input, and calls exactly one target capability.

If an event needs to perform several operations, reduce those operations into
one script, connector, or workflow capability first. The direct
event contract should still target one capability URL.

## Direct Event Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
external payload -> direct-event endpoint -> mapping -> one capability input -> one capability call -> response
` + "`" + `` + "`" + `` + "`" + `

The external system owns the incoming payload shape. The direct-event contract
owns the translation from that payload shape into the target capability's input
shape.

In this template, the scaffolded event maps ` + "`" + `$.message` + "`" + ` from the incoming body
and targets:

` + "`" + `` + "`" + `` + "`" + `text
{{.ExampleCapability}}
` + "`" + `` + "`" + `` + "`" + `

## Package Shape

Each direct-event package should live under:

` + "`" + `` + "`" + `` + "`" + `text
direct_events/<event_key>/
  loom.direct_event.yaml
  README.md
  examples/payload.json
  examples/expected_mapped_input.json
` + "`" + `` + "`" + `` + "`" + `

The payload example should represent what the external system can actually
send. The expected mapped input should represent exactly what LOOM should pass
to the target capability after mapping.

## Contract Responsibilities

The direct-event contract should define:

- stable event key and display name
- integration key and integration auth level
- endpoint slug, event type, and endpoint status
- target capability URL
- auth profiles or references
- mapping rules from external payload to capability input
- required mapped fields
- idempotency strategy
- response mode
- storage policy for raw payloads
- timeout and retry behavior when present
- example payload and expected mapped input fixtures

Keep scaffolded direct events in ` + "`" + `draft` + "`" + ` until the target capability and mapping
fixtures have been tested.

## Mapping Rules

Mapping should be explicit and boring. Do not rely on undocumented behavior in
the receiver script. If the external payload uses names that do not match the
target capability, adapt them in ` + "`" + `mapping.fields` + "`" + `.

Use examples as tests:

- ` + "`" + `examples/payload.json` + "`" + ` is the external body fixture.
- ` + "`" + `examples/expected_mapped_input.json` + "`" + ` is the capability input LOOM should
  produce.

Validation compares mapping output against the expected fixture. Keep these
fixtures small, deterministic, and representative.

## Auth And Secrets

Do not store token values, shared secrets, API keys, OAuth credentials, private
keys, or session cookies in this folder.

Private-network auth can be scaffolded because it does not require storing a
secret value. Token-based auth should reference an existing LOOM auth profile or
credential created outside the project files.

If an external system needs a webhook secret, document the required credential
reference and setup steps, but keep the actual secret outside the project tree.

## Idempotency And Responses

Use idempotency whenever the external system may retry delivery. Prefer a stable
payload path such as an external message id, event id, or delivery id.

Choose response behavior based on the external system:

- accepted/asynchronous response is safest when the sender should not wait for
  capability completion.
- synchronous wait is useful only when the sender can wait and expects an
  immediate result.
- failure response behavior should be designed around the sender's retry rules.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet direct-events --project-root .
` + "`" + `` + "`" + `` + "`" + `

Activation should happen only after the target capability exists. For this
template, activate ` + "`" + `scripts` + "`" + ` first because the direct event targets a
script-backed project capability.

Use direct-event preview/ingest commands for smoke testing when available in the
runtime environment:

` + "`" + `` + "`" + `` + "`" + `sh
loom direct-events preview <endpoint-ref> --body-file examples/payload.json
loom direct-events ingest <endpoint-ref> --body-file examples/payload.json
` + "`" + `` + "`" + `` + "`" + `

## Agent Workflow

When adding a direct event:

1. Identify the external system and the payload it can actually send.
2. Identify or create the one target capability.
3. Create a representative payload fixture.
4. Define mapping fields and required mapped fields.
5. Create the expected mapped input fixture.
6. Choose auth, idempotency, response, storage, timeout, and retry policies.
7. Validate and plan the project.
8. Activate the target capability facet first, then activate direct events.

## Common Mistakes

- Do not target a placeholder workflow.
- Do not store webhook secrets or tokens in YAML.
- Do not assume external systems will send LOOM-shaped payloads.
- Do not skip idempotency for systems that retry delivery.
- Do not let mapping fixtures drift from the target capability schema.
- Do not make one direct event secretly run several capabilities; wrap that
  behavior behind one capability.
`

const directEventContractTemplate = `kind: loom.direct_event
schema_version: direct_event.contract.v0.3

event:
  key: example_event
  display_name: Example Event
  description: Draft scaffolded direct event.
  status: draft

integration:
  key: example_integration
  display_name: Example Integration
  description: Scaffolded direct-event integration.
  main_auth_level: 3

endpoint:
  slug: example_event
  display_name: Example Event
  event_type: example.received
  status: draft

target:
  capability: {{.ExampleCapability}}

auth:
  profiles:
    - name: private_network
      kind: private_network
      create_if_missing: true

mapping:
  fields:
    message:
      source: body
      expr: $.message
  required:
    - message

idempotency:
  strategy: payload_path
  path: $.id

response:
  mode: accepted

storage:
  payload_limit_bytes: 1048576
  store_raw_body: true

examples:
  payload: examples/payload.json
  expected_mapped_input: examples/expected_mapped_input.json
`

const modulesReadmeTemplate = `# Modules

Use this folder for larger LOOM expansion packages.

Modules are heavier than scripts or connectors. They are intended for packages
that can extend LOOM with object types, providers, capabilities, usage
documents, storage requirements, and backup hooks.

## Current Model

In v0.3, module project contracts support validation and registration intent.
Installing, enabling, and exposing module runtime behavior remains explicit.

The scaffolded module package is:

` + "```text" + `
modules/example_module/
  module.json
  loom.module_project.yaml
  README.md
` + "```" + `

## Module Package Versus Project Wrapper

` + "`module.json`" + ` describes the backend module package:

- module id, name, version, kind
- storage requirements
- provided object types
- provided providers and capabilities
- usage documents
- backup hooks

` + "`loom.module_project.yaml`" + ` describes project intent:

- whether the project owns the module
- whether registration is desired
- install policy
- exposure policy
- validation requirements

## Use Something Smaller When Possible

Use:

- ` + "`scripts/`" + ` for one executable project capability
- ` + "`connectors/`" + ` for a provider namespace wrapping an external/local system
- ` + "`schedules/`" + ` for timed calls to one capability
- ` + "`direct_events/`" + ` for external notifications mapped into one capability call

Use ` + "`modules/`" + ` when the work is genuinely a LOOM platform extension.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet modules --project-root .
loom modules list
` + "```" + `

Activation can register supported module metadata. Later install/enable/expose
steps should be treated as separate operational decisions.

## Safety Rules

- Do not use modules for simple shell commands.
- Keep install and exposure explicit-only unless the user decides otherwise.
- Do not declare stateful storage without backup hooks or a backup plan.
- Keep module IDs and provider namespaces stable.
- Do not store credentials in module packages.
`

const modulesAgentsTemplate = `# Module Agents

This folder contains LOOM module packages owned or tracked by the project.

A module is larger than a script or connector. It is a package that can extend
LOOM itself with module metadata, providers, capabilities, object types, usage
documents, storage requirements, and backup hooks. Modules are for deeper
platform expansion, not for a simple one-off command.

In v0.3, project module contracts support registration intent and validation,
but install and exposure policies are explicit-only. Scaffolded module files do
not automatically install code, enable runtime behavior, or expose capabilities.

## Module Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
module package -> module manifest -> project module wrapper -> registration intent -> explicit install/exposure later
` + "`" + `` + "`" + `` + "`" + `

` + "`" + `module.json` + "`" + ` is the backend module package manifest. It describes what the
module is and what it provides.

` + "`" + `loom.module_project.yaml` + "`" + ` is the project wrapper. It describes how this project
wants LOOM to treat the module: registration, ownership, install plan, exposure
plan, and validation expectations.

## Package Shape

Each module should live under:

` + "`" + `` + "`" + `` + "`" + `text
modules/<module_key>/
  module.json
  loom.module_project.yaml
  README.md
  AGENTS.md
  docs/
  examples/
  tests/
` + "`" + `` + "`" + `` + "`" + `

The current substrate expects the manifest file to be named ` + "`" + `module.json` + "`" + ` at the
module package root. Do not point ` + "`" + `loom.module_project.yaml` + "`" + ` at a different
manifest path unless the backend explicitly supports it.

## Manifest Responsibilities

` + "`" + `module.json` + "`" + ` should declare:

- module id, name, version, kind, and description
- dependency requirements
- storage requirements
- object types provided by the module
- providers provided by the module
- capabilities provided by those providers
- usage documents
- backup hooks for stateful storage

If a module requires database or filesystem state, it needs a credible backup
story. The project wrapper defaults to requiring backup hooks for stateful
storage.

## Project Wrapper Responsibilities

` + "`" + `loom.module_project.yaml` + "`" + ` should declare:

- module manifest path, currently ` + "`" + `module.json` + "`" + `
- project ownership and portal visibility
- whether registration intent is enabled
- install policy
- exposure policy
- validation policy

For v0.3, keep these safety rules:

- ` + "`" + `install.plan: explicit_only` + "`" + `
- ` + "`" + `install.install_after_register: false` + "`" + `
- ` + "`" + `install.enable_after_install: false` + "`" + `
- ` + "`" + `exposure.plan: explicit_only` + "`" + `
- ` + "`" + `exposure.expose_after_enable: false` + "`" + `

This preserves a clean boundary between declaring a module and actually changing
the running LOOM system.

## Provider And Capability Namespaces

Module providers and capabilities share the same backend namespace as other
providers. They must not collide with:

- project script provider URLs
- project workflow shim URLs
- connector provider URLs
- other module provider URLs

Treat module provider names and capability URLs as public interfaces. Changing
them can break schedules, direct events, workflows, portal actions, tests, and
agent tools.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet modules --project-root .
loom modules list
` + "`" + `` + "`" + `` + "`" + `

Validation checks local contract shape and collision risks. Registration records
module intent. Activation can register project-owned module package metadata, but
installing, enabling, and exposing module runtime behavior remains explicit.

Use module-specific validation and smoke tests before asking LOOM to install or
enable a module:

` + "`" + `` + "`" + `` + "`" + `sh
loom modules inspect <module-ref>
loom project doctor {{.Slug}} --project-root .
` + "`" + `` + "`" + `` + "`" + `

## Agent Workflow

When adding a module:

1. Confirm that this really needs a module. Use ` + "`" + `scripts/` + "`" + ` or ` + "`" + `connectors/` + "`" + ` for
   smaller capability surfaces.
2. Create ` + "`" + `modules/<module_key>/module.json` + "`" + `.
3. Add ` + "`" + `loom.module_project.yaml` + "`" + ` with explicit-only install and exposure plans.
4. Document what the module provides and what runtime dependencies it needs.
5. Add usage docs and examples for every provider/capability the module will
   eventually expose.
6. Add backup hooks or a backup plan for any stateful storage requirement.
7. Validate and plan the project.
8. Register module intent before considering explicit install/enable/exposure.

## Common Mistakes

- Do not use modules for one simple shell command.
- Do not assume project registration installs or enables a module.
- Do not auto-expose module capabilities from scaffolded files.
- Do not declare stateful storage without backup hooks or an explicit backup
  plan.
- Do not collide with project, connector, or existing backend provider names.
- Do not store credentials in module files.
- Do not change module ids or provider namespaces casually.
`

const reposReadmeTemplate = `# Repositories

Use this folder for project-associated code and repository references.

The ` + "`repos/`" + ` facet lets a LOOM project keep code context near the project that
owns or uses it. This does not automatically make repository contents synced,
indexed, or callable. Runtime behavior belongs in ` + "`scripts/`" + `, ` + "`connectors/`" + `, or
` + "`modules/`" + `.

## What Belongs Here

- small project-owned code folders
- README files describing external repositories tied to the project
- source examples that should be backed up with the project
- implementation notes explaining how a repo relates to this project
- metadata about external checkouts that live outside the project tree

Large standalone repositories can stay outside this folder. In that case, keep
a short reference here describing where the repo lives and why it belongs to the
project.

## Repos Contract

` + "`.loom/contracts/repos.yaml`" + ` separates watched-root policy from explicit
repository membership. ` + "`watch_roots`" + ` controls sync, backup, and index behavior;
` + "`members`" + ` lists only repositories deliberately added with stable ` + "`repo_...`" + `
identities and descriptive ` + "`primary`" + `, ` + "`component`" + `, or ` + "`reference`" + ` roles.

In this template, repo roots are backup-first:

` + "```text" + `
sync: false
backup: true
index: false
` + "```" + `

That means repo material is meant to be preserved as raw project material rather
than turned into searchable markdown/object content by default.

## Relationship To Capabilities

If a repo contains executable behavior that should be callable through LOOM, do
not expose it from ` + "`repos/`" + ` directly.

Use:

- ` + "`scripts/`" + ` for project-owned executable capabilities
- ` + "`connectors/`" + ` for provider namespaces wrapping systems or APIs
- ` + "`modules/`" + ` for deeper LOOM expansion packages

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project watch-plan .
loom project register .
loom project apply-watch-policy {{.Slug}} --project-root .
loom project backup-status {{.Slug}}
` + "```" + `

Validation and registration do not start file watching. The owner node applies
watched-root desired state only after the explicit apply step.

## Safety Rules

- Do not store secrets, tokens, private keys, or ` + "`.env`" + ` files here.
- Avoid backing up large dependency folders such as ` + "`node_modules/`" + `, ` + "`.venv/`" + `,
  ` + "`vendor/`" + `, ` + "`dist/`" + `, or ` + "`build/`" + `.
- Keep repo paths project-relative.
- Update ` + "`.loom/contracts/repos.yaml`" + ` when watched-root policy or explicit
  membership changes.
- Never infer members from folders, ` + "`.git`" + ` directories, or worktrees.
- Do not assume repo contents are searchable unless sync/index policy says so.
`

const reposAgentsTemplate = `# Repositories Agents

This folder is for project-related code and repository material.

LOOM projects can collect notes, scripts, automations, and code under one
project identity. The ` + "`" + `repos/` + "`" + ` facet is where project-owned or project-linked
code repositories are represented inside the project tree.

The important boundary is this: ` + "`" + `repos/` + "`" + ` records code as project material, but
it is not where callable LOOM behavior is declared. If a repo contains something
that should become callable, expose it through ` + "`" + `scripts/` + "`" + `, ` + "`" + `connectors/` + "`" + `, or
` + "`" + `modules/` + "`" + ` and leave ` + "`" + `repos/` + "`" + ` as the source-code/context layer.

## What Belongs Here

Use this folder for:

- small project-owned code repositories
- lightweight source folders that belong to this project
- README files describing external repositories tied to the project
- project-local scripts or source examples that should be backed up with the
  project
- metadata or notes that explain how an external repo relates to the project
- implementation docs that help future agents understand the project codebase

Large standalone repositories can live outside this folder if the user wants to
keep normal development checkouts separate. In that case, use notes or repo
metadata here to document the relationship.

## What Does Not Belong Here

Do not use this folder for:

- secrets, tokens, private keys, OAuth refresh tokens, or ` + "`" + `.env` + "`" + ` files
- large generated dependency trees such as ` + "`" + `node_modules/` + "`" + `, ` + "`" + `.venv/` + "`" + `, ` + "`" + `vendor/` + "`" + `,
  ` + "`" + `dist/` + "`" + `, ` + "`" + `build/` + "`" + `, target directories, or caches
- one-off runtime outputs that should be regenerated
- capability contracts, connector contracts, module manifests, or automation
  contracts that belong in their dedicated facets

## Repos Contract

` + "`" + `.loom/contracts/repos.yaml` + "`" + ` declares watched-root policy and explicit
repository membership for the project.

The repo contract and project policies compile into watched-root desired state.
That lets LOOM back up repo-related files, optionally sync selected project
documentation, and keep project/code relationships visible in the backend.

In ` + "`" + `repos.contract.v0.4` + "`" + `, ` + "`" + `watch_roots` + "`" + ` retain the existing policy meaning and
` + "`" + `members` + "`" + ` are explicit identities. A watched root never implies that a folder,
Git checkout, or worktree is a member. If a repo contains markdown that should
be indexed or synced, make that intent explicit in policy instead of assuming
every file under ` + "`" + `repos/` + "`" + ` is searchable.

Validation and registration do not start file watching. The usual lifecycle is:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project watch-plan .
loom project register .
loom project apply-watch-policy {{.Slug}} --project-root .
loom project backup-status {{.Slug}}
` + "`" + `` + "`" + `` + "`" + `

The owner node-agent applies local filesystem watchers after main records the
desired policy.

## Agent Workflow

When adding or changing repo material:

1. Decide whether the repo should live inside this project or be referenced as an
   external checkout.
2. Put human/agent context near the repo root: what it is, why it belongs to the
   project, how to test it, and how it relates to LOOM.
3. Update ` + "`" + `.loom/contracts/repos.yaml` + "`" + ` if watched-root policy or the explicit
   member set changes. Never discover membership from folders or Git metadata.
4. If the repo contains callable behavior, add the callable contract in the
   correct facet instead of relying on free-form docs.
5. Run project validation and inspect the watch plan before applying policy.

## Editing Rules

- Keep repository material project-relative.
- Do not store secret values, tokens, private keys, ` + "`" + `.env` + "`" + ` files, or generated
  credentials.
- Avoid committing huge dependency folders, build artifacts, caches, or binary
  outputs unless the user explicitly wants them backed up.
- Keep repo descriptions explicit enough that another agent can understand why
  this repo belongs to the project.
- If a repo exposes scripts or capabilities, declare those through ` + "`" + `scripts/` + "`" + `,
  ` + "`" + `connectors/` + "`" + `, or ` + "`" + `modules/` + "`" + `; do not hide runtime behavior in repo notes.
- Preserve stable folder names when other contracts refer to them.
- Run ` + "`" + `loom project validate .` + "`" + ` from the project root after edits.

## Common Mistakes

- Do not assume everything in ` + "`" + `repos/` + "`" + ` is synced. Sync and backup behavior comes
  from the repo contract and policy files.
- Do not move external working repos into this folder without considering size,
  backup cost, and user workflow.
- Do not use ` + "`" + `repos/` + "`" + ` as a secret store.
- Do not create backend object IDs or watched-root IDs by hand.
- Do not rename a repo root without updating ` + "`" + `loom.repos.yaml` + "`" + ` and checking the
  generated watch plan.
`

const datasetsReadmeTemplate = `# Datasets

Use this folder for project datasets, dataset samples, fixtures, and references.

In v0.3, ` + "`datasets/`" + ` is an organizational facet. It does not have a dedicated
dataset contract yet, and files placed here are not automatically synced,
indexed, backed up, or exposed as capabilities.

## What Belongs Here

- small sample datasets
- test fixtures
- input/output corpora for scripts or connectors
- schema notes and data dictionaries
- README files describing external datasets
- references to remote object stores, APIs, databases, or files

For large datasets, prefer a small representative sample plus instructions for
where the authoritative source lives.

## Recommended Shape

` + "```text" + `
datasets/<dataset_key>/
  README.md
  schema.md
  samples/
  fixtures/
` + "```" + `

Each dataset README should explain what the dataset represents, where the
authoritative source lives, expected format/schema, privacy concerns, and which
scripts, tests, or docs use it.

## Relationship To Sync And Backup

Putting files under ` + "`datasets/`" + ` does not automatically make them background
managed. If a dataset should be backed up, synced, or indexed, declare that in
project policy and inspect the watch plan.

Useful commands:

` + "```sh" + `
loom project validate .
loom project watch-plan .
loom project backup-status {{.Slug}}
` + "```" + `

## Safety Rules

- Do not store secrets or credential exports.
- Do not copy large external datasets into the project without user approval.
- Do not store private personal data without explicit policy.
- Do not rely on implicit sync or backup behavior.
- Keep samples small, representative, and documented.
`

const datasetsAgentsTemplate = `# Datasets Agents

This folder contains project datasets and dataset references.

Datasets are project context, not runtime wiring by themselves. A dataset can be
a small local fixture, a sample input/output corpus, a reference to an external
data source, or documentation about data that the project uses. In the current
v0.3 template, this facet is intentionally lightweight: there is no dedicated
dataset contract like ` + "`" + `loom.dataset.yaml` + "`" + ` yet.

## Dataset Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
dataset files or references -> project context -> validation/backup/sync policy as declared elsewhere
` + "`" + `` + "`" + `` + "`" + `

Putting a file under ` + "`" + `datasets/` + "`" + ` does not automatically make it indexed,
synced, backed up, or exposed as a capability. Those behaviors come from project
policies, watched-root plans, scripts, connectors, or modules.

## What Belongs Here

Use this folder for:

- small sample datasets used by tests or examples
- input/output fixtures for project development
- README files describing external datasets
- data dictionaries and schema notes
- local synthetic data that is safe to store in the project
- references to database tables, object stores, APIs, or remote files

Large datasets can be referenced instead of copied into the project. Prefer a
small representative sample plus clear retrieval instructions when the full
dataset is large, private, generated, or expensive to duplicate.

## What Does Not Belong Here

Do not store:

- secret values, API tokens, passwords, private keys, or credential dumps
- private personal data unless the user explicitly says it belongs here
- large generated outputs that should be regenerated
- database snapshots without a clear backup/security policy
- files that should be treated as notes or documentation instead

If a dataset contains sensitive material, document the reference and required
credential name without storing the sensitive payload in the project tree.

## Organization Rules

Prefer this shape:

` + "`" + `` + "`" + `` + "`" + `text
datasets/<dataset_key>/
  README.md
  schema.md
  samples/
  fixtures/
` + "`" + `` + "`" + `` + "`" + `

Each dataset README should explain:

- what the dataset represents
- whether the data is real, synthetic, sample, or external
- where the authoritative source lives
- how scripts/tests use it
- expected format and schema
- size expectations
- privacy or retention concerns

Use stable dataset keys. Scripts, tests, docs, and future dataset contracts may
refer to these paths.

## Relationship To Sync, Backup, And Indexing

Datasets are not automatically watched just because the facet is enabled. If a
dataset should be backed up, synced, or indexed, express that in the project
policy/watch configuration rather than assuming it from folder placement.

As a rule:

- sample fixtures can usually be kept in git/project source
- large raw datasets should usually be referenced externally
- generated datasets should usually be excluded or regenerated by scripts
- markdown data dictionaries may belong in ` + "`" + `docs/` + "`" + ` or ` + "`" + `notes/` + "`" + ` if they should be
  searchable knowledge

## Agent Workflow

When adding a dataset:

1. Decide whether to store data, store only a sample, or store a reference.
2. Create ` + "`" + `datasets/<dataset_key>/` + "`" + `.
3. Add a README with source, schema, usage, privacy, and size notes.
4. Put small fixtures or samples under clear subfolders.
5. Update scripts/tests/docs that depend on the dataset path.
6. Update sync/backup/watch policy only if the dataset needs background
   handling.
7. Run ` + "`" + `loom project validate .` + "`" + ` from the project root.

## Common Mistakes

- Do not assume ` + "`" + `datasets/` + "`" + ` has a dedicated runtime contract in v0.3.
- Do not store secrets or credential exports.
- Do not copy huge external datasets into the project without user approval.
- Do not put user-private data here without explicit policy.
- Do not rely on implicit sync or backup behavior.
- Do not leave dataset files unexplained; future agents need source, schema, and
  usage context.
`

const docsReadmeTemplate = `# Documentation

Use this folder for operational documentation that helps humans and agents build,
run, extend, and troubleshoot the project.

Docs are different from notes:

- ` + "`notes/`" + ` is durable project knowledge that may be synced and indexed.
- ` + "`docs/`" + ` is practical documentation for operating and extending the project.

## Start Here

Read [contracts.md](contracts.md) before changing YAML or JSON contracts. It
explains the main contract files, stable names, capability URLs, activation
order, and safety rules.

## What Belongs Here

- contract authoring guides
- architecture notes
- setup and development instructions
- operational runbooks
- troubleshooting guides
- connector or automation walkthroughs
- decision records

Facet-local docs can stay near the facet they describe. For example, a connector
README belongs inside ` + "`connectors/<connector>/`" + `, while broad project operating
docs belong here.

## Suggested Shape

` + "```text" + `
docs/
  contracts.md
  architecture.md
  operations.md
  development.md
  troubleshooting.md
  decisions/
    0001-example.md
` + "```" + `

Only add files when they reduce ambiguity. Small projects do not need the whole
shape.

## Documentation Rules

- Keep commands executable and project-relative.
- Include full capability URLs when documenting capabilities.
- Include target capability URLs when documenting schedules or direct events.
- Document credential reference names only, never secret values.
- Update docs when contract names, provider keys, endpoints, or lifecycle steps
  change.
- Run ` + "`loom project validate .`" + ` if examples or contract references changed.
`

const docsAgentsTemplate = `# Documentation Agents

This folder contains project documentation for humans and agents.

Docs are different from notes. ` + "`" + `notes/` + "`" + ` is durable project knowledge that may be
synced, indexed, and queried as part of the user's knowledge base. ` + "`" + `docs/` + "`" + ` is
operational documentation for building, running, extending, testing, and
understanding this project.

## Documentation Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
project behavior -> docs -> future humans/agents can safely operate it
` + "`" + `` + "`" + `` + "`" + `

Documentation does not create backend runtime state by itself. It supports the
contracts that do: scripts, connectors, schedules, direct events, modules, and
policies.

## What Belongs Here

Use this folder for:

- architecture notes for this project
- setup and development instructions
- operational runbooks
- connector or automation walkthroughs
- troubleshooting guides
- decision records that explain why a contract is shaped a certain way
- user-facing instructions that should not be mixed into executable packages

Facet-local docs can stay near the facet they describe. For example, a connector
README belongs inside ` + "`" + `connectors/<connector>/` + "`" + `, while broad project operating
docs belong here.

## Documentation Rules

- Keep files project-relative.
- Use stable filenames and headings so other agents can link to them.
- Prefer short examples that match real contract files in the project.
- When documenting a capability, include its full URL.
- When documenting a schedule or direct event, include the one target capability
  it calls.
- When documenting credentials, include reference names only, never secret
  values.
- Update docs in the same change as contract or behavior changes.
- Run ` + "`" + `loom project validate .` + "`" + ` from the project root after edits.

## Recommended Shape

Use this shape when the project grows:

` + "`" + `` + "`" + `` + "`" + `text
docs/
  architecture.md
  operations.md
  development.md
  troubleshooting.md
  decisions/
    0001-example.md
` + "`" + `` + "`" + `` + "`" + `

Small projects do not need all of these files. Add structure when it removes
ambiguity for the next human or agent.

## Agent Workflow

When adding documentation:

1. Decide whether the information belongs in ` + "`" + `docs/` + "`" + `, ` + "`" + `notes/` + "`" + `, or a facet-local
   README.
2. Link to the source contract or capability URL being explained.
3. Keep instructions executable and specific.
4. Do not copy secret values, machine-local absolute paths, or private tokens.
5. Validate the project if contract examples or paths changed.

## Common Mistakes

- Do not let docs become the only place where runtime behavior is declared.
- Do not document a capability URL that does not exist in the contracts.
- Do not put durable research knowledge here if it should be synced/indexed as
  ` + "`" + `notes/` + "`" + `.
- Do not store credentials or copied private payloads.
- Do not leave stale activation instructions after contract names change.
`

const contractsDocTemplate = `# LOOM Contract Authoring Guide

This project is controlled by contract files. A contract is a structured file
that tells LOOM what the project declares. Contracts are validated locally,
registered into the backend, and activated only when a supported runtime surface
should exist.

## Core Lifecycle

` + "```text" + `
edit contracts -> validate -> plan -> register -> activate/apply specific facets
` + "```" + `

Run these from the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
` + "```" + `

Activation is facet-specific:

` + "```sh" + `
loom project activate {{.Slug}} --facet scripts --project-root .
loom project activate {{.Slug}} --facet connectors --project-root .
loom project activate {{.Slug}} --facet schedules --project-root .
loom project activate {{.Slug}} --facet direct-events --project-root .
` + "```" + `

Watched-root policy is also explicit:

` + "```sh" + `
loom project watch-plan .
loom project apply-watch-policy {{.Slug}} --project-root .
` + "```" + `

## Stable Names

Treat these fields as public interfaces:

- ` + "`project.slug`" + `
- ` + "`project.owner_node`" + `
- script ` + "`id`" + `
- script exposure ` + "`expose.endpoint`" + `
- connector ` + "`provider.key`" + `
- connector capability ` + "`endpoint`" + `
- schedule ` + "`schedule.key`" + `
- direct-event ` + "`event.key`" + `, ` + "`integration.key`" + `, and ` + "`endpoint.slug`" + `
- module ` + "`module.id`" + `
- watched-root keys in policy and facet contracts

Changing them can break capability URLs, schedules, direct events, portal
navigation, tests, docs, and user habits.

## Capability URLs

Capabilities use:

` + "```text" + `
node@provider.endpoint
` + "```" + `

Examples:

` + "```text" + `
{{.ExampleCapability}}
{{.OwnerNode}}@example_connector.ping
` + "```" + `

The prefix before ` + "`@`" + ` is the owner/runtime node. The provider is usually the
project slug for project-owned scripts, or a connector/module provider key for
larger provider namespaces.

## Main Contract Files

- ` + "`loom.project.yaml`" + `: root project identity, enabled facets, policy refs, portal metadata
- ` + "`scripts/<script>/loom.script.yaml`" + `: how to run a script package
- ` + "`scripts/<script>/loom.exposure.yaml`" + `: whether that script becomes a capability
- ` + "`connectors/<connector>/loom.connector.yaml`" + `: provider namespace and endpoints
- ` + "`schedules/<schedule>/loom.schedule.yaml`" + `: timer that calls one capability
- ` + "`direct_events/<event>/loom.direct_event.yaml`" + `: external payload mapped into one capability call
- ` + "`workflows/<workflow>/loom.workflow.yaml`" + `: executable workflow package or design-only workflow intent
- ` + "`modules/<module>/module.json`" + `: module package manifest
- ` + "`modules/<module>/loom.module_project.yaml`" + `: module registration/install/exposure intent
- ` + "`policies/*.yaml`" + `: sync, backup, worker, and credential policy

## One-Capability Rule

Schedules and direct events call exactly one capability. If an automation needs
several operations, wrap those operations behind one workflow, script, or
connector capability and target that URL.

## Secret Rule

Do not put secret values anywhere in this project. Store only credential
reference names, auth profile names, setup instructions, and fake placeholders.

## Final Checklist

Before finishing any contract change:

` + "```sh" + `
loom project validate .
loom project plan .
` + "```" + `

Then check:

- Did I keep stable names stable?
- Did I update every dependent capability URL?
- Did I avoid storing secret values?
- Did I add or update examples?
- Did I activate dependencies before automations?
- Did I update README or AGENTS guidance if behavior changed?
`

const secretsReadmeTemplate = `# Secret References

This folder is for documenting secret requirements and credential references.

Do not store secret values in this project. LOOM project files may name a
credential, describe what it is used for, and explain how to create it outside
the project tree. The actual secret value must live in LOOM credentials or an
external secret store.

## Allowed Here

- credential reference names
- auth profile names
- setup instructions
- rotation and revocation notes
- fake placeholder examples
- mappings from connectors/events/scripts to required credentials

## Never Store Here

- API keys
- OAuth client secrets or refresh tokens
- passwords
- private keys
- webhook signing secrets
- session cookies
- database URLs with embedded credentials
- copied ` + "`.env`" + ` files
- real sensitive payloads

## Credential Policy

The project credential policy lives at:

` + "```text" + `
policies/credentials.yaml
` + "```" + `

It should keep:

` + "```yaml" + `
inline_secrets_allowed: false
` + "```" + `

If a connector or direct event needs a token, create or reference a LOOM
credential/auth profile outside this project and document only the reference
name here.

## Suggested Documentation Shape

` + "```text" + `
secrets/
  README.md
  required_credentials.md
` + "```" + `

A credential note should include reference name, purpose, permissions, used-by
list, rotation expectations, and setup steps that do not reveal the value.

## Safety Check

Before finishing changes that touch credentials, run a search and inspect
matches manually:

` + "```sh" + `
rg -n "token|secret|password|api[_-]?key|private[_-]?key|BEGIN " .
` + "```" + `

Documentation may contain these words, but it must not contain real values.
`

const secretsAgentsTemplate = `# Secret References Agents

This folder is for secret references and secret-handling documentation only.

Do not store secret values in this project tree. LOOM project files can describe
that a credential is needed, what it is used for, and which LOOM credential or
auth profile should provide it, but the actual value must live outside the
project source files.

## Secret Reference Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
credential need -> reference name in project files -> actual secret stored in LOOM credentials or external secret store
` + "`" + `` + "`" + `` + "`" + `

Contracts in ` + "`" + `scripts/` + "`" + `, ` + "`" + `connectors/` + "`" + `, ` + "`" + `direct_events/` + "`" + `, and ` + "`" + `policies/` + "`" + ` should
refer to credentials by name or existing reference. They should not contain the
credential value.

## What Belongs Here

Use this folder for:

- README files explaining required credentials
- lists of credential reference names
- setup instructions for creating credentials outside the project tree
- notes about which capabilities, connectors, or events use each credential
- rotation and revocation procedures

## What Must Never Be Stored Here

Never store:

- API keys
- OAuth client secrets or refresh tokens
- passwords
- private keys
- webhook signing secrets
- session cookies
- database URLs with credentials
- copied ` + "`" + `.env` + "`" + ` files
- production payloads containing sensitive data

If you need an example, use fake placeholder values that are obviously invalid,
such as ` + "`" + `example.invalid` + "`" + ` or ` + "`" + `REPLACE_WITH_CREDENTIAL_REFERENCE` + "`" + `.

## Relationship To Policy

` + "`" + `policies/credentials.yaml` + "`" + ` is the project-level credential policy. In this
template it sets:

` + "`" + `` + "`" + `` + "`" + `text
inline_secrets_allowed: false
` + "`" + `` + "`" + `` + "`" + `

Keep that rule. If a connector or direct event needs a token, create or reference
a LOOM credential/auth profile outside project files and document the reference
name here.

## Agent Workflow

When adding a credential requirement:

1. Identify which script, connector, module, schedule, or direct event needs it.
2. Choose a stable credential reference name.
3. Document the purpose, required permissions, and rotation expectations.
4. Update the relevant contract to use the reference, not the value.
5. Confirm no secret values were added with ` + "`" + `rg` + "`" + ` before finishing.
6. Run ` + "`" + `loom project validate .` + "`" + ` from the project root.

Useful local check:

` + "`" + `` + "`" + `` + "`" + `sh
rg -n "token|secret|password|api[_-]?key|private[_-]?key|BEGIN " .
` + "`" + `` + "`" + `` + "`" + `

Review matches manually. Some matches are expected in documentation, but no real
values should be present.

## Common Mistakes

- Do not create ` + "`" + `.env` + "`" + ` files in this folder.
- Do not paste temporary tokens "just for testing".
- Do not put webhook shared secrets into direct-event YAML.
- Do not put provider API keys into connector examples.
- Do not confuse a credential reference with the credential value.
- Do not weaken ` + "`" + `inline_secrets_allowed: false` + "`" + `.
`

const reposContractTemplate = `kind: loom.repos
schema_version: repos.contract.v0.4

repos:
  status: draft
  defaults:
    sync: false
    backup: {{if .HasBackupPolicy}}true{{else}}false{{end}}
    index: false
    include:
      - "**/*"
    exclude: []
  watch_roots:
    - key: repos
      path: .
      display_name: Project Repositories
{{if .RepositoryMembers}}  members:
{{range .RepositoryMembers}}
    - id: {{.ID}}
      key: {{.Key}}
      path: {{.Path}}
      role: {{.Role}}
{{if .StateRoot}}
      state_root: {{.StateRoot}}
{{end}}
{{end}}{{else}}  members: []
{{end}}
`

const moduleReadmeTemplate = `# Example Module

This is a minimal LOOM module package placeholder.

Modules are for deeper LOOM expansion than a single script or connector. A
module can eventually provide object types, provider namespaces, capabilities,
usage documents, storage requirements, and backup hooks.

In v0.3, this package is intentionally conservative: it can be validated and
registered as project intent, but install, enable, and exposure behavior remains
explicit-only.

## Files

- ` + "`module.json`" + `: backend module package manifest.
- ` + "`loom.module_project.yaml`" + `: project wrapper that declares registration,
  install, exposure, and validation intent.
- ` + "`README.md`" + `: package-level context.

## Module Manifest

` + "`module.json`" + ` declares what the module is:

` + "```json" + `
{
  "module": {
    "id": "loom.{{.Slug}}",
    "name": {{printf "%q" .Name}},
    "version": "0.1.0",
    "kind": "native"
  }
}
` + "```" + `

It also declares what the module requires and provides. The scaffolded module
does not provide runtime providers or capabilities yet.

## Project Wrapper

` + "`loom.module_project.yaml`" + ` declares how this project wants LOOM to treat the
module package.

The important safety defaults are:

` + "```yaml" + `
install:
  plan: explicit_only
  install_after_register: false
  enable_after_install: false

exposure:
  plan: explicit_only
  expose_after_enable: false
` + "```" + `

Registration intent is not the same thing as installing or enabling module
runtime behavior.

## Lifecycle

From the project root:

` + "```sh" + `
loom project validate .
loom project plan .
loom project register .
loom project activate {{.Slug}} --facet modules --project-root .
loom modules list
` + "```" + `

Activation records supported module package metadata. A later explicit install
or enable step should be treated as a separate operational decision.

## When To Use A Module

Use a module when the project needs to extend LOOM itself with a durable package
surface.

Use something smaller when possible:

- use ` + "`scripts/`" + ` for one project-owned executable capability
- use ` + "`connectors/`" + ` for a provider namespace wrapping an external/local system
- use ` + "`schedules/`" + ` or ` + "`direct_events/`" + ` only to trigger one existing capability

## Safety Rules

- Do not use a module for a simple shell command.
- Do not declare stateful storage without a backup plan or backup hooks.
- Do not expose module capabilities from scaffolded placeholders.
- Keep module IDs, provider keys, and capability names stable.
- Do not store credentials in module files.
`

const moduleManifestTemplate = `{
  "module": {
    "id": "loom.{{.Slug}}",
    "name": {{printf "%q" .Name}},
    "version": "0.1.0",
    "kind": "native",
    "description": "Scaffolded LOOM module."
  },
  "requires": {},
  "storage": {
    "database": {
      "required": false
    },
    "filesystem": {
      "required": false
    }
  },
  "provides": {
    "object_types": [],
    "providers": [],
    "capabilities": [],
    "usage_documents": [],
    "backup_hooks": []
  }
}
`

const moduleProjectContractTemplate = `kind: loom.module_project
schema_version: module_project.contract.v0.3

module:
  manifest: module.json
  status: draft

project:
  owned_by_project: true
  expose_in_project_portal: true

registration:
  register: true

install:
  plan: explicit_only
  target_node: main
  scope: system
  install_after_register: false
  enable_after_install: false

exposure:
  plan: explicit_only
  expose_after_enable: false
  capabilities: []

validation:
  require_usage_docs: false
  require_backup_hooks_for_stateful_storage: true

metadata:
  scaffolded_by: loom
`

const policiesReadmeTemplate = `# Policies

Policies describe how LOOM should treat this project in the background.

They are declarative. Editing a policy file does not start a watcher, sync job,
backup job, worker, or credential flow by itself. LOOM validates policy files,
derives desired state, registers that desired state, and applies it only through
explicit lifecycle commands.

## Files

- ` + "`sync.yaml`" + `: which project files should become synced/indexed object content.
- ` + "`backup.yaml`" + `: which project files should be preserved as raw project
  material.
- ` + "`workers.yaml`" + `: whether this project wants background workers.
- ` + "`credentials.yaml`" + `: credential-reference rules.

## Sync Versus Backup

Sync is for curated knowledge that should become searchable/queryable object
content. In this template, the notes watched root scans broadly so attachments
can be backed up, while LOOM narrows sync/index output to markdown/text files:

` + "```text" + `
notes/**
` + "```" + `

Backup is for preserving project files. In this template, backup covers the
project root with explicit includes and excludes.

Do not broaden sync or backup casually. Full repositories, generated folders,
binary datasets, dependency trees, and secret folders can be expensive or unsafe
to track.

## Watched-Root Lifecycle

Inspect derived watched-root intent:

` + "```sh" + `
loom project watch-plan .
` + "```" + `

Register the project:

` + "```sh" + `
loom project register .
` + "```" + `

Ask the owner node to apply watched-root desired state:

` + "```sh" + `
loom project apply-watch-policy {{.Slug}} --project-root .
` + "```" + `

Then inspect status:

` + "```sh" + `
loom project sync-status {{.Slug}}
loom project backup-status {{.Slug}}
` + "```" + `

## Credential Policy

` + "`credentials.yaml`" + ` should keep:

` + "```yaml" + `
inline_secrets_allowed: false
` + "```" + `

Store credential reference names only. Do not put tokens, passwords, webhook
secrets, private keys, database URLs with credentials, or copied ` + "`.env`" + ` files in
the project tree.

## Worker Policy

` + "`workers.yaml`" + ` declares intent, not a running process. Keep workers disabled
until the project has a clear background task and the owner node should run it.

## Safety Rules

- Keep policies conservative by default.
- Prefer explicit includes and excludes over broad whole-tree rules.
- Never include ` + "`secrets/`" + ` in backup or sync.
- Do not assume validation or registration starts watchers.
- Review ` + "`loom project watch-plan .`" + ` before applying policy.
`

const policiesAgentsTemplate = `# Policy Agents

This folder contains project-level policy contracts.

Policies describe how LOOM should treat the project in the background: sync,
backup, worker behavior, and credential references. Policy files are
declarative. They do not start workers, watchers, sync jobs, or backups just
because they exist.

## Policy Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
policy files -> validation/plan -> registered desired state -> explicit owner-node application -> workers/watchers act
` + "`" + `` + "`" + `` + "`" + `

The project folder is source of truth. The backend and node-agent derive desired
state from the contracts after explicit lifecycle commands.

## Policy Files

This template uses:

- ` + "`" + `sync.yaml` + "`" + `: which project files should become synced/indexed object content
- ` + "`" + `backup.yaml` + "`" + `: which project files should be backed up as raw project material
- ` + "`" + `workers.yaml` + "`" + `: project worker policy intent
- ` + "`" + `credentials.yaml` + "`" + `: credential reference policy

Each file should keep its ` + "`" + `kind` + "`" + ` and ` + "`" + `schema_version` + "`" + ` stable unless the LOOM
contract schema changes.

## Sync Policy

Sync policy is for files that should become structured/searchable project
knowledge. In this template, the watched root scans the whole notes folder so
attachments can be backed up, while LOOM narrows sync/index output to
markdown/text files:

` + "`" + `` + "`" + `` + "`" + `text
notes/**
` + "`" + `` + "`" + `` + "`" + `

Do not broaden sync casually. Syncing large trees, generated files, secrets,
binary files, or full repositories can make object search noisy and expensive.

Use sync for curated knowledge. Use backup for preserving raw project files.

## Backup Policy

Backup policy is for preserving project material. In this template, backup is
incremental raw backup for the project root with explicit includes and excludes.

Keep exclusions for:

- secrets
- dependency trees
- virtual environments
- git internals
- generated caches
- platform noise such as ` + "`" + `.DS_Store` + "`" + `

When adding a new facet or large folder, decide deliberately whether it belongs
in backup, sync, both, or neither.

## Worker Policy

Worker policy declares background-process intent. It should not imply that a
worker is already running.

In this template, workers are disabled by default:

` + "`" + `` + "`" + `` + "`" + `text
workers.enabled: false
` + "`" + `` + "`" + `` + "`" + `

Turn workers on only when the project has a clear background task and the owner
node is ready to run it. Activation/application remains an explicit lifecycle
step.

## Credential Policy

Credential policy should remain reference-only:

` + "`" + `` + "`" + `` + "`" + `text
inline_secrets_allowed: false
` + "`" + `` + "`" + `` + "`" + `

Do not add inline tokens or secret values here. If a connector, direct event, or
module needs a credential, reference a credential or auth profile that is created
outside project source files.

## Lifecycle

From the project root:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate .
loom project plan .
loom project watch-plan .
loom project register .
loom project apply-watch-policy {{.Slug}} --project-root .
loom project sync-status {{.Slug}}
loom project backup-status {{.Slug}}
` + "`" + `` + "`" + `` + "`" + `

Validation checks local contract shape. ` + "`" + `watch-plan` + "`" + ` shows derived watched-root
intent. Registration records project intent. ` + "`" + `apply-watch-policy` + "`" + ` is the step
that asks the owner node-agent to apply desired watched-root state.

## Agent Workflow

When changing policies:

1. Identify which behavior is changing: sync, backup, workers, or credentials.
2. Keep defaults conservative.
3. Add explicit includes and excludes rather than broad whole-tree rules.
4. Confirm no secret-bearing path is included.
5. Run validation and inspect ` + "`" + `loom project watch-plan .` + "`" + `.
6. Apply watch policy only when the user wants the owner node to adopt it.

## Common Mistakes

- Do not assume ` + "`" + `loom project register .` + "`" + ` starts watchers.
- Do not sync full repositories by default.
- Do not back up secret folders.
- Do not put credential values in ` + "`" + `credentials.yaml` + "`" + `.
- Do not enable workers without a clear owner-node runtime expectation.
- Do not make broad include patterns without matching excludes.
`

const syncPolicyTemplate = `kind: loom.project_sync_policy
schema_version: sync.policy.v0.3

sync:
  enabled: true
  defaults:
    safe_root: project
    mode: markdown
    index: markdown_text
    delete: tombstone
    max_file_bytes: 1048576
    max_text_bytes: 524288
    logical_name_strategy: relative_path
  roots:
    - key: notes
      path: notes
      include:
        - "**/*"
      exclude: []
`

const backupPolicyTemplate = `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3

backup:
  enabled: true
  defaults:
    safe_root: project
    mode: incremental_raw
    max_file_bytes: 1048576
    max_batch_bytes: 1048576
    max_pending_items: 10000
    max_pending_bytes: 1073741824
    include_deletion_markers: true
    on_limit: degrade_and_require_manual_action
  roots:
{{if .HasNotes}}
    - key: notes
      path: notes
      include:
        - "**/*"
      exclude: []
{{end}}
{{if .HasRepos}}
    - key: repos
      path: repos
      include:
        - "**/*"
      exclude: []
{{end}}
    - key: project
      path: .
      include:
        - .loom/project.yaml
        - .loom/contracts/**
        - .loom/agents/**
        - .loom/templates/**
        - .loom/tools/**
        - .loom/.gitignore
        - .loomignore
        - README.md
        - AGENTS.md
        - notes/**
        - scripts/**
        - connectors/**
        - schedules/**
        - direct_events/**
        - modules/**
      exclude: []
`

const workerPolicyTemplate = `kind: loom.worker_policy
schema_version: worker.policy.v0.3

workers:
  enabled: false
  notes: Scaffolded policy only. Registration decides which workers apply.
`

const credentialsPolicyTemplate = `kind: loom.credentials_policy
schema_version: credentials.policy.v0.3

credentials:
  inline_secrets_allowed: false
  references: []
`

const testsReadmeTemplate = `# Tests

Use this folder for project validation and smoke-test helpers.

The default tests should be safe to run in a local project checkout. They should
not require credentials, mutate user data, or depend on external services.

## Default Check

Run:

` + "```sh" + `
./tests/validate_project.sh
` + "```" + `

The helper runs:

` + "```sh" + `
loom project validate <project-root>
loom project plan <project-root>
` + "```" + `

This verifies contract shape and the read-only registration plan.

## Test Layers

Use these layers as the project grows:

1. Contract validation: ` + "`loom project validate .`" + `
2. Registration preview: ` + "`loom project plan .`" + `
3. Watch policy preview: ` + "`loom project watch-plan .`" + `
4. Activation smoke: inspect backend records after activating a facet.
5. Runtime smoke: call low-risk capabilities or preview direct-event mappings.

Keep default tests at layers 1-3 unless the environment is explicitly prepared
for runtime testing.

## Runtime Smoke Examples

After activating scripts:

` + "```sh" + `
loom capability call {{.ExampleCapability}} --input '{}' --wait
` + "```" + `

After activating connectors:

` + "```sh" + `
loom capability call {{.OwnerNode}}@example_connector.ping --input '{}' --wait
` + "```" + `

Run these only when a LOOM node is available and the operation is safe.

## Safety Rules

- Keep tests deterministic.
- Do not require real credentials in default tests.
- Do not call high-risk capabilities by default.
- Do not fire schedules or ingest direct events with external side effects
  without an explicit manual test path.
- Use project-relative paths.
- Keep shell scripts portable and fail loudly with ` + "`set -euo pipefail`" + `.
`

const testsAgentsTemplate = `# Test Agents

This folder contains project validation and smoke-test helpers.

Tests in this project should prove that the project contracts are coherent and
that exposed runtime surfaces still work after activation. Keep tests small,
portable, and explicit.

## Test Mental Model

The flow is:

` + "`" + `` + "`" + `` + "`" + `text
contract edit -> validate -> plan -> activate supported facet -> smoke-test runtime surface
` + "`" + `` + "`" + `` + "`" + `

Validation and planning are always the first checks. Runtime smoke tests should
come after the relevant facet is activated in a suitable LOOM environment.

## Current Test Surface

This template includes:

` + "`" + `` + "`" + `` + "`" + `text
tests/validate_project.sh
` + "`" + `` + "`" + `` + "`" + `

It runs:

` + "`" + `` + "`" + `` + "`" + `sh
loom project validate <project-root>
loom project plan <project-root>
` + "`" + `` + "`" + `` + "`" + `

That script is intentionally minimal. It should work from the project tree
without requiring credentials, network access, or destructive side effects.

## What Belongs Here

Use this folder for:

- validation scripts
- project plan checks
- smoke scripts for safe capabilities
- fixture comparison scripts
- direct-event mapping preview tests
- connector ping/read-only endpoint checks
- docs explaining how to manually verify activation

Facet-local tests may also live inside the facet package when that keeps
fixtures closer to the contract they verify.

## Test Rules

- Keep tests deterministic.
- Prefer read-only or low-risk capability calls for smoke tests.
- Do not require real credentials unless the test is explicitly marked manual.
- Do not mutate user data in default tests.
- Do not assume the main node is clean unless the test says so.
- Keep scripts portable Bash when possible.
- Use project-relative paths.
- Fail loudly on errors with ` + "`" + `set -euo pipefail` + "`" + `.

## Recommended Test Layers

Use these layers as the project grows:

1. Contract validation:
   ` + "`" + `loom project validate .` + "`" + `
2. Read-only plan inspection:
   ` + "`" + `loom project plan .` + "`" + `
3. Watch policy inspection:
   ` + "`" + `loom project watch-plan .` + "`" + `
4. Activation smoke:
   activate the needed facet and inspect resulting backend records.
5. Runtime smoke:
   call safe capabilities, preview direct-event mappings, or fire safe schedules.

Do not put activation or runtime smoke checks into the default script unless the
test environment is expected to have a running LOOM node and the operation is
safe.

## Agent Workflow

When adding a test:

1. Decide whether it is default, optional, or manual.
2. Keep default tests credential-free and side-effect-light.
3. Add clear setup instructions for optional/manual tests.
4. Use real capability URLs from contracts.
5. Store small fixtures under the relevant facet or under ` + "`" + `tests/fixtures/` + "`" + `.
6. Run the default validation script before finishing.

## Common Mistakes

- Do not make default tests depend on private credentials.
- Do not call high-risk capabilities in default smoke tests.
- Do not hard-code machine-specific absolute paths.
- Do not test placeholder workflows as executable workflows.
- Do not fire schedules or ingest direct events that produce external side
  effects without explicit manual opt-in.
`

const validateProjectScriptTemplate = `#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

loom project validate "$PROJECT_ROOT"
loom project plan "$PROJECT_ROOT"
`

const legacyValidateProjectScriptTemplate = `#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

loom project validate "$PROJECT_ROOT"
loom project plan "$PROJECT_ROOT"
`

const simpleFacetReadmeTemplate = `# {{.Title}}

This folder belongs to the {{.Project}} LOOM project.

It is scaffolded as authored project context. Later slices may add validation,
registration, sync, backup, or portal behavior for this facet.
`

const simpleFacetAgentsTemplate = `# {{.Title}} Agents

- Keep files under ` + "`{{.Folder}}/`" + ` project-relative.
- Do not store secret values.
- Run ` + "`loom project validate .`" + ` from the project root after edits.
`
