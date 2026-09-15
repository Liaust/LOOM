# Rendered unit contract. Runtime execution is a separate explicitly supplied
# disposable Linux gate; evaluating this derivation is not systemd evidence.
{ pkgs, lib, self, fixtureRoot ? null }:
let
  evaluated = lib.nixosSystem {
    system = pkgs.stdenv.hostPlatform.system;
    specialArgs = { inherit self; };
    modules = [ ../modules/loom-project-applications.nix ({ ... }: {
      system.stateVersion = "26.05";
      boot.isContainer = true;
      networking.hostName = "application-fixture";
      users.groups.loom-fixture.gid = 1234;
      users.users.loom-fixture = { isSystemUser = true; uid = 1234; group = "loom-fixture"; };
      systemd.services."loom-project-applications@".serviceConfig.ExecStart = lib.mkIf (fixtureRoot != null) (lib.mkForce "${self.packages.${pkgs.stdenv.hostPlatform.system}.loom-service-manager}/bin/loom-service-manager --application-socket --application-fixture-config ${fixtureRoot}/helper-config.json");
      systemd.services.loom-project-applications-restore.serviceConfig.ExecStart = lib.mkIf (fixtureRoot != null) (lib.mkForce "${self.packages.${pkgs.stdenv.hostPlatform.system}.loom-service-manager}/bin/loom-service-manager --application-restore --application-fixture-config ${fixtureRoot}/helper-config.json");
      loom.projectApplications = {
        enable = true;
        nodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAV";
        installerUID = 1234;
        installerGroup = "loom-fixture";
        grants = [{
          owner = { project_id = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"; node_id = "node_01ARZ3NDEKTSV4RRFFQ69G5FAV"; resource = "fixture"; };
          repository_id = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV";
          policy_revision = "fixture-policy";
          location_revision = "fixture-location";
          max_memory_bytes = 67108864;
          data.files = { path = "/srv/fixture-pool/files"; pool = "/srv/fixture-pool"; };
          credentials = {};
        }];
      };
    }) ];
  };
  units = evaluated.config.systemd.units;
  socket = units."loom-project-applications.socket".text;
  helper = units."loom-project-applications@.service".text;
  application = units."loom-application@.service".text;
  restore = units."loom-project-applications-restore.service".text;
in
assert lib.hasInfix "--application-restore" restore;
assert lib.hasInfix "Type=oneshot" restore;
assert lib.hasInfix "systemd-tmpfiles-setup.service" restore;
assert builtins.elem "multi-user.target" evaluated.config.systemd.services.loom-project-applications-restore.wantedBy;
assert !(lib.hasInfix "--application-socket" restore);
assert lib.hasInfix "Accept=true" socket;
assert lib.hasInfix "SocketMode=0660" socket;
assert lib.hasInfix "StandardInput=socket" helper;
assert lib.hasInfix "--application-socket" helper;
assert lib.hasInfix "NoNewPrivileges=true" application;
assert lib.hasInfix "RestrictSUIDSGID=true" application;
assert !(lib.hasInfix "--allowlist" helper);
assert lib.hasInfix "/srv/fixture-pool" helper;
assert !(lib.hasInfix "/srv/fixture-pool/files" helper);
assert lib.hasInfix "IPAddressDeny=any" application;
assert lib.hasInfix "IPAddressAllow=localhost" application;
pkgs.runCommand "loom-project-applications-rendered" { passthru.rendered = { inherit socket helper application restore; policy = evaluated.config.environment.etc."loom/project-applications.json".text; helperConfig = evaluated.config.environment.etc."loom/project-applications-helper.json".text; }; } ''
  mkdir -p "$out"
  cp ${pkgs.writeText "socket" socket} "$out/loom-project-applications.socket"
  cp ${pkgs.writeText "helper" helper} "$out/loom-project-applications@.service"
  cp ${pkgs.writeText "restore" restore} "$out/loom-project-applications-restore.service"
  cp ${pkgs.writeText "application" application} "$out/loom-application@.service"
  cp ${pkgs.writeText "policy" evaluated.config.environment.etc."loom/project-applications.json".text} "$out/policy.json"
  cp ${pkgs.writeText "helper-config" evaluated.config.environment.etc."loom/project-applications-helper.json".text} "$out/helper-config.json"
''
