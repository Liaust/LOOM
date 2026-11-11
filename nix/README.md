# Nix Configuration

The flake packages LOOM and optional dependencies and exposes reusable NixOS
modules. It does not discover your disks, authenticate accounts or safely adopt
an existing host by itself. Keep real hardware, addresses, keys, accounts and
service policy in an operator-owned configuration outside this public source.

The checked-in `hosts/hardware-main` is a synthetic evaluation example. It has
no usable administrator SSH key or real pairing credentials. Never activate it
unchanged. The historical `dev-utm` example is not a required test executor or
installation platform. Start with your own `nixos-generate-config` output and
[the installation guide](../docs/operations/installation.md).

## Owners And Optional Modules

- Core modules provide the daemon, PostgreSQL, node agent, Box/storage and
  private service interfaces.
- The shared `loom.agents` account is independent of the retired ORCA client.
  Optional native Hermes and Codex runtime modules use that existing account.
- Native Hermes owns its conversations, memory, tools and agent schedules.
  LOOM supplies native schedule observation/declared jobs and system operations,
  not a duplicate timer for the same work.
- Box, archive and application-data roots remain real configured paths. Mount
  dependencies must prevent writers from using an unmounted underlying tree.
  Rebuilds do not move payloads or migrate catalog identity automatically.

Installing optional packages does not authenticate model providers, create
business-service grants, pair a Mac or prove recovery. Default-off options and
missing-binding refusals are intentional.

## External Accounts

Named tooling requires an explicit operator-verified `externalAccountBinding`.
It identifies the expected GitHub login/ID and Basecamp identity/account/person/
email without storing OAuth tokens in Nix values. Enabled account tooling with
no binding must refuse rather than inherit the maintainer's account or another
native profile. The synthetic example cannot authenticate anything.

Company and Personal tool contexts select approved stores independently of
agent personality. Source policy examples are unconfigured guidance, not a
standing grant to send, publish or capture tasks. Shared Unix users and Box
policies are soft workflow separation, not OS-level isolation.

## Private Connections And Updates

Supply ordinary SSH host entries and protected keys outside Git. The optional
operator `/etc/loom-agent-ssh/config` grants nothing when absent. Pin host keys
and keep key/private parent modes restrictive. A computer-use forced-command
key is not a general shell key. Private connectivity does not authorize a new
public listener or broader device permissions.

The Electron client uses the selected private SSH alias and Unix-socket API;
its own build is under `modules/workspace-electron`. Development builds do not
receive native signed updates. Operators must configure a signed release
repository explicitly; this source contains no maintainer update credential.

## Dependency Pins And Recovery

`flake.lock` pins inputs; package definitions pin fetched binaries and upstream
patches. Native runtime and legacy recovery/tool closures may be separate.
Use the owner update path rather than self-updating a Nix-owned immutable binary.
Inspect relevant third-party licenses in [the notices](../THIRD_PARTY_NOTICES.md).

Account stores, learned profiles, service state and selected user roots need
their actual recovery owners. A successful package build or Nix evaluation is
not service, device, application-consistency or cloud-protection acceptance.
