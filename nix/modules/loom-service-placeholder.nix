{ config, lib, ... }:

let
  cfg = config.loom;
in
{
  options.loom.servicePlaceholder = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the Slice 0 placeholder loomd systemd service.";
    };
  };

  config = lib.mkIf (cfg.enable && cfg.servicePlaceholder.enable) {
    systemd.services.loomd = {
      description = "LOOM daemon placeholder";
      wantedBy = [
        "multi-user.target"
      ];
      requires = [
        "postgresql.service"
      ];
      after = [
        "postgresql.service"
      ];

      environment = {
        LOOM_ENV = cfg.environment;
        LOOM_DATA_DIR = cfg.dataDir;
        LOOM_OBJECT_STORE = cfg.objectStoreDir;
        LOOM_SOURCE_DIR = cfg.sourceDir;
        LOOM_CONFIG_DIR = cfg.configDir;
        LOOM_DB_NAME = cfg.postgres.database;
        LOOM_DB_USER = cfg.postgres.user;
      };

      script = ''
        echo "loomd placeholder starting"
        echo "LOOM_ENV=$LOOM_ENV"
        echo "LOOM_DATA_DIR=$LOOM_DATA_DIR"
        echo "LOOM_OBJECT_STORE=$LOOM_OBJECT_STORE"
        echo "LOOM_SOURCE_DIR=$LOOM_SOURCE_DIR"
        echo "LOOM_DB_NAME=$LOOM_DB_NAME"

        while true; do
          sleep 3600
        done
      '';

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.sourceDir;
        EnvironmentFile = "-${cfg.configDir}/loom.env";
        Restart = "on-failure";
        RestartSec = "2s";
        NoNewPrivileges = true;
        PrivateTmp = true;
      };
    };
  };
}
