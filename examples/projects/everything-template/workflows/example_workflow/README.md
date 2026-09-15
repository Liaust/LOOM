# Example Workflow

This package is an executable LOOM workflow.

It is intentionally small: the entrypoint writes a normal LOOM result file and
can later be expanded to call other capabilities through `loom capability call`.

## Capability URL

After the project is registered and the workflows facet is activated, this
workflow is intended to expose:

```text
main@everything-template.example_workflow
```

## Direct Execution

From this folder:

```sh
./run.sh
```

Direct execution only checks the local executable. LOOM execution adds provider
registration, policy, routing, job records, logs, outputs, and artifacts.

## Editing Rules

- Keep `workflow.id` aligned with the folder name.
- Keep `entrypoint.command` package-relative.
- Keep the result file JSON-compatible.
- Use stable capability URLs for nested LOOM calls.
- Document side effects before raising risk or authorization level.
