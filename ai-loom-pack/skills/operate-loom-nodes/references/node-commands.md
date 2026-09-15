# Node And Runtime Commands

## Broad Health

```bash
loom status
loom health
loom node list
loom node health <node-ref>
loom node inspect <node-ref>
loom communication health
```

If a command cannot reach main, keep that error. Check the selected config and
socket before diagnosing a remote node as unhealthy.

## Providers And Capabilities

```bash
loom providers list
loom provider inspect <provider-ref>
loom provider health <provider-ref>
loom capabilities list
loom capabilities search <query>
loom capability inspect <capability-ref>
loom capability usage-docs <capability-ref>
```

Capability calls may have external effects. Inspect policy and usage docs before
considering `loom capability call`; do not use it as a generic health probe.

## Jobs And Workers

```bash
loom jobs status
loom jobs queue
loom jobs failures
loom job inspect <job-id>
loom job events <job-id>
loom job logs <job-id>
loom workers list
loom worker inspect <worker-ref>
loom worker runs <worker-ref>
```

Retry, cancel, worker run, and stale-run repair are mutations. Confirm target,
idempotency, downstream effects, and approval before applying them.

## Production Updates

```bash
loom update status
loom update history
loom update plan --help
```

Use the production update runbook for plan inputs, apply, rollback, and
validation. A CLI subcommand existing does not authorize a live update or NixOS
rebuild.
