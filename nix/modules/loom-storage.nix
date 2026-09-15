{ config, lib, pkgs, ... }:

let
  cfg = config.loom;
  storageCfg = cfg.storage;
  cloudCfg = cfg.cloud;
  sourceRoot = builtins.dirOf cfg.sourceDir;
  cloudConfigDir = builtins.dirOf cloudCfg.configPath;
  boxParent = if cfg.boxPath == null then null else builtins.dirOf cfg.boxPath;
  boxOwner = if cfg.boxOwner != "" then cfg.boxOwner else cfg.user;
  boxRootGroup = if cfg.boxGroup != "" then cfg.boxGroup else cfg.group;
  boxServiceGroup = cfg.group;
  managedMainBoxDirectories = [ "Projects" "Documents" "Notes" ".loom" ];
  retiredMainBoxIntakeNames = [ "Dropzone" "loom-lane" "LOOM Lane" ];
  systemdPath = path: ''"${path}"'';
  shellArg = lib.escapeShellArg;
in
{
  options.loom.storage = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable LOOM runtime users and directory creation.";
    };

    deployUser = lib.mkOption {
      type = lib.types.str;
      default = "root";
      description = "Unix user that owns the deployed source directory.";
    };

    deployGroup = lib.mkOption {
      type = lib.types.str;
      default = "root";
      description = "Unix group that owns the deployed source directory.";
    };

    humanStorageHomes = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Deprecated compatibility input. Direct canonical paths/bookmarks replace human storage symlinks.";
    };
  };

  config = lib.mkIf (cfg.enable && storageCfg.enable) {
    assertions = [
      {
        assertion = lib.all (name: !(lib.elem name retiredMainBoxIntakeNames)) managedMainBoxDirectories;
        message = "LOOM Main Box tmpfiles must not recreate retired Dropzone or Main Lane paths.";
      }
    ];

    loom.notesProjectionRoot = lib.mkDefault cfg.canonicalNotesProjectionRoot;

    users.groups.${cfg.group} = { };

    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
      home = cfg.dataDir;
      description = "LOOM service user";
    };

    systemd.tmpfiles.rules = lib.optionals (cfg.storageRoot != cfg.dataDir) [
      "d ${cfg.storageRoot} 2750 ${cfg.user} ${cfg.group} - -"
    ] ++ [
      "d ${cfg.serviceRoot} 0755 ${storageCfg.deployUser} ${storageCfg.deployGroup} - -"
      "d ${cfg.serviceRoot}/agents 0755 root root - -"
      "d ${cfg.importsRoot} 2770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.userBackupsRoot} 2770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.archiveRoot} 2750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.boxStateRoot} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.generatedRoot} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.objectStoreDir} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.objectStoreDir}/blobs 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.objectStoreDir}/objects 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.objectStoreDir}/artifacts 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/lane 2770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/lane/staging 2770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.storageRetentionRoot} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.storageRetentionRoot}/main-documents 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.storageRetentionRoot}/main-documents/by-sha256 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.archiveRoot}/objects 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.archiveRoot}/manifests 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.archiveRoot}/runtime-manifests 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/indexes 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/packages 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/state 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/temp 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/update 0770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/update/history 0770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/update/pending 0770 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/update/rollback 0770 ${cfg.user} ${cfg.group} - -"
      "d ${sourceRoot} 0755 ${storageCfg.deployUser} ${storageCfg.deployGroup} - -"
      "d ${cfg.sourceDir} 0755 ${storageCfg.deployUser} ${storageCfg.deployGroup} - -"
      "d ${cfg.configDir} 0750 root ${cfg.group} - -"
    ] ++ lib.optionals cloudCfg.enable [
      "d ${cloudConfigDir} 0750 root ${cfg.group} - -"
      "d ${cloudCfg.stateDir} 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/snapshots 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/offload 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/manifests 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/restore-drills 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/temp 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.stateDir}/borg 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.borgCacheDir} 0700 ${cfg.user} ${cfg.group} - -"
      "d ${cloudCfg.borgSecurityDir} 0700 ${cfg.user} ${cfg.group} - -"
    ]
      ++ lib.optionals (cfg.manageBoxPaths && cfg.boxPath != null) [
      "a+ ${systemdPath boxParent} - - - - u:${cfg.user}:--x,m::r-x"
      "d ${systemdPath cfg.boxPath} 0755 ${boxOwner} ${boxRootGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Projects"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Documents"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Notes"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/.loom"} 0770 ${boxOwner} ${boxServiceGroup} - -"
    ];

    system.activationScripts.loomBoxParentAcl = lib.mkIf (cfg.manageBoxPaths && cfg.boxPath != null) ''
      if [ -d ${shellArg boxParent} ]; then
        ${pkgs.acl}/bin/setfacl -m u:${cfg.user}:--x,g:${cfg.group}:--x,m::r-x ${shellArg boxParent}
      fi
    '';

    system.activationScripts.loomImportsCustodyParents = ''
      if [ -L ${shellArg cfg.importsRoot} ]; then
        echo "LOOM imports root must not be a symlink: ${cfg.importsRoot}" >&2
        exit 1
      fi
      if [ -d ${shellArg cfg.importsRoot} ]; then
        ${pkgs.findutils}/bin/find ${shellArg cfg.importsRoot} -xdev -mindepth 1 -maxdepth 2 -type d \
          -exec ${pkgs.coreutils}/bin/chown --no-dereference ${cfg.user}:${cfg.group} '{}' + \
          -exec ${pkgs.coreutils}/bin/chmod 2770 '{}' +
      fi
    '';
  };
}
