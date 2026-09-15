{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.projectApplications;
  stateRoot = "/var/lib/loom-project-applications";
  edgeRoot = "/var/lib/loom-project-applications-edge";
  credentialRoot = "/var/lib/loom-project-application-credentials";
  protonEnabled = cfg.provisioning.enable && cfg.provisioning.protonShareIDs != [];
  backupEnabled = cfg.provisioning.enable && cfg.provisioning.cloudBackup;
  policy = {
    schema_version = "application.policy.v1";
    node_id = cfg.nodeID;
    platform = pkgs.stdenv.hostPlatform.system;
    installer_uid = cfg.installerUID;
    publisher_uid = cfg.publisherUID;
    grants = cfg.grants;
  } // lib.optionalAttrs cfg.provisioning.enable {
    provisioning = {
      data_root = cfg.provisioning.dataRoot;
      max_memory_bytes = cfg.provisioning.maxMemoryBytes;
      max_planned_bytes = cfg.provisioning.maxPlannedBytes;
    } // lib.optionalAttrs (cfg.edge.enable && cfg.edge.publicSuffix != "") {
      public_endpoints = { suffix = cfg.edge.publicSuffix; ingress_ipv4 = cfg.edge.ingressIPv4; };
    } // lib.optionalAttrs backupEnabled {
      cloud_backup = true;
    } // lib.optionalAttrs protonEnabled {
      proton = { share_ids = cfg.provisioning.protonShareIDs; credential_root = credentialRoot; }
        // lib.optionalAttrs cfg.provisioning.protonAllowGeneration { allow_generation = true; };
    };
  };
  helperConfig = {
    trusted_parent_owners = cfg.trustedParentOwners;
    policy_path = "/etc/loom/project-applications.json";
    state_root = stateRoot;
    config_root = "/var/lib/loom-project-application-configs";
    unit_root = "/run/systemd/system";
    gc_root = "/nix/var/nix/gcroots/loom-project-applications";
    systemctl = "${pkgs.systemd}/bin/systemctl";
    sysusers = "${pkgs.systemd}/bin/systemd-sysusers";
    nix = "${pkgs.nix}/bin/nix";
    edge_root = if cfg.edge.enable then edgeRoot else "";
    caddy = if cfg.edge.enable then "${pkgs.caddy}/bin/caddy" else "";
    caddy_config = if cfg.edge.enable then cfg.edge.composedConfig else "";
    pass_cli = if protonEnabled then "${config.loom.orca.protonPassPackage}/bin/pass-cli" else "";
    pass_session_ensure = if protonEnabled then "/run/current-system/sw/bin/pass-session-ensure" else "";
  };
  dataPaths = lib.unique (lib.concatMap (g: map (d: d.pool) (builtins.attrValues (g.data or {}))) cfg.grants
    ++ lib.optional cfg.provisioning.enable cfg.provisioning.dataRoot);
  helperSandbox = {
        User = "root";
        Group = "root";
        StandardError = "journal";
        TimeoutStopSec = 5;
        KillMode = "control-group";
        NoNewPrivileges = true;
        # Retain only the identity-switching capabilities needed to launch the
        # Proton child as agents under the helper's inherited seccomp filters.
        AmbientCapabilities = lib.optionals protonEnabled [ "CAP_SETUID" "CAP_SETGID" ];
        RestrictSUIDSGID = true;
        PrivateTmp = true;
        # An inaccessible /home also blocks traversal to the explicit binds.
        # A blank read-only home tree exposes only the three Proton directories.
        ProtectHome = if protonEnabled then "tmpfs" else "yes";
        ProtectSystem = "strict";
        # sysusers publishes account DB files atomically in /etc. The helper
        # accepts only derived identities and fixed sysusers input.
        ReadWritePaths = [ stateRoot "/var/lib/loom-project-application-configs" "/run/systemd/system" "/run" "/etc" "/nix/var/nix/gcroots/loom-project-applications" ] ++ dataPaths ++ lib.optional cfg.edge.enable edgeRoot ++ lib.optional protonEnabled credentialRoot;
        # The child runs as agents, reusing only its existing Proton session.
        BindPaths = lib.optionals protonEnabled [ "/home/agents/.config/proton-pass-cli" "/home/agents/.local/state/proton-pass-cli" "/home/agents/.local/share/proton-pass-cli" ];
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        UMask = "0077";
      };
