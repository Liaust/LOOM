# Named GitHub Identity And Portable Projection

Read `CREDENTIALS.md` and `../OPERATING-POLICY.md` before GitHub work.
Use the installed, release-matched GitHub CLI and Git only within the accepted
task. Missing tooling stops the operation; do not install or authenticate as an
incidental repair. CLI packaging precedes live authentication.

## Personal Identity And Isolated Authentication

Use the operator's personal GitHub identity through a fresh isolated OAuth grant
on Main. Never copy or reuse existing Mac/shared tokens, shared CLI login,
credential helpers, SSH keys or SSH agents, or Codex's, Apollo's or another
agent's credentials. A commit author name is not an authenticated actor.
Do not switch the shared CLI account or change global Git configuration.

The separately authorized integrator bootstrap, with the operator present, must
isolate GitHub CLI authentication in an operator-owned protected configuration
outside the live workspace and its projection. Bind `GH_CONFIG_DIR` to that
approved isolated configuration for every invocation; it must contain only
the operator's verified personal identity under this fresh grant. Freeze the
verified host, login and immutable numeric actor ID in the approved non-secret
operator binding after successful OAuth. This portable protocol supplies no
live login, actor ID or authentication state.

For named task calls use `loom-morathustra-gh`, with the command first and
options after it. It binds the exact native CLI, isolated child HOME/config/cache
and GH_CONFIG_DIR, github.com and Nix's trusted CA bundle. Do not export those
settings in the parent shell. It refuses inherited token/host/transport overrides
and unsafe or missing auth metadata; no bootstrap or credential repair occurs.
Do not fall back to bare `gh`, change the wrapper or invoke auth/config/install
commands to get past a refusal. Use relative `api` endpoints and explicit
`--repo OWNER/REPO` or `--repo github.com/OWNER/REPO` when selecting a repository.

Read the immutable non-secret operator binding documented in release-matched
ORCA Main Operations through supported LOOM docs, using its `github.host`, `github.login` and `github.id`. The native
current-user probe is `loom-morathustra-gh api user`; compare its structured
reply to the bound host/login/numeric actor before task use. The shared agents
UID is not per-agent OS isolation; ordinary coding agents receive no automatic
named-account environment. Installed skills grant no additional external-action
authority. API authentication does not prove Git fetch/push transport; the
separate pre-effect transport verification below remains required.

Before first task access and before an external effect, require that frozen
binding and verify the effective authenticated actor through the supported
GitHub current-user API. Require successful structured evidence matching both
the exact login and numeric ID on the bound host. A matching display name or
locally configured commit email is insufficient. Missing binding, failed
authentication, unexpected actor, host or scope means stop with no fallback.

Check for environment and transport overrides without printing secret values.
For the initial isolated OAuth workflow, `GH_TOKEN`, `GITHUB_TOKEN`,
`GH_ENTERPRISE_TOKEN` and `GITHUB_ENTERPRISE_TOKEN` must be absent; stop if any
is present. Verify effective host selection, Git credential-helper chains, SSH
selection and URL rewrites before remote access. Git's actual fetch/push
transport must use the same verified personal actor and isolated grant with no
inherited credential or transport fallback. Do not assume isolating the CLI
also isolates Git. If this cannot be proved, stop before any fetch or push.

The intended access covers the operator's complete account, repositories and
organisations as their actual granted permissions and SSO policies permit.
Do not impose a example-operator-only or private-only access limit. The private initial
workspace projection below is one destination, not the boundary of account
access. Verify the permissions needed for the exact repository and task.

Account access does not grant blanket action authority. Use only approved
scopes needed for the operator's authorized work; do not automatically request
every admin OAuth scope or change organisation policies to obtain access.
Organization membership or a working login does not establish repository
creation, publication or organization-owner authority.
Unexpected consent, scopes, paid-seat behavior or ownership means stop for the
integrator; never widen access to make a command succeed.

## Private Portable Workspace Repository

The live Hermes workspace remains a non-Git ORCA folder project. Never initialize
Git there, place it inside another worktree, add it as a submodule, or mirror it
wholesale. A private remote is not permission to copy private runtime state.

The initial intended destination is private `example-operator/morathustra-workspace`.
Creation and first publication are separate integrator actions with the operator
present, after the personality/protocol and CLI slices are accepted and
deployed. Verify exact owner, repository name, private visibility and the
verified personal actor's permission before creation and before every push.
If an existing repository has unexpected ownership, visibility or contents, stop;
do not repurpose it or make it public. No public fallback or force push.

Prepare a separate projection directory outside the live workspace. Populate
it from an exact accepted repository checkpoint, with an explicit reviewed
file allowlist and source hashes. Initially allow only these source templates:

- `.hermes/SOUL.md`, the sole personality source, as reviewed portable template
  text from the repository; never copy the live `.hermes` directory.
- `AGENTS.md`, `WORKFLOW.md`, `OPERATING-POLICY.md`, `WORKSPACE-MAP.md`, `TOOLING.md`.
- `protocols/README.md`, `protocols/BASECAMP.md`, `protocols/GITHUB.md`,
  `protocols/CREDENTIALS.md`, `protocols/EXTERNAL-MESSAGING.md`,
  `protocols/LOOM-ROUTING.md`, `protocols/PROJECT-DELEGATION.md`,
  `protocols/PROVENANCE-AND-MEMORY.md`, `protocols/SESSION-RETRIEVAL.md`,
  `protocols/SKILL-CREATION-AND-PROMOTION.md`.
- `handoffs/HANDOFF-TEMPLATE.md`, as an empty handoff form only.

The projection is versioned source for review, not a second active personality,
runtime profile, backup or deployment channel. Promote any future curated live
instruction change through repository review first. It never authorizes editing
LOOM-installed copies or overwriting a live SOUL on scaffold replay.

Exclude every unlisted file. In particular, exclude live `.hermes` config,
credentials, OAuth/cache state, sessions, memories, databases and WAL/SHM files,
cron, logs, native/created skills, recovery payloads, `skills/installed` deployment
copies, investigations, completed handoffs, scratch, machine paths, symlinks,
hardlinks to live files and Git alternates. Do not copy Apollo's identity,
private memory, domain authority or file-backed memory system. Apollo may be a
structural reference only. Do not load private material to construct a filter.

Inspect the complete staged file inventory and bytes, source hashes and every
commit reachable from the proposed push for forbidden state before publication.
An ignore file alone is insufficient: it does not remove tracked files or
history. Stage only explicit allowlisted paths; never stage the whole workspace.
If secret or runtime state is discovered, stop publication and route remediation
to the integrator without printing values or rewriting history unilaterally.
After an authorized push, verify the exact remote commit and private visibility.

Repository work remains governed by its own contracts. Project coding agents
receive bounded scope and evidence, never the named actor's authentication,
Morathustra's personality, private memory or broader operational authority.
