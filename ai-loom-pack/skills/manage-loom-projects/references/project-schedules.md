# Project Schedules

Add a stable resource key in `.loom/project.yaml`:

```yaml
resources:
  health_check:
    kind: schedule
    schedule:
      target: main@system.health.read
      cron: "15 9,13,18 * * *"
      timezone: Europe/Amsterdam
      input_json: '{}'
      status: disabled
```

Use exactly one of `cron` (five-field calendar expression, explicit timezone),
`every` (duration, e.g. `5m`), or `at` (absolute RFC3339 timestamp, never `now`).
Intervals/one-shots default to UTC. `Local` is rejected. `input_json` is a JSON
object encoded as a string; use credential references, never secret values.
`target` must be an installed active capability; discover it with
`loom capabilities search`. A skill is not a capability.

Use normal `loom project plan`, `apply` and `status`. Saving YAML does not enroll
a schedule. Project/scope are derived from the declaration. The native key is
`<project_slug>__<resource_key>` (project hyphens become underscores; long keys
are shortened with a hash); the apply operation reports the native schedule ID.

Omitted status is `disabled`; explicitly choose `active` when execution is
authorized. `paused` is also supported. Default actor is `scheduler:loom`;
`run_as` may name an existing actor, not the ambiguous `owner` alias. Runtime
capability policy still applies; declaration does not grant extra permissions.

Optional limits: `timeout_seconds` (default300, max86400), `max_attempts`
(default1, max10), `lateness_window_seconds` (default300, max86400). Pending work
is skipped instead of overlapping. Calendars skip spring gaps and use the first
repeated fall wall time. Polling means due time is not exact dispatch time.

Use `loom schedule inspect <id-or-key>` and `loom schedule fires <id-or-key>`
for runtime state. Operator controls are `pause`, `resume` and `disable`.
A disabled schedule refuses manual `fire`.

Unchanged timing preserves the next occurrence and completed one-shot state.
Input/limit edits do not reset timing. Unchanged source status preserves an
operator pause/disable; explicitly resume, or apply a source status transition,
to reactivate. Removing the resource and applying disables future runs, retaining
ID, fires and invocations. It does not cancel dispatched work or delete history.
Returning the same key reuses ownership. Project restore leaves runtime inactive.

Hermes/model-driven automation remains a separate integration. This declaration
does not create a Hermes cron job or arbitrary command executor.
