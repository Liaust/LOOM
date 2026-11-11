{ pkgs, lib, fetchgit, applyPatches, makeWrapper }:

let
  upstream = fetchgit {
    url = "https://github.com/trycua/cua.git";
    rev = "721e1b4fb3ab89a95a53b8afdec348449a5ed73c";
    hash = "sha256-B3fDk/pPuVRhmIM8X5bv4XmN5XxUTEFqn/W2mbKmVdI=";
    sparseCheckout = [
      "libs/cua-driver/rust"
      "libs/cua-driver/wayland-helper"
      "libs/cua-driver/compat-fixtures"
      "libs/cua-driver/tests/fixtures/shared/web/index.html"
      "libs/cua-driver/tests/perception-demo/evidence-manifest.schema.json"
      "libs/cua/Cargo.toml"
      "libs/cua/crates/cua-telemetry"
      "libs/cua/crates/cua-teleport/src/ux/targets.json"
      "libs/images/sandbox-images.json"
      "nix/cua-driver/package.nix"
    ];
  };
  patched = applyPatches {
    name = "cua-driver-bound-x11-source";
    src = upstream;
    patches = [ ../patches/cua-x11-bound-session.patch ];
  };
  native = import "${upstream}/nix/cua-driver/package.nix" {
    inherit pkgs;
    src = patched;
    sourceSubdir = "libs/cua-driver/rust";
  };
in native.overrideAttrs (old: {
  nativeBuildInputs = (old.nativeBuildInputs or []) ++ [ makeWrapper ];
  postFixup = (old.postFixup or "") + ''
    wrapProgram "$out/bin/cua-driver" \
      --set-default CUA_DRIVER_RS_TELEMETRY_ENABLED 0
  '';
  # Pure admission regressions need no running desktop or separate test host.
  doCheck = true;
  cargoTestFlags = [ "-p" "platform-linux" "--lib" "interaction::" ];
})
