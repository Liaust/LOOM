---
name: use-proton-pass
description: Use an approved Proton Pass credential for a bounded non-interactive command or maintain a non-secret pass:// reference when credential intent, exposure risk, and approval boundaries must be enforced.
---

# Use Proton Pass

Proton Pass is the credential authority. This skill helps use an already
approved credential without copying its value into LOOM or agent-visible
artifacts. It does not implement a LOOM credential broker.

## Trigger Boundary

Use this skill when a task requires an existing approved credential for one
bounded non-interactive command, or when a contract needs a non-secret
credential reference. Do not use it to expose a value, improvise interactive
TTY injection, create an external account, broaden vault access, or make an
unapproved production/billing/domain/root/cloud/recovery credential change.

## Source-Of-Truth Order

1. Explicit user scope, repository instructions, and the command's own
   production/approval boundary.
2. The Proton Pass item's intended-use note and its current vault/item access.
3. The approved non-secret `pass://` reference, preferably Share ID and Item ID.
4. Current official Proton Pass CLI documentation and installed CLI help.
5. Project `.loom/contracts/credentials.yaml` only as a logical dependency
   reference using its currently supported `env` or absolute `file` source,
   never as a source of secret values or an ad hoc `pass://` binding.

If intended use, item identity, environment, or approval is ambiguous, stop
before resolving the credential.

## Inspect, Plan, Apply, Verify

1. On main, run `pass-session-ensure` before the first Proton operation. It
   checks the shared session and restores an expired session without exposing
   the scoped access token. Never read its token file directly.
2. Classify the credential and target command: environment, external effects,
   production sensitivity, billing/domain/root/cloud/recovery reach, and
   expected outputs.
3. Confirm the item's intended-use note without displaying a secret field.
4. Confirm a unique ID-based reference and the minimum field required.
5. Plan one bounded non-interactive command, preserving Proton's default output
   masking and avoiding debug/verbose output that may leak derived data.
6. Obtain required approval before sensitive use.
7. Set a concise `PROTON_PASS_AGENT_REASON` for every operation that requires
   one, then run through `pass-cli run` only when the child command is suitable
   and its
   side effects are authorized.
8. Verify the intended external result and inspect logs/artifacts for exposure
   without echoing or comparing the secret value.

Read [the Proton Pass command and safety reference](references/proton-pass.md)
before use.

## Safety Classification

- **Inspect:** installed CLI version/help, authentication status, vault/item IDs,
  and an explicitly non-secret intended-use note.
- **Plan:** construct an ID-based reference and a bounded masked command without
  resolving values.
- **Safe run:** approved non-production credential use for one non-interactive,
  non-destructive command with reviewed outputs.
- **Sensitive:** production, billing, domain, root, cloud, recovery, account
  creation, credential creation/rotation/deletion, access changes, sharing,
  PAT/session changes, or any command with destructive/external commitment.

Skills do not grant credential authority. Proton access also does not authorize
the child command's external effect.

## Non-Interactive Only

`pass-cli run` resolves `pass://` references in child-process environment
variables and masks resolved values in stdout/stderr by default. Never pass
`--no-masking`. Keep the command small and non-interactive; do not wrap an Orca,
Codex, shell, editor, or other full TTY session.

Do not work around interactive limitations by printing a password/token with
`pass-cli item view` into an agent-visible shell, command substitution, file,
clipboard, prompt, or chat. Stop for a proof-backed interactive mechanism or
human operation.

## Storage And Logging Prohibitions

Secret values, Proton sessions, PATs, recovery material, and resolved
environment dumps never belong in Git, `.loom/`, LOOM Notes, Basecamp, logs,
support bundles, handoffs, shell history, or agent memory. Durable references
should use `pass://<share-id>/<item-id>/<field>`; IDs are non-secret locators,
not authorization.

## Current Versus Future

Main uses Proton Pass CLI `2.3.3`, an owner-only filesystem-backed session, and
the scoped `loom-main-agents` access token. The token is granted only the
`LOOM CREDENTIALS` share. `pass-session-ensure` is the accepted recovery path
for the shared `agents` identity. Disposable item injection, reboot recovery,
and content-level audit acceptance remain separate tests; do not present those
uncompleted behaviors as accepted operation.
