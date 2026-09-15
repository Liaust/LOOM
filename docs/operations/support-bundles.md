---
title: "Support Bundles"
description: "How to plan and create bounded LOOM support bundles without exposing secrets or collecting excessive production-like data."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - operations
  - troubleshooting
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom support --help"
  - "go run ./cmd/loom support bundle create --help"
  - "go run ./cmd/loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096"
related:
  - "[[Diagnostics And Support]]"
  - "[[Runtime Status And Support]]"
  - "[[Troubleshooting]]"
  - "[[Command Safety]]"
aliases:
  - "Support Bundle"
---
# Support Bundles

## What This Page Covers

Support bundles collect bounded diagnostic evidence for a LOOM problem. They
are useful when status output is not enough and you need a shareable artifact
for debugging.

## Plan First

Always run a dry-run first:

```bash
loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096
```

Slice 4 verified this dry-run shape. It returns the planned bundle scope
without creating the archive.

## Create A Bundle

After reviewing the dry-run:

```bash
loom support bundle create --profile minimal
```

Use broader profiles only when the extra evidence is required and acceptable.
Do not collect secrets, credentials, private keys, tokens, or unrelated user
content.

## What To Include

Good support evidence usually includes:

- `loom status`;
- `loom health`;
- relevant domain status/list/inspect output;
- recent failure ids or correlation ids;
- dry-run plans for repairs;
- exact command and timestamp;
- whether the command was run from Mac, main, or another node.

## What Not To Include

Do not include:

- raw credentials;
- cloud repository passwords;
- SSH private keys;
- unredacted tokens;
- unrelated user files;
- broad raw database dumps;
- generated backup view payloads unless the operation explicitly requires
  inspecting them.

## Storage Hygiene

If the support bundle is part of acceptance testing, place it under a
`.loom-acceptance` scenario folder or a reviewed diagnostics output path. Do
not leave diagnostic archives loose in user storage roots.

## Related Docs

- [[Diagnostics And Support]]
- [[Troubleshooting]]
- [[Runtime Status And Support]]
- [[Command Safety]]
