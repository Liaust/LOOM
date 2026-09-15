# Connector Ping Script

This script package backs the connector endpoint:

```text
main@example_connector.ping
```

It is intentionally minimal and low-risk. It returns a JSON `pong` response and
does not require network access or credentials.

## Files

- `loom.script.yaml`: script package manifest.
- `run.sh`: executable entrypoint.
- `README.md`: endpoint implementation notes.

## Direct Execution

From this folder:

```sh
./run.sh
```

Expected output:

```json
{"message":"pong"}
```

Direct execution only tests the local script. It does not test LOOM provider
registration, capability routing, policy, jobs, or runtime binding.

## LOOM Execution

After activating the connector facet from the project root:

```sh
loom project activate everything-template --facet connectors --project-root .
loom capability call main@example_connector.ping --input '{}' --wait
```

## Editing Rules

- Keep output JSON-friendly.
- Keep failures non-zero and clear.
- Do not add hidden credentials.
- Do not depend on the current terminal directory.
- Document any runtime dependency before using it.
