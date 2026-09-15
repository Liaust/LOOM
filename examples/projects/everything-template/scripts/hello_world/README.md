# Hello World Script

This is the minimal project-owned script package in the template.

It exists to show the full path from local executable code to a LOOM capability:

```text
loom.script.yaml -> loom.exposure.yaml -> main@everything-template.hello_world
```

## Files

- `loom.script.yaml`: describes how to run the script package.
- `loom.exposure.yaml`: declares the LOOM capability surface.
- `run.sh`: executable entrypoint.
- `examples/input.example.json`: small example input fixture.

## Runtime Behavior

The script is intentionally low-risk:

- no network access
- read-only filesystem mode
- short timeout
- JSON output on stdout

Running the entrypoint directly is useful while editing:

```sh
./scripts/hello_world/run.sh
```

That does not test LOOM routing, policy, jobs, runtime binding, or capability
call records. Use the capability call path after activation.

## Capability Exposure

In this example project, `loom.exposure.yaml` has:

```yaml
expose:
  enabled: true
  provider: project
  endpoint: hello_world
```

Because `provider: project`, the provider resolves to the project provider:

```text
main@everything-template
```

The resulting capability URL is:

```text
main@everything-template.hello_world
```

## Lifecycle

From the project root:

```sh
loom project validate .
loom project plan .
loom project register .
loom project activate everything-template --facet scripts --project-root .
loom capability inspect main@everything-template.hello_world
loom capability call main@everything-template.hello_world --input '{}' --wait
```

Activation creates the provider, capability endpoint, endpoint version, script
registration, and runtime binding.

## Editing Rules

- Keep `id: hello_world` stable unless all references are updated.
- Keep `expose.endpoint: hello_world` stable unless schedules, direct events,
  docs, and tests are updated.
- Keep input and output JSON-friendly.
- Do not add hidden credentials or machine-local paths.
- Document any new runtime dependency before using it.
