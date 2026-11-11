# Managed Application Recipes

Load only the section needed for the selected build/effect. Start with
[management boundaries](managed-applications.md); these recipes grant no new
deployment, account, public-exposure or intake authority.

## Source-First Workers

Implement the requested application yourself when useful; another coding agent
or integrator is not a prerequisite. For a private Python standard-library
worker, declare its actual entrypoint and settings in `.loom/project.yaml`:

```yaml
resources:
  intake:
    kind: application
    application:
      source:
        path: .
        python_module: intake.worker
      config:
        schema: intake.worker.v1
        values: {poll_seconds: 30}
      data:
        database: {path: .data}
      network: outbound_https
      actions:
        status:
          python_module: intake.worker
          args: [status]
          read_only: true
```

Use the existing source project, including non-Git projects. Do not create a fake
repository, build descriptor, manifest or extra service just to satisfy LOOM.
The producer captures the selected Python package, not the entire project.
Declare nonsecret package assets with `source.files: [intake/defaults.json]`.
Source must be readable by the node's LOOM builder; keep state and credentials
private rather than opening the whole project. This Python adapter is stdlib-only;
third-party Python environments need their own supported native packaging adapter.

```sh
loom project deploy <project-ref> --resource intake --check
loom project deploy <project-ref> --resource intake --yes
loom project run-action <project-ref> intake status --yes
```

For a path on another node, pass `--node main`; do not resolve that path on the
Mac. `--check` only inspects compatibility, and `run-action --status` only observes
an existing action. Deployment builds, provisions and supervises through the
existing application owner. Repeating an unchanged deployment reuses its
operation. A named action's default intent runs once per installed generation;
use `--run <label>` only for a deliberately separate authorized invocation, not
to bypass a failed/uncertain hold. The active command waits and finalizes a known
successful action without running its package again.

If a failed action has been investigated and its partial domain effects reviewed,
`run-action ... --release-failed --yes` releases that exact hold while preserving
the failed receipt. It never reruns the action or restarts the consumer. Repair
the source before a new deploy. Do not release unknown/running work, invent a
new run label to bypass it, or delete journals.

`data.path` adopts existing private project state in place, preserving owners,
bytes and history. LOOM grants only the managed application the required access.
Do not move a live SQLite database, copy its main file without its WAL, chown
the project or loosen private directory permissions. A missing/foreign-writable
state identity is a real blocker, not permission to start an empty database.

The launcher receives one absolute resolved-config JSON file. Typed values are
under `values`; data parameters become `{data_path: ...}` and credential
parameters become `{credential_file: ...}`. A named action receives the same
config followed by its declared fixed arguments. Do not reconstruct binding
paths in application code or fall back to another database/credential.
Systemd credential delivery can be root-owned and read-only with service-user
ACLs; it need not be owned by the process UID. Respect the resolved delivery and
manager's `CREDENTIALS_DIRECTORY`, rather than chowning it or reading a profile
fallback.

## Cargo And Native Packages

Rust uses the same deploy and action commands. Keep normal Cargo source,
Cargo.lock, workspace members, in-root path dependencies and build assets together:

```yaml
resources:
  worker:
    kind: application
    application:
      source:
        path: repos/worker
        build: {kind: cargo}
      run:
        binary: worker
        args: [{config_file: true}]
      data:
        state: {binding_ref: worker.state}
      actions:
        status:
          args: [{config_file: true}, status]
          read_only: true
```

Use an ordinary Cargo lockfile, not LOOM descriptors or handwritten fetch hashes.
The host selects pinned Rust and generates verified dependency fetching. Crates.io
and pinned public HTTPS Git dependencies on GitHub/GitLab/Codeberg are supported;
same-name/version crates from different remote sources need distinct versions or
a project-specific Nix package. Private registries, Git submodules, outside-root path dependencies, custom Cargo
registry/linker/environment settings and different/nightly toolchains are not.
Keep native libraries in the project's normal locked Nix recipe when needed.
Cargo assets are captured subject to bounded source size, .loomignore and private/
rebuildable exclusions; no target, node_modules, hidden state or credentials.
Multiple candidates need source.build.package and/or run.binary. Optional
features and no_default_features belong in source.build. A normal single-bin
package needs no selectors beyond its source path (or kind when also a flake).

For a conventional native package use source.build: {kind: nix, attribute: worker}
with saved flake.nix/flake.lock. The initial adapter accepts ordinary pinned public
GitHub/GitLab inputs and executable ELF outputs, not interpreter wrappers. Use
run.binary when its output has several programs. No lock rewriting, --impure,
arbitrary build command or extra credentials are inferred from a declaration.

Native run/action args are literal argv, not shell commands or templates. The
only substitution is {config_file: true}; LOOM_CONFIG_FILE is also supplied.
No config positional argument is added unless declared. Explicit stdlib Python
uses source.build: {kind: python}, run.python_module and the same typed args.
Legacy source.python_module retains its original config-first calling convention.

