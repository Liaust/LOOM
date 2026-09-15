# Notes

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

`loom.notes.yaml` declares notes sync/index intent.

In this template:

```yaml
notes:
  sync: true
  index: true
  backup: false
```

That means notes are intended to become synchronized/indexed knowledge. Backup
behavior is controlled by project backup policy.

## Lifecycle

From the project root:

```sh
loom project validate .
loom project watch-plan .
loom project register .
loom project apply-watch-policy everything-template --project-root .
loom project sync-status everything-template
```

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

Use `notes/` for durable project knowledge.

Use `docs/` for operational instructions, contract authoring guides, runbooks,
and troubleshooting material.
