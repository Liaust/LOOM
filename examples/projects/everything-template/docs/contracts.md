# LOOM Contract Authoring Guide

This project is controlled by contract files. A contract is a structured file
that tells LOOM what the project declares. Contracts are validated locally,
registered into the backend, and activated only when a supported runtime surface
should exist.

Use this guide when changing YAML or JSON files in this project.

## Core Lifecycle

```text
edit contracts -> validate -> plan -> register -> activate/apply specific facets
```

Run these from the project root:

```sh
loom project validate .
loom project plan .
loom project register .
```

Activation is facet-specific:

```sh
loom project activate everything-template --facet scripts --project-root .
loom project activate everything-template --facet connectors --project-root .
loom project activate everything-template --facet schedules --project-root .
loom project activate everything-template --facet direct-events --project-root .
```

Watched-root policy is also explicit:

```sh
loom project watch-plan .
loom project apply-watch-policy everything-template --project-root .
```

## Stable Names

Treat these fields as public interfaces:

- `project.slug`
- `project.owner_node`
- script `id`
- script exposure `expose.endpoint`
- connector `provider.key`
- connector capability `endpoint`
- schedule `schedule.key`
- direct-event `event.key`, `integration.key`, and `endpoint.slug`
- module `module.id`
- watched-root keys in policy and facet contracts

Changing them can break capability URLs, schedules, direct events, portal
navigation, tests, docs, and user habits.

## Capability URLs

Capabilities use:

```text
node@provider.endpoint
```

Examples:

```text
main@everything-template.hello_world
main@example_connector.ping
```

The prefix before `@` is the owner/runtime node. The provider is usually the
project slug for project-owned scripts, or a connector/module provider key for
larger provider namespaces.

## Root Project Contract

File:

```text
.loom/project.yaml
```

Purpose:

- identifies the project
- names the owner node
- enables facets
- points to policy files
- defines provider defaults
- exposes portal metadata

Important rule: enabling a facet means LOOM should read that facet. It does not
mean the facet is already registered, activated, watched, synced, or backed up.

## Script Contracts

Files:

```text
scripts/<script_key>/loom.script.yaml
scripts/<script_key>/loom.exposure.yaml
```

`loom.script.yaml` describes how to run the package:

- `id`: stable script key
- `entrypoint.command`: package-relative command
- `runtime.shell`: runtime shell
- `inputs` and `outputs`: expected structured data
- `execution`: timeout, network, and filesystem behavior

`loom.exposure.yaml` describes whether the script becomes a capability:

- `expose.enabled`: whether activation should create the capability
- `expose.provider`: usually `project`
- `expose.endpoint`: endpoint part of the capability URL
- `capability.risk_level`: expected risk
- `capability.side_effects`: declared side effects
- `execution.default_mode`: how calls wait for execution

If `expose.provider: project` and endpoint is `hello_world`, the capability is:

```text
main@everything-template.hello_world
```

## Connector Contracts

File:

```text
connectors/<connector_key>/loom.connector.yaml
```

Purpose:

- declares a provider namespace
- declares one or more capability endpoints
- binds endpoints to runtime implementations
- declares schemas, risk, side effects, and usage docs

Important fields:

- `provider.key`: provider namespace, such as `example_connector`
- `runtime.kind`: connector-level runtime family
- `capabilities[].endpoint`: endpoint part of the capability URL
- `capabilities[].runtime.kind`: endpoint runtime kind
- `capabilities[].runtime.script`: script package for script-backed endpoints

For `provider.key: example_connector` and endpoint `ping`, the capability is:

```text
main@example_connector.ping
```

Do not use the project slug as a connector provider key. The project provider is
reserved for project-owned scripts and workflow shims.

## Schedule Contracts

File:

```text
schedules/<schedule_key>/loom.schedule.yaml
```

Purpose:

- declares a time-based automation
- targets exactly one capability
- defines static input, timing, misfire, concurrency, approval, timeout, and retry

Important fields:

- `schedule.key`: stable schedule key
- `target.capability`: one capability URL
- `target.input_file`: JSON object passed to the target
- `timing.kind` and `timing.expression`: when to fire
- `misfire.policy`: what to do if LOOM missed a run
- `concurrency.policy`: what to do if a previous run is still active

If the automation needs multiple operations, create a script or connector
capability that performs those operations and schedule that one capability.

## Direct Event Contracts

File:

```text
direct_events/<event_key>/loom.direct_event.yaml
```

Purpose:

- declares an endpoint for external notifications
- authenticates the sender according to an auth profile
- maps incoming payload data into one capability input
- calls exactly one target capability
- stores payloads according to policy

Important fields:

- `event.key`: stable event key
- `integration.key`: stable external-system grouping
- `endpoint.slug`: endpoint slug
- `target.capability`: one capability URL
- `mapping.fields`: payload-to-input mapping
- `mapping.required`: fields that must exist after mapping
- `idempotency`: how duplicate deliveries are detected
- `response.mode`: how LOOM responds to the sender
- `examples.payload`: sample external payload
- `examples.expected_mapped_input`: expected capability input

Never store webhook secrets or tokens in this file. Use credential references or
auth profiles created outside the project tree.

## Workflow Contracts

File:

```text
workflows/<workflow_key>/loom.workflow.yaml
```

Purpose:

- defines an executable workflow package or design-only workflow intent
- keeps workflow orchestration in a written entrypoint such as `run.sh`
- can point to a script-backed shim for compatibility

Use:

- `implementation.kind: placeholder` for design-only workflows
- `implementation.kind: script` for script-backed workflow shims
- `implementation.kind: workflow` with `workflow.contract.v0.3.1` for executable workflow packages

Schedules and direct events should not target placeholder workflows.

## Module Contracts

Files:

```text
modules/<module_key>/module.json
modules/<module_key>/loom.module_project.yaml
```

`module.json` describes the module package:

- module id, name, version, kind
- storage requirements
- provided object types, providers, capabilities, usage docs, and backup hooks

`loom.module_project.yaml` describes how this project wants LOOM to treat it:

- registration intent
- explicit install policy
- explicit exposure policy
- validation expectations

In v0.3, keep install and exposure explicit:

```yaml
install:
  plan: explicit_only
  install_after_register: false
  enable_after_install: false

exposure:
  plan: explicit_only
  expose_after_enable: false
```

## Policy Contracts

Files:

```text
.loom/contracts/sync.yaml
.loom/contracts/backup.yaml
.loom/contracts/workers.yaml
.loom/contracts/credentials.yaml
```

Purpose:

- `sync.yaml`: what becomes synced/indexed object content
- `backup.yaml`: what is preserved as raw project material
- `workers.yaml`: background worker intent
- `credentials.yaml`: credential-reference policy

Policy files compile into desired state. They do not start workers or watchers
by themselves.

## Secret Rule

Do not put secret values anywhere in this project.

Allowed:

```text
credential reference names
auth profile names
setup instructions
fake example placeholders
```

Forbidden:

```text
API keys
OAuth refresh tokens
passwords
private keys
webhook secrets
session cookies
.env files with real values
```

## Final Checklist

Before finishing any contract change:

```sh
loom project validate .
loom project plan .
```

Then check:

- Did I keep stable names stable?
- Did I update every dependent capability URL?
- Did I avoid storing secret values?
- Did I add or update examples?
- Did I activate dependencies before automations?
- Did I update README or AGENTS guidance if behavior changed?
