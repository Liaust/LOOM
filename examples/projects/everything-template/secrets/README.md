# Secret References

This folder is for documenting secret requirements and credential references.

Do not store secret values in this project. LOOM project files may name a
credential, describe what it is used for, and explain how to create it outside
the project tree. The actual secret value must live in LOOM credentials or an
external secret store.

## Allowed Here

- credential reference names
- auth profile names
- setup instructions
- rotation and revocation notes
- fake placeholder examples
- mappings from connectors/events/scripts to required credentials

## Never Store Here

- API keys
- OAuth client secrets or refresh tokens
- passwords
- private keys
- webhook signing secrets
- session cookies
- database URLs with embedded credentials
- copied `.env` files
- real sensitive payloads

## Credential Policy

The project credential policy lives at:

```text
.loom/contracts/credentials.yaml
```

It should keep:

```yaml
inline_secrets_allowed: false
```

If a connector or direct event needs a token, create or reference a LOOM
credential/auth profile outside this project and document only the reference
name here.

## Suggested Documentation Shape

```text
secrets/
  README.md
  required_credentials.md
```

A credential note should include:

- reference name
- purpose
- required permissions
- used-by list
- rotation expectations
- setup command or manual setup steps that do not reveal the value

## Safety Check

Before finishing changes that touch credentials, run a search and inspect matches
manually:

```sh
rg -n "token|secret|password|api[_-]?key|private[_-]?key|BEGIN " .
```

Documentation may contain these words, but it must not contain real values.
