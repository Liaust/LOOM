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

## MINA / Hermes schedules

For MINA's context-rich reviews, use the installed native Hermes adapter rather
than wrapping MINA in a LOOM capability. Personal cross-project jobs can be
created directly through Hermes; they need no dummy project. Observe both native
and project-owned jobs with `loom schedules hermes --json` (also in Portal).

A project can declare a MINA job in the same `.loom/project.yaml`:

```yaml
resources:
  mina_review:
    kind: hermes_schedule
    hermes_schedule:
      profile: mina
      every_minutes: 60
      status: paused
      prompt: >-
        Read this project's AGENTS.md and .project/STATE.md. Report progress
        and blockers; do not deploy or change schedules.
```

Use exactly one of `every_minutes`, five-field `cron`, or RFC3339 `at`.
Optional `skills` lists installed skill names. The workdir is the registered
project, not an arbitrary prompt-provided path. Status defaults to `paused`;
explicit `active` requests activation. Use normal project plan/apply/status.
Hermes owns the native job, clock, session and history: LOOM does not create a
second timer. On Main, the gateway uses Europe/Amsterdam; the read-only inventory
does not certify timezone or scheduler liveness.

Reapply preserves an existing pause. Native edits can require reconciliation;
do not overwrite drift blindly. Archive/withdrawal pauses only marked project
jobs. Restore/reactivation never resumes them automatically. A completed
one-shot is not rearmed by reapply. Pausing prevents future triggers, not an
already-running session. Stored `ok` is a native result, not independent proof
of task effects or successful message delivery.

For a general project coding task instead, declare a script capability invoking
`codex exec` with explicit project context, then target it with the LOOM
`schedule` resource above. LOOM owns that job and its bounded completion; MINA's
profile and memories are not implicitly inherited.
