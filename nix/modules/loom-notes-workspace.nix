{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.notesWorkspace;
  recoveryConfig = pkgs.writeText "loom-notes-recovery.json" (builtins.toJSON {
    runtime_config = cfg.runtimeConfigPath;
    cloud_config = config.loom.cloud.configPath;
    state_dir = cfg.stateDir;
    enrollment_inventory = "${cfg.stateDir}/enrollment.json";
    collections = cfg.recovery.collections;
    source_writers = cfg.recovery.sourceWriters;
    couch_dir = "/var/lib/couchdb";
    secrets_dir = "/var/lib/loom-notes-workspace-secrets";
    database = config.loom.postgres.database;
    operational_root = "${config.loom.dataDir}/backups/operational";
    capture_format = cfg.recovery.captureFormat;
    capacity = {
      mode = cfg.recovery.capacity.mode;
      operational_budget_bytes = cfg.recovery.capacity.operationalBudgetGiB * 1024 * 1024 * 1024;
      notes_budget_bytes = cfg.recovery.capacity.notesBudgetGiB * 1024 * 1024 * 1024;
      operational_free_floor_bytes = cfg.recovery.capacity.operationalFreeFloorGiB * 1024 * 1024 * 1024;
      notes_free_floor_bytes = cfg.recovery.capacity.notesFreeFloorGiB * 1024 * 1024 * 1024;
      max_pending = cfg.recovery.capacity.maxPending;
    };
    destination = "/srv/loom/application-data/notes-recovery";
    staging = "/srv/loom/.notes-recovery-staging";
    held_staging = "/srv/loom/application-data/notes-recovery-held";
    loom = "${config.loom.service.cliPackage}/bin/loom";
    client = cfg.recovery.client;
    client_script = "${../../scripts/loom-notes-client-snapshot.py}";
  });
  retentionConfig = pkgs.writeText "loom-local-retention.json" (builtins.toJSON {
    mode = cfg.recovery.retention.mode;
    policy_revision = cfg.recovery.retention.policyRevision;
    data_dir = config.loom.dataDir;
    main_config = "/etc/loom/loom.env";
    cloud_config = config.loom.cloud.configPath;
    loom = "${config.loom.service.cliPackage}/bin/loom";
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
      captureFormat = lib.mkOption {
        type = lib.types.enum [ "self-contained-v1" "reference-v2" ];
        default = "self-contained-v1";
        description = "Select reference-v2 only after v3 cloud/fetch readers are deployed and verified. Old self-contained captures remain readable; this does not rewrite them.";
      };
      capacity = {
        mode = lib.mkOption { type = lib.types.enum [ "off" "observe" "enforce" ]; default = "off"; description = "Select observe for the initial inventory review; enforce defers another full capture before stopping writers. Never deletes held data."; };
        operationalBudgetGiB = lib.mkOption { type = lib.types.ints.positive; default = 96; };
        notesBudgetGiB = lib.mkOption { type = lib.types.ints.positive; default = 192; };
        operationalFreeFloorGiB = lib.mkOption { type = lib.types.ints.positive; default = 50; };
        notesFreeFloorGiB = lib.mkOption { type = lib.types.ints.positive; default = 100; };
        maxPending = lib.mkOption { type = lib.types.ints.positive; default = 1; };
      };
      retention = {
        mode = lib.mkOption { type = lib.types.enum [ "disabled" "plan" "apply" ]; default = "disabled"; description = "Event-driven local retention owner. Apply requires separate policy approval and first-backlog review; disabled is not a periodic planner."; };
        policyRevision = lib.mkOption { type = lib.types.str; default = ""; description = "Exact approved recurring local-retention policy revision; mandatory for apply."; };
      };
      sourceWriters = lib.mkOption {
        type = lib.types.attrsOf (lib.types.listOf lib.types.str);
        default = {};
        description = "Host-approved application service units keyed by their canonical Notes source root. Only enrolled writers are temporarily stopped during archive and comparison; previously active units are resumed, including after capture failure.";
      };
      collections = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = {};
        description = "Legacy rollback map; current recovery consumes the runtime's automatic enrollment inventory.";
      };
      client = lib.mkOption {
        type = lib.types.nullOr (lib.types.submodule {
          options.host = lib.mkOption { type = lib.types.str; };
          options.vault = lib.mkOption { type = lib.types.str; };
          options.workspace = lib.mkOption {
            type = lib.types.nullOr lib.types.str;
            default = null;
            description = "Optional explicit native Workspace app-data directory on the same device. Capture uses the app's private recovery socket or a stopped-client lock; the existing vault remains protected.";
          };
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
        description = "Optional existing agents SSH alias and explicit vault, with additional native Workspace capture when selected; offline devices are reported, not reset.";
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
      systemd.services.loomd.environment = lib.mkIf (cfg.recovery.capacity.mode == "enforce") {
        LOOM_BACKUP_LOCAL_BUDGET_BYTES = toString (cfg.recovery.capacity.operationalBudgetGiB * 1024 * 1024 * 1024);
        LOOM_BACKUP_FREE_FLOOR_BYTES = toString (cfg.recovery.capacity.operationalFreeFloorGiB * 1024 * 1024 * 1024);
      };
      assertions = [{
        assertion = cfg.enable && cfg.couchDB.enable;
        message = "Notes recovery requires the native worker, CouchDB and its resolved source inventory.";
      }];
      systemd.tmpfiles.rules = [
        "d /srv/loom/.notes-recovery-staging 0700 root root - -"
        "d /srv/loom/application-data/notes-recovery 0700 root root - -"
        "d /srv/loom/application-data/notes-recovery-held 0700 root root - -"
        "d ${config.loom.dataDir}/notes-recovery-publications 0755 root root - -"
        "f ${config.loom.dataDir}/backups/operational.retention.lock 0640 loom loom - -"
      ];
      systemd.services.loom-notes-recovery = {
        description = "Capture protected Notes state and its exact operational package";
        after = [ "loomd.service" ];
        requires = [ "loomd.service" ];
        path = [ pkgs.coreutils pkgs.util-linux pkgs.acl pkgs.gnutar pkgs.gzip pkgs.pigz pkgs.systemd pkgs.openssh config.services.postgresql.package ];
        serviceConfig = {
          Type = "oneshot";
          User = "root";
          UMask = "0077";
          # Coherent database/archive capture currently takes about 30 minutes.
          # Leave headroom before the unchanged 03:00 daily backup.
          TimeoutStartSec = 2700;
          ExecStart = "${pkgs.python3}/bin/python3 ${../../scripts/loom-notes-recovery.py} ${recoveryConfig}";
          # Run after the capture cgroup is dead, including timeout/SIGKILL.
          # Restores transport and clears only the lock-fenced pause marker.
          ExecStopPost = "${pkgs.python3}/bin/python3 ${../../scripts/loom-notes-recovery.py} --cleanup ${recoveryConfig}";
        };
      };
      systemd.timers.loom-notes-recovery = {
        wantedBy = [ "timers.target" ];
        timerConfig = { OnCalendar = "*-*-* 02:00:00 Europe/Amsterdam"; Persistent = false; };
      };
    })
    (lib.mkIf (cfg.recovery.enable && cfg.recovery.retention.mode != "disabled") {
      assertions = [{ assertion = cfg.recovery.retention.mode != "apply" || cfg.recovery.retention.policyRevision != ""; message = "Local retention apply requires its explicitly approved policy revision and backlog review."; }];
      systemd.services.loom-local-retention = {
        description = "Reconcile local recovery retention after committed producer events";
        after = [ "loomd.service" ];
        requires = [ "loomd.service" ];
        path = [ pkgs.coreutils pkgs.util-linux pkgs.openssh pkgs.borgbackup ];
        serviceConfig = {
          Type = "oneshot";
          User = "root";
          UMask = "0077";
          TimeoutStartSec = 1800;
          ExecStart = "${pkgs.python3}/bin/python3 ${../../scripts/loom-local-retention.py} ${retentionConfig}";
        };
      };
      # PathChanged, not PathExists: a deferred request never spins in a loop.
      systemd.paths.loom-local-retention = {
        wantedBy = [ "multi-user.target" ];
        pathConfig = { PathChanged = "${config.loom.dataDir}/backups/local-retention-request.json"; Unit = "loom-local-retention.service"; };
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
