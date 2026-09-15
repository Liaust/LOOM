---
title: "Installation"
description: "Understand the manual developer-preview installation path and the configuration a new operator must supply."
audience: [operator, developer]
tags: [loom, installation, nix, configuration]
status: draft
verified_at: "2026-09-15"
source_scope: ["go.mod", "flake.nix", "nix/README.md", "internal/loomcli/setup.go", "internal/loomdapp/root.go"]
related: ["[[Getting Started]]", "[[Configuration]]", "[[Local Development]]", "[[Updating And Rebuilding]]", "[[Main And Mac Operations]]"]
---

# Installation

## Developer Preview, Not A One-Command Appliance

The public source can be built and inspected without access to the maintainer's
machines. A functioning deployment still needs an operator to configure storage,
PostgreSQL, identity, network, credentials and services. Guided CLI installation
is planned over the existing setup primitives; it is not available as a complete
download-and-configure wizard today.

The reference service host is x86_64 Linux with NixOS. macOS is used as a
development/workspace client. The flake exposes additional package/development
systems, but that does not establish full runtime support on every platform.
Several optional binary packages are x86_64-linux-only.

## Inspect Or Build The CLI

Use Go matching `go.mod` (currently 1.25.7 or newer):

```sh
go run ./cmd/loom --help
go run ./cmd/loom docs --docs-dir docs search "configuration"
go build -o ./bin/loom ./cmd/loom
```

These commands do not provision services. `go run` can download Go dependencies
and the selected Go toolchain. With Nix installed, `nix develop` supplies the
development shell; it may download its pinned inputs and packages. You do not
need a VM, remote build executor, or a replica installation just to read or
change the source.

## Configure Your Host

Read [nix/README.md](../../nix/README.md). The checked-in `hardware-main` is a
fully enabled **evaluation example**, not a safe configuration to apply unchanged.
Its public account IDs, disk labels, keys and addresses are synthetic. Keep your
actual machine-specific configuration in an operator-owned module outside this
public source.

Before activation, supply and review:

1. The actual hardware configuration and mounts. Box/archive operations that
   require atomic moves need the same filesystem. Do not repurpose a disk or
   existing directory merely because an example uses that path.
2. Node identity, service users, database configuration and private API access.
3. Private network peers and client pairing. Public ingress is separate and
   should remain off until its domain, TLS and routing are configured.
4. Credential references and protected bootstrap files for selected integrations.
   OAuth tokens and private keys must not enter the Nix store or Git.
5. The required source roots, workers and recovery policy. A mounted folder is
   not automatically indexed, synchronized or backed up.
6. Optional agent runtime, model/gateway authentication and Mac pairing. None is
   necessary for source inspection or the core CLI.

The `loom setup` command family provides existing plan/apply/status/doctor and
workspace setup surfaces. Inspect release-matched help and the printed plan;
these are not a substitute for supplying the operator's real machine inputs.

## Verify The Installed Context

On the machine or configured client, inspect:

```sh
loom version
loom status
loom health
```

Confirm the node, selected backend, migrations, paths and required services.
Then test one small actual workflow on data you own. Check source enrollment,
processing and recovery independently; a green daemon does not prove every
optional worker or application is configured.

Use the existing update/rollback path described in [[Updating And Rebuilding]].
Preserve relevant recovery before destructive changes, but do not create a full
backup transfer or restore drill for every ordinary patch.

## What Is Still Missing

There is no general fresh-machine acceptance claim, automatic hardware wizard,
cross-platform service installer, or supported prebuilt release bundle yet.
Installation gaps are useful bug reports: include the intended topology and
bounded error, without private configuration or credentials.
