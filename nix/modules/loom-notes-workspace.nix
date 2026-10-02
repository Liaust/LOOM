{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.notesWorkspace;
  recoveryConfig = pkgs.writeText "loom-notes-recovery.json" (builtins.toJSON {
    runtime_config = cfg.runtimeConfigPath;
    cloud_config = config.loom.cloud.configPath;
    state_dir = cfg.stateDir;
    collections = cfg.recovery.collections;
    couch_dir = "/var/lib/couchdb";
    secrets_dir = "/var/lib/loom-notes-workspace-secrets";
    database = config.loom.postgres.database;
    operational_root = "${config.loom.dataDir}/backups/operational";
    destination = "/srv/loom/application-data/notes-recovery";
    staging = "/srv/loom/.notes-recovery-staging";
    loom = "${config.loom.service.cliPackage}/bin/loom";
    client = cfg.recovery.client;
    client_script = "${../../scripts/loom-notes-client-snapshot.py}";
  });
in {
  options.loom.notesWorkspace = {
    enable = lib.mkEnableOption "the canonical Notes workspace worker";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.notes-workspace-sync;
      description = "Pinned native replica adapter; not a filesystem mirror.";
    };
    runtimeConfigPath = lib.mkOption {
      type = lib.types.str;
      default = "/etc/loom/notes-workspace.json";
      description = "Operator-managed collection bindings and protected settings path, loaded at loomd startup.";
    };
    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/notes-workspace";
      description = "Durable native replica directory; preserve on package rollback.";
    };
    recovery = {
      enable = lib.mkEnableOption "a protected daily Notes recovery cohort";
      collections = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = {};
        description = "Exact enrolled collection keys and canonical selected directories; no implicit discovery.";
      };
      client = lib.mkOption {
        type = lib.types.nullOr (lib.types.submodule {
          options.host = lib.mkOption { type = lib.types.str; };
          options.vault = lib.mkOption { type = lib.types.str; };
          options.maxBytes = lib.mkOption {
            type = lib.types.ints.positive;
            default = 17179869184;
            description = "Maximum uncompressed bytes in the explicit client recovery capture; archives spool to temporary disk.";
          };
          options.timeoutSeconds = lib.mkOption {
            type = lib.types.ints.positive;
            default = 900;
            description = "Maximum duration of the SSH client capture, including compression and transfer.";
          };
        });
        default = null;
        description = "Optional existing agents SSH alias and explicit vault; offline devices are reported, not reset.";
      };
    };
    couchDB = {
      enable = lib.mkEnableOption "the private Notes CouchDB transport";
      address = lib.mkOption { type = lib.types.str; default = "10.44.0.2"; };
      interface = lib.mkOption { type = lib.types.str; default = "wg0"; };
      secretsFile = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/loom-notes-workspace-secrets/couchdb.ini";
        description = "Protected host INI containing transport authentication, never imported into the Nix store.";
      };
    };
  };
  config = lib.mkMerge [
    (lib.mkIf cfg.recovery.enable {
      assertions = [{
        assertion = cfg.enable && cfg.couchDB.enable && cfg.recovery.collections != {};
        message = "Notes recovery requires the configured native worker, CouchDB and explicit source collections.";
      }];
      systemd.tmpfiles.rules = [
        "d /srv/loom/.notes-recovery-staging 0700 root root - -"
        "d /srv/loom/application-data/notes-recovery 0700 root root - -"
      ];
      systemd.services.loom-notes-recovery = {
        description = "Capture protected Notes state and its exact operational package";
        after = [ "loomd.service" ];
        requires = [ "loomd.service" ];
        path = [ pkgs.coreutils pkgs.util-linux pkgs.acl pkgs.gnutar pkgs.gzip pkgs.systemd pkgs.openssh config.services.postgresql.package ];
        serviceConfig = {
          Type = "oneshot";
          User = "root";
          UMask = "0077";
          TimeoutStartSec = 1800;
          ExecStart = "${pkgs.python3}/bin/python3 ${../../scripts/loom-notes-recovery.py} ${recoveryConfig}";
          # Also restore transport if the capture is terminated by systemd.
          ExecStopPost = "${pkgs.systemd}/bin/systemctl start couchdb.service";
        };
      };
      systemd.timers.loom-notes-recovery = {
        wantedBy = [ "timers.target" ];
        timerConfig = { OnCalendar = "*-*-* 02:50:00 Europe/Amsterdam"; Persistent = false; };
      };
    })
    (lib.mkIf cfg.enable {
      assertions = [{
        assertion = config.loom.service.enable && lib.hasPrefix "/" cfg.runtimeConfigPath && lib.hasPrefix "/var/lib/loom/" cfg.stateDir;
        message = "Notes workspace requires loomd, an absolute runtime config path, and durable LOOM runtime storage.";
      }];
      environment.systemPackages = [ cfg.package ];
      systemd.tmpfiles.rules = [ "d ${cfg.stateDir} 0700 loom loom - -" ];
      systemd.services.loomd.environment.LOOM_NOTES_WORKSPACE_CONFIG = cfg.runtimeConfigPath;
      systemd.services.loomd.serviceConfig.ReadWritePaths = lib.mkAfter [ cfg.stateDir ];
    })
    (lib.mkIf cfg.couchDB.enable {
      assertions = [{
        assertion = cfg.couchDB.address != "0.0.0.0" && cfg.couchDB.address != "::";
        message = "Notes CouchDB must bind a specific private-network address.";
      }];
      services.couchdb = {
        enable = true;
        bindAddress = cfg.couchDB.address;
        extraConfigFiles = [ cfg.couchDB.secretsFile ];
        extraConfig = {
          couchdb.single_node = true;
          chttpd = { require_valid_user = true; enable_cors = true; max_http_request_size = 33554432; };
          couch_httpd_auth.require_valid_user = true;
          cors = { origins = "app://obsidian.md,capacitor://localhost,http://localhost"; credentials = true; methods = "GET, PUT, POST, HEAD, DELETE"; headers = "accept, authorization, content-type, origin, referer"; };
        };
      };
      networking.firewall.interfaces.${cfg.couchDB.interface}.allowedTCPPorts = [ config.services.couchdb.port ];
      systemd.services.couchdb.after = [ "wireguard-${cfg.couchDB.interface}.service" ];
    })
  ];
}
