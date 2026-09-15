# dev-utm Host

This host is the first development target for LOOM: a NixOS VM running in UTM.

The host adapter is intentionally limited to UTM-specific VM support. LOOM runtime behavior belongs in reusable modules and profiles.

## SSH Contract

All development scripts should target the SSH alias:

```text
loom-dev
```

Example macOS SSH config:

```text
Host loom-dev
    HostName <vm-ip-or-nat-host>
    Port <ssh-port-if-nat-forwarded>
    User <admin-user>
    IdentityFile ~/.ssh/<key>
```

Pending VM-specific values:

- `HostName`: `192.168.64.2`
- `Port`: default `22`
- `User`: `loomadmin`
- `IdentityFile`: `~/.ssh/loom_dev`
- network mode: UTM Shared Network, after bridged networking did not receive DHCP

The alias should hide whether the VM is reached through bridged networking, UTM NAT forwarding, or a later WireGuard address.
