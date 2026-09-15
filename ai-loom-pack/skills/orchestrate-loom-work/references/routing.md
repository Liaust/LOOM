# Routing And Handoff Rules

## Inventory Inputs

```bash
loom project list
loom project inspect <project-ref>
loom project status <project-ref>
loom docs search "<workflow>"
```

Also read each relevant project root `AGENTS.md`, `.loom/project.yaml`, planning
folder, progress note, and handoff. Use the connected canonical task interface
only within the authenticated account and project in scope.

## Routing Table

| Work | Primary owner |
|---|---|
| Project content, contracts, or feature | Project-root worker |
| LOOM core code or cross-project platform defect | LOOM repository developer |
| Node, production, rebuild, backup, or restore | Integrator/operator |
| Ambiguous failure with multiple possible layers | Incident investigator |
| Credential value or Proton workflow | Approved Proton Pass operator/skill |
| Priority, external commitment, or the operator-only context | the operator |

## Handoff Fields

- requested outcome and why it matters;
- canonical source root and task/project identifiers;
- relevant instructions and accepted planning files;
- expected and forbidden files or systems;
- dependencies and upstream/downstream owners;
- observed evidence and current state;
- authority and production boundaries;
- validation commands and completion evidence;
- stop conditions and open decisions.

Pass only task-relevant context. Never include private global memory,
credentials, unrelated project data, or hidden assumptions.
