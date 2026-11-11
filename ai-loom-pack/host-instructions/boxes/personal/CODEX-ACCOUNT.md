# Personal Codex Account Binding Example

UNCONFIGURED: this file supplies no authenticated binding or action authority.
The operator must provide an approved non-secret binding outside public source
before any account access. Do not authenticate or provision an account as part
of ordinary task work. Missing or placeholder values mean stop account work.

The binding must name the selected Personal Box, literal CLI profile, service
endpoint, expected global `identity.id`, exact `accounts[].id`, account-scoped
recording `person_id` and verified email. Obtain them from authenticated evidence
during separately authorized setup, never from a display name or this template.
Store references, not tokens, passwords, browser cookies or credential values.

For an ordinary Codex worker, configure a dedicated profile named `codex` and
include literal `--profile codex` on every Basecamp command. This example does
not bind that profile to any account. A named agent uses its own account protocol.
Never set `BASECAMP_PROFILE`, change the shared default, copy another actor's
binding or use ambient authentication as a fallback.

Before first task access and after authentication/account changes, run the
native identity probe under that literal profile. Require successful parseable
structured output, exactly one current account, and exact typed matches for
global identity, account and verified email against the frozen binding.
Missing fields, multiple current accounts, parse errors or mismatches stop reads
and writes. Do not repair or rebind the account to make a task succeed.

Resolve one destination from the request, trusted repository mapping or live
active projects in the verified account. Ambiguity stops the operation.
An approved mapping selects a destination, not an actor or new write authority.
After creation, verify `creator.id` against the account-scoped `person_id`,
never the global `identity.id`. Preserve redacted evidence and actual Codex
authorship independently of the service's authenticated actor.
