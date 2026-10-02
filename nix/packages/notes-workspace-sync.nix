{ lib, buildNpmPackage, fetchurl, nodejs_22, python3, git, makeWrapper }:

buildNpmPackage {
  pname = "loom-notes-workspace-sync";
  version = "1.0.32-loom1";
  src = fetchurl {
    name = "obsidian-livesync-1.0.32.tar.gz";
    url = "https://codeload.github.com/vrtmrz/obsidian-livesync/tar.gz/7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b";
    sha256 = "d427a25cac9a124610fb25d0747dc3dc329a7a486475841ba032652859b04403";
  };
  nodejs = nodejs_22;
  npmDepsHash = "sha256-JUQGzlMSZKM1qPJ7iPDPlt3WruEg20i5QkdszK1GZ9k=";
  npmRebuildFlags = [ "--ignore-scripts" ];
  npmBuildFlags = [ "--workspace" "self-hosted-livesync-cli" ];
  nativeBuildInputs = [ python3 git makeWrapper ];
  postConfigure = ''
    npm rebuild leveldown --offline --no-audit --no-fund
    mkdir .loom-overlays
    cp -r ${../../modules/notes-workspace-client} .loom-overlays/notes-workspace-client
    cp -r ${../../modules/notes-workspace-sync} .loom-overlays/notes-workspace-sync
    python3 .loom-overlays/notes-workspace-client/apply.py "$PWD"
    python3 .loom-overlays/notes-workspace-sync/overlay.py "$PWD"
  '';
  installPhase = ''
    runHook preInstall
    mkdir -p "$out/lib/notes-workspace-sync" "$out/bin"
    cp -r node_modules "$out/lib/notes-workspace-sync/"
    mkdir -p "$out/lib/notes-workspace-sync/src"
    cp -r src/apps "$out/lib/notes-workspace-sync/src/"
    cp LICENSE package.json package-lock.json "$out/lib/notes-workspace-sync/"
    makeWrapper ${nodejs_22}/bin/node "$out/bin/loom-notes-workspace-sync" \
      --add-flags "$out/lib/notes-workspace-sync/src/apps/cli/dist/index.cjs"
    runHook postInstall
  '';
  meta = {
    description = "Pinned native LiveSync transport with LOOM source-intent IPC";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
    mainProgram = "loom-notes-workspace-sync";
  };
}
