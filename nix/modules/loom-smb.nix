{ config, lib, pkgs, ... }:

let
  cfg = config.loom;
  smbCfg = cfg.smb;
  sambaBindInterfaces =
    if smbCfg.bindInterfaces == [ ] then
      smbCfg.interfaces
    else
      smbCfg.bindInterfaces;
  sambaList = values: lib.concatStringsSep " " values;
  effectiveStorageShareName =
    if smbCfg.shareName == null then smbCfg.storageShareName else smbCfg.shareName;
  effectiveStoragePath =
    if smbCfg.path == null then smbCfg.storagePath else smbCfg.path;
  effectiveBoxPath = if smbCfg.boxPath == null then "" else smbCfg.boxPath;
  shareRoots = [ effectiveBoxPath effectiveStoragePath ];
  shareSettings = path: readOnly: forceUser: forceGroup: {
    inherit path;
    browseable = "yes";
    "read only" = if readOnly then "yes" else "no";
    "guest ok" = "no";
    "valid users" = smbCfg.user;
    "force user" = forceUser;
    "force group" = forceGroup;
    "create mask" = "0660";
    "directory mask" = "0770";
    "wide links" = "no";
    # Finder may create .DS_Store, AppleDouble, resource-fork, and xattr
    # sidecars while copying. Do not reject those at the SMB protocol layer.
    "ea support" = "yes";
    "store dos attributes" = "yes";
    "vfs objects" = "catia fruit streams_xattr";
    "fruit:metadata" = "stream";
    "fruit:resource" = "stream";
    "fruit:posix_rename" = "yes";
  };
  shareRootCheck = pkgs.writeShellScript "loom-smb-share-root-check" ''
    set -eu

    check_root() {
      label="$1"
      candidate="$2"
      if [ ! -d "$candidate" ]; then
        echo "LOOM SMB $label root is missing or not a directory: $candidate" >&2
        exit 1
      fi
      resolved="$(${pkgs.coreutils}/bin/realpath -e -- "$candidate")" || {
        echo "LOOM SMB $label root cannot be resolved safely: $candidate" >&2
        exit 1
      }
      if [ "$resolved" != "$candidate" ]; then
        echo "LOOM SMB $label root is symlinked or non-canonical: $candidate -> $resolved" >&2
        exit 1
      fi
    }

    check_root loom-main-box ${lib.escapeShellArg effectiveBoxPath}
    check_root loom-storage ${lib.escapeShellArg effectiveStoragePath}
  '';
