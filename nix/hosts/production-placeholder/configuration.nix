{ lib, ... }:

{
  imports = [
    ../../profiles/production-placeholder.nix
  ];

  networking.hostName = "loom-main-placeholder";

  # Placeholder filesystem so the profile can evaluate before real hardware exists.
  # Replace with generated hardware config for the eventual main-node machine.
  boot.loader.systemd-boot.enable = lib.mkDefault true;
  boot.loader.efi.canTouchEfiVariables = lib.mkDefault true;
  fileSystems."/" = {
    device = lib.mkDefault "/dev/disk/by-label/nixos";
    fsType = lib.mkDefault "ext4";
  };

  system.stateVersion = "25.11";
}
