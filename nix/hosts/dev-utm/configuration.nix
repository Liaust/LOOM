{ lib, ... }:

{
  imports = [
    ./hardware-configuration.nix
    ../../profiles/dev.nix
    ../../profiles/wireguard-vps-main.nix
    ../../adapters/utm.nix
  ];

  networking.hostName = "loom-dev";

  boot.loader.systemd-boot.enable = lib.mkDefault true;
  boot.loader.efi.canTouchEfiVariables = lib.mkDefault true;
  boot.kernelParams = lib.mkDefault [
    "console=ttyAMA0,115200n8"
    "console=tty0"
  ];

  users.users.loomadmin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = [
    ];
  };

  security.sudo.wheelNeedsPassword = lib.mkForce false;

  system.stateVersion = "25.11";
}
