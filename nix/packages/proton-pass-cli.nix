{
  autoPatchelfHook,
  fetchurl,
  glibc,
  lib,
  stdenv,
  stdenvNoCC,
}:

stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "proton-pass-cli";
  version = "2.3.3";

  src = fetchurl {
    url = "https://proton.me/download/pass-cli/${finalAttrs.version}/pass-cli-linux-x86_64";
    hash = "sha256-tbSaiz/Qr4gwwMGXnyjqDJDM7Oc/WQI6i8qCRdS2jak=";
  };

  dontUnpack = true;
  nativeBuildInputs = [ autoPatchelfHook ];
  buildInputs = [
    glibc
    stdenv.cc.cc.lib
  ];

  installPhase = ''
    runHook preInstall
    install -Dm755 "$src" "$out/bin/pass-cli"
    runHook postInstall
  '';

  meta = {
    description = "Official Proton Pass command-line client";
    homepage = "https://protonpass.github.io/pass-cli/";
    license = lib.licenses.gpl3Only;
    mainProgram = "pass-cli";
    platforms = [ "x86_64-linux" ];
    sourceProvenance = with lib.sourceTypes; [ binaryNativeCode ];
  };
})
