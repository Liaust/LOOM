{ ... }:

{
  networking.firewall.checkReversePath = "loose";

  # LOOM HTTP and RDP are private-network services. They are intentionally not
  # opened on public/LAN interfaces by this production config.
  networking.firewall.interfaces.wg0.allowedTCPPorts = [
    8080
    3389
  ];

  loom.httpListenAddr = "10.44.0.2:8080";

  networking.wireguard.interfaces.wg0 = {
    ips = [ "10.44.0.2/24" ];
    privateKeyFile = "/etc/wireguard/loom-main.key";
    peers = [
      {
        # loom-vps WireGuard public key.
        publicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
        endpoint = "192.0.2.1:51820";
        allowedIPs = [
          "10.44.0.0/24"
        ];
        persistentKeepalive = 25;
      }
    ];
  };

  systemd.services.loomd = {
    requires = [ "wireguard-wg0.service" ];
    after = [ "wireguard-wg0.service" ];
  };
}
