{ config, lib, ... }:

let
  cfg = config.loom;
  caddyCfg = cfg.caddy;
  privatePrefixes = [
    "10."
    "127."
    "192.168."
  ] ++ map (n: "172.${toString n}.") (lib.range 16 31);
  isPrivateListenAddress =
    address:
    address == "localhost" || lib.any (prefix: lib.hasPrefix prefix address) privatePrefixes;
  listenSite = "http://${caddyCfg.listenAddress}:${toString caddyCfg.listenPort}";
in
{
  options.loom.caddy = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the private LOOM Caddy reverse proxy.";
    };

    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "10.44.0.2";
      description = "Private interface address where Caddy listens.";
    };

    listenPort = lib.mkOption {
      type = lib.types.port;
      default = 8081;
      description = "Private Caddy listener port.";
    };

    upstreamURL = lib.mkOption {
      type = lib.types.str;
      default =
        if cfg.httpListenAddr != "" then
          "http://${cfg.httpListenAddr}"
        else
          "http://127.0.0.1:8080";
      description = "Private loomd HTTP upstream URL.";
    };

    allowedInterface = lib.mkOption {
      type = lib.types.str;
      default = "wg0";
      description = "Firewall interface where the private Caddy port may be opened.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Open the Caddy listener port on the configured private interface.";
    };

    extraConfig = lib.mkOption {
      type = lib.types.lines;
      default = "";
      description = "Additional Caddy directives for the private LOOM virtual host.";
    };
  };

  config = lib.mkIf (cfg.enable && caddyCfg.enable) {
    assertions = [
      {
        assertion = isPrivateListenAddress caddyCfg.listenAddress;
        message = "loom.caddy.listenAddress must be loopback or RFC1918 private in v0.1.";
      }
      {
        assertion = caddyCfg.listenPort != 80 && caddyCfg.listenPort != 443;
        message = "loom.caddy.listenPort must not use public HTTP/HTTPS ports in v0.1.";
      }
    ];

    services.caddy = {
      enable = true;
      virtualHosts.${listenSite}.extraConfig = ''
        encode zstd gzip
        reverse_proxy ${caddyCfg.upstreamURL}
        ${caddyCfg.extraConfig}
      '';
    };

    networking.firewall.interfaces.${caddyCfg.allowedInterface}.allowedTCPPorts =
      lib.mkIf caddyCfg.openFirewall [
        caddyCfg.listenPort
      ];
  };
}

