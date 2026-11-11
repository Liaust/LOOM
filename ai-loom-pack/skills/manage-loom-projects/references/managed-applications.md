# Managed Applications

Ordinary application code/builds/tests need no managed runtime declaration.
Implement authorized work directly; another agent or integrator is not a
prerequisite. Declare only requested managed effects in `.loom/project.yaml`.
Keep member repos portable; a missing adapter must not dictate their language.

## Select The Existing Owner

Source-first Python, Cargo and locked Nix packages use the existing application
build/job owner. Use the real project, including non-Git source; never fabricate
a repository, build metadata, second service or scheduler. Inspect compatibility
with `loom project deploy <ref> --resource <key> --check`; authorized deployment
uses `--yes`. Select `--node main` for a Main path, not the Mac filesystem.

Prepared artifacts still use project-relative `manifest` and `artifact_descriptor`
through normal plan/apply/status. Do not mix prepared and source modes.

Load only the applicable [Python/action](managed-application-recipes.md#source-first-workers),
[Cargo/Nix](managed-application-recipes.md#cargo-and-native-packages) or
[prepared-artifact](managed-application-recipes.md#prepared-applications) recipe.
No mandatory all-recipes read before ordinary work. Recipes preserve supported
build limits, typed argv/config delivery and exact operation recovery.

## Data, Credentials And Actions

Keep existing private state in place. Do not copy a live SQLite main file without
its WAL, start an empty replacement, chown project data or loosen permissions.
Applications consume resolved config/credential paths, not profile fallbacks.
Optional `backup: cloud_history` selects normal protection; pending evidence
does not prove application consistency, authenticated readiness or client delivery.

Use exact `pass://SHARE_ID/ITEM_ID/FIELD` references. the operator creates or registers
missing credentials; request the reference, never a secret in chat. Automatic
generation is deferred: no `pass+generate://`, broader grants or token upgrade.
Already-authorized Google imports use the supported protected importer, not a
new consent or exposed private profile path; see [credentials](managed-application-recipes.md#existing-credentials).

Actions run once per installed generation. Inspect the original operation before
retrying. Failed/uncertain work must not be bypassed with another run label or
deleted journals. An explicitly reviewed failed-hold release never reruns it.

## Explicit Public Exposure

For an authorized Main endpoint, the prepared recipe supports:

```yaml
endpoint:
  exposure: public_https
  hostname: webdav.apps.example.com
```

Use an unused name under `apps.example.com`, a manifest listener on `127.0.0.1`
and the declared HTTP health check. No per-app DNS edit, TLS setup or host grant
is needed; never hand-edit Caddy. DNS, trusted TLS, route and observed HTTP status
are separate evidence, not established by installation alone. Public exposure
requires task authority. It grants no outbound network access, authenticated
client acceptance, old-backlog intake or immediate cloud protection.

Read the [endpoint recipe](managed-application-recipes.md#public-https-endpoints)
before exposure, replacement or removal; it
preserves hostname-collision refusal, old-route retention during replacement,
archive/data history and the proxy's client-IP limitation.
