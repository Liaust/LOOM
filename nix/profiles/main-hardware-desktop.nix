{ config, lib, pkgs, ... }:

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
    ../modules/loom-mini-dashboard.nix
    ../modules/loom-orca.nix
    ../modules/loom-caddy.nix
    ../modules/loom-smb.nix
  ];

  loom = {
    enable = true;
    environment = "production";
    nodeId = "main";
    nodeKind = "main";
    nodeRole = "main";
    runtimeClass = "main_full";
    serviceRoot = "/srv/loom";
    canonicalBoxPath = "/srv/loom/box";
    canonicalStorageRoot = "/srv/loom/storage";
    canonicalImportsRoot = "/srv/loom/storage/imports";
    importsBackupPolicy = "legacy_complete_physical_custody";
    canonicalUserBackupsRoot = "/srv/loom/storage/backups";
    canonicalArchiveRoot = "/srv/loom/storage/archive";
    canonicalGeneratedRoot = "/var/lib/loom/generated";
    canonicalBoxStateRoot = "/var/lib/loom/box-state";
    canonicalNotesProjectionRoot = "/var/lib/loom/generated/notes";
    filesystemCutoverEnabled = true;
    # Slice 10 activates the canonical roots only after every writer and Samba
    # stop and the reviewed same-filesystem moves complete.
    boxPath = if config.loom.filesystemCutoverEnabled then config.loom.canonicalBoxPath else "/home/loomadmin/loom-box";
    boxStateRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalBoxStateRoot else "/home/loomadmin/loom-box/.loom/state";
    storageRoot = config.loom.canonicalStorageRoot;
    importsRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalImportsRoot else "/var/lib/loom/lane/accepted";
    userBackupsRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalUserBackupsRoot else "/var/lib/loom/private-backups";
    archiveRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalArchiveRoot else "/var/lib/loom/storage-archive";
    generatedRoot = config.loom.canonicalGeneratedRoot;
    notesProjectionRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalNotesProjectionRoot else "/var/lib/loom/loom-notes";
    boxProfile = "main";
    boxOwner = "loomadmin";
    boxGroup = "users";
    manageBoxPaths = true;
    autoMigrate = true;
    bootstrapDev = false;
    bootstrapMode = "production";
    cloud = {
      enable = true;
      provider = "hetzner_storage_box";
      driver = "rclone";
      remoteName = "loom-cloud";
      remoteRoot = "loom";
      installRclone = true;
      installBorg = true;
      snapshotBackend = "borg";
      borgPassphraseFile = "/etc/loom/cloud/borg.passphrase";
      borgCacheDir = "/var/lib/loom/cloud/borg/cache";
      borgSecurityDir = "/var/lib/loom/cloud/borg/security";
    };
    storage = {
      enable = true;
      deployUser = "loomadmin";
      deployGroup = "users";
    };
    postgres = {
      enable = true;
      provenance = {
        database = "loom_provenance";
        user = "loom_provenance";
      };
    };
    service.enable = true;
    restoreAuthority.enable = true;
    nodeAgent.enable = true;
    orca.enable = true;
    smb = {
      enable = true;
      boxShareName = "loom-main-box";
      boxPath = config.loom.boxPath;
      storageShareName = "loom-storage";
      storagePath = if config.loom.filesystemCutoverEnabled then config.loom.canonicalStorageRoot else config.loom.storageExportRoot;
      user = "loomshare";
      interfaces = [ "wg0" ];
      bindInterfaces = [ "10.44.0.2/24" ];
      allowedHosts = [ "10.44.0." ];
      afterServices = [ "wireguard-wg0.service" ];
    };
  };

  networking.networkmanager.enable = lib.mkDefault true;

  services.displayManager.gdm.enable = lib.mkDefault true;
  services.desktopManager.gnome.enable = lib.mkDefault true;

  # Enables the GNOME remote-desktop backend. Remote Login/Desktop still needs
  # credentials and user-level setup on the installed machine.
  services.gnome.gnome-remote-desktop.enable = lib.mkDefault true;
  systemd.services.gnome-remote-desktop.wantedBy = lib.mkDefault [
    "graphical.target"
  ];
  services.displayManager.autoLogin.enable = lib.mkDefault false;
  services.getty.autologinUser = lib.mkDefault null;

  # XRDP is kept as a declared fallback option, not the default path.
  services.xrdp = {
    enable = lib.mkDefault false;
    openFirewall = lib.mkDefault false;
  };

  systemd.targets.sleep.enable = lib.mkDefault false;
  systemd.targets.suspend.enable = lib.mkDefault false;
  systemd.targets.hibernate.enable = lib.mkDefault false;
  systemd.targets.hybrid-sleep.enable = lib.mkDefault false;
  services.logind.settings.Login = {
    IdleAction = lib.mkDefault "ignore";
    HandleLidSwitch = lib.mkDefault "ignore";
    HandleLidSwitchDocked = lib.mkDefault "ignore";
    HandleLidSwitchExternalPower = lib.mkDefault "ignore";
    HandleSuspendKey = lib.mkDefault "ignore";
    HandleHibernateKey = lib.mkDefault "ignore";
  };

  # GNOME Remote Login creates short-lived headless GDM greeter sessions before
  # handing the RDP client to the authenticated user session. Current NixOS/GDM
  # generations preallocate only a small greeter range; repeated failed or
  # disconnected RDP attempts can exhaust it and produce a black-screen login.
  users.users = {
    loomadmin.extraGroups = [
      "loom"
      "networkmanager"
    ];
    loomdesk = {
      isNormalUser = true;
      description = "LOOM graphical desktop user";
      extraGroups = [
        "audio"
        "loom"
        "networkmanager"
        "video"
      ];
    };
  } // lib.listToAttrs (
    map (n: {
      name = "gdm-greeter-${toString n}";
      value = {
        isSystemUser = lib.mkDefault true;
        uid = lib.mkDefault (60578 + n);
        group = lib.mkDefault "gdm";
        home = lib.mkDefault "/run/gdm-${toString n}";
      };
    }) (lib.range 5 16)
  );

  environment.systemPackages = with pkgs; [
    lsof
    nodejs_22
    openssl
    pciutils
    tree
    wireguard-tools
  ];

  services.openssh.openFirewall = lib.mkDefault true;

  # RDP must be private-network only. The host WireGuard template opens 3389 on
  # wg0 when remote desktop is intentionally enabled.
  networking.firewall.allowedTCPPorts = lib.mkDefault [ ];
}
