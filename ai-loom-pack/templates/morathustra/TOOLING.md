# Morathustra Tooling

| Need | Supported owner/surface |
|---|---|
| Projects and repositories | LOOM project registry and owning repository contracts |
| Technical file identity/version or extraction state | LOOM Objects |
| Source wording | LOOM Notes |
| Qualified decisions, constraints, relationships and unresolved cases | LOOM Provenance |
| Jobs, workers and permissioned actions | LOOM capability and worker surfaces |
| Interactive folder/worktree/task/terminal lifecycle | Installed release-matched ORCA tools |
| Model execution and conversation retrieval | Pinned Hermes runtime and session-search tools |
| Conversational continuity | Hermes memory, with pilot write approval |
| Native/created skills | Hermes `skill_manage`, with `category="created"` for new skills and write approval |
| Reviewed LOOM skill distribution | Repository source and operator-owned `skills/installed` deployment |
| Credential values | Approved external store and protected runtime injection |
| Named Basecamp task interface | `loom-morathustra-basecamp` with the literal profile under `protocols/BASECAMP.md` |
| Named GitHub actor and portable workspace versioning | `loom-morathustra-gh` under `protocols/GITHUB.md`; separate Git transport gate |

Discover an available tool and inspect its help/schema before use. Use
release-matched LOOM documentation through `loom docs search` and local help;
do not invent capability names, arguments or installation commands. Follow
`protocols/LOOM-ROUTING.md` for the first retrieval surface.

Hermes' `skills.external_dirs` exposes the installed set. Pinned Hermes has no
`skills.create_dir` setting. Use the native `created` category within
`.hermes/skills`, not another root. Read
`protocols/SKILL-CREATION-AND-PROMOTION.md` before creating or maintaining a skill.
Do not patch Hermes, redirect its store through symlinks, or treat instructions
as sandbox or credential enforcement. Same-user read-only modes are not a
security sandbox; runtime OS/service custody must independently protect
installed skills in both gateway and TUI.

## Managed Account Entry Points

Use `loom-morathustra-gh` and `loom-morathustra-basecamp` with literal
`--profile morathustra` for named account work, following the corresponding protocol preflight against
operator binding documented in release-matched ORCA Main Operations. Put the command first (after Basecamp's
literal profile pair), then its options. The wrappers select only the pinned
native CLI and protected auth HOME/config/cache inside the child. Ordinary `gh`
and `basecamp` retain their normal behavior for other coding agents. Never set
global account environment or a shared default to make a call succeed.

The shared agents UID is not per-agent OS isolation. Named tools preserve
custody and environment selection, not a new permission model. Installed skills
and successful API authentication add no action authority or Git transport proof.
Missing/unsafe auth metadata, conflicting selectors and operator-only lifecycle
commands fail closed; hand off the refusal without credential values. Native
refresh may write only the approved existing external auth root in the gateway;
no auth store is provisioned by package installation.

## Nix And Writable State

Nix owns package versions and dependencies, service definitions, immutable
store paths and system security. Do not edit the package store, bypass managed
mode, or repair a blocked change with pip, npm, system package installation,
sudo or a second Hermes profile. A dependency or service change belongs in the
owning repository and the reviewed operator deployment flow.

Managed `hermes config set` can refuse a change and still return exit zero.
Read the refusal and re-read effective state through supported config surfaces
before claiming success; an exit code alone is insufficient. Read only the
relevant non-secret setting, never dump a complete credential-bearing profile.
The Nix baseline is a provisioning template, not an automatic overwrite
mechanism for every live config edit. Live reconciliation is operator-owned
and must preserve unrelated values and concurrent user changes. Skill selection
and curator settings are dynamically read; a gateway restart is not needed
merely to reload them. Preserve active sessions. The pinned gateway bypasses
the curator's configured idle threshold at its hourly tick; read the skill
protocol before making an idle-safety claim.

Runtime profile state is not all immutable. Native skill and memory tools,
sessions, theme files and bounded task state remain available under their own
permissions and approval rules. A managed config refusal does not prohibit
that work. Keep `skills.write_approval=true`, `memory.write_approval=true` and
`skills.guard_agent_created=true`; the guard is heuristic review, not a
security guarantee. Use the native pending/diff/approve/reject flow with
the operator; never approve your own pending changes on his behalf.

For a required managed change that is blocked, send a short handoff with the
intent, exact non-secret failure, current and desired setting, and source
owner. Continue unrelated work. Do not infer sudo or credential authority from
the blocker or from an available terminal.

Never open a runtime database directly or query its tables. Retrieve sessions
through supported Hermes tools and semantic state through LOOM. Recovery must
use a supported consistent snapshot workflow after its separate acceptance;
copying mutable profile files is not a verified backup.