--check performs read-only bounded inspection, without compilation or downloads.
Deploy creates a durable Main build job and normally waits; --no-wait returns its
build/operation IDs while Main continues without the Mac. Inspect the original
operation with loom project operation <id> and build with loom jobs list --type
application_build. Bounded compiler stderr is retained with the job's log refs.
Unchanged deployment reuses its operation. A failed build leaves the installed
generation untouched; interrupted/uncertain attempts require review, not a new
run label or automatic compiler replay. Existing project plan/apply/status remains
the owner; do not create a second service, scheduler or build daemon.

`network: outbound_https` permits public TCP443 and controlled DNS on opted-in
nodes. It opens no listener and is not a domain allowlist. Omit it for loopback
only. Manager health proves process supervision, not authenticated intake,
initialization, business readiness or sending authority. Distinguish those from
the application's own current status. An old input backlog must never be
imported merely because deployment succeeded; follow the user's intake scope.

## Existing Credentials

For an already-authorized Google OAuth file under Main's approved private
profile root, use `loom project import-credential <project-ref> <application>`
with `--source-file`, exact `--account`, repeated `--scope`, and `--yes`. It
verifies and snapshots inside the protected owner; it does not repeat consent.
Bind the returned opaque `runtime://...` reference under `credential_sources`.
Never print the file, embed tokens in source or use a private profile path as a
runtime binding. Other supported sources remain `pass://SHARE_ID/ITEM_ID/FIELD`.

## Prepared Applications

Existing prepared artifacts remain supported. Build normally on the target node,
then set `manifest` and `artifact_descriptor` to project-relative files and use
normal project plan/apply/status. Do not mix them with `source`. Apply obtains
the node grant, verifies the real closure and installs through the same owner.
Never fabricate build metadata or reimplement LOOM validators when a producer is
missing; report the precise unsupported producer.

Background applications can omit `listener` and use `health: {kind: manager}`.
This proves process supervision, not authenticated intake or business readiness.
Prepared applications retain their existing loopback policy; adding an HTTP
wrapper or public URL does not authorize outbound access.

Use logical `binding_ref` names for allocated data; optional `backup: cloud_history`
enrolls it in the node's existing backup. Do not chmod private app data to make
backups work. Status stays pending until a fresh matching archive succeeds;
file history does not prove database consistency or client acceptance.

In prepared mode, `credentials` lists logical names and `credential_sources` maps each name to
`pass://SHARE_ID/ITEM_ID/FIELD` (preserve trailing `=` padding). the operator creates
or registers missing credentials manually in the approved Proton vault. Tell him
the required account/item and fields; use the resulting reference, without asking
him to paste a password or token into chat. The project agent owns binding that
reference and continuing the requested rollout. Credentials never belong in
project files.

Automatic generation is deferred by the operator's current decision. Keep the
working Viewer token; do not use `pass+generate://` or request broader Proton
access as a prerequisite for deployment. Existing generation code is retained,
not a claim of live availability. If a historical operation reports
`credential.generation_pending`, reconcile it with the operator rather than
deleting its journal or using a new source name to retry creation.

## Public HTTPS Endpoints

For an explicitly requested public application on Main, add this to its
application declaration, beside `manifest` and `artifact_descriptor`:

```yaml
endpoint:
  exposure: public_https
  hostname: webdav.apps.example.com
```

Choose one unused name beneath `apps.example.com`. Wildcard DNS and the shared
VPS-to-Main ingress are node infrastructure; no per-app DNS edit, certificate,
endpoint registration or host grant is needed. Do not use `endpoint_ref` with
`hostname`. Other domains and legacy private endpoint adapters are not enabled
by this path. Omit the endpoint (or select loopback) to keep the app private.

For public HTTP exposure, the manifest must bind `127.0.0.1` on an unused high port and declare an HTTP
health check, including its expected status (401 is valid for an authenticated
service). LOOM verifies the listener belongs to the installed process before
publishing it. Caddy terminates HTTPS on Main; app authentication stays in the app.
Public exposure must be part of the requested, reviewed project plan.

`project status` reports the desired URL, DNS, trusted TLS, route and observed
HTTP status separately. A successful installation/reload may precede certificate
issuance; pending endpoint evidence is not a healthy public service. Authenticated
client acceptance and backup coverage are separate. The simple VPS TCP proxy
does not preserve the original client IP; do not rely on it for per-client logs
or rate limiting. Application data and Caddy state enter the ordinary cloud file
history; no immediate archive or application-consistent recovery is implied.

A hostname collision leaves the other app unchanged. Removing the public
endpoint withdraws only this app's route. Renames retain the old hostname until
the replacement answers through trusted HTTPS; a pending replacement is reported
for retry through the existing operation. Archive withdraws the route without
deleting application data. Never edit Caddy fragments or host grants yourself.
