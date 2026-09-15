{ config, lib, ... }:

# Fully enabled reference configuration used by the Nix contract tests.
# All identities, keys, addresses and disk labels below are examples. Do not
# activate this configuration on an existing machine. See nix/README.md.
{
  imports = [
    ./hardware-configuration.nix
    ../../profiles/main-hardware-desktop.nix
    ../../modules/loom-project-applications.nix
    ./wireguard-main-production.nix
  ];

  networking.hostName = "loom-main";

  # Operator-provisioned key bytes stay outside Nix and release sources.
  loom.workspaceArchive = {
    enable = true;
    manifestKeySource = "/var/lib/loom-workspace-archive-secrets/manifest-key";
  };

  # Node-wide allocation policy; project apply derives grants without per-app
  # host edits. No project application is activated by this configuration.
  loom.projectApplications = {
    enable = true;
    nodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAV";
    installerUID = 990; # Set to the target loom-node-agent account's UID.
    installerGroup = "loom";
    publisherUID = 0;
    grants = [];
    trustedParentOwners."/srv/loom" = 1000; # Match the operator-owned mount root.
    edge = {
      enable = true;
      publicSuffix = "apps.example.com";
      ingressIPv4 = "192.0.2.1";
    };
    provisioning = {
      enable = true;
      dataRoot = "/srv/loom/application-data";
      maxMemoryBytes = 2147483648;
      maxPlannedBytes = 107374182400;
      cloudBackup = true;
      protonShareIDs = [];
      protonAllowGeneration = false;
    };
  };

  # Keep Box and archive custody on one filesystem for atomic workspace moves.
  fileSystems."/srv/loom" = {
    device = "/dev/disk/by-label/loom-data";
    fsType = "ext4";
    neededForBoot = true;
    options = [ "defaults" ];
  };

  # Mount failure must not expose the retained original tree to service writers.
  systemd.services = lib.genAttrs ([
    "loomd" "loom-node-agent" "loom-box-layout-preflight"
    "loom-project-applications-restore"
    "orca-serve"
    "loom-mini-dashboard" "samba-smbd" "samba-nmbd" "samba-winbindd"
    "systemd-tmpfiles-setup" "systemd-tmpfiles-resetup"
  ] ++ lib.optional config.loom.mina.enable "loom-mina"
    ++ lib.optional config.loom.mina.recoveryEnabled "loom-mina-recovery-publish") (_: {
    requires = lib.mkAfter [ "srv-loom.mount" ];
    after = lib.mkAfter [ "srv-loom.mount" ];
    bindsTo = [ "srv-loom.mount" ];
  });

  # Example runtime options; authentication and recovery keys are not included.
  loom.mina = {
    enable = true;
    externalAccountsEnabled = true;
    externalAccountBinding = {
      githubLogin = "example-operator";
      githubID = 100001;
      basecampIdentityID = 200001;
      basecampAccountID = 300001;
      basecampPersonID = 400001;
      basecampEmail = "agent@example.test";
    };
    browserEnabled = true;
    macComputerUseEnabled = true;
    macComputerUseBindingSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    recoveryEnabled = true;
    recoveryPublicKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    retainedMorathustra = [];
    model = "gpt-6-astra";
    modelProvider = "openai-codex";
  };

  loom.miniDashboard = {
    enable = true;
    kiosk.enable = true;
  };

  boot.loader.systemd-boot.enable = lib.mkDefault true;
  boot.loader.efi.canTouchEfiVariables = lib.mkDefault true;

  users.users.loomadmin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = [];
  };

  security.sudo.wheelNeedsPassword = lib.mkForce true;

  system.stateVersion = "25.11";
}
