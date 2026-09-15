# Incident Evidence And Handoff Contract

## Minimum Evidence

- requested outcome and incident scope;
- expected and observed behavior;
- affected node, project, capability, path, or storage entry;
- first/latest timestamps and timezone;
- correlation, job, worker-run, transfer, or storage IDs;
- exact commands and concise relevant output;
- deterministic reproduction or reason it is unsafe;
- attempted repairs and result of each;
- defect classification and suspected component;
- constraints, forbidden actions, and current production/data safety state;
- next owner, requested action, and required validation.

Separate observations, inferences, and unknowns.

## Useful Read-Only Commands

```bash
loom status
loom health
loom events list
loom job inspect <job-id>
loom job events <job-id>
loom job logs <job-id>
loom storage failures
loom support bundle create --help
```

Use the narrower project, storage, or node skill for domain-specific commands.
Confirm exact flags with `--help` and use redaction defaults.

## Bounded Remediation Gate

Proceed only when all are true:

1. the failing layer and owner are identified;
2. the action is within explicit scope and authority;
3. expected state change and rollback are known;
4. evidence is preserved;
5. validation tests the original symptom and downstream health;
6. credentials and user data remain protected.

Otherwise stop at a plan or handoff.
