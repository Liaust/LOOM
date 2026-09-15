{ lib, ... }:

{
  imports = [
    ./base-server.nix
    ../modules/loom-base.nix
    ../modules/loom-storage.nix
    ../modules/loom-postgres.nix
    ../modules/loom-embeddings.nix
    ../modules/loom-notes-ai.nix
    ../modules/loom-service.nix
    ../modules/loom-restore-authority.nix
    ../modules/loom-caddy.nix
  ];

  loom = {
    enable = true;
    environment = "production-placeholder";
    autoMigrate = true;
    storage.enable = true;
    postgres.enable = true;
    service.enable = true;
    restoreAuthority.enable = true;
  };

  services.openssh.openFirewall = lib.mkDefault true;
}
