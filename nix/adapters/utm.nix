{ lib, ... }:

{
  # UTM is a host adapter. Keep VM-specific support here, not in LOOM modules.
  services.qemuGuest.enable = lib.mkDefault true;

  boot.initrd.availableKernelModules = lib.mkDefault [
    "xhci_pci"
    "virtio_pci"
    "virtio_scsi"
    "virtio_blk"
    "virtio_net"
    "virtio_console"
  ];

  networking.useDHCP = lib.mkDefault true;
}
