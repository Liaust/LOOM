# Datasets

Use this folder for project datasets, dataset samples, fixtures, and references.

In v0.3, `datasets/` is an organizational facet. It does not have a dedicated
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

```text
datasets/<dataset_key>/
  README.md
  schema.md
  samples/
  fixtures/
```

Each dataset README should explain:

- what the dataset represents
- whether it is real, synthetic, sample, or external
- where the authoritative source lives
- expected format and schema
- privacy or retention concerns
- which scripts, tests, or docs use it

## Relationship To Sync And Backup

Putting files under `datasets/` does not automatically make them background
managed. If a dataset should be backed up, synced, or indexed, declare that in
project policy and inspect the watch plan.

Useful commands:

```sh
loom project validate .
loom project watch-plan .
loom project backup-status everything-template
```

## Safety Rules

- Do not store secrets or credential exports.
- Do not copy large external datasets into the project without user approval.
- Do not store private personal data without explicit policy.
- Do not rely on implicit sync or backup behavior.
- Keep samples small, representative, and documented.
