# Keep the supported OS baseline; backport only security release gaps here.
{ nixpkgs-security }:
final: prev:
let
  security = import nixpkgs-security { system = prev.stdenv.hostPlatform.system; };
in {
  buildGoModule = prev.buildGoModule.override { go = security.go_1_27; };
  inherit (security) nodejs_22 nodejs_26;
  caddy = prev.caddy.overrideAttrs (old: {
    version = "2.11.8";
    vendorHash = "sha256-0W8maqztlMNYSMNXivym57sj1B7XO+XZ6sz427spOLo=";
    src = prev.fetchFromGitHub {
      owner = "caddyserver";
      repo = "caddy";
      tag = "v2.11.8";
      hash = "sha256-6xwOnB9NBgvhusYUGm5HXz7t+d2duF/Bzb6nY54AIBA=";
    };
    ldflags = map (builtins.replaceStrings [ "v2.11.7" ] [ "v2.11.8" ]) old.ldflags;
    passthru = old.passthru // {
      dist = prev.fetchFromGitHub {
        owner = "caddyserver";
        repo = "dist";
        tag = "v2.11.8";
        hash = "sha256-v9VaNgkWekTPI5nt04QoyZeJRRvymK2q0o3JxygC2JA=";
      };
    };
  });
  apacheHttpd = prev.apacheHttpd.overrideAttrs (_: {
    version = "2.4.70";
    src = prev.fetchurl {
      url = "mirror://apache/httpd/httpd-2.4.70.tar.bz2";
      hash = "sha256-Hu/X3JWDbFhyiUzo5qN/dz1aZ2Twdmhp05uLzwsbr8M=";
    };
  });
  openssh = prev.openssh.overrideAttrs (_: {
    version = "10.6p2";
    src = prev.fetchurl {
      url = "mirror://openbsd/OpenSSH/portable/openssh-10.6p2.tar.gz";
      hash = "sha256-muRIcgkM8vBrPPn/FH5/FvMJZxNAdj7S+ZJIOYrinVs=";
    };
  });
  couchdb3 = prev.couchdb3.overrideAttrs (_: {
    version = "3.5.3";
    src = prev.fetchurl {
      url = "mirror://apache/couchdb/source/3.5.3/apache-couchdb-3.5.3.tar.gz";
      hash = "sha256-rguzdMyJkA1ssd+42OgMeZGG3PLRQ8qIYpbCMK9CCJY=";
    };
  });
  borgbackup = prev.borgbackup.overridePythonAttrs (_: {
    version = "1.4.5";
    src = prev.fetchFromGitHub {
      owner = "borgbackup";
      repo = "borg";
      tag = "1.4.5";
      hash = "sha256-jkFF+nNZL8lEXgworFLNoEKTr2LgCat1fOFlTh7AWs4=";
    };
  });
}
