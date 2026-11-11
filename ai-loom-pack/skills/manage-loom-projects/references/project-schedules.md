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

## Hermes Agentic Schedules

Main's native Hermes owns agentic timing. MINA's personal cross-project jobs
can be created through Hermes without a fabricated project. Project routines
can use the same declaration plan/apply/status path:

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
second timer. Configure and verify the gateway's timezone; the read-only
inventory does not certify timezone or scheduler liveness.

Reapply preserves an existing pause. Native edits can require reconciliation;
do not overwrite drift blindly. Archive/withdrawal pauses only marked project
jobs. Restore/reactivation never resumes them automatically. A completed
one-shot is not rearmed by reapply. Pausing prevents future triggers, not an
already-running session. Stored `ok` is a native result, not independent proof
of task effects or successful message delivery.

An operator-configured weekly Provenance routine uses Hermes as its sole clock.
Its script-only occurrence can invoke the supported conditional LOOM/Codex
executor. An empty eligible queue launches no agent. This example supplies no
live job ID, timezone, activation receipt or standing reconciliation authority.
Discover the actual job and verify its profile, workdir and authorized scope.
Preserve existing pauses and retired jobs. Inspect the native occurrence and
linked LOOM/Codex result; never resume a second clock.

## Optional Project Profile

Add an `agent` resource only when the project needs a persistent Hermes identity:

```yaml
resources:
  researcher:
    kind: agent
    agent:
      instructions_ref: notes/researcher.md
      model: gpt-6-astra
      provider: openai-codex
  research_review:
    kind: hermes_schedule
    hermes_schedule:
      profile: researcher
      every_minutes: 60
      status: paused
      prompt: Read this project's notes and summarize any new questions.
```

Instructions are a project-relative Markdown source captured in the plan.
Optional `workdir` is project-relative; it defaults to the project root.
The owner creates a fresh stable native profile without cloning MINA's memory,
credentials, channels or jobs. Model/provider selection is explicit; a missing
native login or endpoint is not permission to start OAuth or choose a fallback.
Native profiles are not OS security boundaries. Mutable memory/history/skills
stay native. Managed instructions/config edits surface as drift; review them
instead of overwriting. Resource withdrawal retains the profile and pauses its
project-owned triggers. Restore leaves those triggers paused.

## Standalone Codex From Hermes Timing

Use `target: codex` when the task needs a normal project Codex run, not a
Hermes conversation or another persistent persona:

```yaml
resources:
  coding_review:
    kind: hermes_schedule
    hermes_schedule:
      profile: mina
      target: codex
      codex:
        model: gpt-6-astra
        timeout_seconds: 120
        sandbox: read-only
      every_minutes: 60
      status: paused
      prompt: Read this project's AGENTS.md and report current blockers. Do not edit.
```

Here `profile` selects the native timer store, not a supervisor model. A native
script-only occurrence calls the existing authorized Main job owner; no Hermes
inference turn launches Codex and the Mac is unnecessary. Codex receives the
registered project root, its project instructions and explicit execution settings,
not MINA's personality. `skills` is omitted for this target. Timeout is 1-1800
seconds; native script timeout must cover dispatch plus execution. Inspect actual
native occurrence and linked LOOM call/job/thread/outcome, not merely enqueue
acceptance. `needs_input` or unknown outcome requires attention, not blind replay.

`loom schedules hermes --json` includes selected project profiles and sanitized
native state. Ordinary LOOM system/service/backup schedules remain with LOOM.

## Optional Internal Job Notifications

A project can opt in to native diagnostic calls for `job.failed` and
`job.timed_out`. Reuse its explicit integration identity and registered project
ID; the CLI builds the internal-only mapping:

```sh
loom direct-event endpoints create --slug project-job-alerts \
  --integration <integration-key> --project <project-id> \
  --hermes-profile reader --internal-event job.failed --internal-event job.timed_out
```

Use `mina` or that project's declared agent resource key. No HTTP auth profile
or public listener is created; external ingestion into this subscription is
refused. Existing endpoints without this opt-in remain unchanged. Pause/resume
through `loom direct-event endpoint`; archive pauses project-owned native work,
and restored projects/routines need explicit reactivation rather than catch-up.

LOOM commits source identity to its existing queue, then calls the fixed
`main@system.hermes.event` capability. The backend checks the real event/job and
holds the project's archive fence through native admission. Up to 16 pending
events coalesce without resetting the first due time. Native cadence can add
roughly a minute; this is not instantaneous delivery. Full batches and unknown
outcomes require attention, not silent loss or automatic side-effect retries.
Queued/coalesced/duplicate receipts contain a native job ID, not proof the agent
finished. Use native execution/output history for that. Events are observations,
not new human authority; diagnostics do not automatically repair/retry jobs.
