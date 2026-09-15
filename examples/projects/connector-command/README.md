# Connector Command

Example v0.3 connector project.

The connector declares provider `main@example_command` and capability `main@example_command.ping`.

```sh
loom project validate .
loom project register .
loom project activate connector-command --facet connectors
loom capability call main@example_command.ping --input '{}' --wait
```
