"""Preserve native Codex settings while selecting operator-owned updates."""

import json
import os
from pathlib import Path
import tempfile


def configure(home):
    directory = Path(home) / "app-server-daemon"
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    if directory.is_symlink() or directory.stat().st_uid != os.getuid():
        raise ValueError("Codex daemon settings must belong to the executing user")
    path = directory / "settings.json"
    settings = {}
    if path.exists() or path.is_symlink():
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW)) as source:
            if os.fstat(source.fileno()).st_uid != os.getuid():
                raise ValueError("Codex daemon settings owner changed")
            settings = json.load(source)
    if not isinstance(settings, dict):
        raise ValueError("Codex daemon settings must be an object")
    updater = settings.setdefault("updater", {})
    if not isinstance(updater, dict):
        raise ValueError("Codex updater settings must be an object")
    updater["autoUpdateEnabled"] = False
    fd, temporary = tempfile.mkstemp(prefix=".loom-settings-", dir=directory)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(settings, output, indent=2)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


if __name__ == "__main__":
    configure(os.environ["CODEX_HOME"])
