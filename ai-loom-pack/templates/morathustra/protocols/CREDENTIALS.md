# Credentials

Use the owning project's approved non-secret reference contract and the
installed, release-matched credential workflow for the exact authorized task.
Values belong in the approved external store or owner-protected runtime state,
never in workspace instructions, configuration templates, Git, prompts, logs,
Notes, Provenance, Basecamp, handoffs or Hermes memory.

Confirm the account, actor, scope and intended effect before using a credential.
Inject values through the supported protected runtime surface without printing
or copying them. Missing access stops that operation; never fall back to another
identity, widen scope or start an OAuth flow silently.

Provider authorization, messaging bot setup, credential creation/rotation and
Proton account operations require their own explicit operator authorization.
Repository scaffolding neither performs these operations nor supplies runtime
credentials. Record only redacted outcomes and approved source pointers.
