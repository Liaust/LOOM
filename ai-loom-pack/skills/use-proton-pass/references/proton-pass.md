# Proton Pass Command And Safety Reference

## Officially Documented Shape

A durable reference should prefer the unique Share ID and Item ID:

```text
pass://<share-id>/<item-id>/<field>
```

Names can be ambiguous. IDs identify the intended vault share and item but do
not grant access.

Verify the locally installed command surface before use:

```bash
pass-cli --version
pass-session-ensure
pass-cli info
pass-cli run --help
```

On main, `pass-session-ensure` serializes session recovery and reads the scoped
agent access token from an owner-only host file. Never open, print, copy, or
move that file. The helper is not a LOOM credential broker; it only restores
the normal Proton CLI session after expiry.

If the item has an explicitly non-secret intended-use note, inspect only that
field through an approved UI or bounded field reference. Never display a
password, token, TOTP seed, recovery value, or whole item in an agent-visible
terminal.

## Bounded Non-Interactive Use

```bash
export SERVICE_TOKEN='pass://<share-id>/<item-id>/<field>'
PROTON_PASS_AGENT_REASON='<concise task-bound reason>' \
  pass-cli run -- <approved-non-interactive-command>
```

The child command receives the resolved value. Proton masks matching secret
values in stdout and stderr by default. Do not use `--no-masking`. Masking does
not prevent the child from sending the credential externally, writing derived
data, changing an account, or exposing it through an unrecognized transform;
review the child command independently.

Do not put literal values in `.env` files. A reference-only environment file is
still sensitive operational configuration and must follow the current scope's
storage rules.

## Interactive Boundary

Official `run` documentation describes it as aimed at scripts and
non-interactive programs, not a full pseudo-terminal. This pack deliberately
does not adopt the documented `item view` plus shell export workaround because
that would expose a secret to an agent-visible shell. Do not wrap an interactive
Orca/Codex process in `pass-cli run`.

## Approval Boundary

Stop for explicit approval before:

- creating an external account or credential item;
- rotating, deleting, sharing, or changing access;
- creating or changing PAT/session/key-provider state;
- using production, billing, domain, root, cloud, or recovery credentials;
- running a command that publishes, contacts, spends, deploys, or destroys.

## Accepted Main Authentication And Deferred Behavior

Main runs Proton Pass CLI `2.3.3` as the shared `agents` identity. The accepted
session uses the filesystem key provider and the standard Linux session path.
The scoped access token can see only the `LOOM CREDENTIALS` share. The token
value remains outside this pack, prompts, memory, Git, LOOM, and Basecamp.

Disposable item injection, audit-log review, reboot recovery, interactive
injection, and any unattended LOOM runtime integration remain deferred until
their explicit acceptance gates pass.
