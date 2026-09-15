# backup_policy Surface Guidance

<!-- loom-managed: project-surface-guidance/v1 -->

This guidance applies to the `backup_policy` facet of LOOM Everything Template.

- Keep authored paths project-relative.
- Treat source files and contracts as authoritative over backend projections.
- Preserve package-local manifests beside their code, fixtures, and examples.
- Never store secret values in the project.
- Run `loom project validate .` after contract changes.
