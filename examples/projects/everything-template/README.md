# LOOM Everything Template

This is a LOOM project scaffold. A LOOM project is a folder that describes
something the user is working on: notes, code, scripts, automations,
connectors, modules, datasets, documentation, or any combination of those.

The folder is the source of truth. LOOM reads the contracts in this tree,
validates them, registers declared intent in the backend, and activates specific
runtime surfaces only when asked.

## Project Identity

- Project slug: `everything-template`
- Owner node: `main`
- Project provider: `main@everything-template`
- Preset: `minimal`
- Facets: `notes, repos, scripts, workflows, connectors, schedules, direct_events, modules, datasets, docs, tests, secrets, sync_policy, backup_policy, worker_policy, portal`

## What LOOM Does With This Folder

LOOM treats this project as authored source. Files in this folder can declare:

- durable notes that can be synced and indexed
- repo/code material that can be backed up
- scripts that can become project capabilities
- connectors that expose provider-specific capabilities
- schedules that call one capability on a timer
- direct events that map external payloads into one capability call
- modules that describe deeper LOOM expansion packages
- policies for sync, backup, workers, and credential references

Nothing becomes active just because a file exists. Validation, registration,
activation, and watched-root application are explicit lifecycle steps.

## First Commands

Validate local contracts:

```sh
loom project validate .
```

Preview what LOOM would register:

```sh
loom project plan .
```

Register declared project intent:

```sh
loom project register .
```

Inspect project health after registration or activation:

```sh
loom project doctor everything-template --project-root .
```

## Capability URLs

Capabilities use this address form:

```text
node@provider.endpoint
```

In this project:

```text
main@everything-template.hello_world
```

means the capability runs from node `main`, belongs to project provider
`everything-template`, and exposes endpoint `hello_world`.

Project-owned scripts use the project provider by default. Connectors and
modules declare their own provider namespaces.

## Activation Order

Use this order when enabling runtime behavior:

1. Validate and plan the project.
2. Register the project.
3. Activate capabilities first, usually `scripts` or `connectors`.
4. Activate automations that target those capabilities, such as `schedules` and
   `direct-events`.
5. Apply watched-root policy only when the owner node should start using the
   sync/backup/indexing plan.

Example:

```sh
loom project activate everything-template --facet scripts --project-root .
loom project activate everything-template --facet connectors --project-root .
loom project activate everything-template --facet schedules --project-root .
loom project activate everything-template --facet direct-events --project-root .
loom project apply-watch-policy everything-template --project-root .
```

## Facets

- `notes/`: markdown project knowledge that can be synced and indexed
- `repos/`: project-associated code and repository references
- `scripts/`: executable packages that can expose project capabilities
- `workflows/`: executable workflow packages and design-only workflow intent
- `connectors/`: provider namespaces and grouped capability endpoints
- `schedules/`: timer-based automations that call one capability
- `direct_events/`: external notifications mapped into one capability call
- `modules/`: larger LOOM expansion packages
- `datasets/`: datasets, samples, fixtures, or external data references
- `docs/`: operational documentation for humans and agents
- `secrets/`: credential references only, never secret values
- `policies/`: sync, backup, worker, and credential policy
- `tests/`: validation and smoke-test helpers

Every facet with its own `AGENTS.md` has additional editing rules. Read that
file before changing the facet.

## Contract Authoring

Start with [docs/contracts.md](docs/contracts.md) when creating or modifying
LOOM contracts. It explains the main contract files and which fields are stable
public interfaces.

Useful rules:

- Do not invent backend IDs by hand.
- Keep project slugs, provider keys, schedule keys, event keys, module IDs, and
  capability endpoints stable unless all references are updated.
- Do not store secret values anywhere in the project tree.
- Prefer small examples and fixtures that validation can check.
- If an automation needs multiple steps, wrap those steps behind one capability
  and target that capability.

## Smoke Checks

Run the default project check:

```sh
./tests/validate_project.sh
```

After activating the script facet, inspect and call the example capability:

```sh
loom capability inspect main@everything-template.hello_world
loom capability call main@everything-template.hello_world --input '{}' --wait
```

After activating the connector facet, inspect and call the example connector:

```sh
loom providers inspect main@example_connector
loom capability call main@example_connector.ping --input '{}' --wait
```
