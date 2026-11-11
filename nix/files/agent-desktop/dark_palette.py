"""Apply a near-black palette once, retaining native controls and guarded undo."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd

from session_launcher import selected_session
from xfce_preferences import GLib, Xfconf, decode, encode, save


def digest(text):
    return None if text is None else hashlib.sha256(text.encode()).hexdigest()


def write_css(path, text, mode=0o600):
    if text is None:
        path.unlink(missing_ok=True)
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".loom-tmp")
    with open(temporary, "w", opener=lambda p, f: os.open(p, f, mode)) as output:
        output.write(text)
    temporary.replace(path)


def palette(store, receipt_path, css, restore=False):
    if css.is_symlink() or css.parent.is_symlink():
        raise RuntimeError("GTK4 CSS is managed by a symlink; a reviewed merge is required")
    if css.exists() and css.stat().st_size > 65536:
        raise RuntimeError("Existing GTK4 CSS is too large for this bounded merge")
    current_css = css.read_text() if css.exists() else None
    if restore:
        receipt = json.loads(receipt_path.read_text())
        held = []
        for row in reversed(receipt["changes"]):
            current = encode(store.get(row["channel"], row["key"]))
            if current == row["before"]:
                continue
            if current != row["after"]:
                held.append(row["key"])
            else:
                store.put(row["channel"], row["key"], decode(row["before"]))
        if current_css != receipt["css_before"]:
            if digest(current_css) == receipt["css_after_sha256"]:
                write_css(css, receipt["css_before"], receipt["css_before_mode"])
            else:
                held.append("GTK4 user CSS changed after application")
        receipt.update(state="restore-held" if held else "restored", held=held)
        save(receipt_path, receipt)
        print(json.dumps(dict(state=receipt["state"], held=held)))
        return
    if receipt_path.exists():
        state = json.loads(receipt_path.read_text())["state"]
        if state == "applying":
            raise RuntimeError("Incomplete palette application: inspect/restore before continuing")
        print(f"Palette already handled ({state}); preserving user choices")
        return
    theme = os.environ["LOOM_DARK_THEME"]
    for asset in ["gtk-3.0/gtk.css", "xfwm4/themerc"]:
        if not (Path(os.environ["LOOM_DARK_THEME_ROOT"]) / theme / asset).is_file():
            raise RuntimeError(f"Missing near-black theme asset: {asset}")
    block = "\n/* BEGIN LOOM NEAR-BLACK COLOURS */\n" + Path(os.environ["LOOM_DARK_PALETTE_CSS"]).read_text()
    block += "\n/* END LOOM NEAR-BLACK COLOURS */\n"
    if "BEGIN LOOM NEAR-BLACK COLOURS" in (current_css or ""):
        raise RuntimeError("Existing palette block has no receipt; inspect before replacing it")
    after_css = (current_css or "") + block
    targets = [("xsettings", "/Net/ThemeName", GLib.Variant("s", theme)),
               ("xfwm4", "/general/theme", GLib.Variant("s", theme)),
               ("xfce4-panel", "/panels/panel-1/background-rgba",
                GLib.Variant("ad", [0.035, 0.04, 0.045, 1.0]))]
    rows = [dict(channel=c, key=k, before=encode(store.get(c, k)), after=encode(v))
            for c, k, v in targets]
    receipt = dict(state="applying", changes=rows, css_before=current_css,
                   css_before_mode=css.stat().st_mode & 0o777 if css.exists() else 0o600,
                   css_after_sha256=digest(after_css))
    save(receipt_path, receipt)
    for row in rows:
        if encode(store.get(row["channel"], row["key"])) != row["before"]:
            raise RuntimeError("A palette setting changed concurrently; preserving it")
        store.put(row["channel"], row["key"], decode(row["after"]))
    if (css.read_text() if css.exists() else None) != current_css:
        raise RuntimeError("GTK4 CSS changed concurrently; preserving it")
    write_css(css, after_css, receipt["css_before_mode"])
    receipt["state"] = "applied"
    save(receipt_path, receipt)
    print("Near-black GTK/Xfwm/panel palette applied; native GTK4 colours saved")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        raise RuntimeError("The palette belongs only to agents")
    selected_session()
    directory = Path.home() / ".local/state/loom-agent-desktop"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (directory / "palette.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        palette(Xfconf(), directory / "near-black-v1.json",
                Path.home() / ".config/gtk-4.0/gtk.css", args.restore)


if __name__ == "__main__":
    main()
