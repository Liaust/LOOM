{ lib, pkgs, ... }:

{
  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];

  time.timeZone = lib.mkDefault "UTC";
  i18n.defaultLocale = lib.mkDefault "en_US.UTF-8";

  networking.firewall.enable = lib.mkDefault true;

  services.openssh = {
    enable = lib.mkDefault true;
    openFirewall = lib.mkDefault true;
    settings = {
      PasswordAuthentication = lib.mkDefault false;
      KbdInteractiveAuthentication = lib.mkDefault false;
      PermitRootLogin = lib.mkDefault "no";
    };
  };

  services.journald.extraConfig = ''
    SystemMaxUse=1G
    RuntimeMaxUse=256M
  '';

  security.sudo.wheelNeedsPassword = lib.mkDefault true;

  environment.systemPackages = with pkgs; [
    curl
    git
    htop
    jq
    rsync
    tmux
    vim
  ];
}
