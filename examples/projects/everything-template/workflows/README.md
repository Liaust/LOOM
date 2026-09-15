# Workflows

Use this folder for executable workflow packages.

A workflow is a larger executable package that owns its orchestration logic. It
may call scripts, connectors, agents, or other LOOM capabilities, but those
nested calls should still go through normal LOOM capability URLs.

LOOM should not micro-manage workflow internals. LOOM validates the package,
registers the callable surface, runs one workflow job, and captures logs,
outputs, and artifacts.

## Folder Shape

Each workflow should live under:

```text
workflows/<workflow_key>/
  loom.workflow.yaml
  run.sh
  README.md
  examples/input.example.json
```

The example workflow exposes:

```text
main@everything-template.example_workflow
```

## Implementation Kinds

- `workflow`: executable workflow package. The entrypoint is run as one
  workflow job.
- `script`: compatibility shim that points at an existing script package.
- `placeholder`: design-only intent. It validates with a warning and is not
  callable.

Use `workflow.contract.v0.3.1` for executable workflow packages.

## Lifecycle

From the project root:

```sh
loom project validate .
loom project plan .
loom project workflows list .
loom project register .
loom project activate everything-template --facet workflows --project-root .
loom capability inspect main@everything-template.example_workflow
```

Runtime execution lands in a later v0.3.1 part. This first contract slice makes
the executable workflow package validate, plan, and scaffold correctly.

## Safety Rules

- Keep the workflow entrypoint deterministic and package-relative.
- Use full capability URLs for nested LOOM calls.
- Document side effects, approval expectations, retries, and idempotency.
- Do not call providers, scripts, or shell commands through hidden lanes when a
  LOOM capability URL exists.
- Keep placeholder workflows draft-only.
