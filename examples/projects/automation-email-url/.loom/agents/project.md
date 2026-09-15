# Automation Email URL Project Guidance

<!-- loom-managed: project-guidance/v1 -->

This project is identified by `.loom/project.yaml` as `automation-email-url` and is owned
by the `main` node. Work with project-relative paths so the same tree
can be used from a workspace node, main, or an agent-managed checkout.

- Lifecycle state: `draft`
- Enabled facets: `direct_events, notes, scripts`

Use this loop for contract changes:

```sh
loom project validate .
loom project plan .
```

Registration, activation, watched-root application, and other runtime mutations
are separate explicit operations. Package-local manifests such as
`scripts/<key>/loom.script.yaml` stay with their packages. Keep human content
in the enabled visible work surfaces and portable LOOM control material under
`.loom/`. Proton Pass owns credential values; project contracts may contain
non-secret `pass://` references only.
