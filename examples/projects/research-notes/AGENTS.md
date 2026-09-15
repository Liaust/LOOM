# Project Agent Entry Point

<!-- loom-managed: project-agent-entry/v1 -->

Your default working boundary is this project root.

Before changing content or contracts, read `.loom/project.yaml`,
`.loom/agents/project.md`, and the relevant files under
`.loom/agents/surfaces/`. Use `search-loom-docs` for authoritative current
guidance and `manage-loom-projects` for project lifecycle work.

Edit canonical project sources only. Never edit generated `loom-storage`
views. Validate with `loom project validate .` and review
`loom project plan .` before registration or activation. Escalate node,
storage, backup, credential, or LOOM-core defects with evidence instead of
broadening the project boundary.

Skills provide instructions; they do not grant authority. Proton Pass is the
credential authority. Keep credential values out of project files. The current
`.loom/contracts/credentials.yaml` schema uses logical references bound to
environment variables or absolute files; direct Proton bindings are deferred.
Do not store host-specific absolute paths, private agent memory, or identity
instructions here.
