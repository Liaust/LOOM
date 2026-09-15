# Release-Matched Documentation Workflow

## Commands

```bash
loom docs status
loom docs search "<query>" --limit 10
loom docs inspect "<title-or-path>"
loom docs related "<title-or-path>"
```

Use `--json` when stable fields are more useful than prose. In repository
development, `--docs-dir <path>` may select an explicit corpus. Normal release
use should rely on packaged resolution.

Verify an operational command separately:

```bash
loom <command> --help
loom <command> <subcommand> --help
```

## Interpreting Results

- Prefer an exact title or alias match over a broad lexical match.
- Check `status`, `verified_at`, `source_scope`, and audience.
- Cite the page title and repository-relative path, not a search-result rank.
- Inspect only the relevant heading when the page is long.
- An unresolved wikilink is a corpus finding, not proof the target behavior is
  absent.

## Public Docs And Development Plans

The packaged public corpus under `docs/` describes supported human-facing
behavior. Repository planning under `.project/` controls feature work but is
not automatically a public promise. Material under `.project/archive/`
explains historical rationale only.

If a public page and CLI help differ, report the drift and trust the installed
CLI for command availability. If the CLI exists but the page marks the workflow
deferred, stop and inspect the narrower command help and implementation before
describing it as supported.
