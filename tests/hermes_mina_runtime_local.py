#!/usr/bin/env python3
"""Pinned native skin/selection proof in one disposable profile, no TUI/model."""
import hashlib
import os
from pathlib import Path
import tempfile

REPO = Path(__file__).resolve().parents[1]
EXPECTED = "0175be4213c7deb52710dd993691d5f0c30bacd763d2a854381ca85a019095ef"
asset = REPO / "nix/files/mina/skins/mina-matrix-teal.yaml"
old_asset = REPO / "nix/files/morathustra/skins/morathustra-ash.yaml"
raw, old_raw = asset.read_bytes(), old_asset.read_bytes()
assert hashlib.sha256(raw).hexdigest() == EXPECTED
receipt_root = REPO / ".loom-acceptance/mina-w3"
receipt_root.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix="native-skin-", dir=receipt_root) as temporary:
    home = Path(temporary) / ".hermes"
    (home / "skins").mkdir(parents=True)
    # Exercise native operator selection only in this disposable profile.
    os.environ.update(HOME=temporary, HERMES_HOME=str(home), HERMES_MANAGED="",
                      HERMES_DISABLE_LAZY_INSTALLS="1", PYTHONDONTWRITEBYTECODE="1")
    import yaml
    from rich.color import Color
    from rich.text import Text
    from hermes_cli import config, skin_engine, skin_cmd

    assert skin_engine.get_hermes_home() == home
    (home / "skins/mina-matrix-teal.yaml").write_bytes(raw)
    (home / "skins/morathustra-ash.yaml").write_bytes(old_raw)
    data = yaml.safe_load(raw)
    loaded = skin_engine.load_skin("mina-matrix-teal")
    assert loaded.name == "mina-matrix-teal"
    assert loaded.branding["agent_name"] == "MINA"
    for key, value in data["colors"].items():
        Color.parse(value)
        assert loaded.colors[key] == value
    assert loaded.branding == data["branding"]
    assert loaded.banner_hero == data["banner_hero"]
    assert loaded.banner_logo == data["banner_logo"]
    hero = Text.from_markup(loaded.banner_hero)
    assert len(hero.split("\n")) == 40
    assert all(line.cell_len == 80 for line in hero.split("\n"))
    assert len(Text.from_markup(loaded.banner_logo).split("\n")) == 6

    initial = config.load_config()
    initial["model"] = {"default": "fixture-model", "provider": "fixture-provider"}
    initial["display"]["skin"] = "morathustra-ash"
    initial["display"]["resume_banner"] = False
    initial["skills"]["external_dirs"] = [str(Path(temporary) / "installed")]
    initial["sessions"] = {"retention_days": 91, "auto_prune": False}
    initial["fixture_user_field"] = {"preserved": ["one", "two"]}
    config.save_config(initial)
    before = yaml.safe_load((home / "config.yaml").read_text())
    skin_cmd._use("mina-matrix-teal")
    after = yaml.safe_load((home / "config.yaml").read_text())
    expected = dict(before)
    expected["display"] = dict(before["display"], skin="mina-matrix-teal")
    assert after == expected, "native selection changed unrelated settings"
    assert skin_cmd._active_skin() == "mina-matrix-teal"
    # Production packages remain managed; their read-only startup loader must
    # honor the published field without provisioning or changing settings.
    os.environ["HERMES_MANAGED"] = "true"
    skin_engine.init_skin_from_config(config.load_config_readonly())
    assert yaml.safe_load((home / "config.yaml").read_text()) == expected
    assert skin_engine.get_active_skin_name() == "mina-matrix-teal"
    assert skin_engine.get_active_skin().branding["agent_name"] == "MINA"
    assert (home / "skins/morathustra-ash.yaml").read_bytes() == old_raw
    assert (home / "skins/mina-matrix-teal.yaml").read_bytes() == raw
assert asset.read_bytes() == raw and old_asset.read_bytes() == old_raw
print(f"PASS native MINA skin: sha256={EXPECTED}; {len(data['colors'])} colors; 80x40 hero; selection-only config change; old skin retained")
