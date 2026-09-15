{
  config,
  lib,
  pkgs,
  self,
  ...
}:

let
  cfg = config.loom;
  serviceCfg = cfg.service;
  nodeAgentCfg = cfg.nodeAgent;
  cloudCfg = cfg.cloud;
  embeddingsCfg = cfg.embeddings;
  notesAICfg = cfg.notesAI;
  workspaceArchiveCfg = cfg.workspaceArchive;
  serviceManagerCfg = cfg.serviceManager or {
    enable = false;
    allowlistPath = "";
    package = null;
  };
  system = pkgs.stdenv.hostPlatform.system;
  dbURL = "user=${cfg.postgres.user} dbname=${cfg.postgres.database} host=/run/postgresql sslmode=disable";
  systemdPath = path: ''"${path}"'';
  boxDocumentsPath = if cfg.boxPath == null then "" else "${cfg.boxPath}/Documents";
  boxNotesPath = if cfg.boxPath == null then "" else "${cfg.boxPath}/Notes";
  pathInside = parent: child: child == parent || lib.hasPrefix "${parent}/" child;
  nodeAgentMainURL =
    if nodeAgentCfg.mainURL != null then
      nodeAgentCfg.mainURL
    else if cfg.httpListenAddr != "" then
      "http://${cfg.httpListenAddr}"
    else
      "";
  nodeAgentInitScript = pkgs.writeShellScript "loom-node-agent-init" ''
    set -eu
    if [ ! -f ${lib.escapeShellArg nodeAgentCfg.configPath} ]; then
      ${nodeAgentCfg.package}/bin/loom-node-agent \
        --config ${lib.escapeShellArg nodeAgentCfg.configPath} \
        --state ${lib.escapeShellArg nodeAgentCfg.statePath} \
        --data-dir ${lib.escapeShellArg nodeAgentCfg.dataDir} \
        init \
        --main-url ${lib.escapeShellArg nodeAgentMainURL} \
        --node-key ${lib.escapeShellArg cfg.nodeId} \
        --display-name ${lib.escapeShellArg "LOOM ${cfg.nodeId} node-agent"} \
        --kind ${lib.escapeShellArg cfg.nodeKind} \
        --role ${lib.escapeShellArg cfg.nodeRole} \
        --runtime-class ${lib.escapeShellArg cfg.runtimeClass}
    fi
  '';
  extraRuntimeWritePaths =
    lib.filter (path: !(pathInside cfg.dataDir path)) ([
      cfg.importsRoot
      cfg.userBackupsRoot
      cfg.archiveRoot
      cfg.boxStateRoot
      cfg.notesProjectionRoot
      cfg.storageExportRoot
      cfg.storageRetentionRoot
    ] ++ lib.optionals cloudCfg.enable [
      cloudCfg.stateDir
      cloudCfg.borgCacheDir
      cloudCfg.borgSecurityDir
    ]);
  # Each ReadWritePaths entry is a bind mount, even under ProtectSystem=full.
  # The move endpoints must remain on their common writable /srv mount.
  workspaceArchiveMoveRoots = [ "${cfg.storageRoot}/archive" ]
    ++ lib.optionals (cfg.boxPath != null) (map
      (kind: "${cfg.boxPath}/${kind}") [ "Topics" "Projects" "Library" ]);
  loomRuntimeWritePaths = lib.filter
    (path: !workspaceArchiveCfg.enable || !(lib.elem path workspaceArchiveMoveRoots))
    (extraRuntimeWritePaths ++ lib.optionals (cfg.boxPath != null) [
      "${cfg.boxPath}/.loom"
      "${cfg.boxPath}/Projects"
      boxDocumentsPath
      boxNotesPath
    ]);
  cloudRuntimePackages =
    lib.optionals (cloudCfg.enable && cloudCfg.installRclone) [
      pkgs.rclone
    ] ++ lib.optionals (cloudCfg.enable && cloudCfg.installBorg) [
      pkgs.borgbackup
    ];
  embeddingRuntimePackages =
    lib.optionals (embeddingsCfg.ollama.installPackage || embeddingsCfg.ollama.enableService) [
      embeddingsCfg.ollama.package
    ];
  boxLayoutPreflight = pkgs.writeShellScript "loom-box-layout-preflight" ''
    set -eu
    for path in ${lib.escapeShellArg boxDocumentsPath} ${lib.escapeShellArg boxNotesPath}; do
      if [ -L "$path" ] || [ ! -d "$path" ]; then
        echo "LOOM canonical Box path must be a real directory: $path" >&2
        exit 1
      fi
      if [ ! -w "$path" ]; then
        echo "LOOM canonical Box path is not writable by the service: $path" >&2
        exit 1
      fi
    done
    findmnt_output=""
    if findmnt_output="$(${pkgs.util-linux}/bin/findmnt -rn --mountpoint ${lib.escapeShellArg boxDocumentsPath} 2>&1)"; then
      echo "LOOM canonical Box Documents must not be a separate or bind mount: ${boxDocumentsPath}" >&2
      exit 1
    else
      findmnt_status="$?"
      if [ "$findmnt_status" -ne 1 ] || [ -n "$findmnt_output" ]; then
        echo "LOOM could not verify canonical Box Documents mount state: $findmnt_output" >&2
        exit 1
      fi
    fi
  '';
  loomEnv = ''
    LOOM_ENV=${cfg.environment}
    LOOM_NODE_ID=${cfg.nodeId}
    LOOM_NODE_KIND=${cfg.nodeKind}
    LOOM_NODE_ROLE=${cfg.nodeRole}
    LOOM_RUNTIME_CLASS=${cfg.runtimeClass}
    LOOM_DATA_DIR=${cfg.dataDir}
    LOOM_OBJECT_STORE=${cfg.objectStoreDir}
    LOOM_SERVICE_ROOT=${cfg.serviceRoot}
    LOOM_STORAGE_ROOT=${cfg.storageRoot}
    LOOM_CANONICAL_USER_BACKUPS_ROOT=${cfg.canonicalUserBackupsRoot}
	LOOM_IMPORTS_BACKUP_POLICY=${cfg.importsBackupPolicy}
    LOOM_IMPORTS_ROOT=${cfg.importsRoot}
    LOOM_USER_BACKUPS_ROOT=${cfg.userBackupsRoot}
    LOOM_ARCHIVE_ROOT=${cfg.archiveRoot}
    LOOM_GENERATED_ROOT=${cfg.generatedRoot}
    LOOM_BOX_STATE_ROOT=${cfg.boxStateRoot}
    LOOM_MAIN_DOCUMENTS_ROOT=${cfg.mainDocumentsDir}
    LOOM_NOTES_PROJECTION_ROOT=${cfg.notesProjectionRoot}
    LOOM_STORAGE_EXPORT_ROOT=${cfg.storageExportRoot}
    LOOM_STORAGE_RETENTION_ROOT=${cfg.storageRetentionRoot}
    LOOM_DB_URL=${dbURL}
    LOOM_SOCKET_PATH=${cfg.socketPath}
    LOOM_HTTP_LISTEN_ADDR=${cfg.httpListenAddr}
    LOOM_LOG_LEVEL=${cfg.logLevel}
    LOOM_MIGRATIONS_DIR=${serviceCfg.package}/share/loom/migrations
    LOOM_AUTO_MIGRATE=${if cfg.autoMigrate then "true" else "false"}
    LOOM_BOOTSTRAP_DEV=${if cfg.bootstrapDev then "true" else "false"}
    LOOM_BOOTSTRAP_MODE=${cfg.bootstrapMode}
    LOOM_LEGACY_SPLIT_ROOTS=${if cfg.filesystemCutoverEnabled then "false" else "true"}
    LOOM_WORKSPACE_ARCHIVE_ENABLED=${if workspaceArchiveCfg.enable then "true" else "false"}
    LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID=${workspaceArchiveCfg.manifestKeyId}
    LOOM_EMBEDDINGS_ENABLED=${if embeddingsCfg.enable then "true" else "false"}
    LOOM_EMBEDDING_RUNTIME=${embeddingsCfg.runtime}
    LOOM_EMBEDDING_MODEL=${embeddingsCfg.model}
    LOOM_EMBEDDING_OLLAMA_URL=${embeddingsCfg.ollama.url}
    LOOM_EMBEDDING_DIMENSIONS=${toString embeddingsCfg.dimensions}
    LOOM_EMBEDDING_QUIET_WINDOW_SECONDS=${toString embeddingsCfg.quietWindowSeconds}
    LOOM_EMBEDDING_CONCURRENCY=${toString embeddingsCfg.concurrency}
    LOOM_VISION_ENABLED=${if notesAICfg.vision.enable then "true" else "false"}
    LOOM_VISION_RUNTIME=${notesAICfg.vision.runtime}
    LOOM_VISION_MODEL=${notesAICfg.vision.model}
    LOOM_VISION_OLLAMA_URL=${notesAICfg.vision.ollamaURL}
    LOOM_VISION_MAX_BYTES=${toString notesAICfg.vision.maxBytes}
    LOOM_VISION_MAX_PIXELS=${toString notesAICfg.vision.maxPixels}
    LOOM_CONFIG_FILE=${cfg.configDir}/loom.env
    LOOM_CLOUD_CONFIG_PATH=${cloudCfg.configPath}
    LOOM_CLOUD_STATE_DIR=${cloudCfg.stateDir}
  '' + lib.optionalString (cfg.boxPath != null) ''
    LOOM_BOX_PATH=${cfg.boxPath}
  '' + lib.optionalString (cfg.boxProfile != "") ''
    LOOM_BOX_PROFILE=${cfg.boxProfile}
  '' + lib.optionalString serviceManagerCfg.enable ''
    LOOM_SERVICE_ALLOWLIST_PATH=${serviceManagerCfg.allowlistPath}
  '';
