# MINA Tooling

Use known routes directly. The single quick routing table is in
`protocols/DEVICE-AND-TOOL-ROUTING.md`; consult it only when the surface is unclear.
Main Chromium, an exact shared page and the Mac desktop are distinct targets.

## Core Work Surfaces

These are peers with different authority, not optional integrations beneath LOOM.
Use the relevant surface without a compulsory sequence or calls to all three.

| Surface | Responsibility and route |
|---|---|
| LOOM | Files, project registration, knowledge and technical execution/state; use the owning source or supported `loom` surface. Git and original artifacts remain source authority. |
| Basecamp | Canonical commitments, task/Kanban state, progress, blockers and the operator's input; `loom-mina-basecamp` under `protocols/BASECAMP.md`. Ordinary work maintains relevant tasks without waiting for an explicit Basecamp request. |
| Provenance | Shared source-backed decisions, preferences, rationale and qualified project context; Main's `loom provenance`, `use-loom-provenance` and `protocols/PROVENANCE-AND-MEMORY.md`. Retrieve when prior meaning matters and selectively capture new durable meaning during work. |

Human appointments, calendar events and deadlines belong in Basecamp. Hermes
owns agentic routine timing; LOOM owns system/service/backup and capability
schedules. A calendar entry is not an executable automation, and a successful
run is not automatically a completed Basecamp commitment.

## Supporting Runtime And Access Owners

| Need | Supported owner/surface |
|---|---|
| Projects and repositories | LOOM project registry and owning repository contracts |
| Jobs, workers and permissioned actions | LOOM capability and worker surfaces |
| Persistent conversations, memory, native child tasks and agentic routines | Full native Hermes on Main; Desktop and Hermex are clients; see HERMES-RUNTIME.md |
| Native Codex model, thread and managed-worktree lifecycle | Native Codex controls, exact resume and supported thread reads; opening another client does not transfer ownership |
| Desktop interaction | Native Hermes computer_use on Main; existing loom-mac MCP for the Mac; native Codex tools in Codex. ORCA is retired. |
| Conversational continuity | Current thread plus source-attributed relevant history; LOOM Provenance is a separate ledger |
| Native/created skills | Active harness's skill tools; Hermes profile skills or Codex `.agents/skills`, without duplicate external copies |
| Reviewed LOOM skill distribution | Repository source and supported account/admin native skill roots |
| Credential values | Approved external store and protected runtime injection |
| Named GitHub actor and portable workspace versioning | `loom-mina-gh` under `protocols/GITHUB.md`; separate Git transport gate |
| Approved Box project work, from native Desktop or direct CLI | Main terminal and project-local tools; both reach the same approved Box files |

Prefer `rg --files`, `rg`, directory listings and direct reads for known source.
This needs no search skill, indexing wait or health preflight. Use search for
discovery or relevant history; get exact record references directly. Use known
commands and inspect local help only for an unfamiliar operation or actual
mismatch; consult relevant skill/docs detail when needed. Do not invent
capability names, arguments or installation commands. Consult
`protocols/LOOM-ROUTING.md` when the retrieval surface is unclear.

Native Codex discovers supported account/admin and workspace skill roots. Read
`protocols/SKILL-CREATION-AND-PROMOTION.md` before creating or maintaining a skill.
Do not duplicate installed bodies into the workspace or shadow a shared skill.
Same-user read-only modes are not a security sandbox; OS/service custody must
independently protect installed skills. Mac offline is a tool outcome, not a
reason to block an unrelated thread or substitute another device.

For selected native Mac desktop work, load `protocols/MAC-COMPUTER-USE.md` for
fresh targeting and uncertain delivery, then reuse the unchanged protocol.
No protocol read activates a connector or proves current availability.

## Managed Account Entry Points

The selected MINA profile's account entry points require operator configuration
and authenticated verification. After configuration, for ordinary task work,
load the relevant skill and run its identity preflight; do not repeat OAuth,
installation or an operator handoff merely because the tool is managed.
A new host still needs deployment and authenticated verification before use.
This template ships no credentials.
An actual refusal is evidence to report, not permission to fall back to another
identity. Follow `OPERATING-POLICY.md` for the task's action scope.


For Basecamp, use `loom-mina-basecamp` with literal `--profile mina` before the
command. For GitHub, use `loom-mina-gh <command>`. The `--profile mina` pair belongs only to
Basecamp; GitHub CLI has no such flag. Follow the corresponding protocol's
identity preflight against the current protected operator binding. Put the command first (after Basecamp's literal profile
pair), then its options. The wrappers select only the pinned
native CLI and protected auth HOME/config/cache inside the child. Ordinary `gh`
and `basecamp` retain their normal behavior for other coding agents. Never set
global account environment or a shared default to make a call succeed.

The shared agents UID is not per-agent OS isolation. Named tools preserve
custody and environment selection, not a new permission model. Installed skills
and successful API authentication add no action authority or Git transport proof.
Missing/unsafe auth metadata, conflicting selectors and operator-only lifecycle
commands fail closed; hand off the refusal without credential values. Native
refresh may write only the approved existing external auth root;
no auth store is provisioned by package installation.

## Nix And Writable State

Nix owns package versions for system tools and dependencies, service definitions, immutable
store paths and system security. Do not edit the package store, bypass managed
mode, or repair a blocked harness change with pip, npm, system package installation,
sudo or a second agent account. A dependency or service change belongs in the
owning repository and the reviewed operator deployment flow.
This does not prohibit installing project-local dependencies into the project's
own virtualenv or node_modules as part of an approved development task.

Codex's native installer owns its releases; the boot service owns the resident
process. Updates require a quiet maintenance window, not an unattended restart.
CLI and resident server versions may differ until that handover. Preserve active
threads and prior binaries. Do not kill other sessions or edit the Nix store.
Runtime threads, created skills and task state remain writable. Ordinary Main
Codex has read/write/network access under the existing agents OS boundaries;
neither its settings profile nor the SOUL grants sudo or credential authority.
Inspect only relevant non-secret settings, not complete auth-bearing configs.

For a required managed change that is blocked, send a short handoff with the
intent, exact non-secret failure, current and desired setting, and source
owner. Continue unrelated work. Do not infer sudo or credential authority from
the blocker or from an available terminal.

Never open a runtime database directly or query its tables. Retrieve sessions
through the owning harness's native Hermes or Codex tools and semantic state
through LOOM Provenance. Recovery must
use a supported consistent snapshot workflow after its separate acceptance;
copying mutable profile files is not a verified backup.
