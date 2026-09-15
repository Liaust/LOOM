{ hermes-upstream, system }:

# Standalone callable contract; no module, activation hook or flake export.
# The integrator selects this package plus patched hermes-agent and binds only
# HERMES_MAC_BINDING / HERMES_CUA_DRIVER_CMD in the chosen TUI/gateway scopes.
let
  pkgs = hermes-upstream.inputs.nixpkgs.legacyPackages.${system};
  hermes = hermes-upstream.packages.${system}.messaging;
  python = hermes.hermesVenv;
  openssh = pkgs.openssh;
in
assert hermes-upstream.rev == "29112bef099274229cadff79cdff7bf7b99c4b77";
assert hermes.version == "0.21.0";
pkgs.runCommand "loom-mac-computer-use-bridge-0.1.0" {
  # Selected host modules override only this public identity pin.
  pinnedBindingSHA256 = "";
  passthru = {
    inherit python openssh;
    bridgeRelative = "bin/loom-mac-computer-use-bridge";
    endpointRelative = "bin/loom-mac-endpoint";
    payloadRelative = "share/loom-mac-computer-use";
    setupRelative = "bin/loom-mac-computer-use-setup";
    schemaRelative = "share/loom-mac-computer-use/endpoint-binding.schema.json";
    endpointDestination = "/usr/local/libexec/loom-mac-endpoint";
    endpointConfig = "/usr/local/etc/loom-mac-endpoint.json";
    runtimeRebind = "Freshly verified PID/socket turnover expires targets; stable host/user/driver/policy changes require reviewed setup.";
  };
  meta = {
    description = "Inactive pinned Main-to-Mac MCP transport and offline setup";
    platforms = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
  };
} ''
  mkdir -p "$out/bin" "$out/share/loom-mac-computer-use"
  cp ${../files/hermes-mac-computer-use/bridge.py} "$out/share/loom-mac-computer-use/bridge.py"
  cp ${../files/hermes-mac-computer-use/mac_endpoint.py} "$out/share/loom-mac-computer-use/mac_endpoint.py"
  cp ${../files/mac-computer-use/endpoint-binding.schema.json} "$out/share/loom-mac-computer-use/endpoint-binding.schema.json"
  cp ${../files/mac-computer-use/ssh_config.template} "$out/share/loom-mac-computer-use/ssh_config.template"
  cp ${../locks/mac-computer-use.json} "$out/share/loom-mac-computer-use/dependency_lock.json"
  ${python}/bin/python3 -B - "$out" <<'PY'
import hashlib, json, os, pathlib, re, sys
out = pathlib.Path(sys.argv[1])
payload = out / 'share/loom-mac-computer-use'
pin = os.environ['pinnedBindingSHA256'] or None
assert pin is None or re.fullmatch('[0-9a-f]{64}', pin)
for name, module in [('loom-mac-computer-use-bridge', 'bridge'), ('loom-mac-endpoint', 'mac_endpoint')]:
    invocation = ('import bridge\nbridge.PINNED_BINDING_SHA256 = ' + repr(pin) + '\nbridge.main()\n'
                  if module == 'bridge' else 'from mac_endpoint import main\nmain()\n')
    (out / 'bin' / name).write_text('#!${python}/bin/python3 -I\nimport sys\nsys.dont_write_bytecode = True\nsys.path.insert(0, ' + repr(str(payload)) + ')\n' + invocation)
setup = pathlib.Path('${../../scripts/loom-mac-computer-use-setup}').read_text()
setup = setup.replace('#!/usr/bin/env python3', '#!${python}/bin/python3 -I', 1)
setup = setup.replace('PACKAGE_DIR = None', 'PACKAGE_DIR = ' + repr(str(payload)), 1)
setup = setup.replace('PACKAGE_SSH = None', "PACKAGE_SSH = '${openssh}/bin/ssh'", 1)
(out / 'bin/loom-mac-computer-use-setup').write_text(setup)
for path in (out / 'bin').iterdir():
    path.chmod(0o555)
    compile(path.read_bytes(), str(path), 'exec')
for name in ('bridge.py', 'mac_endpoint.py'):
    compile((payload / name).read_bytes(), name, 'exec')
(payload / 'payload-sha256.json').write_text(json.dumps({p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(payload.iterdir())}, sort_keys=True) + '\n')
PY
''
