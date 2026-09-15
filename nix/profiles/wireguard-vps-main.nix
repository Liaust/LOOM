{ pkgs, ... }:

{
  environment.systemPackages = with pkgs; [
    wireguard-tools
  ];

  networking.firewall.checkReversePath = "loose";
  networking.firewall.interfaces.wg0.allowedTCPPorts = [
    8080
  ];

  loom.httpListenAddr = "10.44.0.2:8080";

  networking.wireguard.interfaces.wg0 = {
    ips = [ "10.44.0.2/24" ];
    privateKeyFile = "/etc/wireguard/loom-main.key";
    peers = [
      {
        publicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
        endpoint = "192.0.2.1:51820";
        allowedIPs = [ "10.44.0.0/24" ];
        persistentKeepalive = 25;
      }
    ];
  };

  systemd.services.loomd = {
    requires = [ "wireguard-wg0.service" ];
    after = [ "wireguard-wg0.service" ];
  };
}
