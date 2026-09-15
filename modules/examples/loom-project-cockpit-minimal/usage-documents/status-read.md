# Project Cockpit Status Read Usage

## Direct Use

Use `status.read` when an actor needs a compact status summary for one project cockpit view. The expected input is optional `project_ref`; without it, the module should eventually return the active/default project cockpit status for the caller context.

## Workflow Use

Use this capability inside higher-level workflows that need to decide whether a project is blocked, ready for review, or missing recent worklog information. The capability is read-only and should not mutate LOOM project records.
