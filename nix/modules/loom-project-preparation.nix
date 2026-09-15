{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.projectPreparation;
  runtime = import ../lib/project-preparation-runtime.nix { inherit pkgs; };
  path = "/run/loom-project-preparation";
in {
  options.loom.projectPreparation = {
    enable = lib.mkEnableOption "the private single-flight preparation executor";
    callerUID = lib.mkOption { type = lib.types.ints.positive; description = "Fixed authenticated caller UID. No root caller."; };
    uidBase = lib.mkOption { type = lib.types.ints.positive; default = 268435456; description = "Reserved start of the fixed 65536 UID/GID range, audited against live accounts and subordinate allocations."; };
    otherReservations = lib.mkOption {
      type = lib.types.listOf (lib.types.submodule {
        options = {
          name = lib.mkOption { type = lib.types.nonEmptyStr; };
          base = lib.mkOption { type = lib.types.ints.unsigned; };
          count = lib.mkOption { type = lib.types.ints.positive; };
        };
      });
      # systemd DynamicUser and Nix's optional auto-allocate-uids reservation.
      default = [ { name = "systemd-dynamic"; base = 61184; count = 4352; } { name = "nix-auto-allocate"; base = 872415232; count = 8388608; } ];
      description = "Complete other container/allocator reservations on this node. Operator must include any additional ranges; no allocator is created here.";
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      { assertion = pkgs.stdenv.hostPlatform.isLinux; message = "Project preparation requires Linux."; }
      { assertion = cfg.uidBase >= 65536 && lib.mod cfg.uidBase 65536 == 0 && cfg.uidBase + 65536 < 4294967296; message = "Project preparation requires an aligned non-root UID/GID range."; }
      { assertion = cfg.callerUID < 4294967295 && (cfg.callerUID < cfg.uidBase || cfg.callerUID >= cfg.uidBase + 65536); message = "Caller cannot be in the payload range."; }
      { assertion = builtins.all (r: r.base + r.count <= 4294967296 && (r.base >= cfg.uidBase + 65536 || r.base + r.count <= cfg.uidBase)) cfg.otherReservations; message = "Preparation range overlaps another reservation."; }
    ];
    environment.etc."loom/project-preparation.json".text = builtins.toJSON {
      schema = "loom.preparation.v1";
      caller_uid = cfg.callerUID;
      uid_base = cfg.uidBase;
      reservations = cfg.otherReservations;
      runtime_manifest = "${runtime}/manifest.json";
      nspawn = "${pkgs.systemd}/bin/systemd-nspawn";
      getent = "${pkgs.getent}/bin/getent";
    };
    systemd.tmpfiles.rules = [ "d ${path} 0755 root root -" ];
    systemd.sockets.loom-project-preparation = {
      wantedBy = [ "sockets.target" ];
      after = [ "systemd-tmpfiles-setup.service" ];
      socketConfig = {
        ListenStream = "${path}/prepare.sock";
        Accept = false;
        FileDescriptorName = "preparation";
        FlushPending = true;
        SocketUser = toString cfg.callerUID;
        SocketMode = "0600";
        DirectoryMode = "0755";
        Backlog = 8;
        RemoveOnStop = true;
      };
    };
    systemd.services.loom-project-preparation = {
      requires = [ "loom-project-preparation.socket" ];
      after = [ "loom-project-preparation.socket" ];
      serviceConfig = {
        Type = "exec";
        ExecStart = "${self.packages.${pkgs.stdenv.hostPlatform.system}.loom-project-preparation}/bin/loom-project-preparation";
        User = "root";
        Group = "root";
        StandardInput = "null";
        StandardOutput = "null";
        StandardError = "journal";
        Delegate = true;
        DelegateSubgroup = "supervisor";
        MemoryMax = "3G";
        MemorySwapMax = 0;
        TasksMax = 128;
        CPUQuota = "100%";
        OOMPolicy = "kill";
        KillMode = "control-group";
        TimeoutStopSec = "10s";
        SendSIGKILL = true;
        Restart = "no";
        RuntimeMaxSec = "315s";
        PrivateMounts = true;
        ProtectSystem = "strict";
        ReadWritePaths = [ path "/sys/fs/cgroup" ];
        NoNewPrivileges = true;
        UMask = "0077";
        LimitCORE = 0;
        LimitNOFILE = 1024;
      };
    };
  };
}
