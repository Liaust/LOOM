{ lib, stdenv, fetchurl, dpkg, autoPatchelfHook, openblas, postgresql }:

assert stdenv.hostPlatform.system == "x86_64-linux";
assert lib.versions.major postgresql.version == "17";

# The upstream PG17 release is repackaged into a Nix extension output. No Debian
# package manager or maintainer script runs on the host.
stdenv.mkDerivation {
  pname = "pg-search-bin";
  version = "0.25.11";
  src = fetchurl {
    url = "https://github.com/paradedb/paradedb/releases/download/v0.25.11/postgresql-17-pg-search_0.25.11-1PARADEDB-bookworm_amd64.deb";
    hash = "sha256-SUKfw1/CGEvNtEt72u36v+FEAsW2FKrXAv8IQ0wcRTw=";
  };

  nativeBuildInputs = [ dpkg autoPatchelfHook ];
  buildInputs = [ stdenv.cc.cc.lib openblas ];
  unpackPhase = ''
    runHook preUnpack
    dpkg-deb -x "$src" unpacked
    runHook postUnpack
  '';
  dontConfigure = true;
  dontBuild = true;
  installPhase = ''
    runHook preInstall
    mkdir -p "$out/lib" "$out/share/postgresql/extension" "$out/share/doc/pg_search"
    cp unpacked/usr/lib/postgresql/17/lib/pg_search.so "$out/lib/"
    cp unpacked/usr/share/postgresql/17/extension/pg_search* "$out/share/postgresql/extension/"
    cp -r unpacked/usr/share/doc/postgresql-17-pg-search/. "$out/share/doc/pg_search/"
    runHook postInstall
  '';
  passthru = {
    inherit postgresql;
    sourceCommit = "e66d6b01be8054694d18b1132d8300223f379ac4";
  };
  meta = {
    description = "Pinned upstream PostgreSQL 17 pg_search binary for the Notes search evaluation";
    homepage = "https://github.com/paradedb/paradedb/tree/v0.25.11";
    license = lib.licenses.agpl3Only;
    platforms = [ "x86_64-linux" ];
  };
}
