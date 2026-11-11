"""Previously accepted GNOME defaults; no screenshot or input service."""
import json
import os
from pathlib import Path
import pwd

def seed_preferences():
    import gi
    gi.require_version("Gio", "2.0")
    from gi.repository import Gio, GLib
    marker = Path.home() / ".local/state/loom-agent-desktop/preferences-v1.json"
    if marker.exists():
        return
    wallpaper = Path("/var/lib/loom-agent-desktop/wallpaper.jpg")
    if not wallpaper.is_file():
        raise RuntimeError("The approved Main wallpaper asset is missing; preferences not seeded.")
    before = {}
    for schema, values in {
        "org.gnome.desktop.background": {"picture-uri": wallpaper.as_uri(), "picture-uri-dark": wallpaper.as_uri(), "picture-options": "zoom"},
        "org.gnome.desktop.peripherals.mouse": {"accel-profile": "adaptive", "speed": 0.0},
    }.items():
        settings = Gio.Settings.new(schema)
        before[schema] = {k: settings.get_value(k).print_(True) for k in values}
        for key, value in values.items():
            if settings.get_user_value(key) is not None:
                continue
            variant = GLib.Variant("d", value) if isinstance(value, float) else GLib.Variant("s", value)
            if not settings.set_value(key, variant):
                raise RuntimeError("Could not set a user-changeable desktop preference.")
    settings = Gio.Settings.new("org.gnome.shell")
    before["enabled-extensions"] = settings.get_strv("enabled-extensions")
    extension = "tilingshell@ferrarodomenico.com"
    metadata = Path("/run/current-system/sw/share/gnome-shell/extensions") / extension / "metadata.json"
    with metadata.open() as stream:
        supported = json.load(stream).get("shell-version", [])
    if "50" not in supported:
        raise RuntimeError("Installed Tiling Shell does not declare GNOME50 support; it was not enabled.")
    enabled = list(before["enabled-extensions"])
    if extension not in enabled:
        settings.set_strv("enabled-extensions", enabled + [extension])
    Gio.Settings.sync()
    marker.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with open(marker, "x", opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        json.dump({"before": before, "cursor_theme_changed": False, "scope": "One-time defaults; later user changes remain authoritative."}, stream)



if __name__ == "__main__" and pwd.getpwuid(os.getuid()).pw_name == "agents":
    seed_preferences()