in {
  options.loom.projectApplications = {
    enable = lib.mkEnableOption "typed owner-node application installation";
    package = lib.mkOption { type = lib.types.package; default = self.packages.${pkgs.stdenv.hostPlatform.system}.loom-service-manager; };
    nodeID = lib.mkOption { type = lib.types.str; description = "Exact registered node ID, not its display key."; };
    installerUID = lib.mkOption { type = lib.types.ints.positive; description = "Exact unprivileged node-agent UID."; };
    installerGroup = lib.mkOption { type = lib.types.str; description = "Group allowed to connect to the root helper socket."; };
    publisherUID = lib.mkOption { type = lib.types.ints.unsigned; default = 0; description = "Independent reviewed artifact producer UID; installation UID cannot publish."; };
    grants = lib.mkOption { type = lib.types.listOf (lib.types.attrsOf lib.types.anything); default = []; description = "Root-owned typed project/node/resource trust, data and credential policy. Validated fail-closed by the helper; no credential values."; };
    trustedParentOwners = lib.mkOption { type = lib.types.attrsOf lib.types.ints.unsigned; default = {}; description = "Exact host-managed ancestor paths and operator UIDs. Other ancestors and allocation roots retain root custody; symlinks and group/world writes remain rejected."; };
    provisioning.enable = lib.mkEnableOption "node-policy-managed application grants";
    provisioning.cloudBackup = lib.mkOption { type = lib.types.bool; default = false; description = "Include the entire allocation pool in the existing cloud-history worker. Explicitly grants loomd daemon-wide CAP_DAC_READ_SEARCH so private application files retain their modes; not path-scoped, no write bypass, no new schedule, and not application-consistent database recovery."; };
    provisioning.dataRoot = lib.mkOption { type = lib.types.str; default = ""; description = "Dedicated persistent application allocation root; never an agent-supplied path."; };
    provisioning.maxMemoryBytes = lib.mkOption { type = lib.types.ints.unsigned; default = 0; description = "Per-application memory ceiling; required when provisioning is enabled."; };
    provisioning.maxPlannedBytes = lib.mkOption { type = lib.types.ints.unsigned; default = 0; description = "Per-application aggregate planned data ceiling, not a quota or reservation."; };
    provisioning.protonShareIDs = lib.mkOption { type = lib.types.listOf lib.types.str; default = []; description = "Approved Proton Share IDs usable by managed applications. Empty disables materialization. Does not create credentials or expand the agents token's vault access."; };
    provisioning.protonAllowGeneration = lib.mkOption { type = lib.types.bool; default = false; description = "Permit explicit password-generation declarations in approved shares; Proton must independently grant Editor/Manager/Admin/Owner access. Does not change vault permissions or tokens."; };
    edge.enable = lib.mkEnableOption "separate approved application Caddy fragment import";
    edge.publicSuffix = lib.mkOption { type = lib.types.str; default = ""; description = "Approved wildcard-DNS application suffix; exact one-label subdomains only."; };
    edge.ingressIPv4 = lib.mkOption { type = lib.types.str; default = ""; description = "Public forwarder address used for DNS and HTTPS observation."; };
    edge.listenAddress = lib.mkOption { type = lib.types.str; default = "10.44.0.2"; description = "Main WireGuard address for application HTTP/TLS listeners."; };
    edge.forwarderIPv4 = lib.mkOption { type = lib.types.str; default = "10.44.0.1"; description = "WireGuard source permitted to reach application ingress."; };
    edge.composedConfig = lib.mkOption { type = lib.types.str; default = "/etc/caddy/caddy_config"; description = "Complete Caddyfile validated before a scoped Caddy reload."; };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      { assertion = cfg.installerUID != cfg.publisherUID; message = "Application publisher and installer identities must be distinct."; }
      { assertion = !cfg.edge.enable || (cfg.provisioning.enable && cfg.edge.publicSuffix != "" && builtins.match "[a-z0-9.-]+" cfg.edge.publicSuffix != null && builtins.match "[0-9.]+" cfg.edge.ingressIPv4 != null && builtins.match "10\\.44\\.0\\.[0-9]+" cfg.edge.listenAddress != null && builtins.match "10\\.44\\.0\\.[0-9]+" cfg.edge.forwarderIPv4 != null); message = "Public applications require a suffix, public ingress address, allocation pool and WireGuard-only listeners/forwarder."; }
      { assertion = lib.hasPrefix "node_" cfg.nodeID; message = "Applications require an exact registered node ID."; }
      { assertion = !protonEnabled || config.loom.orca.enable; message = "Application Proton materialization requires the existing agents Proton session integration."; }
      { assertion = !backupEnabled || config.loom.service.enable; message = "Application cloud history requires the existing LOOM daemon backup worker."; }
      { assertion = !cfg.provisioning.enable || (builtins.match "/[A-Za-z0-9_/-]+" cfg.provisioning.dataRoot != null && !(lib.hasSuffix "/" cfg.provisioning.dataRoot) && !(lib.hasInfix "//" cfg.provisioning.dataRoot) && cfg.provisioning.maxMemoryBytes > 0 && cfg.provisioning.maxPlannedBytes > 0); message = "Application provisioning requires a dedicated absolute data root and positive memory/planned-capacity ceilings."; }
    ];
    environment.etc."loom/project-applications.json".text = builtins.toJSON policy;
    environment.etc."loom/project-applications-helper.json".text = builtins.toJSON helperConfig;
    systemd.services.loomd = lib.mkIf backupEnabled {
      environment.LOOM_APPLICATION_DATA_BACKUP_ROOT = cfg.provisioning.dataRoot;
      # Reading private application data is a node-level authority decision.
      # Keep application modes/ACLs and the existing single Borg pipeline intact.
      serviceConfig.AmbientCapabilities = [ "CAP_DAC_READ_SEARCH" ];
      unitConfig.RequiresMountsFor = [ cfg.provisioning.dataRoot ];
      after = [ "systemd-tmpfiles-setup.service" ];
    };
    systemd.tmpfiles.rules = [
      "d /run/loom-project-applications 0750 root ${cfg.installerGroup} -"
      "d ${stateRoot} 0700 root root -"
      "d /var/lib/loom-project-application-configs 0711 root root -"
      "d /nix/var/nix/gcroots/loom-project-applications 0755 root root -"
    ] ++ lib.optional cfg.edge.enable "d ${edgeRoot} 0755 root root -"
      ++ lib.optional protonEnabled "d ${credentialRoot} 0700 root root -";
    # tmpfiles refuses the intentional operator -> root ownership transition
    # below the mounted service root. Create only these fixed module-owned dirs.
    systemd.services.loom-project-application-directories = lib.mkIf cfg.provisioning.enable {
      description = "Prepare persistent LOOM application service directories";
      wantedBy = [ "multi-user.target" ];
      after = [ "local-fs.target" "systemd-sysusers.service" "systemd-tmpfiles-setup.service" ];
      before = [ "shutdown.target" "loom-project-applications.socket" "loom-project-applications-restore.service" ] ++ lib.optional cfg.edge.enable "caddy.service";
      conflicts = [ "shutdown.target" ];
      # A prerequisite of sockets.target must not wait for basic.target.
      unitConfig = { DefaultDependencies = false; RequiresMountsFor = [ cfg.provisioning.dataRoot ]; };
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        test ! -L ${lib.escapeShellArg cfg.provisioning.dataRoot}
        ${pkgs.coreutils}/bin/install -d -o root -g root -m 0711 ${lib.escapeShellArg cfg.provisioning.dataRoot}
      '' + lib.optionalString cfg.edge.enable ''
        test ! -L ${lib.escapeShellArg "${cfg.provisioning.dataRoot}/.caddy"}
        ${pkgs.coreutils}/bin/install -d -o caddy -g caddy -m 0700 ${lib.escapeShellArg "${cfg.provisioning.dataRoot}/.caddy"}
      '';
    };
    systemd.sockets.loom-project-applications = {
      requires = lib.optional cfg.provisioning.enable "loom-project-application-directories.service";
      after = lib.optional cfg.provisioning.enable "loom-project-application-directories.service";
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "/run/loom-project-applications/helper.sock";
        Accept = true;
        SocketUser = "root";
        SocketGroup = cfg.installerGroup;
        SocketMode = "0660";
        MaxConnections = 16;
        RemoveOnStop = true;
      };
    };
    systemd.services."loom-project-applications@" = {
      description = "One bounded typed LOOM application helper request";
      serviceConfig = helperSandbox // {
        ExecStart = "${cfg.package}/bin/loom-service-manager --application-socket";
        StandardInput = "socket";
        StandardOutput = "socket";
        Type = "exec";
        RuntimeMaxSec = 90;
      };
    };
    # Root-owned durable completion records are the sole boot persistence owner.
    # The fixed oneshot reconstructs volatile drop-ins only after current policy,
    # publication, credentials, data and archive-fence checks under owner locks.
    systemd.services.loom-project-applications-restore = {
      requires = lib.optional cfg.provisioning.enable "loom-project-application-directories.service";
      description = "Restore permitted committed LOOM applications";
      wantedBy = [ "multi-user.target" ];
      after = [ "local-fs.target" "systemd-sysusers.service" "systemd-tmpfiles-setup.service" ] ++ lib.optional cfg.edge.enable "caddy.service" ++ lib.optional cfg.provisioning.enable "loom-project-application-directories.service";
      serviceConfig = helperSandbox // {
        ExecStart = "${cfg.package}/bin/loom-service-manager --application-restore";
        StandardInput = "null";
        StandardOutput = "journal";
        Type = "oneshot";
        TimeoutStartSec = 185;
      };
    };
    systemd.services."loom-application@" = {
      description = "LOOM owned application %i";
      serviceConfig = {
        Type = "exec";
        Slice = "system.slice";
        # An unconfigured template is inert; a validated owned drop-in replaces
        # this command and supplies its dedicated UID/GID, credentials and data.
        ExecStart = "${pkgs.coreutils}/bin/false";
        User = "nobody";
        Group = "nogroup";
        NoNewPrivileges = true;
        RestrictSUIDSGID = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictNamespaces = true;
        LockPersonality = true;
        CapabilityBoundingSet = "";
        IPAccounting = true;
        IPAddressDeny = "any";
        IPAddressAllow = "localhost";
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        UMask = "0077";
        TimeoutStartSec = 30;
        TimeoutStopSec = 20;
        KillMode = "control-group";
      };
    };
    # This additive import never changes the private LOOM API virtual host.
    services.caddy = lib.mkIf cfg.edge.enable {
      enable = true;
      # The existing cloud-history allocation root includes this private state.
      # No cert/key material enters a release, the VPS or application grants.
      dataDir = "${cfg.provisioning.dataRoot}/.caddy";
      globalConfig = ''
        default_bind ${cfg.edge.listenAddress}
      '';
      extraConfig = ''
        import ${edgeRoot}/*.caddy
      '';
    };
    systemd.services.caddy = lib.mkIf cfg.edge.enable {
      after = [ "systemd-tmpfiles-setup.service" "wireguard-wg0.service" "loom-project-application-directories.service" ];
      requires = [ "wireguard-wg0.service" "loom-project-application-directories.service" ];
      unitConfig.RequiresMountsFor = [ cfg.provisioning.dataRoot ];
    };
    # No global 80/443 rule and no forwarding to the private LOOM API.
    networking.firewall.extraCommands = lib.mkIf cfg.edge.enable ''
      iptables -I nixos-fw -i wg0 -s ${cfg.edge.forwarderIPv4}/32 -d ${cfg.edge.listenAddress}/32 -p tcp -m multiport --dports 80,443 -j nixos-fw-accept
    '';
    networking.firewall.extraStopCommands = lib.mkIf cfg.edge.enable ''
      iptables -D nixos-fw -i wg0 -s ${cfg.edge.forwarderIPv4}/32 -d ${cfg.edge.listenAddress}/32 -p tcp -m multiport --dports 80,443 -j nixos-fw-accept 2>/dev/null || true
    '';
  };
}
