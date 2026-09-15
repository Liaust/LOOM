---
name: search-loom-docs
description: Find and cite release-matched LOOM documentation when a user asks how LOOM works, whether a command or workflow exists, or whether behavior is current, deferred, or historical.
---

# Search LOOM Docs

Use LOOM's packaged documentation before relying on memory. This skill is
inspect-only: it finds authoritative guidance and verifies claims, but does not
perform the workflow being researched.

## Trigger Boundary

Use this skill for LOOM command discovery, path or architecture questions,
current-versus-future checks, and source-backed explanations. Do not use it as
a substitute for a project, storage, node, incident, or credential workflow.

## Source-Of-Truth Order

1. The current scope's `AGENTS.md` and repository/project instructions.
2. The release-matched public corpus resolved by `loom docs`.
3. Current CLI help for the installed `loom` executable.
4. Runtime evidence when a question is about observed deployed state.
5. Development plans only when the task is development work or public docs
   explicitly mark behavior deferred.
6. Historical notes only for rationale, never as current operating guidance.

When sources disagree, follow the narrower current instruction and report the
mismatch. Public docs explain supported user behavior; development plans may
describe work that is not shipped.

## Workflow

1. Restate the question as a small search query.
2. Run `loom docs status` if corpus source or health is uncertain.
3. Search with `loom docs search` and prefer a verified page matching the
   user's audience and task.
4. Inspect the exact page or heading with `loom docs inspect`.
5. Follow related-page edges only when the first page does not settle the
   question.
6. Verify every proposed command with `loom <group> <command> --help`.
7. Answer with the document title, repository-relative path, and status.

Read [the docs workflow](references/docs-workflow.md) for concrete commands and
interpretation rules.

## Current Versus Future

Treat `status: verified` as reviewed against the page's `source_scope`, not as a
promise that every node is healthy. Treat `draft` cautiously. Treat `deferred`
as unavailable unless runtime evidence and newer public docs prove otherwise.
Never convert an implementation slice, architecture aspiration, or historical
example into a current command.

## Output Contract

Return:

- the direct answer;
- title, path, and status of each controlling page;
- the exact verified command, if any;
- any runtime prerequisite or owner-node distinction;
- explicit uncertainty or deferral;
- the next operational skill when action is requested.

Do not invent flags, quote large portions of a page, expose private development
material to an end user, or claim that finding instructions grants authority to
run them.
