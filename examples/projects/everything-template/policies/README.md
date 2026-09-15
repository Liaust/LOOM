# Policies

Policies describe how LOOM should treat this project in the background. Their
canonical files are under `.loom/contracts/`; this preserved guide remains in
the visible project tree for readers of the example.

They are declarative. Editing a policy file does not start a watcher, sync job,
backup job, worker, or credential flow by itself. LOOM validates policy files,
derives desired state, registers that desired state, and applies it only through
explicit lifecycle commands.

## Files

- `.loom/contracts/sync.yaml`: which project files should become synced/indexed object content.
- `.loom/contracts/backup.yaml`: which project files should be preserved as raw project
  material.
- `.loom/contracts/workers.yaml`: whether this project wants background workers.
- `.loom/contracts/credentials.yaml`: credential-reference rules.

## Sync Versus Backup

Sync is for curated knowledge that should become searchable/queryable object
content. In this template, sync focuses on markdown notes:

```text
notes/**/*.md
notes/**/*.markdown
```

Backup is for preserving project files. In this template, backup covers the
project root with explicit includes and excludes.

Do not broaden sync or backup casually. Full repositories, generated folders,
binary datasets, dependency trees, and secret folders can be expensive or unsafe
to track.

## Watched-Root Lifecycle

Inspect derived watched-root intent:

```sh
loom project watch-plan .
```

Register the project:

```sh
loom project register .
```

Ask the owner node to apply watched-root desired state:

```sh
loom project apply-watch-policy everything-template --project-root .
```

Then inspect status:

```sh
loom project sync-status everything-template
loom project backup-status everything-template
```

## Credential Policy

`.loom/contracts/credentials.yaml` should keep:

```yaml
inline_secrets_allowed: false
```

Store credential reference names only. Do not put tokens, passwords, webhook
secrets, private keys, database URLs with credentials, or copied `.env` files in
the project tree.

## Worker Policy

`.loom/contracts/workers.yaml` declares intent, not a running process. Keep workers disabled
until the project has a clear background task and the owner node should run it.

## Safety Rules

- Keep policies conservative by default.
- Prefer explicit includes and excludes over broad whole-tree rules.
- Never include `secrets/` in backup or sync.
- Do not assume validation or registration starts watchers.
- Review `loom project watch-plan .` before applying policy.
