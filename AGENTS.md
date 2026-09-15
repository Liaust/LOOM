# LOOM Contributor Guidance

Read README.md and CONTRIBUTING.md first. Use .project/STATE.md and
.project/ROADMAP.md for current scope. Source code and current user instructions
take precedence over historical examples or plans.

- Make the smallest useful change. Run focused tests and report what was not
  tested. Do not create replica infrastructure or an agent benchmark unless the
  task explicitly calls for that product capability.
- Use a branch/worktree when it helps independent work, not as a requirement for
  every edit. Check branch, HEAD, and uncommitted changes before editing.
- Never infer authorization to use a machine, external account, or deployment
  from repository access. No maintainer host, credential, Basecamp account, or
  named agent identity is part of this public checkout.
- Keep secrets, personal context, generated backups, and acceptance outputs out
  of Git. Use an owned .loom-acceptance directory for local test artifacts.
- LOOM owns coordination and durable system services; Hermes/ORCA/Codex own
  their respective agent and workspace runtimes. Preserve that distinction.
- Write human-readable docs with YAML frontmatter and existing wikilink titles.
  Public navigation also needs ordinary Markdown links for GitHub readers.
  Mark unverified/deferred behavior honestly; do not claim installed services
  from a source-only test.
- Treat pending Provenance candidates as qualified clues, not accepted truth,
  project membership, or permission. Source artifacts remain authoritative.
- Do not modify existing user changes, delete data, stage releases, activate
  schedules, publish, or rotate credentials unless the task authorizes it.

GitHub issues and PRs are the public contribution interface. Optional agent
workspace protocols in ai-loom-pack are examples for a configured installation,
not extra prerequisites for contributing to LOOM.
