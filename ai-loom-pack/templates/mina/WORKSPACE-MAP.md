# MINA Workspace Map

All paths below are relative to this workspace. Deployment chooses and verifies
the canonical root; portable instructions contain no machine-local locator.

| Path | Owner and purpose |
|---|---|
| `.hermes/SOUL.md` | Single substantive personality source; preserved on scaffold replay |
| `AGENTS.md` | Workspace authority, instruction priority and routing |
| `WORKFLOW.md` | Intake, evidence, delegation, verification and handoff |
| `OPERATING-POLICY.md` | Inspection, reversible action and explicit approval boundaries |
| `TOOLING.md` | Supported interfaces and tool ownership |
| `protocols/` | Task-scoped operating instructions |
| `skills/installed/` | LOOM-managed external deployed skills; owner/ACL/service custody governs access, not a creation target |
| `.hermes/skills/` | Native Hermes bundled and user skill store; existing bundled skills stay in place |
| `.hermes/skills/created/<name>/SKILL.md` | New native skills use `category="created"`; writes save immediately under the task policy |
| `.hermes/pending/` | Existing proposals remain reviewable through native pending/diff/approve/reject surfaces; normal writes no longer queue |
| `investigations/` | Bounded redacted evidence and pointers to original sources |
| `handoffs/` | Task-specific transfers of scope and evidence |
| `recovery/` | Reserved for later verified recovery publication, not scratch data |
| `tmp/` | Disposable task scratch; never canonical evidence or credentials |

A reviewed provisioner prepares Hermes' config, session/history state,
memories, cron, logs and native/created `.hermes/skills` under the same profile.
The scaffold supplies only the SOUL there. It creates no runtime database,
config, session, private memory, account, provider or schedule state and does
not initialize Hermes.

There is no root personality compatibility file, hand-written memory tree or
speculative integrations directory. Existing legacy workspaces need a separate
non-overwriting migration with a reviewed inventory and rollback evidence.
Scaffolding stops when it encounters legacy or runtime state; it cannot migrate
or validate a live workspace.

Nix owns harness versions, dependencies, services and immutable package paths.
The runtime profile is not all immutable: native skill and memory tools,
sessions, theme files and bounded task state keep their own permissions.
The baseline template does not automatically overwrite live state. See
`TOOLING.md` for managed-setting refusals and a bounded handoff.

The `created` category does not transfer ownership to the curator. Foreground
skills remain user-owned until explicit supported adoption. The effective native selection
is authoritative; historical counts are not a curated catalogue or a ceiling.
Native and managed installed skills retain their separate owners. See
`protocols/SKILL-CREATION-AND-PROMOTION.md` for selection and maintenance rules.
Same-user read-only modes alone are not a security sandbox for installed skills.
