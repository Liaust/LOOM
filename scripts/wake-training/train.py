"""Private, bounded MINA classifier training on native openWakeWord features.

Personal splits are immutable. Only training audio is augmented. Validation
selects the checkpoint/threshold; final evaluation is a separate command.
The frozen speech frontend is Hermes's pyopen-wakeword, not a new detector.
"""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
import time
import urllib.request
import wave

os.environ.setdefault("TF_CPP_MIN_LOG_LEVEL", "2")
os.environ.setdefault("OMP_NUM_THREADS", "4")
os.environ.setdefault("OPENBLAS_NUM_THREADS", "1")

import numpy as np
from scipy import signal

from model import check, export_dense_head

RATE = 16000
FRAME = 1280
WINDOWS = 16
SEED = 20261017
VOICE_REV = "c10ece1aade47bb51c153c893d14e5bf8e5b7117"
VOICE_BASE = f"https://huggingface.co/rhasspy/piper-voices/resolve/{VOICE_REV}/en/en_US/libritts_r/medium/"
VOICE_NAME = "en_US-libritts_r-medium.onnx"


def log(stage, **values):
    print(json.dumps({"stage": stage, **values}, sort_keys=True), flush=True)


def write_json(path, value):
    path = Path(path)
    with path.open("x") as output:
        json.dump(value, output, indent=2, sort_keys=True)


