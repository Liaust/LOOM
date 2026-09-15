# Example Connector

This connector demonstrates how a project can declare a provider namespace and
one or more capability endpoints.

The connector provider is:

```text
main@example_connector
```

The example endpoint is:

```text
main@example_connector.ping
```

## Files

- `loom.connector.yaml`: authoritative connector contract.
- `capabilities/ping.yaml`: lightweight authoring aid for the `ping` endpoint.
- `scripts/ping/loom.script.yaml`: script package backing the endpoint.
- `scripts/ping/run.sh`: endpoint entrypoint.
- `examples/input.example.json`: example input fixture.

## How The Connector Works

`loom.connector.yaml` declares:

- provider metadata under `provider`
- connector runtime defaults under `runtime`
- endpoint contracts under `capabilities`
- usage docs under `usage_documents`

The `ping` endpoint is script-backed:

```yaml
runtime:
  kind: script
  script: scripts/ping
```

After connector activation, LOOM owns the outer execution path: provider lookup,
capability URL parsing, policy, routing, jobs, audit, and result capture. The
script is only the inner execution step.

## Adding Another Endpoint

To add a connector endpoint:

1. Add a new item to `capabilities` in `loom.connector.yaml`.
2. Choose a specific endpoint name, such as `note.create` or `message.tag`.
3. Define input and output schemas.
4. Declare risk level and side effects.
5. Add a runtime binding, usually `runtime.kind: script`.
6. Create the referenced script package under `scripts/<endpoint>/`.
7. Add examples and usage docs.
8. Validate and plan from the project root.

Do not use vague endpoint names such as `run`, `execute`, or `do_action` when a
specific operation name is possible.

## Lifecycle

From the project root:

```sh
loom project validate .
loom project plan .
loom project register .
loom project activate everything-template --facet connectors --project-root .
loom providers inspect main@example_connector
loom capability inspect main@example_connector.ping
loom capability call main@example_connector.ping --input '{}' --wait
```

## Safety Rules

- Keep `provider.key: example_connector` stable.
- Keep endpoint names stable once other contracts reference them.
- Do not store API tokens or credentials in this folder.
- Use credential references for external systems.
- Keep smoke-test endpoints low-risk and deterministic.
