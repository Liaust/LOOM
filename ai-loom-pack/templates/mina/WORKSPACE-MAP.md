# MINA Workspace Map

Paths below are relative to the existing non-Git workspace. Deployment verifies
the canonical root; portable instructions contain no machine-local locator.

| Path | Owner and purpose |
|---|---|
| `SOUL.md` | One active personality source, preserving live edits during migration |
| `AGENTS.md` | Bootstrap, instruction priority and routing |
| `WORKFLOW.md` | Intake, evidence, delegation, verification and handoff |
| `OPERATING-POLICY.md` | Task authority and sensitive-action boundaries |
| `TOOLING.md` | Supported interfaces and tool ownership |
| `protocols/` | Task-scoped operating instructions |
| `.agents/skills/<name>/SKILL.md` | Existing MINA-owned Codex skills, externally discoverable by Hermes without duplication |
| `skills/installed/` | Installed LOOM skill sources, externally discoverable; edit their canonical source |
| `.hermes/` | Native Hermes configuration, memory, sessions and skills plus retained attributed history |
| `context/` | Source-attributed operational handoffs and the transferred reconciliation cursor; not a semantic ledger |
| `automations/` | Registered script packages; timing and execution owners stay explicit, with one timer per occurrence |
| `investigations/` | Bounded redacted evidence and pointers to original sources |
| `handoffs/` | Task-specific scope and evidence transfers |
| `recovery/` | Verified published recovery packages, never scratch |
| `recovery-codex/` | Selected private Codex captures for encrypted cloud protection, not workspace Git export |
| `tmp/` | Disposable task scratch, not canonical evidence or credentials |

Hermes-created skills live in `.hermes/skills`; do not shadow externally installed
names. Its profile SOUL references the canonical root personality. Native
conversational memory is separate from Notes and Provenance.

Codex threads, auth and settings belong to the shared account's CODEX_HOME,
outside this workspace. A settings profile is not a separate agent, account or
conversation store. Normal cwd-filtered resume distinguishes MINA and project
conversations. Follow `SESSION-RETRIEVAL.md` before continuing an exact thread.

The scaffold supplies portable instructions only. It does not provision a login,
create a schedule, initialize Git, migrate live files or verify a backup. A live
workspace update separately preserves edited files and historical Hermes state.
There is no automatic bulk memory import or second personality source.

Nix owns system tool versions and service definitions; native Codex releases
use their installer and coordinated updates. Runtime state and created skills
remain writable. Shared installed skills use the supported account/admin root
and retain their source owner. See
`protocols/SKILL-CREATION-AND-PROMOTION.md` for discovery and promotion.
