# Automation Email URL

Example v0.3 project for a Gmail-style automation.

The project exposes one script-backed capability under `main@automation-email-url.fetch_url` and one direct-event endpoint that maps an external email payload into that capability input.

Lifecycle:

```sh
loom project validate .
loom project register .
loom project activate automation-email-url --facet scripts
loom project activate automation-email-url --facet direct-events
loom project doctor automation-email-url --project-root .
```
