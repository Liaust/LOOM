{ lib, pkgs, ... }:

{
  imports = [
    ./base-server.nix
    ../modules/loom-base.nix
    ../modules/loom-storage.nix
    ../modules/loom-postgres.nix
    ../modules/loom-embeddings.nix
    ../modules/loom-notes-ai.nix
    ../modules/loom-service.nix
    ../modules/loom-restore-authority.nix
    ../modules/loom-caddy.nix
    ../modules/loom-smb.nix
  ];

  loom = {
    enable = true;
    environment = "dev";
    nodeKind = "main";
    nodeRole = "main";
    runtimeClass = "main_full";
    boxPath = "/home/loomadmin/loom-box";
    boxProfile = "main";
    boxOwner = "loomadmin";
    boxGroup = "users";
    manageBoxPaths = true;
    autoMigrate = true;
    bootstrapDev = true;
    bootstrapMode = "dev";
    cloud = {
      enable = true;
      provider = "hetzner_storage_box";
      driver = "rclone";
      remoteName = "loom-cloud";
      remoteRoot = "loom";
      installRclone = true;
      installBorg = true;
      snapshotBackend = "legacy_tree";
      borgPassphraseFile = "/etc/loom/cloud/borg.passphrase";
      borgCacheDir = "/var/lib/loom/cloud/borg/cache";
      borgSecurityDir = "/var/lib/loom/cloud/borg/security";
    };
    storage = {
      enable = true;
      deployUser = "loomadmin";
      deployGroup = "users";
      humanStorageHomes = [
        "/home/loomadmin"
      ];
    };
    postgres.enable = true;
    service.enable = true;
    restoreAuthority.enable = true;
    smb = {
      enable = true;
      shareName = "loom-storage";
      user = "loomshare";
      interfaces = [ "enp0s1" ];
      allowedHosts = [ "192.168.64." ];
    };
  };

  users.users.loomadmin.extraGroups = [
    "loom"
  ];

  environment.systemPackages = with pkgs; [
    poppler-utils
    tree
  ];

  services.openssh.openFirewall = lib.mkDefault true;
}
