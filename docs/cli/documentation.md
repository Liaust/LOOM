---
title: "Documentation CLI"
description: "Search, inspect, and validate the release-matched LOOM documentation corpus without a daemon or database."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - cli
  - reference
status: verified
verified_at: "2026-08-16"
source_scope:
  - "internal/loomdocs"
  - "internal/loomcli/docs.go"
  - "go run ./cmd/loom docs status --docs-dir docs"
  - "go run ./cmd/loom docs search 'project activation' --docs-dir docs"
  - "go run ./cmd/loom --json docs inspect 'Projects And Scopes' --docs-dir docs"
related:
  - "[[CLI Reference]]"
  - "[[Command Index]]"
  - "[[Environment Variables]]"
  - "[[Configuration]]"
aliases:
  - "LOOM Docs CLI"
  - "Docs Commands"
---
# Documentation CLI

## What This Page Covers

`loom docs` reads the documentation shipped with the current LOOM release. It
can validate the corpus, search it, inspect one bounded page or heading, and
list related pages. The command is deliberately local: it does not contact
`loomd`, PostgreSQL, the Notes index, embeddings, or cloud services.

## Who Should Read This

Use this command family when a person or external agent needs authoritative
product guidance without loading the entire documentation tree. Developers can
also point it at a checkout while changing LOOM.

## Mental Model

Public product documentation and user Notes are separate sources. `loom docs`
loads Markdown from one resolved `docs/` directory into memory, parses its
frontmatter and wikilinks, and performs deterministic lexical search. It never
writes index rows.

The root is resolved in this order:

1. `--docs-dir`;
2. `LOOM_DOCS_DIR`;
3. `../share/loom/docs` beside an installed executable;
4. the nearest repository-local `docs/` directory.

An explicit or environment path that is missing is an error. LOOM does not
silently fall through to a different corpus.

## Workflow

Start with corpus health:

```bash
loom docs status
```

Then search for the user goal and inspect a bounded result:

```bash
loom docs search "project activation" --audience user --limit 5
loom docs inspect "Projects And Scopes" --heading "Activate And Deactivate"
```

Use a relative path when titles or aliases are ambiguous:

```bash
loom docs inspect cli/projects-and-scopes.md --max-chars 12000
```

Relative inspection paths are rooted at the resolved documentation directory,
not at the process working directory.

## Commands And Output

### Status

```bash
loom docs status [--docs-dir <path>]
```

Status reports the resolved root and source kind, LOOM version, release-match
indicator, document count, corpus hash, parse errors, duplicate titles or
aliases, and unresolved wikilinks. Development templates under `docs/` may be
reported as invalid public pages; they are not silently included as searchable
documents.

### Search

```bash
loom docs search <query> \
  [--tag <tag>] [--audience <audience>] [--status <status>] [--limit <n>]
```

Filters are exact and case-insensitive. Ranking uses deterministic local BM25
with additional boosts for exact titles, aliases, tags, and headings. Ties are
ordered by relative path. Human output includes the title, path, status, tags,
match explanation, and a short snippet.

### Inspect

```bash
loom docs inspect <title-alias-or-relative-path> \
  [--heading <heading>] [--max-chars <n>]
```

Inspection accepts an exact title, exact alias, or contained relative path.
Ambiguous names return candidate paths. Heading selection is exact and
case-insensitive. The default bound is 12,000 Unicode characters. Path
traversal and absolute paths are rejected.

### Related

```bash
loom docs related <title-alias-or-relative-path>
```

Related output combines valid outgoing wikilinks with pages that link back to
the inspected document.

All four commands support the root `--json` flag. JSON includes the resolved
corpus metadata and stable typed result fields; search also includes structured
warnings.

## Healthy Output

A healthy release resolves `source: packaged`, reports the expected LOOM
version and a stable corpus hash, and returns the same result order for the same
query and filters. Parse or graph counts are actionable documentation defects,
not daemon-health failures.

In a development checkout, `source: repository` or `source: explicit` is
expected. `release_matched` describes the source type; it is not proof that a
manually supplied directory belongs to the running binary.

## Common Problems

- Documentation root not found: set `--docs-dir` for a checkout or verify that
  the installed package contains `share/loom/docs`.
- Ambiguous title or alias: repeat `inspect` with one of the candidate relative
  paths.
- Unresolved wikilinks: use `status --json` to retrieve structured issue paths
  and targets, then fix the source documentation rather than hiding the error.
- A future plan appears absent: `loom docs` searches packaged public docs, not
  repository `notes/`. Inspect accepted development plans explicitly when the
  task is future planning.

## Related Docs

- [[CLI Reference]]
- [[Command Index]]
- [[Environment Variables]]
- [[Configuration]]
