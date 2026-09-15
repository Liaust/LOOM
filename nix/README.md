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

## Dependency Ownership

`flake.lock` pins the input sources. Package definitions pin separately fetched
binaries. Update the appropriate pin and inspect local compatibility patches;
do not run an upstream self-updater against a Nix-owned package. Model APIs,
messaging, accounts and platform permissions are configured separately from
installing a binary. See ../THIRD_PARTY_NOTICES.md and the installation guide.
