# Contributing To LOOM

LOOM is a developer preview. Useful contributions make a real workflow simpler,
fix a reproducible bug, improve explanation, or remove an unnecessary constraint.
You do not need Basecamp, a particular agent, or access to the maintainer's machines.

## Before Starting

Read the [README](README.md), [current state](.project/STATE.md), and
[roadmap](.project/ROADMAP.md). Search existing GitHub issues before opening one.
Discuss architectural changes or new dependencies in an issue before a large PR.
Small fixes can go straight to a pull request.

Include the command or workflow, relevant version/commit, expected result,
actual result, and a small reproduction. Remove credentials, private paths,
identities, and user content from logs. Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md), not in a public issue.

## Implement And Check

Use an ordinary branch or worktree. Keep the change focused and leave unrelated
formatting or generated files alone. Run the tests for the packages you change:

```sh
go test ./internal/<affected-package>
go vet ./internal/<affected-package>
git diff --check
```

For documentation and the instruction pack:

```sh
go test ./internal/loomdocs ./internal/agentpack
go run ./cmd/loom --json docs --docs-dir docs status
go run ./cmd/loom agent pack validate --pack-dir ai-loom-pack
```

Some existing integration tests need PostgreSQL, Nix, Borg, or an explicitly
owned machine. A skipped test is not a passed integration test. State what ran
and what did not. Do not build a replica platform merely to validate a small
change, and never run storage/deployment tests against someone else's data.
The maintainer can check machine-specific behavior on the development system.

## Pull Requests

Explain the user-visible change, the reason for it, relevant tests, and any
migration, compatibility, security, or documentation impact. Include screenshots
for UI changes without personal data. Preserve upstream notices when modifying
third-party material. By submitting a contribution, you agree to provide it under
the repository's Apache-2.0 license unless explicitly agreed otherwise; do not
submit code or assets you lack permission to contribute.

AI-assisted contributions are welcome. The submitter remains responsible for
understanding the change, validating it, and explaining its limitations. A large
unreviewed generated patch is not a substitute for a focused contribution.

There is no support SLA or guaranteed response time. Be specific, patient, and
respectful; critique designs and behavior rather than people.
