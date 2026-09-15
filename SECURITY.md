# Security

LOOM is an experimental self-hosted system with filesystem, credential,
database, deployment, and optional computer-control authority. Use it only on
machines you administer and with backups appropriate to your data. A successful
unit test or an old backup receipt is not proof of a safe current deployment.

Only the current public preview is actively maintained. No supported LTS branch,
security certification, or response-time guarantee is offered.

## Reporting A Vulnerability

Use GitHub's **Security > Report a vulnerability** for this repository when
private vulnerability reporting is enabled. If unavailable, contact the
maintainer through the contact route on [liaust.com](https://liaust.com/) and
request a private reporting channel. Do not put an exploit, credential, personal
data, or private infrastructure details in a public issue.

Include affected versions, impact, reproduction steps, and a minimal example
using data and machines you control. Do not test against the maintainer's
deployment or third-party accounts without explicit permission.

## Deployment Boundaries

- Example Nix hosts are reference configurations, not the maintainer's live
  machines. Supply your own disk layout, public identities, network bindings,
  credential references, and recovery keys.
- Keep tokens, password values, SSH private keys, database dumps, agent session
  stores, and recovery packages outside Git and issue attachments.
- Pair Mac computer use explicitly. Shell access, a shared browser, and desktop
  input are different permissions; one does not imply the others.
- A dependency's native automation or memory is not governed by LOOM simply
  because LOOM starts its process. Configure the dependency's own boundaries too.
- Review update/migration plans and preserve rollback information before changes
  that affect existing data. Prefer the supported runtime interfaces over
  editing database rows or retained archive contents.