in
{
  options.loom.service = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the real LOOM daemon systemd service.";
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loomd;
      description = "Package that provides the loomd binary.";
    };

    cliPackage = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loom;
      description = "Package that provides the loom CLI.";
    };
  };

  options.loom.nodeAgent = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable a managed loom-node-agent systemd service for this host.";
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loom-node-agent;
      description = "Package that provides the loom-node-agent binary.";
    };

    configPath = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.dataDir}/node-agent/config.json";
      description = "Path to the node-agent config JSON.";
    };

    statePath = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.dataDir}/node-agent/state.json";
      description = "Path to the node-agent state JSON.";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.dataDir}/node-agent";
      description = "Node-agent runtime data directory.";
    };

    mainURL = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Main-node URL used by node-agent; defaults to loom.httpListenAddr when available.";
    };
  };

  config = lib.mkIf (cfg.enable && serviceCfg.enable) {
    assertions = [
      {
        assertion = cfg.postgres.enable;
        message = "The real LOOM service requires loom.postgres.enable = true for Slice 1.";
      }
      {
        assertion = cfg.bootstrapMode != "production" || cfg.boxPath != null;
        message = "Production LOOM service requires loom.boxPath to be set explicitly.";
      }
      {
        assertion = !(cfg.filesystemCutoverEnabled && cfg.bootstrapMode == "production" && cfg.nodeKind == "main") || cfg.canonicalBoxPath != null;
        message = "production-main loom.filesystemCutoverEnabled requires loom.canonicalBoxPath.";
      }
      {
        assertion = !(cfg.bootstrapMode == "production" && cfg.nodeKind == "main") || cfg.boxProfile == "main";
        message = "Production main LOOM service requires loom.boxProfile = \"main\".";
      }
      {
        assertion = !nodeAgentCfg.enable || nodeAgentMainURL != "";
        message = "loom.nodeAgent.enable requires loom.nodeAgent.mainURL or loom.httpListenAddr.";
      }
      {
        assertion = !workspaceArchiveCfg.enable || cfg.boxPath != null;
        message = "loom.workspaceArchive.enable requires loom.boxPath.";
      }
      {
        assertion = !workspaceArchiveCfg.enable || (
          workspaceArchiveCfg.manifestKeySource != null
          && lib.hasPrefix "/" workspaceArchiveCfg.manifestKeySource
          && !lib.hasPrefix "/nix/store/" workspaceArchiveCfg.manifestKeySource
        );
        message = "loom.workspaceArchive.enable requires an absolute operator-owned manifestKeySource outside the Nix store.";
      }
      {
        assertion = !workspaceArchiveCfg.enable || cfg.archiveRoot == "${cfg.storageRoot}/archive";
        message = "loom.workspaceArchive.enable requires the archive root directly beneath loom.storageRoot.";
      }
      {
        assertion = !workspaceArchiveCfg.enable || (
          config.systemd.services.loomd.serviceConfig.ProtectSystem == "full"
          && lib.all (path: lib.hasPrefix "/srv/" path) workspaceArchiveMoveRoots
        );
        message = "Workspace archive requires writable /srv roots with ProtectSystem=full; per-root writable bind mounts cannot support atomic moves.";
      }
    ];

    environment.etc."loom/loom.env" = {
      text = loomEnv;
      mode = "0640";
      group = cfg.group;
    };

    environment.systemPackages = [
      serviceCfg.package
      serviceCfg.cliPackage
    ] ++ lib.optionals nodeAgentCfg.enable [
      nodeAgentCfg.package
    ] ++ cloudRuntimePackages ++ embeddingRuntimePackages;

    systemd.services."loom-box-layout-preflight" = lib.mkIf (cfg.boxPath != null) {
      description = "Validate the LOOM Box layout before starting loomd";
      unitConfig.RequiresMountsFor = [
        boxDocumentsPath
        boxNotesPath
      ];
      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        Group = cfg.group;
        ExecStart = boxLayoutPreflight;
      };
    };

    systemd.services.loomd = {
      description = "LOOM daemon";
      wantedBy = [
        "multi-user.target"
      ];
      requires = [
        "postgresql.service"
      ] ++ lib.optionals (cfg.boxPath != null) [
        "loom-box-layout-preflight.service"
      ] ++ lib.optionals (embeddingsCfg.enable && embeddingsCfg.ollama.enableService) [
        "ollama.service"
      ];
      after = [
        "postgresql.service"
      ] ++ lib.optionals (cfg.boxPath != null) [
        "loom-box-layout-preflight.service"
      ] ++ lib.optionals (embeddingsCfg.enable && embeddingsCfg.ollama.enableService) [
        "ollama.service"
      ];
      unitConfig = {
        RequiresMountsFor = lib.optionals (cfg.boxPath != null) [
          boxDocumentsPath
          boxNotesPath
        ];
      };
      path = [
        pkgs.git
        pkgs.nodejs_22
        pkgs.poppler-utils
        config.services.postgresql.package
      ] ++ cloudRuntimePackages ++ embeddingRuntimePackages;

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.sourceDir;
        EnvironmentFile = "${cfg.configDir}/loom.env";
        LoadCredential = lib.optional workspaceArchiveCfg.enable "workspace-archive-manifest-key:${workspaceArchiveCfg.manifestKeySource}";
        ExecStart = "${serviceCfg.package}/bin/loomd serve";
        Restart = "on-failure";
        RestartSec = "2s";
        RuntimeDirectory = "loom";
        RuntimeDirectoryMode = "0750";
        RuntimeDirectoryPreserve = "yes";
        StateDirectory = "loom";
        StateDirectoryMode = "0750";
        UMask = "0027";
        NoNewPrivileges = lib.mkDefault true;
        PrivateTmp = lib.mkDefault true;
        ProtectSystem = lib.mkDefault "full";
        ProtectHome = lib.mkDefault "read-only";
        RestrictSUIDSGID = lib.mkDefault true;
        LockPersonality = lib.mkDefault true;
        SystemCallArchitectures = lib.mkDefault "native";
        ReadWritePaths = [
          cfg.dataDir
          "/run/loom"
        ] ++ map systemdPath loomRuntimeWritePaths;
      };
    };

    systemd.tmpfiles.rules = lib.optionals nodeAgentCfg.enable [
      "d ${nodeAgentCfg.dataDir} 0750 ${cfg.user} ${cfg.group} - -"
    ];

    systemd.services."loom-node-agent" = lib.mkIf nodeAgentCfg.enable {
      description = "LOOM node agent";
      wantedBy = [
        "multi-user.target"
      ];
      requires = [
        "loomd.service"
      ];
      after = [
        "loomd.service"
      ];

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.sourceDir;
        EnvironmentFile = "${cfg.configDir}/loom.env";
        ExecStartPre = "${nodeAgentInitScript}";
        ExecStart = "${nodeAgentCfg.package}/bin/loom-node-agent --config ${nodeAgentCfg.configPath} --state ${nodeAgentCfg.statePath} --data-dir ${nodeAgentCfg.dataDir}${lib.optionalString serviceManagerCfg.enable " --service-allowlist ${serviceManagerCfg.allowlistPath} --service-manager-helper ${serviceManagerCfg.package}/bin/loom-service-manager"} serve";
        Restart = "on-failure";
        RestartSec = "2s";
        StateDirectory = "loom";
        StateDirectoryMode = "0750";
        UMask = "0027";
        NoNewPrivileges = lib.mkDefault true;
        PrivateTmp = lib.mkDefault true;
        ProtectSystem = lib.mkDefault "full";
        ProtectHome = lib.mkDefault "read-only";
        RestrictSUIDSGID = lib.mkDefault true;
        LockPersonality = lib.mkDefault true;
        SystemCallArchitectures = lib.mkDefault "native";
        ReadWritePaths = [
          cfg.dataDir
          "/run/loom"
        ] ++ extraRuntimeWritePaths;
      };
    };
  };
}
