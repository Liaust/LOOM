---
title: "LOOM Documentation"
description: "Navigate the public developer-preview guide, architecture, operations and implementation reference."
audience: [user, operator, developer, agent]
tags: [loom, documentation]
status: draft
verified_at: "2026-09-15"
source_scope: ["README.md", "internal/loomdocs"]
related: ["[[User Guide]]", "[[Concepts]]", "[[CLI Reference]]", "[[Operations]]"]
aliases: ["Docs Home"]
---

# LOOM Documentation

LOOM is a self-hosted coordination system for projects, machines, files,
knowledge and agent-accessible operations. This public preview includes the
implementation and its documentation, but not a turnkey installation or the
maintainer's private machine configuration.

Start with the [repository README](../README.md), [current state](../.project/STATE.md)
and [roadmap](../.project/ROADMAP.md). The README explains what LOOM owns and
what belongs to Hermes, ORCA, model providers and supporting services.

## Choose A Path

- [Getting started](user-guide/getting-started.md): everyday use of an existing installation.
- [Installation](operations/installation.md): source build, configuration prerequisites and limits.
- [Projects](user-guide/projects.md): ordinary folders, declarations and portable development state.
- [Notes and search](user-guide/notes-and-search.md): source retrieval and processing.
- [Agent integration](user-guide/agents.md): optional personas, harnesses, skills and device routes.
- [Concepts](concepts/README.md): architecture and ownership.
- [CLI reference](cli/README.md) and [API reference](api/README.md): exact interfaces.
- [Portal guide](portal/README.md): the terminal user interface.
- [Operations](operations/README.md): updates, recovery, storage and diagnostics.
- [Developer documentation](developer/README.md): repository structure and contribution workflows.

## Reading The Documentation

Pages retain YAML metadata and Obsidian-style wikilinks for the documentation
graph. On GitHub, use the ordinary links in this index and section indexes;
wikilinks such as [[Projects]] are not native GitHub navigation. A rendered
documentation site is planned separately.

The packaged CLI searches the same source:

```sh
go run ./cmd/loom docs --docs-dir docs search "projects"
go run ./cmd/loom --json docs --docs-dir docs status
```

Use an installed CLI without `go run ./cmd/loom` when working from a configured
release. The health command validates documentation metadata and links; it
does not run every documented workflow.

## Evidence And Status

`draft` means a page still needs review or broader verification.
`verified` refers to the checks recorded when that page was written, not a
guarantee about your machine today. `deferred` identifies work not yet ready.
The public [state snapshot](../.project/STATE.md) is the current orientation
when an older dated example conflicts with it. Report a concrete discrepancy
rather than treating an old private deployment receipt as a prerequisite.

No personal account, private host, OAuth grant, live agent workspace or backup
archive is distributed with these docs. Example paths describe intended roles;
substitute your own authorized configuration before operational commands.
