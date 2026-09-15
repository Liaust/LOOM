{ stdenvNoCC, lib, fetchurl, autoPatchelfHook, makeWrapper, chromium }:

stdenvNoCC.mkDerivation {
  pname = "agent-browser";
  version = "0.26.0";
  src = fetchurl {
    url = "https://github.com/vercel-labs/agent-browser/releases/download/v0.26.0/agent-browser-linux-x64";
    sha256 = "8784dc259abf72ee04e751b45677d956387af50c99aec5dcd7a41a9bc498e3c3";
  };
  dontUnpack = true;
  nativeBuildInputs = [ autoPatchelfHook makeWrapper ];
  installPhase = ''
    runHook preInstall
    install -Dm755 "$src" "$out/bin/agent-browser"
    install -Dm644 ${../files/agent-browser/SKILL.md} "$out/share/agent-browser/SKILL.md"
    wrapProgram "$out/bin/agent-browser" \
      --set AGENT_BROWSER_EXECUTABLE_PATH ${chromium}/bin/chromium \
      --run 'if [ "''${LOOM_BROWSER_RUNTIME_DIR:-}" = /run/loom-morathustra-browser ] || [ "''${LOOM_BROWSER_RUNTIME_DIR:-}" = /run/loom-mina-browser ]; then
        export XDG_CONFIG_HOME="$LOOM_BROWSER_RUNTIME_DIR/config"
        export XDG_CACHE_HOME="$LOOM_BROWSER_RUNTIME_DIR/cache"
        export AGENT_BROWSER_SOCKET_DIR="''${AGENT_BROWSER_SOCKET_DIR:-$LOOM_BROWSER_RUNTIME_DIR/sockets}"
      fi'
    runHook postInstall
  '';
  passthru = { inherit chromium; };
  meta = {
    description = "Pinned native browser driver with Nix-managed Chromium for LOOM agents";
    homepage = "https://github.com/vercel-labs/agent-browser";
    license = lib.licenses.asl20;
    mainProgram = "agent-browser";
    platforms = [ "x86_64-linux" ];
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
  };
}
