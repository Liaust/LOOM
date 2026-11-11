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
  extraBoxRoots = lib.mapAttrsToList (_: box: box.rootPath) cfg.boxes;
  extraBoxDirectories = [ "Projects" "Documents" "Notes" "Topics" "Library" ".loom" ];
  extraBoxACL = lib.concatStringsSep "," (
    [ "u:${cfg.user}:rwx" "d:u:${cfg.user}:rwx" "m::rwx" "d:m::rwx" ]
    ++ lib.optionals (lib.attrByPath [ "loom" "agents" "enable" ] false config) [ "u:agents:rwx" "d:u:agents:rwx" ]
  );
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
      {
        assertion = lib.all (key: builtins.match "[a-z][a-z0-9_-]{0,62}" key != null) (builtins.attrNames cfg.boxes);
        message = "LOOM Box keys must be stable lowercase filesystem-safe identifiers.";
      }
      {
        assertion = builtins.length extraBoxRoots == builtins.length (lib.unique extraBoxRoots);
        message = "LOOM additional Boxes must have distinct source roots.";
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
      "d ${cfg.dataDir}/boxes 0750 ${cfg.user} ${cfg.group} - -"
      "d ${cfg.dataDir}/temp 0750 ${cfg.user} ${cfg.group} - -"
      "f ${cfg.dataDir}/update.activation.lock 0660 ${cfg.user} ${cfg.group} - -"
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
      "a+ ${systemdPath boxParent} - - - - u:${cfg.user}:r-x,m::r-x"
      "d ${systemdPath cfg.boxPath} 0755 ${boxOwner} ${boxRootGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Projects"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Documents"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/Notes"} 2770 ${boxOwner} ${boxServiceGroup} - -"
      "d ${systemdPath "${cfg.boxPath}/.loom"} 0770 ${boxOwner} ${boxServiceGroup} - -"
    ] ++ lib.concatMap (root:
      [
        "d ${systemdPath (builtins.dirOf root)} 2770 ${boxOwner} ${boxServiceGroup} - -"
        "d ${systemdPath root} 2770 ${boxOwner} ${boxServiceGroup} - -"
      ] ++ map (area: "d ${systemdPath "${root}/${area}"} 2770 ${boxOwner} ${boxServiceGroup} - -") extraBoxDirectories
      ++ map (path: "a+ ${systemdPath path} - - - - ${extraBoxACL}") ([root] ++ map (area: "${root}/${area}") extraBoxDirectories)
    ) extraBoxRoots;

    system.activationScripts.loomAdditionalBoxAccess = lib.stringAfter [ "users" "loomBoxParentAcl" ] ''
      ${lib.optionalString (extraBoxRoots != [ ]) ''
        storage=${shellArg cfg.storageRoot}
        if [ ! -d "$storage" ] || [ "$(${pkgs.coreutils}/bin/readlink -e "$storage")" != "$storage" ]; then
          echo "LOOM additional Box archives require canonical existing storage" >&2
          exit 1
        fi
        for path in ${lib.escapeShellArgs ([ "${cfg.storageRoot}/Boxes" ] ++ lib.mapAttrsToList (key: _: "${cfg.storageRoot}/Boxes/${key}") cfg.boxes)}; do
          if [ -L "$path" ] || { [ -e "$path" ] && [ ! -d "$path" ]; }; then
            echo "LOOM additional Box archive path is not a real directory" >&2
            exit 1
          fi
        done
        # tmpfiles refuses the existing loomadmin -> loom parent ownership
        # transition. Provision only these exact managed directories here.
        ${pkgs.coreutils}/bin/install -d -o ${shellArg cfg.user} -g ${shellArg cfg.group} -m 2750 ${shellArg "${cfg.storageRoot}/Boxes"}
        ${lib.concatMapStringsSep "\n" (key: ''
          path=${shellArg "${cfg.storageRoot}/Boxes/${key}"}
          if [ ! -d "$path" ]; then
            ${pkgs.coreutils}/bin/install -d -o ${shellArg cfg.user} -g ${shellArg cfg.group} -m 2770 "$path"
          elif [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$path")" != ${shellArg "${cfg.user}:${cfg.group}"} ]; then
            echo "LOOM additional Box archive root owner is invalid" >&2
            exit 1
          fi
        '') (builtins.attrNames cfg.boxes)}
      ''}
      ${lib.concatMapStringsSep "\n" (root: ''
        for path in ${lib.escapeShellArgs ([root] ++ map (area: "${root}/${area}") extraBoxDirectories)}; do
          if [ -L "$path" ]; then
            echo "LOOM additional Box paths must not be symlinks" >&2
            exit 1
          fi
          if [ -d "$path" ]; then
            ${pkgs.acl}/bin/setfacl -m ${shellArg extraBoxACL} "$path"
          fi
        done
      '') extraBoxRoots}
    '';

    system.activationScripts.loomBoxParentAcl = lib.mkIf (cfg.manageBoxPaths && cfg.boxPath != null) ''
      if [ -d ${shellArg boxParent} ]; then
        ${pkgs.acl}/bin/setfacl -m u:${cfg.user}:r-x,g:${cfg.group}:--x,m::r-x ${shellArg boxParent}
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