def sha(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def read_wave(path):
    with wave.open(str(path), "rb") as source:
        if (source.getnchannels(), source.getsampwidth(), source.getframerate()) != (1, 2, RATE):
            raise ValueError("Expected mono PCM16 16kHz recording")
        pcm = source.readframes(source.getnframes())
    return np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768, pcm


def audit(recordings):
    items, batches, hashes = [], {}, set()
    counts = {s: {"positive": 0, "negativeSeconds": 0.0} for s in ("train", "validation", "test")}
    for path in sorted(recordings.glob("*/sample.json")):
        meta = json.loads(path.read_text())
        audio, pcm = read_wave(path.parent / "audio.wav")
        split, kind = meta["split"], meta["kind"]
        if split not in counts or kind not in ("positive", "negative"):
            raise ValueError("Invalid recording split/kind")
        if meta["sampleId"] != path.parent.name or meta["target"] != "mina" or meta["dropped"] != 0:
            raise ValueError("Invalid recording identity or dropped frames")
        digest = hashlib.sha256(pcm).hexdigest()
        if digest != meta["pcmSha256"] or digest in hashes:
            raise ValueError("Recording hash mismatch or duplicate original")
        hashes.add(digest)
        if abs(len(audio) / RATE - meta["seconds"]) > 1 / RATE:
            raise ValueError("Recording duration mismatch")
        batch = meta["batchId"]
        if batch in batches and batches[batch] != (split, kind):
            raise ValueError("Recording batch crossed a split or label")
        batches[batch] = (split, kind)
        if kind == "positive":
            counts[split]["positive"] += 1
        else:
            counts[split]["negativeSeconds"] += len(audio) / RATE
        items.append({**meta, "path": str(path.parent / "audio.wav")})
    for split, positive, negative in (("train", 60, 120), ("validation", 20, 60), ("test", 20, 60)):
        if counts[split]["positive"] < positive or counts[split]["negativeSeconds"] < negative:
            raise ValueError(f"Incomplete {split} recordings")
    result = {"samples": len(items), "batches": len(batches), "counts": counts,
              "microphones": sorted({v["microphone"] for v in items}),
              "manifestSha256": hashlib.sha256(json.dumps(items, sort_keys=True).encode()).hexdigest()}
    return items, result


def download(url, path, max_bytes, expected=None, algorithm="sha256"):
    if not path.exists():
        partial = path.with_suffix(path.suffix + ".partial")
        with urllib.request.urlopen(url, timeout=90) as response, partial.open("xb") as output:
            count = 0
            while block := response.read(1024 * 1024):
                count += len(block)
                if count > max_bytes:
                    raise ValueError("Asset exceeded bounded download size")
                output.write(block)
        partial.rename(path)
    digest = hashlib.new(algorithm)
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    if expected and digest.hexdigest() != expected:
        raise ValueError("Public asset checksum mismatch")
    return {"url": url, "bytes": path.stat().st_size, "sha256": sha(path)}


def assets(root):
    destination = root / "assets"
    destination.mkdir(mode=0o700, exist_ok=True)
    receipts = []
    for name, limit, digest in ((VOICE_NAME, 80_000_000, "10bb85e071d616fcf4071f369f1799d0491492ab3c5d552ec19fb548fac13195"),
                                (VOICE_NAME + ".json", 100_000, None), ("MODEL_CARD", 10_000, None)):
        receipts.append(download(VOICE_BASE + name, destination / name, limit, digest))
    receipts.append(download("https://www.openslr.org/resources/12/dev-clean.tar.gz", destination / "dev-clean.tar.gz",
                             400_000_000, "42e2234ba48799c1f50f24a7926300a1", "md5"))
    if not (destination / "receipts.json").exists():
        write_json(destination / "receipts.json", receipts)
    log("assets", bytes=sum(x["bytes"] for x in receipts))


class Frontend:
    def __init__(self):
        from pyopen_wakeword import OpenWakeWordFeatures
        self.native = OpenWakeWordFeatures.from_builtin()

    def embeddings(self, audio):
        self.native.reset()
        # Discard native startup autofill, then mirror continuous 80ms capture.
        for _ in range(25):
            list(self.native.process_streaming(np.zeros(FRAME, dtype="<i2").tobytes()))
        rows = []
        for start in range(0, len(audio), FRAME):
            frame = np.zeros(FRAME, dtype=np.float32)
            part = audio[start:start + FRAME]
            frame[:len(part)] = part
            pcm = (np.clip(frame, -1, 1) * 32767).astype("<i2")
            rows.extend(e.reshape(-1, 96) for e in self.native.process_streaming(pcm.tobytes()))
        return np.concatenate(rows)

    def windows(self, audio, stride=1):
        if len(audio) < RATE * 2:
            audio = np.pad(audio, (RATE * 2 - len(audio), 0))
        embeddings = self.embeddings(audio)
        return np.stack([embeddings[i:i + WINDOWS] for i in range(0, len(embeddings) - WINDOWS + 1, stride)])


def active_audio(audio):
    energies = np.array([np.sqrt(np.mean(audio[i:i + 320] ** 2)) for i in range(0, len(audio), 320)])
    indices = np.flatnonzero(energies > max(0.003, energies.max() * 0.12))
    if not len(indices):
        raise ValueError("Positive recording contains no usable speech")
    return audio[max(0, indices[0] * 320 - 1280):min(len(audio), (indices[-1] + 1) * 320 + 1280)]


def positive_example(audio, rng, background=None, augment=True):
    voice = active_audio(audio)
    if augment:
        voice = signal.resample(voice, min(int(RATE * 1.85), int(len(voice) * rng.uniform(0.85, 1.15)))).astype(np.float32)
        if rng.random() < 0.5 and len(voice) < RATE * 1.6:
            kernel = np.zeros(int(RATE * 0.25), dtype=np.float32)
            kernel[0] = 1
            delays = rng.integers(200, len(kernel), size=5)
            kernel[delays] = rng.uniform(-0.15, 0.15, 5)
            voice = signal.fftconvolve(voice, kernel).astype(np.float32)
    if len(voice) > RATE * 1.9:
        raise ValueError("Positive speech is too long for a single-word wake head")
    output = np.zeros(RATE * 3, dtype=np.float32)
    # Keep the complete word in the final 2s receptive field, not partial words.
    tail = int(RATE * rng.uniform(0.15, 0.4))
    start = len(output) - len(voice) - tail
    output[start:start + len(voice)] = voice
    if augment:
        output *= rng.uniform(0.025, 0.35) / max(np.sqrt(np.mean(voice ** 2)), 0.001)
        if background is not None and rng.random() < 0.7:
            bg = np.resize(background, len(output))
            bg = np.roll(bg, int(rng.integers(0, len(bg))))
            bg *= np.sqrt(np.mean(output ** 2)) / max(np.sqrt(np.mean(bg ** 2)), 0.001) / (10 ** rng.uniform(0.6, 1.6))
            output += bg
        output += rng.normal(0, rng.uniform(0.0001, 0.003), len(output)).astype(np.float32)
    return np.clip(output, -0.98, 0.98)


def public_negatives(root, extra=False):
    import soundfile as sf
    archive = root / "assets/dev-clean.tar.gz"
    result = {s: [] for s in ("train", "validation", "test")}
    with tarfile.open(archive) as source:
        members = source.getmembers()
        transcripts = {}
        for member in members:
            if member.name.endswith(".trans.txt"):
                for line in source.extractfile(member).read().decode().splitlines():
                    name, text = line.split(" ", 1)
                    transcripts[name] = text
        speakers = sorted({m.name.split("/")[2] for m in members if m.name.endswith(".flac")})
        np.random.default_rng(SEED).shuffle(speakers)
        split_for = {v: "train" if i < 30 else "validation" if i < 35 else "test" for i, v in enumerate(speakers)}
        used = {}
        for member in members:
            if not member.name.endswith(".flac"):
                continue
            speaker = member.name.split("/")[2]
            split = split_for[speaker]
            count = used.get(speaker, 0)
            if re.search(r"\b(MINA|MINNA|MEENA)\b", transcripts.get(Path(member.name).stem, "")):
                continue
            used[speaker] = count + 1
            if not extra and count >= 8:
                continue
            if extra and (split == "validation" or count < 8 or (split == "test" and count >= 24)):
                continue
            audio, rate = sf.read(io.BytesIO(source.extractfile(member).read()), dtype="float32")
            if rate != RATE or audio.ndim != 1:
                raise ValueError("Unexpected public speech format")
            result[split].append(audio)
    log("public_speech", **{s: {"clips": len(v), "seconds": round(sum(len(a) for a in v) / RATE, 2)} for s, v in result.items()})
    return result


def extend(root, base):
    """Broaden training-only speech; keep failed candidate and spent holdouts intact."""
    data = np.load(base / "train.npz")
    x, y, weights = list(data["x"]), list(data["y"]), list(data["weights"])
    public = public_negatives(base, extra=True)
    frontend = Frontend()
    rng = np.random.default_rng(SEED + 1)
    for i, audio in enumerate(public["train"]):
        # Simulate gain/channel variation without changing the non-trigger label.
        if rng.random() < 0.5:
            audio = signal.sosfilt(signal.butter(2, 180, "highpass", fs=RATE, output="sos"), audio).astype(np.float32)
        audio = np.clip(audio * rng.uniform(0.2, 1.5), -0.98, 0.98)
        windows = frontend.windows(audio, stride=3)
        x.extend(windows); y.extend([0] * len(windows)); weights.extend([2] * len(windows))
        if (i + 1) % 100 == 0:
            log("extended_negative_features", clips=i + 1, total=len(public["train"]), windows=len(y))
    np.savez(root / "train.npz", x=np.asarray(x, dtype=np.float32), y=np.asarray(y, dtype=np.float32), weights=np.asarray(weights, dtype=np.float32))
    shutil.copytree(base / "validation", root / "validation")
    # This original test set is now regression evidence, never a fresh holdout.
    shutil.copytree(base / "test", root / "test")
    destination = root / "fresh-public"
    destination.mkdir(mode=0o700)
    manifest = []
    for i, audio in enumerate(public["test"]):
        name = f"fresh-public-{i}"
        np.save(destination / (name + ".npy"), frontend.windows(audio))
        manifest.append({"id": name, "kind": "negative", "seconds": len(audio) / RATE, "origin": "public"})
    write_json(destination / "manifest.json", manifest)
    frontend.native.close()
    write_json(root / "preparation.json", {"base": str(base), "new_training_negative_clips": len(public["train"]),
        "new_training_negative_seconds": sum(len(a) / RATE for a in public["train"]), "training_windows": len(y),
        "original_test_is_regression": True, "fresh_public_negative_seconds": sum(m["seconds"] for m in manifest),
        "test_audio_added_to_training": False})
    log("expanded_features_ready", windows=len(y), new_negative_clips=len(public["train"]))


def prepare(root, recordings, synthetic=5000, real_variants=20):
    from piper import PiperVoice, SynthesisConfig
    import onnxruntime as ort
    from piper.config import PiperConfig
    items, audit_result = audit(recordings)
    write_json(root / "audit.json", audit_result)
    log("audit", **audit_result)
    rng = np.random.default_rng(SEED)
    frontend = Frontend()
    x, y, weights = [], [], []
    holdouts = {s: [] for s in ("validation", "test")}
    personal_background = []
    for item in items:
        audio, _ = read_wave(item["path"])
        if item["split"] != "train":
            holdouts[item["split"]].append((item["sampleId"], item["kind"], frontend.windows(audio), len(audio) / RATE, "personal"))
        elif item["kind"] == "negative":
            personal_background.append(audio.copy())
            for gain in (0.5, 1, 2):
                windows = frontend.windows(np.clip(audio * gain, -0.98, 0.98), stride=2)
                x.extend(windows); y.extend([0] * len(windows)); weights.extend([2] * len(windows))
    # Match the user's real quiet microphone floor and include pure silence.
    for i in range(150):
        silence = rng.normal(0, rng.uniform(0, 0.015), RATE * 3).astype(np.float32)
        windows = frontend.windows(silence, stride=4)
        x.extend(windows); y.extend([0] * len(windows)); weights.extend([1] * len(windows))
    positives = [item for item in items if item["split"] == "train" and item["kind"] == "positive"]
    for i, item in enumerate(positives):
        audio, _ = read_wave(item["path"])
        for _ in range(real_variants):
            augmented = positive_example(audio, rng, personal_background[int(rng.integers(len(personal_background)))])
            x.append(frontend.windows(augmented)[-1]); y.append(1); weights.append(4)
        if (i + 1) % 10 == 0:
            log("personal_positive_features", originals=i + 1)
    options = ort.SessionOptions()
    options.intra_op_num_threads = 2
    options.inter_op_num_threads = 1
    config_path = root / "assets" / (VOICE_NAME + ".json")
    voice = PiperVoice(config=PiperConfig.from_dict(json.loads(config_path.read_text())),
                       session=ort.InferenceSession(str(root / "assets" / VOICE_NAME), sess_options=options, providers=["CPUExecutionProvider"]))
    # IPA is escaped in source so these name variants do not depend on TTS spelling.
    pronunciations = ["[[m\u02c8i\u02d0n\u0259]]", "[[m\u02c8in\u0250]]", "[[m\u02c8i\u02d0na]]"]
    started = time.monotonic()
    for i in range((synthetic + 3) // 4):
        settings = SynthesisConfig(speaker_id=int(rng.integers(voice.config.num_speakers)),
                                   length_scale=float(rng.uniform(0.8, 1.3)), noise_scale=0.5, noise_w_scale=0.7)
        chunks = list(voice.synthesize(pronunciations[i % len(pronunciations)], settings))
        audio = np.concatenate([c.audio_float_array for c in chunks])
        audio = signal.resample_poly(audio, RATE, voice.config.sample_rate)
        for _ in range(min(4, synthetic - i * 4)):
            augmented = positive_example(audio, rng, personal_background[int(rng.integers(len(personal_background)))])
            x.append(frontend.windows(augmented)[-1]); y.append(1); weights.append(1)
        if (i + 1) % 50 == 0:
            log("synthetic_positive_features", generated=min(synthetic, (i + 1) * 4), elapsed_seconds=round(time.monotonic() - started))
    # Short confusing words supply hard negatives beyond the public corpus.
    for text in ("mean", "meaning", "minute", "minimum", "minor", "mine", "dinner", "Nina", "Tina", "arena", "terminal", "me now"):
        for _ in range(12):
            settings = SynthesisConfig(speaker_id=int(rng.integers(voice.config.num_speakers)))
            audio = np.concatenate([c.audio_float_array for c in voice.synthesize(text, settings)])
            audio = signal.resample_poly(audio, RATE, voice.config.sample_rate)
            padded = positive_example(audio, rng)
            windows = frontend.windows(padded, stride=2)
            x.extend(windows); y.extend([0] * len(windows)); weights.extend([2] * len(windows))
    public = public_negatives(root)
    for split, clips in public.items():
        for i, audio in enumerate(clips):
            windows = frontend.windows(audio, stride=3 if split == "train" else 1)
            if split == "train":
                x.extend(windows); y.extend([0] * len(windows)); weights.extend([1] * len(windows))
            else:
                holdouts[split].append((f"public-{split}-{i}", "negative", windows, len(audio) / RATE, "public"))
        log("negative_features", split=split, training_windows=len(y))
    frontend.native.close()
    np.savez(root / "train.npz", x=np.asarray(x, dtype=np.float32), y=np.asarray(y, dtype=np.float32), weights=np.asarray(weights, dtype=np.float32))
    for split, clips in holdouts.items():
        destination = root / split
        destination.mkdir(mode=0o700)
        manifest = []
        for name, kind, windows, seconds, origin in clips:
            np.save(destination / (name + ".npy"), windows)
            manifest.append({"id": name, "kind": kind, "seconds": seconds, "origin": origin})
        write_json(destination / "manifest.json", manifest)
    write_json(root / "preparation.json", {"synthetic_augmented": synthetic, "synthetic_originals": (synthetic + 3) // 4,
               "personal_training_originals": len(positives), "personal_variants": real_variants,
               "training_windows": len(y), "positive_windows": sum(y), "windows": WINDOWS,
               "frontend": "pyopen-wakeword==1.1.0", "seed": SEED})
    log("features_ready", windows=len(y), positives=sum(y))


def clip_scores(model, directory):
    result = []
    for meta in json.loads((directory / "manifest.json").read_text()):
        windows = np.load(directory / (meta["id"] + ".npy"))
        scores = np.concatenate([np.asarray(model(windows[i:i + 256], training=False)).ravel() for i in range(0, len(windows), 256)])
        result.append((meta, scores))
    return result


def metrics(scored, threshold):
    result = {}
    for origin in ("personal", "public"):
        positive = [v for m, v in scored if m["origin"] == origin and m["kind"] == "positive"]
        negative = [(m, v) for m, v in scored if m["origin"] == origin and m["kind"] == "negative"]
        result[origin] = {"positive_clips": len(positive), "detected": sum(activation_score(v) >= threshold for v in positive),
            "negative_seconds": sum(m["seconds"] for m, _ in negative),
            "false_positive_clips": sum(activation_score(v) >= threshold for _, v in negative),
            "max_negative_score": max((activation_score(v) for _, v in negative), default=0),
            "min_positive_score": min((activation_score(v) for v in positive), default=None)}
    return result


def activation_score(scores):
    # Native Hermes requires three consecutive 80ms over-threshold frames.
    if len(scores) < 3:
        return 0.0
    return float(np.lib.stride_tricks.sliding_window_view(scores, 3).min(axis=-1).max())


def tensorflow():
    import tensorflow as tf
    tf.config.threading.set_intra_op_parallelism_threads(4)
    tf.config.threading.set_inter_op_parallelism_threads(2)
    tf.keras.utils.set_random_seed(SEED)
    return tf


def fit(root, features=None):
    tf = tensorflow()
    features = features or root
    data = np.load(features / "train.npz")
    x, y, weights = data["x"], data["y"], data["weights"]
    hard_count = 0
    personal_negative = np.array([], dtype=int)
    preparation = json.loads((features / "preparation.json").read_text())
    if preparation.get("base"):
        baseline = np.load(Path(preparation["base"]) / "train.npz")
        # The original preparation writes personal negatives first, then noise.
        stop = int(np.flatnonzero(baseline["weights"] != 2)[0])
        personal_negative = np.arange(stop)
        if not np.all(y[personal_negative] == 0):
            raise ValueError("Personal negative feature boundary changed")
        previous = tf.keras.models.load_model(Path(preparation["base"]) / "mina.keras", compile=False)
        # Mine training-only errors, never the failed final clips or fresh checks.
        for start in range(0, len(x), 256):
            score = np.asarray(previous(x[start:start + 256], training=False)).ravel()
            hard = (y[start:start + 256] == 0) & (score >= 0.1)
            weights[start:start + 256][hard] *= 3
            hard_count += int(hard.sum())
        log("training_hard_negatives", windows=hard_count)
    # Fold feature normalization into the first dense weights before native export.
    mean, std = x.mean(axis=(0, 1)), np.maximum(x.std(axis=(0, 1)), 0.1)
    model = tf.keras.Sequential([tf.keras.Input((WINDOWS, 96)), tf.keras.layers.Flatten(),
        tf.keras.layers.Dense(64, activation="relu", kernel_regularizer=tf.keras.regularizers.l2(1e-4)),
        tf.keras.layers.Dense(32, activation="relu"), tf.keras.layers.Dense(1, activation="sigmoid")])
    x_norm = ((x - mean) / std).astype(np.float32)
    model.compile(optimizer=tf.keras.optimizers.Adam(learning_rate=0.0005), loss="binary_crossentropy")
    positive, negative = np.flatnonzero(y == 1), np.flatnonzero(y == 0)
    rng = np.random.default_rng(SEED)
    best, history = None, []
    for epoch in range(1, 17):
        losses = []
        for _ in range(160):
            negatives = (np.concatenate([rng.choice(personal_negative, 64), rng.choice(negative, 128)])
                         if len(personal_negative) else rng.choice(negative, 192))
            indices = np.concatenate([rng.choice(positive, 64), negatives])
            losses.append(float(model.train_on_batch(x_norm[indices], y[indices], sample_weight=weights[indices])))
        normalized_model = lambda z, training=False: model((z - mean) / std, training=training)
        scores = clip_scores(normalized_model, features / "validation")
        negative_max = max(activation_score(v) for m, v in scores if m["kind"] == "negative")
        threshold = max(0.5, min(0.99, negative_max + 0.03))
        quality = metrics(scores, threshold)
        margin = quality["personal"]["min_positive_score"] - negative_max
        key = (-sum(v["false_positive_clips"] for v in quality.values()), quality["personal"]["detected"], margin, -threshold)
        row = {"epoch": epoch, "threshold": threshold, "loss": losses[-1], "validation": quality}
        history.append(row)
        log("fit", **row)
        if best is None or key > best[0]:
            best = (key, [v.copy() for v in model.get_weights()], row)
        if epoch >= 5 and quality["personal"]["detected"] == quality["personal"]["positive_clips"] and negative_max < 0.1:
            break
    model.set_weights(best[1])
    reference = np.asarray(model(x_norm[:8], training=False))
    matrix, bias = model.layers[1].get_weights()
    norm_mean, norm_std = np.tile(mean, WINDOWS), np.tile(std, WINDOWS)
    offset = np.einsum("i,ij->j", norm_mean / norm_std, matrix, dtype=np.float64, optimize=False)
    model.layers[1].set_weights([matrix / norm_std[:, None], (bias - offset).astype(np.float32)])
    folding_error = float(np.max(np.abs(reference - np.asarray(model(x[:8], training=False)))))
    if not np.isfinite(folding_error) or folding_error > 5e-5:
        raise ValueError("Export normalization folding changed classifier output")
    model.save(root / "mina.keras")
    exported = export_dense_head(model, root / "mina.tflite")
    exported.update(check(root / "mina.tflite"))
    write_json(root / "selection.json", {"selected": best[2], "history": history, "export": exported,
               "normalization_folding_error": folding_error, "training_hard_negative_windows": hard_count,
               "feature_source": str(features), "personal_negative_pool_windows": len(personal_negative),
               "test_opened": False, "qualification": "Validation is short; no per-hour false-activation guarantee."})
    log("model_selected", selected=best[2], export=exported)


def evaluate(root, recordings, features=None):
    from pyopen_wakeword import OpenWakeWord, OpenWakeWordFeatures
    selection = json.loads((root / "selection.json").read_text())
    features = features or Path(selection.get("feature_source", str(root)))
    threshold = selection["selected"]["threshold"]
    items, _ = audit(recordings)
    detector = OpenWakeWord.from_model(root / "mina.tflite")
    frontend = OpenWakeWordFeatures.from_builtin()
    scored = []
    # Exact original WAVs through the native frontend/classifier, reset per clip.
    # This is a head diagnostic; model.py score checks Hermes's full decision path.
    for item in items:
        if item["split"] != "test":
            continue
        _, pcm = read_wave(item["path"])
        frontend.reset(); detector.reset()
        scores = []
        for start in range(0, len(pcm), FRAME * 2):
            frame = pcm[start:start + FRAME * 2].ljust(FRAME * 2, b"\0")
            current = []
            for embedding in frontend.process_streaming(frame):
                current.extend(detector.process_streaming(embedding))
            # Preserve every completed window in order, including startup batches.
            # Pooling by PCM frame hides intervening low scores and changes the
            # consecutive-window rule used by the native reliability patch.
            scores.extend(current)
        scored.append(({"origin": "personal", "kind": item["kind"], "seconds": item["seconds"]}, np.asarray(scores)))
    detector.close(); frontend.close()
    tf = tensorflow()
    model = tf.keras.models.load_model(root / "mina.keras", compile=False)
    scored.extend((m, v) for m, v in clip_scores(model, features / "test") if m["origin"] == "public")
    result = {"threshold": threshold, "modelSha256": sha(root / "mina.tflite"), "final_check": metrics(scored, threshold),
              "decision_path": "classifier-window-diagnostics",
              "native_engine_check": "Run model.py score in the actual native Hermes environment; capture/rearm are separate checks.",
              "test_used_for_tuning": False, "qualification": "Short held-out recordings, not daily-use acceptance or a false-alarm rate guarantee."}
    if (features / "fresh-public").exists():
        result["original_test_is_regression"] = True
        result["test_used_for_tuning"] = True
        result["test_feedback_use"] = "First failure informed broader training data; checkpoint/threshold selection still uses validation only."
        result["test_audio_added_to_training"] = False
        result["fresh_public_check"] = metrics(clip_scores(model, features / "fresh-public"), threshold)
        result["qualification"] = "Original final clips are regression evidence after the first candidate failed; fresh human live acceptance remains required."
    write_json(root / "final-check.json", result)
    log("final_check", **result)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("audit", "assets", "prepare", "extend", "fit", "evaluate"))
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--recordings", type=Path)
    parser.add_argument("--synthetic", type=int, default=5000)
    parser.add_argument("--real-variants", type=int, default=20)
    parser.add_argument("--base", type=Path)
    parser.add_argument("--features", type=Path)
    args = parser.parse_args()
    if not args.root.is_absolute() or args.root.is_symlink():
        parser.error("Use an absolute private non-symlink output directory")
    args.root.mkdir(mode=0o700, exist_ok=True)
    if args.command == "audit":
        log("audit", **audit(args.recordings)[1])
    elif args.command == "assets":
        assets(args.root)
    elif args.command == "prepare":
        prepare(args.root, args.recordings, args.synthetic, args.real_variants)
    elif args.command == "extend":
        if not args.base or not args.base.is_absolute() or args.root == args.base:
            parser.error("extend requires a different absolute --base run")
        extend(args.root, args.base)
    elif args.command == "fit":
        fit(args.root, args.features)
    else:
        evaluate(args.root, args.recordings, args.features)


if __name__ == "__main__":
    main()
