let
  source = (import ./source-flake.nix {}).nixosConfigurations.hardware-main;
  # Account/ACL retirement does not require realizing the Linux Hermes package.
  s = source.extendModules {
    modules = [ ({ lib, ... }: { loom.hermesPlatform.enable = lib.mkForce false; }) ];
  };
  c = s.config;
  old = (s.extendModules { modules = [ ({ lib, ... }: { loom.orca.enable = lib.mkForce true; }) ]; }).config;
  account = cfg: builtins.removeAttrs cfg.users.users.agents [ "packages" ];
  packages = map (p: p.pname or p.name) c.environment.systemPackages;
in {
  failures = map (a: a.message) (builtins.filter (a: !a.assertion) c.assertions);
  agentsEnabled = c.loom.agents.enable;
  orcaDisabled = !c.loom.orca.enable;
  noOrcaUnit = !(c.systemd.units ? "orca-serve.service");
  noOrcaPackage = builtins.all (name: builtins.match "orca-.*" name == null) packages;
  noOrcaPort = !(builtins.elem 6768 c.networking.firewall.interfaces.wg0.allowedTCPPorts);
  accountPreserved = account c == account old;
  sshPreserved = c.programs.ssh.extraConfig == old.programs.ssh.extraConfig;
  aclPreserved = c.system.activationScripts.loomAgentsAccess.text == old.system.activationScripts.loomAgentsAccess.text;
  shellPreserved = c.system.activationScripts.loomAgentsShell.text == old.system.activationScripts.loomAgentsShell.text;
  nativeCodex = c.loom.codexRuntime.enable && c.loom.codexJobs.enable;
  nativeHermes = source.config.loom.hermesPlatform.enable
    && source.config.loom.mina.selected && !source.config.loom.mina.enable
    && source.config.systemd.services ? loom-hermes-platform
    && source.config.systemd.services ? loom-hermes-scheduler;
  retainedOptIn = old.systemd.services ? orca-serve && old.loom.agents.enable;
  noPasswordOverride = !(c.users.users.agents.hashedPassword != null || c.users.users.agents.password != null);
}