in
{
  options.loom.smb = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable private SMB exports for the canonical LOOM Main Box and Storage roots.";
    };

    boxShareName = lib.mkOption {
      type = lib.types.str;
      default = "loom-main-box";
      description = "Samba share name for the active configured LOOM Main Box root.";
    };

    boxPath = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = cfg.boxPath;
      description = "Active configured LOOM Main Box root exported read/write.";
    };

    storageShareName = lib.mkOption {
      type = lib.types.str;
      default = "loom-storage";
      description = "Samba share name for canonical physical LOOM Storage.";
    };

    storagePath = lib.mkOption {
      type = lib.types.str;
      default = cfg.storageRoot;
      description = "Canonical physical LOOM Storage root exported read-only.";
    };

    # One-release compatibility for profiles that still name the former single
    # storage share. These inputs can affect only the read-only Storage share.
    shareName = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Deprecated alias for loom.smb.storageShareName.";
    };

    path = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Deprecated alias for loom.smb.storagePath.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "loomshare";
      description = "Dedicated Unix/Samba account allowed to authenticate to both LOOM Main SMB shares.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = cfg.group;
      description = "Primary Unix group for the dedicated SMB account.";
    };

    boxForceUser = lib.mkOption {
      type = lib.types.str;
      default = if cfg.boxOwner != "" then cfg.boxOwner else cfg.user;
      description = "Unix owner forced for read/write Box operations.";
    };

    boxForceGroup = lib.mkOption {
      type = lib.types.str;
      default = cfg.group;
      description = "Unix group forced for read/write Box operations.";
    };

    storageForceUser = lib.mkOption {
      type = lib.types.str;
      default = cfg.user;
      description = "Unix owner used while reading canonical Storage.";
    };

    storageForceGroup = lib.mkOption {
      type = lib.types.str;
      default = cfg.group;
      description = "Unix group used while reading canonical Storage.";
    };

    interfaces = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "wg0" ];
      description = "Private network interfaces where TCP/445 may be opened.";
    };

    bindInterfaces = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Samba interfaces/address entries written to smb.conf. Defaults to loom.smb.interfaces. WireGuard deployments should prefer address/CIDR entries such as 10.44.0.2/24.";
    };

    afterServices = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Systemd services Samba should start after and want, such as wireguard-wg0.service.";
    };

    allowedHosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "10.44.0." ];
      description = "Samba hosts allow entries for private LOOM clients.";
    };
  };

  config = lib.mkIf (cfg.enable && smbCfg.enable) {
    assertions = [
      {
        assertion = effectiveBoxPath != "" && lib.hasPrefix "/" effectiveBoxPath;
        message = "loom.smb.boxPath must be a configured absolute filesystem path.";
      }
      {
        assertion = lib.hasPrefix "/" effectiveStoragePath;
        message = "loom.smb.storagePath must be an absolute filesystem path.";
      }
      {
        assertion = smbCfg.boxShareName != "" && effectiveStorageShareName != "" && smbCfg.boxShareName != effectiveStorageShareName;
        message = "loom.smb Box and Storage share names must be non-empty and distinct.";
      }
      {
        assertion = effectiveBoxPath != effectiveStoragePath;
        message = "loom.smb Box and Storage paths must be distinct.";
      }
      {
        assertion = effectiveBoxPath != cfg.storageExportRoot && (!cfg.filesystemCutoverEnabled || effectiveStoragePath != cfg.storageExportRoot);
        message = "loom.smb shares must not target the retired generated storage export after filesystem cutover.";
      }
      {
        assertion = smbCfg.interfaces != [ ];
        message = "loom.smb.interfaces must include at least one private interface.";
      }
      {
        assertion = sambaBindInterfaces != [ ];
        message = "loom.smb.bindInterfaces or loom.smb.interfaces must include at least one Samba bind interface or address.";
      }
      {
        assertion = smbCfg.allowedHosts != [ ];
        message = "loom.smb.allowedHosts must include at least one private host or subnet.";
      }
    ];

    users.groups.${smbCfg.group} = { };
    users.users.${smbCfg.user} = {
      isSystemUser = true;
      group = smbCfg.group;
      home = cfg.dataDir;
      description = "LOOM Main SMB account";
    };

    services.samba = {
      enable = true;
      openFirewall = false;
      settings = {
        global = {
          workgroup = "WORKGROUP";
          "server string" = "LOOM Main private storage";
          security = "user";
          "map to guest" = "Never";
          "bind interfaces only" = "yes";
          interfaces = sambaList sambaBindInterfaces;
          "hosts allow" = sambaList smbCfg.allowedHosts;
          "smb ports" = "445";
          "server min protocol" = "SMB3";
        };

        ${smbCfg.boxShareName} = shareSettings effectiveBoxPath false smbCfg.boxForceUser smbCfg.boxForceGroup;
        ${effectiveStorageShareName} = shareSettings effectiveStoragePath true smbCfg.storageForceUser smbCfg.storageForceGroup;
      };
    };

    networking.firewall.interfaces = lib.genAttrs smbCfg.interfaces (_: {
      allowedTCPPorts = [ 445 ];
    });

    systemd.services = {
      samba-smbd = {
        wants = smbCfg.afterServices;
        after = smbCfg.afterServices;
        serviceConfig.ExecStartPre = [ shareRootCheck ];
        unitConfig.RequiresMountsFor = shareRoots;
      };
      samba-nmbd = {
        wants = smbCfg.afterServices;
        after = smbCfg.afterServices;
        serviceConfig.ExecStartPre = [ shareRootCheck ];
        unitConfig.RequiresMountsFor = shareRoots;
      };
      samba-winbindd = {
        wants = smbCfg.afterServices;
        after = smbCfg.afterServices;
        serviceConfig.ExecStartPre = [ shareRootCheck ];
        unitConfig.RequiresMountsFor = shareRoots;
      };
    };
  };
}
