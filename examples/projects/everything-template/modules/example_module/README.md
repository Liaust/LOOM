# Example Module

This is a minimal LOOM module package placeholder.

Modules are for deeper LOOM expansion than a single script or connector. A
module can eventually provide object types, provider namespaces, capabilities,
usage documents, storage requirements, and backup hooks.

In v0.3, this package is intentionally conservative: it can be validated and
registered as project intent, but install, enable, and exposure behavior remains
explicit-only.

## Files

- `module.json`: backend module package manifest.
- `loom.module_project.yaml`: project wrapper that declares registration,
  install, exposure, and validation intent.
- `README.md`: package-level context.

## Module Manifest

`module.json` declares what the module is:

```json
{
  "module": {
    "id": "loom.everything-template",
    "name": "LOOM Everything Template",
    "version": "0.1.0",
    "kind": "native"
  }
}
```

It also declares what the module requires and provides. The scaffolded module
does not provide runtime providers or capabilities yet.

## Project Wrapper

`loom.module_project.yaml` declares how this project wants LOOM to treat the
module package.

The important safety defaults are:

```yaml
install:
  plan: explicit_only
  install_after_register: false
  enable_after_install: false

exposure:
  plan: explicit_only
  expose_after_enable: false
```

Registration intent is not the same thing as installing or enabling module
runtime behavior.

## Lifecycle

From the project root:

```sh
loom project validate .
loom project plan .
loom project register .
loom project activate everything-template --facet modules --project-root .
loom modules list
```

Activation records supported module package metadata. A later explicit install
or enable step should be treated as a separate operational decision.

## When To Use A Module

Use a module when the project needs to extend LOOM itself with a durable package
surface.

Use something smaller when possible:

- use `scripts/` for one project-owned executable capability
- use `connectors/` for a provider namespace wrapping an external/local system
- use `schedules/` or `direct_events/` only to trigger one existing capability

## Safety Rules

- Do not use a module for a simple shell command.
- Do not declare stateful storage without a backup plan or backup hooks.
- Do not expose module capabilities from scaffolded placeholders.
- Keep module IDs, provider keys, and capability names stable.
- Do not store credentials in module files.
