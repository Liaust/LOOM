"""Evaluation parity using disposable synthetic PCM, never private recordings."""

import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import wave

try:
    import numpy as np
    from scipy import signal  # train.py uses the existing optional training environment
except ModuleNotFoundError:
    raise unittest.SkipTest("Run evaluation checks in the existing wake-training environment")


REPO = Path(__file__).resolve().parents[2]
SCRIPTS = REPO / "scripts/wake-training"
spec = importlib.util.spec_from_file_location("wake_training_model", SCRIPTS / "model.py")
model = importlib.util.module_from_spec(spec)
spec.loader.exec_module(model)
with patch.dict(sys.modules, {"model": model}):
    spec = importlib.util.spec_from_file_location("wake_training_train", SCRIPTS / "train.py")
    train = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(train)


class FakeEngine:
    frame_length = 4
    instances = []

    def __init__(self, config):
        self.config, self.frames, self.resets, self.closed = config, [], 0, False
        self.instances.append(self)

    def process(self, frame):
        self.frames.append(frame.copy())
        return frame[0] == -32768

    def reset(self):
        self.resets += 1

    def close(self):
        self.closed = True


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        scratch = REPO / ".loom-acceptance"
        scratch.mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="wake-evaluation-", dir=scratch)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.model = self.root / "mina.tflite"
        self.model.write_bytes(b"synthetic test model")
        self.recordings = self.root / "recordings"
        self.recordings.mkdir()

    def recording(self, name, kind, samples, digest=None):
        directory = self.recordings / name
        directory.mkdir()
        pcm = np.asarray(samples, dtype="<i2").tobytes()
        with wave.open(str(directory / "audio.wav"), "wb") as target:
            target.setparams((1, 2, 16000, 0, "NONE", "not compressed"))
            target.writeframes(pcm)
        (directory / "sample.json").write_text(json.dumps({"kind": kind, "split": "test",
            "batchId": name,
            "pcmSha256": digest or hashlib.sha256(pcm).hexdigest()}))

    def score(self, batch_ids=None):
        with patch.dict(sys.modules, {"tools.wake_word_engines": SimpleNamespace(_OpenWakeWordEngine=FakeEngine)}):
            return model.score_recordings(self.model, self.recordings, 0.6, "test", 2, batch_ids)

    def test_native_replay_preserves_pcm_padding_and_settings(self):
        self.recording("a", "positive", [-32768, 1, 32767])
        self.recording("b", "negative", [32767, -32768])
        result = self.score()
        engine = FakeEngine.instances[-1]
        self.assertEqual([frame.tolist() for frame in engine.frames], [[-32768, 1, 32767, 0], [32767, -32768, 0, 0]])
        self.assertEqual(engine.config["confirmation_frames"], 2)
        self.assertEqual(engine.resets, 2)
        self.assertTrue(engine.closed)
        self.assertEqual((result["detected"], result["false_positive_clips"]), (1, 0))
        self.assertEqual(result["decision_path"], "native-hermes-openwakeword")
        self.assertEqual(len(result["engineSourceSha256"]), 64)

    def test_changed_pcm_fails_and_closes_engine(self):
        self.recording("a", "positive", [1], digest="invalid")
        with self.assertRaisesRegex(ValueError, "hash mismatch"):
            self.score()
        self.assertTrue(FakeEngine.instances[-1].closed)

    def test_fresh_batch_selection_excludes_old_clips_and_requires_all_batches(self):
        self.recording("fresh-positive", "positive", [-32768])
        self.recording("fresh-negative", "negative", [0])
        self.recording("old-negative", "negative", [-32768])
        result = self.score(["fresh-positive", "fresh-negative"])
        self.assertTrue(result["batch_filter_applied"])
        self.assertEqual((result["negative_clips"], result["false_positive_clips"]), (1, 0))
        with self.assertRaisesRegex(ValueError, "absent"):
            self.score(["fresh-positive", "fresh-negative", "missing"])
        self.assertTrue(FakeEngine.instances[-1].closed)

    def test_head_diagnostics_preserve_window_order(self):
        (self.root / "selection.json").write_text(json.dumps({"selected": {"threshold": 0.6}}))
        backend = SimpleNamespace(keras=SimpleNamespace(models=SimpleNamespace(load_model=lambda *a, **k: None)))
        public = [({"origin": "public", "kind": "negative", "seconds": 1}, np.array([0.1] * 3))]
        items = [{"split": "test", "path": "synthetic.wav", "kind": "negative", "seconds": 0.24}]
        frontend = SimpleNamespace(reset=lambda: None, close=lambda: None,
                                   process_streaming=lambda frame: [None])
        # Each frame contains a high and a low: max-per-frame would invent a
        # three-frame confirmation. The full ordered windows must reject it.
        head = SimpleNamespace(reset=lambda: None, close=lambda: None,
                               process_streaming=lambda emb: [0.9, 0.1])
        native = SimpleNamespace(OpenWakeWord=SimpleNamespace(from_model=lambda *a: head),
                                 OpenWakeWordFeatures=SimpleNamespace(from_builtin=lambda: frontend))
        with patch.object(train, "audit", return_value=(items, {})), \
             patch.object(train, "read_wave", return_value=(None, b"\0" * (1280 * 2 * 3))), \
             patch.dict(sys.modules, {"pyopen_wakeword": native}), \
             patch.object(train, "tensorflow", return_value=backend), patch.object(train, "clip_scores", return_value=public), \
             patch.object(train, "log"):
            train.evaluate(self.root, self.recordings)
        result = json.loads((self.root / "final-check.json").read_text())
        self.assertEqual(result["final_check"]["personal"]["false_positive_clips"], 0)
        self.assertEqual(result["final_check"]["public"]["false_positive_clips"], 0)
        self.assertEqual(result["decision_path"], "classifier-window-diagnostics")


if __name__ == "__main__":
    unittest.main()
