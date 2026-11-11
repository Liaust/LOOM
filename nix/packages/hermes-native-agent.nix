{ pkgs, hermes-runtime-upstream, system, chromium ? null }:

let
  revision = "d923a5eb487a17c0a68801bba9ef0f56c81040a8";
  source = pkgs.applyPatches {
    name = "hermes-native-source";
    src = hermes-runtime-upstream;
    patches = [
      ./hermes-sherpa-runtime.patch
      ./hermes-nix-plugin-selection.patch
      ./hermes-nix-plugin-update.patch
      ./hermes-mcp-lookup-discovery.patch
      ./hermes-wake-reliability.patch
      ./hermes-security-dependencies.patch
      ./hermes-tornado-security.patch
      ./hermes-security-browser-harness.patch
      ./hermes-cua-token-compat.patch
      ./hermes-cua-native-menu.patch
      ./hermes-mcp-startup.patch
      ./hermes-sqlite-runtime.patch
      ./hermes-profile-context.patch
      ./hermes-bot-browser-lifetime.patch
      ./hermes-bot-browser-desktop.patch
    ];
    postPatch = ''
      cp ${../files/hermes-platform/shared_browser.py} tools/bot_desktop/shared_browser.py
      cp ${./hermes-opentelemetry-context.patch} nix/loom-opentelemetry-context.patch
      cp ${./hermes-sqlite.nix} nix/loom-sqlite.nix
      cp ${./hermes-sqlite-test-cleanup.patch} nix/hermes-sqlite-test-cleanup.patch
    '';
  };
  browserPrefix = pkgs.lib.optionalString (chromium != null) ''
    export AGENT_BROWSER_EXECUTABLE_PATH="''${AGENT_BROWSER_EXECUTABLE_PATH:-${chromium}/bin/chromium}"
  '';
  desktopPrefix = pkgs.lib.optionalString pkgs.stdenv.isLinux ''
    if [[ -x /run/current-system/sw/bin/loom-hermes-desktop ]]; then
      export HERMES_CUA_DRIVER_CMD="''${HERMES_CUA_DRIVER_CMD:-/run/current-system/sw/bin/loom-hermes-desktop}"
    elif [[ -x /run/current-system/sw/bin/loom-main-desktop ]]; then
      export HERMES_CUA_DRIVER_CMD="''${HERMES_CUA_DRIVER_CMD:-/run/current-system/sw/bin/loom-main-desktop}"
    fi
  '';
  upstream = pkgs.callPackage "${source}/nix/hermes-agent.nix" {
    inherit (hermes-runtime-upstream.inputs) uv2nix pyproject-nix pyproject-build-systems;
    npm-lockfile-fix = hermes-runtime-upstream.inputs.npm-lockfile-fix.packages.${system}.default;
    rev = revision;
    lastModified = hermes-runtime-upstream.lastModified;
    version = "0.22.2";
    extraDependencyGroups = [ "messaging" "voice" "edge-tts" "wake-sherpa" "wake-openwakeword" "piper" ];
  };
in
assert hermes-runtime-upstream.rev == revision;
upstream.overrideAttrs (old: {
  postInstall = (old.postInstall or "") + ''
    ${upstream.hermesVenv}/bin/python3 - <<'PY'
import sqlite3
import tornado
from hermes_cli.sqlite_runtime import is_sqlite_wal_reset_vulnerable
assert tornado.version_info >= (6, 6), tornado.version
assert sqlite3.sqlite_version_info >= (3, 51, 3), sqlite3.sqlite_version
assert not is_sqlite_wal_reset_vulnerable(sqlite3.sqlite_version_info)
PY
    # Nix skill sources have epoch timestamps. Scope ZIP compatibility to the
    # native backup command; never rewrite the source files or their mtimes.
    ${upstream.hermesVenv}/bin/python3 - "$out" <<'PY'
import pathlib, re, sys
out = pathlib.Path(sys.argv[1])
wrapper = out / 'bin/hermes'
text = wrapper.read_text()
matches = list(re.finditer(r'^exec "(/nix/store/[^"\n]+/bin/hermes)"  "\$@" *$', text, re.M))
if len(matches) != 1:
    raise SystemExit('native Hermes launcher changed; review backup compatibility')
entry = matches[0].group(1)
shim = out / 'share/loom-hermes-backup'
shim.mkdir()
code = """import sys
if sys.argv[:2] == [ENTRY, "backup"]:
    import zipfile
    original = zipfile.ZipFile.__init__
    def initialize(self, file, mode="r", *args, **kwargs):
        if mode in ("w", "x", "a"):
            kwargs.setdefault("strict_timestamps", False)
        return original(self, file, mode, *args, **kwargs)
    zipfile.ZipFile.__init__ = initialize
""".replace('ENTRY', repr(entry))
(shim / 'sitecustomize.py').write_text(code)
prefix = 'if [[ "' + chr(36) + '{1-}" == backup ]]; then\n  umask 0077\n  export PYTHONPATH="' + str(shim) + chr(36) + '{PYTHONPATH:+:$PYTHONPATH}"\nfi\n'
wrapper.write_text(text[:matches[0].start()] + ${builtins.toJSON (browserPrefix + desktopPrefix)} + prefix + text[matches[0].start():])
PY
    ${upstream.hermesVenv}/bin/python3 - "$out/bin/hermes" <<'PY'
import subprocess, sys
result = subprocess.run([sys.argv[1], "--help"], check=True,
                        capture_output=True, text=True, timeout=30)
assert "usage: hermes" in result.stdout
PY
  '';
  passthru = (old.passthru or { }) // {
    upstreamRevision = revision;
    runtimeOwner = "native-hermes";
  };
})
