{ config, lib, pkgs, ... }:

let
  cfg = config.loom;
  provenanceCfg = cfg.postgres.provenance;
  restoreAuthorityPeerMap = "loom_restore_authority";
  adminUsers = lib.filter (name: name != cfg.postgres.user && name != provenanceCfg.user) cfg.postgres.adminUsers;
  provenanceDBURL = "user=${provenanceCfg.user} dbname=${provenanceCfg.database} host=/run/postgresql sslmode=disable";
  postgresPackage =
    if cfg.postgres.pgvector.enable then
      cfg.postgres.package.withPackages (ps: [
        ps.pgvector
      ])
    else
      cfg.postgres.package;
in
{
  options.loom.postgres = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the LOOM PostgreSQL service configuration.";
    };

    database = lib.mkOption {
      type = lib.types.str;
      default = "loom_main";
      description = "Primary LOOM PostgreSQL database name.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = cfg.user;
      description = "Primary LOOM PostgreSQL user.";
    };

    adminUsers = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "loomadmin" ];
      description = "Local OS users allowed to access the LOOM database through PostgreSQL peer auth for admin maintenance tooling.";
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.postgresql_17;
      description = "PostgreSQL package used by LOOM. Keep this aligned with the deployed data directory major version.";
    };

    pgvector.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Install and create the PostgreSQL pgvector extension for LOOM notes embeddings.";
    };

    provenance = {
      database = lib.mkOption {
        type = lib.types.strMatching "^[a-z_][a-z0-9_]*$";
        default = "loom_provenance";
        description = "Dedicated LOOM Provenance PostgreSQL database name.";
      };

      user = lib.mkOption {
        type = lib.types.strMatching "^[a-z_][a-z0-9_]*$";
        default = "loom_provenance";
        description = "Least-privilege PostgreSQL role used only for the provenance database.";
      };

      peerMap = lib.mkOption {
        type = lib.types.strMatching "^[a-z_][a-z0-9_]*$";
        default = "loom_provenance_service";
        description = "PostgreSQL peer map from the LOOM service OS identity to the provenance role.";
      };
    };
  };

  config = lib.mkIf (cfg.enable && cfg.postgres.enable) {
    assertions = [
      {
        assertion = provenanceCfg.database != cfg.postgres.database;
        message = "LOOM provenance database must not collide with the primary LOOM database.";
      }
      {
        assertion = provenanceCfg.user != cfg.postgres.user;
        message = "LOOM provenance role must not collide with the primary LOOM PostgreSQL role.";
      }
      {
        assertion = !(lib.elem provenanceCfg.user cfg.postgres.adminUsers);
        message = "LOOM provenance role must not collide with a PostgreSQL admin role.";
      }
      {
        assertion = cfg.postgres.database != "postgres" && cfg.postgres.user != "postgres"
          && provenanceCfg.database != "postgres" && provenanceCfg.user != "postgres";
        message = "LOOM databases and service roles must not collide with PostgreSQL cluster administration.";
      }
    ];

    services.postgresql = {
      enable = true;
      enableTCPIP = false;
      package = postgresPackage;
      ensureDatabases = [
        cfg.postgres.database
        provenanceCfg.database
      ];
      ensureUsers = [
        {
          name = cfg.postgres.user;
        }
        {
          name = provenanceCfg.user;
        }
      ] ++ map (name: { inherit name; }) adminUsers;
      authentication = lib.mkBefore ''
        local "/^loom_restore_drill_[a-z0-9_]+$" "${cfg.postgres.user}" peer map=${restoreAuthorityPeerMap}
        local "/^loom_provenance_restore_drill_[a-z0-9_]+$" "${provenanceCfg.user}" peer map=${restoreAuthorityPeerMap}
        local "${provenanceCfg.database}" "${provenanceCfg.user}" peer map=${provenanceCfg.peerMap}
      '';
      identMap = lib.mkAfter ''
        ${restoreAuthorityPeerMap} postgres ${cfg.postgres.user}
        ${restoreAuthorityPeerMap} postgres ${provenanceCfg.user}
        ${restoreAuthorityPeerMap} ${cfg.user} ${cfg.postgres.user}
        ${restoreAuthorityPeerMap} ${cfg.user} ${provenanceCfg.user}
        ${provenanceCfg.peerMap} ${cfg.user} ${provenanceCfg.user}
      '';
    };

    environment.etc."loom/loom.env".text = lib.mkAfter ''
      LOOM_PROVENANCE_DB_URL=${provenanceDBURL}
    '';

    systemd.services.loomd = {
      requires = lib.mkAfter [ "postgresql-setup.service" ];
      after = lib.mkAfter [ "postgresql-setup.service" ];
    };

    environment.systemPackages = [
      postgresPackage
    ];

    systemd.services.postgresql-setup.script = lib.mkAfter ''
      psql -v ON_ERROR_STOP=1 -d postgres <<'SQL'
      ALTER ROLE "${cfg.postgres.user}" NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
      REVOKE "${provenanceCfg.user}" FROM "${cfg.postgres.user}";
      ALTER DATABASE "${cfg.postgres.database}" OWNER TO "${cfg.postgres.user}";
      GRANT ALL PRIVILEGES ON DATABASE "${cfg.postgres.database}" TO "${cfg.postgres.user}";
      ALTER ROLE "${provenanceCfg.user}" NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
      REVOKE "${cfg.postgres.user}" FROM "${provenanceCfg.user}";
      ALTER DATABASE "${provenanceCfg.database}" OWNER TO "${provenanceCfg.user}";
      REVOKE ALL PRIVILEGES ON DATABASE "${provenanceCfg.database}" FROM PUBLIC;
      GRANT CONNECT, TEMPORARY ON DATABASE "${provenanceCfg.database}" TO "${provenanceCfg.user}";
      ${lib.concatMapStringsSep "\n" (adminUser: ''
      REVOKE "${adminUser}" FROM "${provenanceCfg.user}";
      GRANT "${cfg.postgres.user}" TO "${adminUser}";
      GRANT "${provenanceCfg.user}" TO "${adminUser}";
      '') adminUsers}
      SQL

      ${lib.optionalString cfg.postgres.pgvector.enable ''
        psql -v ON_ERROR_STOP=1 -d template1 -c 'CREATE EXTENSION IF NOT EXISTS vector;'
        psql -v ON_ERROR_STOP=1 -d "${cfg.postgres.database}" -c 'CREATE EXTENSION IF NOT EXISTS vector;'
      ''}
    '';
  };
}
