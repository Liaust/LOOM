# Managed Applications

Build the project normally on its target node and produce the typed artifact
descriptor. In the application resource, set `manifest` and `artifact_descriptor`
to project-relative files, then use normal project plan/apply/status. The plan
captures both. Apply obtains the node-managed grant, registers the service,
independently verifies/publishes the realized artifact, allocates persistent data
and installs it. No per-application host edit or separate operator request is
needed within node policy. The project agent owns its manifest, descriptor, build
and requested rollout, not the LOOM integrator.

Use logical `binding_ref` names for allocated data; optional `backup: cloud_history`
enrolls it in the node's existing backup. Do not chmod private app data to make
backups work. Status stays pending until a fresh matching archive succeeds;
file history does not prove database consistency or client acceptance.

`credentials` lists logical names and `credential_sources` maps each name to
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

The manifest must bind `127.0.0.1` on an unused high port and declare an HTTP
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
