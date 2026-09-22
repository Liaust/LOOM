# Nix Configuration

The flake packages LOOM and optional dependencies and exposes NixOS modules and
reference hosts. It is not an installer that can discover a safe disk layout or
authenticate external services for you.

`hosts/hardware-main` is a fully enabled **evaluation example** retained for
configuration contract tests. Its disk labels, node/account IDs, public keys,
hashes and documentation IP/domain are synthetic. It deliberately has no admin
SSH key, real credentials, or usable Mac pairing. Do not rebuild an existing
machine using it unchanged. `dev-utm` is a historical development example, not
a required runtime, test executor, or supported installation path.

For an installation, start from the modules needed by your machine and the
output of `nixos-generate-config`. Keep your machine-specific module outside
this public source and import it from an operator-owned flake/configuration.
Review mounts and service access before enabling writers. Box and archive
payloads must remain on the same filesystem where atomic moves require it.

Configure node identity, addresses, WireGuard peers, account ownership, ingress,
credential sources and recovery keys for your own deployment. Example
`192.0.2.1` and `apps.example.com` values are not working endpoints. External
agent features are optional and their module options default off; the example
host enables them to exercise configuration, not to establish readiness.

## Named External Accounts

Enabling `loom.mina.externalAccountsEnabled` also requires
`loom.mina.externalAccountBinding`. It contains non-secret, independently
verified `githubLogin`, `githubID`, `basecampIdentityID`, `basecampAccountID`,
`basecampPersonID`, and `basecampEmail` fields. The account wrapper's Basecamp
profile is fixed to `mina`; the legacy runtime uses `morathustra`. Neither
profile inherits the maintainer's accounts. OAuth tokens remain in protected
operator-provisioned auth storage, never in Nix values or this repository.

The checked-in host uses unmistakably synthetic account values for evaluation.
Copying them cannot authenticate an account. Obtain and verify your real public
identifiers during your own authorized bootstrap. The agent protocols describe
the distinction between named-agent and general coding-agent profiles.

## Agent SSH Hosts

When ORCA's shared agent account is enabled, the system SSH client includes the
optional operator-owned `/etc/loom-agent-ssh/config`. An absent file enables no
remote access. Configure ordinary OpenSSH host blocks there; keep the file and
its parent root-owned and not writable by agents. End the included file with
`Host *` so its last host block does not scope later system defaults.

Provision dedicated private keys outside both Git/Nix values and `/home`, for
example in `/var/lib/loom-agent-ssh` with directory mode 0700 and key mode 0600,
owned by the intended agent account. This also works for the existing
`ProtectHome` gateway without weakening its filesystem restrictions. Pin the
remote host key in a protected known-hosts file; use `IdentitiesOnly yes`,
`StrictHostKeyChecking yes`, `BatchMode yes` and bounded connection timeouts.
The normal SSH client, not an extra LOOM wrapper, performs the connection.

The pinned ORCA FHS environment exposes the host's `/etc` at `/.host-etc` and
does not expose `/etc/ssh`. For these terminals, also install the selected host
block as the agent's ordinary `~/.ssh/config` (agent-owned, mode 0600); preserve
unrelated existing entries. List both the normal and `/.host-etc` known-hosts
paths in `UserKnownHostsFile`. The system include serves the home-isolated
gateway, while the user config serves ORCA. Keep their host/key values in sync.

For a configured `loom-mac` alias, validate from an actual ORCA terminal and the
gateway environment. Account access is broad user-level access, not per-agent
isolation. Do not reuse a computer-use forced-command key, grant sudo, expose
SSH publicly or change remote privacy permissions as part of host discovery.
Revocation is the ordinary removal of this dedicated public key on the target.

## Dependency Ownership

`flake.lock` pins the input sources. Package definitions pin separately fetched
binaries. Update the appropriate pin and inspect local compatibility patches;
do not run an upstream self-updater against a Nix-owned package. Model APIs,
messaging, accounts and platform permissions are configured separately from
installing a binary. See ../THIRD_PARTY_NOTICES.md and the installation guide.
