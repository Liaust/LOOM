"""Export a small openWakeWord dense head and check Hermes's actual inference API.

The frozen feature models and streaming inference are pyopen-wakeword's, not a
second detector implementation. `compat` produces an explicitly UNTRAINED probe;
it is never a MINA detector and must not be activated as one.
"""

import argparse
import hashlib
import inspect
import json
import os
from pathlib import Path
import wave

os.environ.setdefault("TF_CPP_MIN_LOG_LEVEL", "2")
os.environ.setdefault("OMP_NUM_THREADS", "4")


def export_dense_head(model, destination):
    import numpy as np
    import tensorflow as tf

    shape = tuple(model.input_shape[1:])
    if len(shape) != 2 or shape[1] != 96 or not 1 <= shape[0] <= 64:
        raise ValueError("Expected an openWakeWord [windows, 96] feature input.")
    layers = []
    for layer in model.layers:
        weights = layer.get_weights()
        if not weights:
            if not isinstance(layer, (tf.keras.layers.InputLayer, tf.keras.layers.Flatten)):
                raise ValueError("Only Flatten and Dense layers are supported.")
            continue
        if not isinstance(layer, tf.keras.layers.Dense) or len(weights) != 2:
            raise ValueError("Expected a small fully connected wake classifier.")
        activation = layer.get_config()["activation"]
        if activation not in ("relu", "sigmoid", "linear"):
            raise ValueError("Unsupported classifier activation.")
        layers.append((tf.constant(np.asarray(weights[0], dtype=np.float32)),
                       tf.constant(np.asarray(weights[1], dtype=np.float32)), activation))
    if not layers or layers[-1][1].shape != (1,) or layers[-1][2] != "sigmoid":
        raise ValueError("Expected one sigmoid probability output.")

    @tf.function(input_signature=[tf.TensorSpec([1, *shape], tf.float32)])
    def infer(features):
        value = tf.reshape(features, [1, shape[0] * 96])
        for matrix, bias, activation in layers:
            value = tf.matmul(value, matrix) + bias
            if activation == "relu":
                value = tf.nn.relu(value)
            elif activation == "sigmoid":
                value = tf.nn.sigmoid(value)
        return value

    converter = tf.lite.TFLiteConverter.from_concrete_functions([infer.get_concrete_function()])
    converter.target_spec.supported_ops = [tf.lite.OpsSet.TFLITE_BUILTINS]
    data = converter.convert()
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with destination.open("xb") as output:
        destination.chmod(0o600)
        output.write(data)
    return {"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data), "input": [1, *shape]}


def check(model_path):
    import numpy as np
    from pyopen_wakeword import OpenWakeWord, OpenWakeWordFeatures

    detector = OpenWakeWord.from_model(model_path)
    features = OpenWakeWordFeatures.from_builtin()
    scores = []
    try:
        for _ in range(30):
            for embedding in features.process_streaming(np.zeros(1280, dtype=np.int16).tobytes()):
                scores.extend(detector.process_streaming(embedding))
        if not scores or not all(np.isfinite(score) and 0 <= score <= 1 for score in scores):
            raise ValueError("Native streaming classifier did not return valid probabilities.")
        return {"native_load": True, "windows_scored": len(scores), "input_windows": detector.input_windows}
    finally:
        detector.close()
        features.close()


def score_recordings(model_path, recordings, threshold, split, confirmation_frames=3, batch_ids=None):
    """Replay exact original PCM through the installed native decision path.

    The engine source digest identifies which runtime semantics were checked.
    Replaying an old split is regression evidence, never a fresh holdout claim.
    """
    import numpy as np
    from tools.wake_word_engines import _OpenWakeWordEngine

    if not 0 < threshold < 1 or not 1 <= confirmation_frames <= 10:
        raise ValueError("Invalid native threshold or confirmation count")
    requested_batches = set(batch_ids or [])
    matched_batches = set()
    source_digest = hashlib.sha256(Path(inspect.getfile(_OpenWakeWordEngine)).read_bytes()).hexdigest()
    result = {"split": split, "positive_clips": 0, "detected": 0, "negative_seconds": 0,
              "negative_clips": 0, "false_positive_clips": 0,
              "confirmation_frames": confirmation_frames, "threshold": threshold,
              "engineSourceSha256": source_digest, "decision_path": "native-hermes-openwakeword",
              "batch_filter_applied": bool(requested_batches),
              "modelSha256": hashlib.sha256(model_path.read_bytes()).hexdigest(),
              "qualification": "Recorded split replay; freshness and daily-use acceptance require separate evidence."}
    engine = _OpenWakeWordEngine({"capture": "client", "provider": "openwakeword",
        "openwakeword": {"model": str(model_path)}, "sensitivity": threshold,
        "confirmation_frames": confirmation_frames})
    try:
        for path in sorted(recordings.glob("*/sample.json")):
            meta = json.loads(path.read_text())
            if meta["split"] != split:
                continue
            if requested_batches and meta.get("batchId") not in requested_batches:
                continue
            matched_batches.add(meta.get("batchId"))
            if meta["kind"] not in ("positive", "negative"):
                raise ValueError("Invalid recording kind")
            with wave.open(str(path.parent / "audio.wav"), "rb") as source:
                if (source.getnchannels(), source.getsampwidth(), source.getframerate()) != (1, 2, 16000):
                    raise ValueError("Expected mono PCM16/16kHz")
                pcm = source.readframes(source.getnframes())
            if hashlib.sha256(pcm).hexdigest() != meta["pcmSha256"]:
                raise ValueError("Recording hash mismatch")
            audio = np.frombuffer(pcm, dtype="<i2")
            engine.reset()
            fired = False
            for start in range(0, len(audio), engine.frame_length):
                frame = np.zeros(engine.frame_length, dtype=np.int16)
                frame[:len(audio[start:start + engine.frame_length])] = audio[start:start + engine.frame_length]
                fired = engine.process(frame) or fired
            if meta["kind"] == "positive":
                result["positive_clips"] += 1
                result["detected"] += int(fired)
            else:
                result["negative_clips"] += 1
                result["negative_seconds"] += len(audio) / 16000
                result["false_positive_clips"] += int(fired)
    finally:
        engine.close()
    if requested_batches and matched_batches != requested_batches:
        raise ValueError("Requested batch is absent from the selected split")
    if result["positive_clips"] == 0 or result["negative_seconds"] == 0:
        raise ValueError("Selected recording split is incomplete")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("compat", "export", "check", "score"))
    parser.add_argument("--model", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--recordings", type=Path)
    parser.add_argument("--threshold", type=float)
    parser.add_argument("--split", choices=("validation", "test"), default="test")
    parser.add_argument("--confirmation-frames", type=int, choices=range(1, 11), default=3)
    parser.add_argument("--batch-id", action="append", help="Replay only these immutable batches; repeat for positives and negatives")
    args = parser.parse_args()
    if args.command == "score":
        if not args.model or not args.recordings or args.threshold is None or not 0 < args.threshold < 1:
            parser.error("score requires --model, --recordings and --threshold between0 and1")
        result = score_recordings(args.model, args.recordings, args.threshold, args.split, args.confirmation_frames, args.batch_id)
    elif args.command == "check":
        if not args.model:
            parser.error("check requires --model")
        result = check(args.model)
    else:
        if not args.output:
            parser.error("an exclusive --output path is required")
        import tensorflow as tf
        tf.config.threading.set_intra_op_parallelism_threads(4)
        tf.config.threading.set_inter_op_parallelism_threads(2)
        if args.command == "compat":
            tf.keras.utils.set_random_seed(20261017)
            model = tf.keras.Sequential([tf.keras.Input((16, 96)), tf.keras.layers.Flatten(),
                tf.keras.layers.Dense(64, activation="relu"),
                tf.keras.layers.Dense(32, activation="relu"),
                tf.keras.layers.Dense(1, activation="sigmoid")])
        else:
            if not args.model:
                parser.error("export requires a trained --model .keras file")
            model = tf.keras.models.load_model(args.model, compile=False)
        result = export_dense_head(model, args.output)
        result.update(check(args.output))
        result["trained_mina"] = False if args.command == "compat" else "requires_dataset_and_quality_review"
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
