# hardware-main Host

This directory contains the NixOS host definition for the physical LOOM
production main-node machine.

## Intended Lifecycle

```text
loom-dev       UTM development main VM and fallback
loom-main      physical production main node
```

Production WireGuard address:

```text
10.44.0.2
```

Historical staging WireGuard address:

```text
10.44.0.4
```

## Production Shape

The active production host uses:

```text
configuration.nix
hardware-configuration.nix
wireguard-main-production.nix
```

The flake output is:

```text
.#hardware-main
```

Normal rebuild from the machine:

```bash
cd /srv/loom/current
sudo nixos-rebuild switch --flake .#hardware-main --show-trace
```

## Historical Bring-Up Notes

The original bring-up used `loom-hardware` and `10.44.0.4` as staging labels.
Those labels are historical after production cutover. New operator and agent
commands should use `loom-main` and `10.44.0.2`.

`wireguard-main-staging.nix` is retained only as historical/fallback material.
Do not rebuild production from it unless the operator explicitly chooses a
rollback-to-staging network path.

## Remote Desktop

The profile enables the GNOME remote-desktop backend, but GNOME Remote Login or
Desktop Sharing still needs credentials/user setup on the installed machine.

Do not expose RDP publicly. The production WireGuard profile opens TCP `3389`
only on `wg0`.

XRDP is declared as a disabled fallback in the profile.
