{ lib, hermes-upstream, system }:

let
  # Native upstream CLI, gateway and TUI, with Discord/Telegram/Slack
  # dependencies already in the immutable Python environment.
  upstream = hermes-upstream.packages.${system}.messaging;
  centeredTui = upstream.hermesTui.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ../patches/hermes-centered-skin-banner.patch ];
    patchFlags = [ "-p1" "--fuzz=0" ];
  });
  packagingPython = hermes-upstream.inputs.nixpkgs.legacyPackages.${system}.python3;
  patchTool = hermes-upstream.inputs.nixpkgs.legacyPackages.${system}.patch;
  revision = "29112bef099274229cadff79cdff7bf7b99c4b77";
in
assert hermes-upstream.rev == revision;
assert upstream.version == "0.21.0";
upstream.overrideAttrs (old: {
  postInstall = (old.postInstall or "") + ''
    # Replace only the TUI output link; Python, the sealed venv and gateways
    # continue to use their pinned upstream closures.
    test -L "$out/ui-tui"
    ln -sfn ${centeredTui}/lib/hermes-tui "$out/ui-tui"
    # Keep a complete immutable source view so upstream TUI child spawning and
    # import hardening retain the patched tools without exposing cwd packages.
    # All unchanged modules remain links into the pinned wheel, not copies.
    ${packagingPython}/bin/python3 - "$out" <<'PYMACPATCH'
import hashlib, json, pathlib, shutil, subprocess, sys
out = pathlib.Path(sys.argv[1])
lock = json.loads(pathlib.Path("${../locks/mac-computer-use.json}").read_text())
source = pathlib.Path("${upstream.hermesVenv}/lib/python3.12/site-packages")
patch = pathlib.Path("${../patches/hermes-mac-computer-use.patch}")
owners = ["tools/computer_use/cua_backend.py", "tools/computer_use/tool.py", "tools/computer_use/backend.py"]
lines = patch.read_text().splitlines()
assert [s[6:] for s in lines if s.startswith("--- a/")] == owners
assert [s[6:] for s in lines if s.startswith("+++ b/")] == owners
for name in owners + ["tools/computer_use/schema.py", "tools/computer_use_tool.py", "tools/browser_tool.py"]:
    assert hashlib.sha256((source / name).read_bytes()).hexdigest() == lock["hermes"]["file_sha256"][name], name
overlay = out / "share/loom-hermes-native"
(overlay / "tools").mkdir(parents=True)
wheel = (source / "hermes_bootstrap.py").resolve().parent
for child in wheel.iterdir():
    if child.name not in {"tools", "hermes_cli", "__pycache__"} and child.suffix != ".pyc":
        (overlay / child.name).symlink_to(child)
# The native CLI resolves its symlinked package back into the original wheel
# and prepends it to sys.path before gateway imports. Bind that one bootstrap
# expression to this immutable source; leave all other CLI modules unchanged.
(overlay / "hermes_cli").mkdir()
for child in (wheel / "hermes_cli").iterdir():
    if child.name not in {"main.py", "__pycache__"} and child.suffix != ".pyc":
        (overlay / "hermes_cli" / child.name).symlink_to(child)
cli_source = (wheel / "hermes_cli/main.py").read_text()
bootstrap = '_bootstrap_root = os.path.realpath(os.path.join(os.path.dirname(__file__), os.pardir))'
assert cli_source.count(bootstrap) == 1, 'pinned CLI bootstrap changed'
cli_source = cli_source.replace(bootstrap, '_bootstrap_root = ' + repr(str(overlay)), 1)
cli_target = overlay / "hermes_cli/main.py"
cli_target.write_text(cli_source)
compile(cli_source, str(cli_target), "exec")
for child in (source / "tools").iterdir():
    if child.name not in {"computer_use", "__pycache__"} and child.suffix != ".pyc":
        (overlay / "tools" / child.name).symlink_to(child)
shutil.copytree(source / "tools/computer_use", overlay / "tools/computer_use",
                ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
for child in (overlay / "tools/computer_use").rglob("*"):
    child.chmod(0o755 if child.is_dir() else 0o644)
(overlay / "tools/computer_use").chmod(0o755)
subprocess.run(["${patchTool}/bin/patch", "-p1", "--batch", "--fuzz=0", "-i", str(patch)], cwd=overlay, check=True)
for name in owners:
    assert (source / name).read_bytes() != (overlay / name).read_bytes(), name
    compile((overlay / name).read_bytes(), name, "exec")
(overlay / "patch-identity.json").write_text(json.dumps({
    "upstream_revision": lock["hermes"]["revision"],
    "patch_sha256": hashlib.sha256(patch.read_bytes()).hexdigest(),
    "files": {name: hashlib.sha256((overlay / name).read_bytes()).hexdigest() for name in owners}
}, sort_keys=True) + "\n")
PYMACPATCH
    # Validate the pinned entrypoint before adding an immutable, backup-only
    # import shim. Never copy the venv or normalize mutable source timestamps.
    ${packagingPython}/bin/python3 - "$out" <<'PYCOMPAT'
import pathlib, re, subprocess, sys
out = pathlib.Path(sys.argv[1])
wrapper = out / "bin/hermes"
text = wrapper.read_text()
entries = re.findall(r'^exec "(/nix/store/[^"\n]+/bin/hermes)"  "\$@" *$', text, re.M)
py = re.findall(r"^export HERMES_PYTHON='(/nix/store/[^'\n]+/bin/python3)'$", text, re.M)
if len(entries) != 1 or len(py) != 1 or sum(line.lstrip().startswith("exec ") for line in text.splitlines()) != 1 or pathlib.Path(entries[0]).parent != pathlib.Path(py[0]).parent:
    raise SystemExit("pinned Hermes wrapper invocation shape changed")
entry = pathlib.Path(entries[0])
expected = """#!""" + str(entry.parent / "python3.12") + """
# -*- coding: utf-8 -*-
import sys
from hermes_cli.main import main
if __name__ == "__main__":
    if sys.argv[0].endswith("-script.pyw"):
        sys.argv[0] = sys.argv[0][:-11]
    elif sys.argv[0].endswith(".exe"):
        sys.argv[0] = sys.argv[0][:-4]
    sys.exit(main())
"""
if entry.read_text() != expected:
    raise SystemExit("pinned Hermes Python entrypoint changed")
snapshot = out / "bin/loom-hermes-snapshot-db"
snapshot.write_text("#!" + py[0] + "\n" + pathlib.Path("${../../internal/hermesprofile/native_snapshot.py}").read_text())
snapshot.chmod(0o555)
shim_dir = out / "share/loom-hermes-backup"
shim_dir.mkdir()
shim = r"""
# BEGIN LOOM BACKUP TIMESTAMP SHIM
import functools
import sys

# Both the wrapper and interpreter argv must select this exact pinned command.
if sys.argv[:2] == ["@LOOM_HERMES_ENTRYPOINT@", "backup"]:
    import zipfile
    _loom_zip_init = zipfile.ZipFile.__init__

    @functools.wraps(_loom_zip_init)
    def _loom_backup_zip_init(self, file, mode="r", *args, **kwargs):
        if mode in ("w", "x", "a"):
            kwargs.setdefault("strict_timestamps", False)
        _loom_zip_init(self, file, mode, *args, **kwargs)

    zipfile.ZipFile.__init__ = _loom_backup_zip_init
# END LOOM BACKUP TIMESTAMP SHIM
"""
(shim_dir / "sitecustomize.py").write_text(shim.replace("@LOOM_HERMES_ENTRYPOINT@", str(entry)))
line = 'exec "' + str(entry) + '"  "$@"'
# No shim path is exported for ordinary CLI, gateway, TUI or ACP invocations.
condition = 'if [[ "' + chr(36) + '{1-}" == backup ]]; then\n  export PYTHONPATH="' + str(shim_dir) + chr(36) + '{PYTHONPATH:+:$PYTHONPATH}"\nfi\n'
text = text.replace(line, condition + line, 1)
wrapper.write_text(text)
contract = r"""
# BEGIN LOOM BACKUP TIMESTAMP TEST
import os, pathlib, stat, sys, tempfile, zipfile
shim, entry, command = sys.argv[1:]
original = zipfile.ZipFile.__init__
sys.argv = [entry, command]
if command == "other-entry":
    sys.argv = [entry + "-agent", "backup"]
exec(compile(pathlib.Path(shim).read_text(), shim, "exec"), {})
active = command == "backup"
assert (zipfile.ZipFile.__init__ is not original) == active
with tempfile.TemporaryDirectory(prefix="loom-zip-contract-") as tmp:
    root = pathlib.Path(tmp)
    source = root / "skill.md"
    source.write_bytes(b"exact skill bytes\n")
    source.chmod(0o640)
    os.utime(source, ns=(1000000000, 1000000000))
    before = source.stat()
    errors = 0
    try:
        with zipfile.ZipFile(root / "profile.zip", "w") as z:
            z.write(source, "skills/skill.md")
    except ValueError:
        errors += 1
    assert errors == (0 if active else 1), (command, errors)
    if active:
        with zipfile.ZipFile(root / "profile.zip") as z:
            assert z.namelist() == ["skills/skill.md"]
            assert z.read("skills/skill.md") == source.read_bytes()
            info = z.getinfo("skills/skill.md")
            assert info.date_time == (1980, 1, 1, 0, 0, 0)
            assert stat.S_IMODE(info.external_attr >> 16) == 0o640
    if active:
        future = root / "future-skill.md"
        future.write_bytes(b"future skill")
        os.utime(future, ns=(4354819200000000000, 4354819200000000000))
        with zipfile.ZipFile(root / "future.zip", "w") as z:
            z.write(future, "future-skill.md")
        with zipfile.ZipFile(root / "future.zip") as z:
            assert z.getinfo("future-skill.md").date_time == (2107, 12, 31, 23, 59, 58)
            assert z.read("future-skill.md") == b"future skill"
        assert future.stat().st_mtime_ns == 4354819200000000000
    # Explicit caller decisions retain normal Python behavior.
    try:
        with zipfile.ZipFile(root / "strict.zip", "w", strict_timestamps=True) as z:
            z.write(source, "skill.md")
    except ValueError:
        pass
    else:
        raise AssertionError("explicit strict timestamps lost")
    with zipfile.ZipFile(root / "relaxed.zip", "w", strict_timestamps=False) as z:
        z.write(source, "skill.md")
    after = source.stat()
    assert after.st_mtime_ns == before.st_mtime_ns == 1000000000
    assert after.st_mode == before.st_mode and after.st_ino == before.st_ino
# END LOOM BACKUP TIMESTAMP TEST
"""
for command in ["backup", "--version", "gateway", "--tui", "chat", "import", "other-entry"]:
    subprocess.run([py[0], "-c", contract, str(shim_dir / "sitecustomize.py"), str(entry), command], check=True)
PYCOMPAT
    for executable in hermes hermes-agent hermes-acp; do
      wrapProgram "$out/bin/$executable" \
        --prefix PYTHONPATH : "$out/share/loom-hermes-native" \
        --set HERMES_PYTHON_SRC_ROOT "$out/share/loom-hermes-native" \
        --set HERMES_MANAGED true \
        --set HERMES_DISABLE_LAZY_INSTALLS 1 \
        --unset HERMES_LAZY_INSTALL_TARGET \
        --set HERMES_SKIP_NODE_BOOTSTRAP 1
    done
  '';

  passthru = (old.passthru or { }) // {
    upstreamRevision = revision;
    upstreamTag = "v2026.8.31";
    upstreamSourceHash = hermes-upstream.narHash;
    macComputerUseSourceRelative = "share/loom-hermes-native";
    hermesTui = centeredTui;
  };

  meta = old.meta // {
    description = "Pinned native Hermes harness for LOOM Morathustra";
    platforms = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
  };
})
