"""Focused native wake regressions; no microphone, models or pm installation.

Run with HERMES_WAKE_SOURCE pointing to the native source tree after applying
nix/packages/hermes-wake-reliability.patch. Only the two owned modules are
needed; dependency loading is stubbed because these tests inject score streams.
"""

import importlib.util
import os
from pathlib import Path
import sys
import threading
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import Mock, patch


def load_native():
    source = os.environ.get("HERMES_WAKE_SOURCE")
    if not source:
        raise unittest.SkipTest("Set HERMES_WAKE_SOURCE to the pinned native source with the wake patch applied")
    root = Path(source)
    package = ModuleType("tools")
    package.__path__ = [str(root / "tools")]
    modules = {"tools": package, "pm": SimpleNamespace(install_hint=lambda *a: "")}
    with patch.dict(sys.modules, modules):
        for name in ("wake_word_engines", "wake_word"):
            spec = importlib.util.spec_from_file_location("tools." + name, root / "tools" / (name + ".py"))
            module = importlib.util.module_from_spec(spec)
            sys.modules[spec.name] = module
            setattr(package, name, module)
            spec.loader.exec_module(module)
    return package.wake_word_engines, package.wake_word


engines, ww = load_native()


def score_engine(batches, needed=3):
    engine = engines._OpenWakeWordEngine.__new__(engines._OpenWakeWordEngine)
    engine._threshold, engine._confirm_needed, engine._confirm_streak = 0.6, needed, 0
    engine._features = SimpleNamespace(process_streaming=lambda data: iter([None]))
    batches = iter(batches)
    engine._model = SimpleNamespace(process_streaming=lambda embedding: iter(next(batches)))
    return engine


FRAME = SimpleNamespace(tobytes=lambda: b"unused")


class ScoreConfirmationTests(unittest.TestCase):
    def test_low_score_inside_batch_breaks_confirmation(self):
        engine = score_engine([[0.9, 0.1], [0.9, 0.1], [0.9, 0.1]])
        self.assertEqual([engine.process(FRAME) for _ in range(3)], [False] * 3)

    def test_each_completed_window_counts_in_order(self):
        engine = score_engine([[0.9, 0.9, 0.9]])
        self.assertTrue(engine.process(FRAME))

    def test_empty_output_does_not_invent_a_low_score(self):
        engine = score_engine([[0.9], [], [0.9], [0.9]])
        self.assertEqual([engine.process(FRAME) for _ in range(4)], [False, False, False, True])

    def test_invalid_scores_break_confirmation(self):
        for invalid in (float("nan"), float("inf"), -float("inf"), 1.1, -0.1):
            with self.subTest(score=invalid):
                engine = score_engine([[0.9], [invalid], [0.9]])
                self.assertFalse(any(engine.process(FRAME) for _ in range(3)))

    def test_scores_after_detection_are_consumed(self):
        consumed = []
        engine = score_engine([])
        def scores(embedding):
            for value in [0.9, 0.9, 0.9, 0.1]:
                consumed.append(value)
                yield value
        engine._model.process_streaming = scores
        self.assertTrue(engine.process(FRAME))
        self.assertEqual(consumed, [0.9, 0.9, 0.9, 0.1])
        self.assertEqual(engine._confirm_streak, 0)

    def test_normal_three_frame_confirmation(self):
        engine = score_engine([[0.9], [0.9], [0.9], [0.1], [0.9], [0.9]])
        self.assertEqual([engine.process(FRAME) for _ in range(6)], [False, False, True, False, False, False])


class MissingAudioTests(unittest.TestCase):
    def run_capture(self, data, error_at=None, external=True):
        engine = score_engine([[0.9]] * sum(item is not None for item in data))
        engine.reset = Mock(side_effect=lambda: setattr(engine, "_confirm_streak", 0))
        if error_at:
            original = engine.process
            calls = [0]
            def process(frame):
                calls[0] += 1
                if calls[0] == error_at:
                    raise RuntimeError("incomplete frame")
                return original(frame)
            engine.process = process
        detector = ww.WakeWordDetector(engine, lambda: None, external_audio=external)
        received = iter(data)
        def read(stop):
            try:
                return next(received)
            except StopIteration:
                stop.set()
                return None
        capture = SimpleNamespace(rate=ww.SAMPLE_RATE, read=read, close=Mock())
        detector._open_capture = lambda length: capture
        fired = []
        detector._fire = lambda: fired.append(True)
        # A stand-in frame needs bytes for the injected native score engine.
        for_frame = SimpleNamespace(tobytes=lambda: b"unused")
        data_read = capture.read
        capture.read = lambda stop: for_frame if data_read(stop) is not None else None
        detector._note_silence = lambda *a: None
        ready, errors = threading.Event(), []
        detector._run(ready, errors)
        self.assertTrue(ready.is_set())
        self.assertEqual(errors, [])
        capture.close.assert_called_once()
        return engine, fired

    def test_missing_audio_cannot_join_two_score_streaks(self):
        engine, fired = self.run_capture([1, 1, None, None, 1])
        self.assertEqual(fired, [])
        # Startup, first gap, and end-of-input gap; no repeated resets while idle.
        self.assertEqual(engine.reset.call_count, 3)

    def test_normal_continuous_audio_still_fires(self):
        _, fired = self.run_capture([1, 1, 1])
        self.assertEqual(fired, [True])

    def test_engine_error_drops_partial_confirmation(self):
        engine, fired = self.run_capture([1, 1, 1, 1], error_at=3)
        self.assertEqual(fired, [])
        self.assertEqual(engine.reset.call_count, 3)


if __name__ == "__main__":
    unittest.main()
