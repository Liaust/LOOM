"""One-time agents appearance defaults, with typed before-images and guarded undo."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pwd
import subprocess

from gi.repository import Gio, GLib

from session_launcher import selected_session


THEME = "Fluent-teal-Dark"


def settings(launcher_id):
    return {
        "xsettings": {
            "/Net/ThemeName": ("s", THEME),
            "/Net/IconThemeName": ("s", "Papirus-Dark"),
            "/Gtk/FontName": ("s", "Inter 11"),
            "/Gtk/MonospaceFontName": ("s", "JetBrains Mono 11"),
            "/Gtk/DecorationLayout": ("s", "menu:minimize,maximize,close"),
            "/Xft/Antialias": ("i", 1),
            "/Xft/Hinting": ("i", 1),
            "/Xft/HintStyle": ("s", "hintslight"),
        },
        "xfwm4": {
            "/general/theme": ("s", THEME),
            "/general/title_font": ("s", "Inter Semi-Bold 11"),
            "/general/title_alignment": ("s", "left"),
            "/general/button_layout": ("s", "O|HMC"),
            "/general/show_frame_shadow": ("b", True),
            "/general/show_dock_shadow": ("b", False),
            "/general/shadow_opacity": ("i", 30),
        },
        "xfce4-panel": {
            f"/plugins/plugin-{launcher_id}": ("s", "whiskermenu"),
            "/plugins/plugin-2/show-labels": ("b", False),
            "/plugins/plugin-2/flat-buttons": ("b", True),
            "/plugins/plugin-6/icon-size": ("u", 22),
            "/plugins/plugin-6/symbolic-icons": ("b", True),
            "/panels/dark-mode": ("b", True),
            "/panels/panel-1/size": ("u", 36),
            "/panels/panel-1/icon-size": ("u", 22),
            "/panels/panel-1/background-style": ("u", 1),
            "/panels/panel-1/background-rgba": ("ad", [0.09, 0.10, 0.11, 1.0]),
            "/panels/panel-1/plugin-ids": ("av", [GLib.Variant("i", n) for n in
                                                   [launcher_id, 2, 3, 4, 6, 8, 10]]),
            # Retain the bottom panel's configuration/plugins for reviewed undo.
            "/panels": ("av", [GLib.Variant("i", 1)]),
        },
        "xfce4-desktop": {"/desktop-icons/style": ("i", 0)},
    }


class Xfconf:
    def __init__(self):
        self.bus = Gio.DBusConnection.new_for_address_sync(
            f"unix:path=/run/user/{os.getuid()}/bus",
            Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT |
            Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION, None, None)

    def call(self, method, signature, values):
        return self.bus.call_sync("org.xfce.Xfconf", "/org/xfce/Xfconf",
                                  "org.xfce.Xfconf", method,
                                  GLib.Variant(signature, values), None,
                                  Gio.DBusCallFlags.NONE, 5000, None)

    def get(self, channel, key):
        if not self.call("PropertyExists", "(ss)", (channel, key)).unpack()[0]:
            return None
        return self.call("GetProperty", "(ss)", (channel, key)).get_child_value(0).get_variant()

    def put(self, channel, key, value):
        command = ["/run/current-system/sw/bin/xfconf-query", "--channel", channel,
                   "--property", key]
        if value is None:
            command += ["--reset"]
        else:
            # Xfconf's CLI owns GValue conversion (not generic GVariant marshaling).
            command += ["--create"]
            if value.get_type_string().startswith("a"):
                command += ["--force-array"]
                values = [value.get_child_value(i) for i in range(value.n_children())]
                values = [item.get_variant() if item.get_type_string() == "v" else item
                          for item in values]
            else:
                values = [value]
            types = {"s": "string", "b": "bool", "i": "int", "u": "uint", "d": "double"}
            for item in values:
                text = str(item.unpack()).lower() if item.get_type_string() == "b" else str(item.unpack())
                command += ["--type", types[item.get_type_string()], "--set", text]
        subprocess.run(command, check=True, timeout=10,
                       env=os.environ | {"DBUS_SESSION_BUS_ADDRESS": f"unix:path=/run/user/{os.getuid()}/bus"})

    def snapshot(self, channel):
        values = self.call("GetAllProperties", "(ss)", (channel, "/")).get_child_value(0)
        result = {}
        for index in range(values.n_children()):
            item = values.get_child_value(index)
            result[item.get_child_value(0).unpack()] = encode(item.get_child_value(1).get_variant())
        return result


def encode(value):
    if value is not None and value.get_type_string() == "ad":
        # Xfconf exposes all GValue arrays as av, including RGBA double arrays.
        value = GLib.Variant("av", [value.get_child_value(i) for i in range(value.n_children())])
    return None if value is None else value.print_(True)


def decode(value):
    return None if value is None else GLib.Variant.parse(None, value, None, None)


def save(path, data):
    temporary = path.with_suffix(".tmp")
    with open(temporary, "w", opener=lambda p, f: os.open(p, f, 0o600)) as output:
        json.dump(data, output, indent=2)
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


def apply(store, path, resume=False):
    if path.exists():
        receipt = json.loads(path.read_text())
        if receipt["state"] == "applying":
            if not resume:
                raise RuntimeError("Incomplete appearance change: inspect before-image before --resume")
        else:
            print(f"Appearance already handled ({receipt['state']}); preserving user changes")
            return
    else:
        theme = Path(os.environ["LOOM_XFCE_THEME_ROOT"]) / THEME
        for asset in ["gtk-3.0/gtk.css", "xfwm4/themerc"]:
            if not (theme / asset).is_file():
                raise RuntimeError(f"Missing coordinated theme asset: {asset}")
        launcher_id = 19
        while store.get("xfce4-panel", f"/plugins/plugin-{launcher_id}") is not None:
            launcher_id += 1
        rows = []
        for channel, entries in settings(launcher_id).items():
            for key, (signature, value) in entries.items():
                rows.append(dict(channel=channel, key=key, before=encode(store.get(channel, key)),
                                 after=encode(GLib.Variant(signature, value))))
        receipt = dict(version=1, state="applying", changes=rows,
                       channels_before={channel: store.snapshot(channel)
                                        for channel in settings(launcher_id)})
        save(path, receipt)
    for row in receipt["changes"]:
        # Detect edits after the snapshot rather than overwrite a concurrent user.
        current = encode(store.get(row["channel"], row["key"]))
        if current == row["after"]:
            continue
        if current != row["before"]:
            raise RuntimeError(f"Setting changed concurrently: {row['channel']} {row['key']}")
        store.put(row["channel"], row["key"], decode(row["after"]))
    receipt["state"] = "applied"
    save(path, receipt)
    print(f"Applied {len(receipt['changes'])} appearance keys; private undo receipt: {path}")


def restore(store, path):
    receipt = json.loads(path.read_text())
    held = []
    for row in reversed(receipt["changes"]):
        current = encode(store.get(row["channel"], row["key"]))
        if current == encode(decode(row["before"])):
            continue
        if current != encode(decode(row["after"])):
            held.append(f"{row['channel']} {row['key']}")
            continue
        store.put(row["channel"], row["key"], decode(row["before"]))
    receipt.update(state="restore-held" if held else "restored", held=held)
    save(path, receipt)
    print(json.dumps(dict(state=receipt["state"], held=held)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--restore", action="store_true",
                        help="Undo only keys still matching the installed defaults")
    parser.add_argument("--resume", action="store_true",
                        help="Continue an inspected incomplete apply; preserve its original before-image")
    args = parser.parse_args()
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        raise RuntimeError("Appearance defaults belong only to the agents account")
    selected_session()
    directory = Path.home() / ".local/state/loom-agent-desktop"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    path = directory / "xfce-appearance-v1.json"
    with (directory / "appearance.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.restore:
            restore(Xfconf(), path)
        else:
            apply(Xfconf(), path, args.resume)


if __name__ == "__main__":
    main()
