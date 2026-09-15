{ config, lib, ... }:

let
  cfg = config.loom;
in
{
  options.loom = {
    enable = lib.mkEnableOption "LOOM runtime";

    environment = lib.mkOption {
      type = lib.types.str;
      default = "dev";
      description = "LOOM runtime environment label.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "loom";
      description = "Unix user that will run LOOM services.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "loom";
      description = "Unix group that will own LOOM runtime paths.";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom";
      description = "Linux-native LOOM runtime data directory.";
    };

    serviceRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom";
      description = "Canonical root for LOOM-owned human and agent filesystem data.";
    };

    storageRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/storage";
      description = "Canonical physical custody root for imports, user backups, and archives.";
    };

    canonicalStorageRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/storage";
      description = "Planned canonical physical Storage target; production Storage identity remains canonical even while explicitly named legacy custody roots stay active before cutover.";
    };

    importsRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/storage/imports";
      description = "Canonical custody root for promoted Lane imports.";
    };

    canonicalImportsRoot = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.canonicalStorageRoot}/imports";
      description = "Planned canonical Lane Imports custody target.";
    };

    importsBackupPolicy = lib.mkOption {
      type = lib.types.enum [
        "committed_lane_custody_only"
        "legacy_complete_physical_custody"
      ];
      default = "committed_lane_custody_only";
      description = "Explicit Imports backup provenance policy. Existing markerless production custody remains complete-physical until a separately reviewed provenance conversion; filesystem cutover does not change this value.";
    };

    userBackupsRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/storage/backups";
      description = "Canonical custody root for human-inspectable user-data backups.";
    };

    canonicalUserBackupsRoot = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.canonicalStorageRoot}/backups";
      description = "Planned canonical user-backup custody target.";
    };

    archiveRoot = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/storage/archive";
      description = "Canonical custody root for archived user data.";
    };

    canonicalArchiveRoot = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.canonicalStorageRoot}/archive";
      description = "Planned canonical archive custody target.";
    };

    generatedRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/generated";
      description = "Internal root for rebuildable generated artifacts that are not canonical data.";
    };

    canonicalGeneratedRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/generated";
      description = "Planned canonical internal generated-artifact target.";
    };

    boxStateRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/box-state";
      description = "Node-owned mutable Box compatibility and workspace transfer state root.";
    };

    canonicalBoxStateRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/box-state";
      description = "Planned canonical node-owned Box runtime-state target.";
    };

    sourceDir = lib.mkOption {
      type = lib.types.str;
      default = "/srv/loom/current";
      description = "Deployed LOOM source directory on the server.";
    };

    configDir = lib.mkOption {
      type = lib.types.str;
      default = "/etc/loom";
      description = "LOOM server configuration directory.";
    };

    objectStoreDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/object-store";
      description = "LOOM object-store root directory.";
    };

    mainDocumentsDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/main-documents";
      description = "Deprecated migration input for the legacy main Documents backing directory.";
    };

    notesProjectionRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/loom-notes";
      description = "Generated read-only LOOM notes projection root.";
    };

    canonicalNotesProjectionRoot = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.canonicalGeneratedRoot}/notes";
      description = "Planned canonical generated Notes projection target.";
    };

    storageExportRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/storage-views/main-export";
      description = "Deprecated migration input for the legacy generated main storage export.";
    };

    storageRetentionRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/loom/storage-retention";
      description = "Content-addressed retention payload root for retained LOOM storage bytes.";
    };

    nodeId = lib.mkOption {
      type = lib.types.str;
      default = "dev-main";
      description = "LOOM node id for this runtime.";
    };

    nodeKind = lib.mkOption {
      type = lib.types.str;
      default = "main";
      description = "LOOM node kind for this runtime.";
    };

    nodeRole = lib.mkOption {
      type = lib.types.str;
      default = "main";
      description = "LOOM node role for this runtime.";
    };

    runtimeClass = lib.mkOption {
      type = lib.types.str;
      default = "main_full";
      description = "LOOM runtime class for this node.";
    };

    boxPath = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Explicit LOOM Box path for this node.";
    };

    canonicalBoxPath = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Planned canonical Box target; does not switch production writers until filesystemCutoverEnabled is true.";
    };

    filesystemCutoverEnabled = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Atomically select the canonical production Box, runtime-state, Storage, Imports, backup, archive, generated Notes, and SMB roots after the operator completes the explicit filesystem cutover.";
    };

    boxProfile = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "LOOM Box profile, for example main or workspace.";
    };

    boxOwner = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Human owner of the LOOM Box when Box paths are managed by Nix.";
    };

    boxGroup = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Group assigned to human-visible LOOM Box paths when managed by Nix.";
    };

    manageBoxPaths = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Create/manage LOOM Box service subdirectories with tmpfiles.";
    };

    bootstrapMode = lib.mkOption {
      type = lib.types.enum [
        "none"
        "dev"
        "production"
      ];
      default = "none";
      description = "Bootstrap mode expected for this node.";
    };

    socketPath = lib.mkOption {
      type = lib.types.str;
      default = "/run/loom/loomd.sock";
      description = "Unix socket path for local loomd HTTP/JSON transport.";
    };

    httpListenAddr = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Optional private TCP HTTP listen address for node-agent-to-main development traffic.";
    };

    logLevel = lib.mkOption {
      type = lib.types.str;
      default = "info";
      description = "LOOM service log level.";
    };

    autoMigrate = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Apply database migrations automatically before loomd starts serving.";
    };

    workspaceArchive = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Construct the bounded workspace archive lifecycle runtime. Disabled by default and never activated by this option alone.";
      };

      manifestKeyId = lib.mkOption {
        type = lib.types.str;
        default = "workspace-archive-manifest-key-v1";
        description = "Non-secret fixed manifest authentication key identifier recorded in archive evidence.";
      };

      manifestKeySource = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Operator-owned source path loaded by systemd as workspace-archive-manifest-key; key bytes must not be represented in Nix.";
      };
    };

    bootstrapDev = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Ensure deterministic development bootstrap records before loomd starts serving.";
    };

    cloud = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Enable LOOM cloud-storage substrate paths and runtime support.";
      };

      provider = lib.mkOption {
        type = lib.types.str;
        default = "hetzner_storage_box";
        description = "Cloud storage provider identifier.";
      };

      driver = lib.mkOption {
        type = lib.types.str;
        default = "rclone";
        description = "Cloud storage driver identifier.";
      };

      configPath = lib.mkOption {
        type = lib.types.str;
        default = "/etc/loom/cloud/config.json";
        description = "Path to the non-secret LOOM cloud config JSON.";
      };

      stateDir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/loom/cloud";
        description = "LOOM cloud state, manifests, and staging directory.";
      };

      rcloneConfigPath = lib.mkOption {
        type = lib.types.str;
        default = "/etc/loom/cloud/rclone.conf";
        description = "Path to the operator-managed rclone credential config.";
      };

      remoteName = lib.mkOption {
        type = lib.types.str;
        default = "loom-cloud";
        description = "rclone remote name for LOOM cloud storage.";
      };

      remoteRoot = lib.mkOption {
        type = lib.types.str;
        default = "loom";
        description = "Remote root prefix owned by LOOM.";
      };

      installRclone = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Install rclone in the system package set when cloud support is enabled.";
      };

      snapshotBackend = lib.mkOption {
        type = lib.types.enum [ "legacy_tree" "borg" ];
        default = "legacy_tree";
        description = "Snapshot backend for main-node disaster-recovery cloud snapshots.";
      };

      installBorg = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Install BorgBackup in the system package set when cloud support is enabled.";
      };

      borgBinary = lib.mkOption {
        type = lib.types.str;
        default = "borg";
        description = "BorgBackup binary name used by LOOM cloud snapshot commands.";
      };

      borgRepository = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Borg repository URI/path. This is non-secret, but usually operator-specific.";
      };

      borgPassphraseFile = lib.mkOption {
        type = lib.types.str;
        default = "/etc/loom/cloud/borg.passphrase";
        description = "Operator-managed file containing the Borg repository passphrase.";
      };

      borgCacheDir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/loom/cloud/borg/cache";
        description = "Borg cache directory writable by the LOOM service user.";
      };

      borgSecurityDir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/loom/cloud/borg/security";
        description = "Borg security directory writable by the LOOM service user.";
      };

      borgEncryption = lib.mkOption {
        type = lib.types.str;
        default = "repokey-blake2";
        description = "Borg repository encryption mode used during explicit backend initialization.";
      };

      borgCompression = lib.mkOption {
        type = lib.types.str;
        default = "zstd,6";
        description = "Borg compression setting used for packed cloud snapshots.";
      };

      borgCheckMode = lib.mkOption {
        type = lib.types.enum [ "repository" "archive" "full" "disabled" ];
        default = "repository";
        description = "Borg check mode used after create and during verification.";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.dataDir != cfg.sourceDir;
        message = "LOOM runtime dataDir must not be the same as sourceDir.";
      }
      {
        assertion = !cfg.manageBoxPaths || cfg.boxPath != null;
        message = "LOOM Box path management requires loom.boxPath to be set explicitly.";
      }
      {
        assertion = !cfg.manageBoxPaths || cfg.boxOwner != "";
        message = "LOOM Box path management requires loom.boxOwner to be set explicitly.";
      }
    ];
  };
}
