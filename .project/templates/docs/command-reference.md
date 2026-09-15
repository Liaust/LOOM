---
title: "<CLI Domain> CLI Reference"
description: "<One sentence explaining this group of loom commands.>"
audience:
  - user
  - operator
tags:
  - loom
  - cli
status: draft
created_at: "<RFC3339 UTC timestamp>"
updated_at: "<RFC3339 UTC timestamp>"
verified_at: null
source_scope:
  - "loom <domain> --help"
related:
  - "[[Command Safety]]"
aliases:
  - "<CLI Domain Commands>"
---

# <CLI Domain> CLI Reference

## What These Commands Are For

Explain the user or operator goal before listing command syntax.

## Safety

State whether these commands are read-only, mutating, production-like,
destructive, or safe to run against `.loom-acceptance` fixtures.

## Common Workflows

### <Workflow Name>

Explain when to use this workflow.

```bash
loom <domain> ...
```

Expected result:

- Explain what the important output means.

## Command Reference

### `loom <domain> <command>`

Purpose:

- Explain what the command does.

Important flags:

- `--flag`: Explain the behavior and when to use it.

Safe example:

```bash
loom <domain> <command> --dry-run
```

Output:

- Explain the important fields or lines.

Audit:

- Record the corresponding audit row when the plan requires command auditing.

## Common Problems

- Problem:
  Explain symptom, likely cause, and next safe check.

## Related Docs

- [[Command Safety]]
