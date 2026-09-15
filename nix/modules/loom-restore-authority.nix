{ config, lib, pkgs, self, ... }:

let
  cfg = config.loom;
  authorityCfg = cfg.restoreAuthority;
  system = pkgs.stdenv.hostPlatform.system;
  postgresPackage = config.services.postgresql.package;
  restoreAuthorityRuntimeDirectory = "/run/loom-restore-authority";
  restoreAuthoritySocketPath = "${restoreAuthorityRuntimeDirectory}/restore-authority.sock";
  restoreAuthoritySocketUnit = config.systemd.units."loom-restore-authority.socket".unit;
  validDatabase = value:
    builtins.stringLength value <= 63 && builtins.match "^[a-z_][a-z0-9_]*$" value != null;
  validUnixIdentity = value:
    builtins.stringLength value <= 63 && builtins.match "^[a-z_][a-z0-9_-]*$" value != null;
in
{
  options.loom.restoreAuthority = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the PostgreSQL-owned local strict-restore authority.";
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loom-restore-authority;
      description = "Package providing the fixed LOOM restore-authority binary.";
    };

    socketPath = lib.mkOption {
      type = lib.types.str;
      default = restoreAuthoritySocketPath;
      description = "Private local Unix socket used only by strict disposable restores.";
    };
  };

  config = lib.mkIf (cfg.enable && authorityCfg.enable) {
    assertions = [
      {
        assertion = cfg.service.enable && cfg.postgres.enable;
        message = "loom.restoreAuthority requires the LOOM service and PostgreSQL.";
      }
      {
        assertion = authorityCfg.socketPath == restoreAuthoritySocketPath;
        message = "The restore authority uses only its fixed reviewed Unix socket path.";
      }
      {
        assertion = validUnixIdentity cfg.user && validUnixIdentity cfg.group;
        message = "Restore-authority Unix identities must be strict fixed names.";
      }
      {
        assertion = cfg.postgres.user != "postgres" && cfg.postgres.provenance.user != "postgres";
        message = "Disposable restored databases must not be owned by postgres.";
      }
      {
        assertion = cfg.postgres.database != cfg.postgres.provenance.database
          && cfg.postgres.user != cfg.postgres.provenance.user;
        message = "Operational and Provenance restore identities must remain distinct.";
      }
      {
        assertion = validDatabase cfg.postgres.database
          && validDatabase cfg.postgres.user
          && validDatabase cfg.postgres.provenance.database
          && validDatabase cfg.postgres.provenance.user;
        message = "Restore-authority database and owner configuration must use strict PostgreSQL identifiers of at most 63 bytes.";
      }
      {
        assertion = !(lib.elem cfg.postgres.database [ "postgres" "template0" "template1" ])
          && !(lib.elem cfg.postgres.provenance.database [ "postgres" "template0" "template1" ]);
        message = "Restore-authority active databases must not be PostgreSQL system databases.";
      }
      {
        assertion = !lib.hasPrefix "loom_restore_drill_" cfg.postgres.database
          && !lib.hasPrefix "loom_provenance_restore_drill_" cfg.postgres.database
          && !lib.hasPrefix "loom_restore_drill_" cfg.postgres.provenance.database
          && !lib.hasPrefix "loom_provenance_restore_drill_" cfg.postgres.provenance.database;
        message = "Active databases must remain outside both disposable restore namespaces.";
      }
    ];

    environment.systemPackages = [ authorityCfg.package ];

    environment.etc."loom/loom.env".text = lib.mkAfter ''
      LOOM_RESTORE_AUTHORITY_SOCKET_PATH=${authorityCfg.socketPath}
      LOOM_RESTORE_AUTHORITY_SOCKET_OWNER=postgres
      LOOM_RESTORE_AUTHORITY_SOCKET_GROUP=${cfg.group}
      LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER=root
      LOOM_RESTORE_AUTHORITY_EXECUTOR_USER=postgres
      LOOM_RESTORE_OPERATIONAL_DATABASE=${cfg.postgres.database}
      LOOM_RESTORE_OPERATIONAL_OWNER=${cfg.postgres.user}
      LOOM_RESTORE_PROVENANCE_DATABASE=${cfg.postgres.provenance.database}
      LOOM_RESTORE_PROVENANCE_OWNER=${cfg.postgres.provenance.user}
    '';

    systemd.tmpfiles.rules = [
      "r /run/loom/restore-authority.sock - - - - -"
      "d ${restoreAuthorityRuntimeDirectory} 0750 postgres ${cfg.group} - -"
    ];

    systemd.sockets."loom-restore-authority" = {
      description = "LOOM local strict-restore authority socket";
      wantedBy = [ "sockets.target" ];
      before = [ "loomd.service" ];
      after = [ "systemd-tmpfiles-setup.service" ];
      socketConfig = {
        ListenStream = authorityCfg.socketPath;
        SocketUser = "postgres";
        SocketGroup = cfg.group;
        SocketMode = "0660";
        DirectoryMode = "0750";
        RemoveOnStop = true;
      };
    };

    systemd.services."loom-restore-authority-socket-reconcile" = {
      description = "Reconcile the LOOM strict-restore authority socket after activation";
      wantedBy = [ "multi-user.target" ];
      before = [ "loomd.service" ];
      # ExecStart restarts the socket, so a Requires dependency here would
      # propagate that stop back into this reconcile service.
      after = [
        "systemd-tmpfiles-setup.service"
        "loom-restore-authority.socket"
      ];
      restartTriggers = [ restoreAuthoritySocketUnit ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.systemd}/bin/systemctl restart loom-restore-authority.socket";
      };
    };

    systemd.services."loom-restore-authority" = {
      description = "LOOM PostgreSQL-owned strict-restore authority";
      requires = [ "postgresql-setup.service" ];
      after = [ "postgresql-setup.service" ];
      serviceConfig = {
        Type = "simple";
        User = "postgres";
        Group = "postgres";
        SupplementaryGroups = [ cfg.group ];
        ExecStart = lib.concatStringsSep " " [
          "${authorityCfg.package}/bin/loom-restore-authority"
          "serve"
          "--listen-fd 3"
          "--socket-path ${lib.escapeShellArg authorityCfg.socketPath}"
          "--peer-user ${lib.escapeShellArg cfg.user}"
          "--socket-owner postgres"
          "--socket-group ${lib.escapeShellArg cfg.group}"
          "--operational-database ${lib.escapeShellArg cfg.postgres.database}"
          "--operational-owner ${lib.escapeShellArg cfg.postgres.user}"
          "--provenance-database ${lib.escapeShellArg cfg.postgres.provenance.database}"
          "--provenance-owner ${lib.escapeShellArg cfg.postgres.provenance.user}"
          "--createdb-path ${postgresPackage}/bin/createdb"
          "--pg-restore-path ${postgresPackage}/bin/pg_restore"
          "--dropdb-path ${postgresPackage}/bin/dropdb"
          "--postgres-socket-directory /run/postgresql"
          "--postgres-port 5432"
          "--max-dump-bytes 68719476736"
        ];
        Restart = "on-failure";
        RestartSec = "2s";
        UMask = "0077";
        NoNewPrivileges = true;
        PrivateNetwork = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        SystemCallArchitectures = "native";
        RestrictAddressFamilies = [ "AF_UNIX" ];
      };
    };

    systemd.services.loomd = {
      requires = lib.mkAfter [ "loom-restore-authority.socket" ];
      after = lib.mkAfter [ "loom-restore-authority.socket" ];
    };
  };
}
