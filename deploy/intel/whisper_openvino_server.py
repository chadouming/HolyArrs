"""OpenVINO GenAI speech server compatible with HolyArrs whisper-server calls."""

from __future__ import annotations

import argparse
from array import array
from collections.abc import Mapping
from contextlib import nullcontext
from email.message import Message
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import math
import os
from pathlib import Path
import re
import signal
import sys
import threading
from typing import Any, Callable, Sequence
import wave

_SCRIPT_DIR = str(Path(__file__).resolve().parent)
if _SCRIPT_DIR not in sys.path:
    sys.path.insert(0, _SCRIPT_DIR)

from openvino_common import (  # noqa: E402
    _THREAD_ENV_VARS,
    _configure_thread_env,
    _default_thread_cap,
    _file_lock,
    _is_cache_fs_error,
    _is_discrete_device_type,
    _is_unsupported_prop_error,
    _order_devices_by_type,
    _read_exact,
    get_cache_dir,
    log_err,
    parse_bool_env,
    query_available_devices,
    query_device_types,
    read_http_body,
    select_device,
)


if __name__ == "__main__":
    _pre_parser = argparse.ArgumentParser(add_help=False)
    _pre_parser.add_argument("-t", "--threads", type=int, default=0)
    try:
        _pre_args, _ = _pre_parser.parse_known_args(sys.argv[1:])
        _configure_thread_env(_pre_args.threads)
    except SystemExit:
        pass

try:
    import numpy as np
except ImportError:  # pragma: no cover - fallback when numpy is absent
    np = None  # type: ignore[assignment]

SAMPLE_RATE = 16000
DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 8080
MAX_DETECT_SECONDS = 30.0

VAD_FRAME_MS = 30
VAD_PAD_MS = 250
VAD_MIN_SILENCE_MS = 400
VAD_MIN_SPEECH_MS = 200
VAD_MAX_SPAN_SEC = 25.0
VAD_MIN_SPLIT_SEC = 15.0
VAD_PACK_GAP_SEC = 3.0

DEFAULT_SILERO_ONNX = "/opt/whisper/silero_vad.onnx"
SILERO_WINDOW_SAMPLES = 512
SILERO_CONTEXT_SAMPLES = 64

_LANG_TOKEN_RE = re.compile(r"<\|([a-z]{2,3})\|>")
_LANG_CODE_RE = re.compile(r"^[a-z]{2,3}$")
_LANG_NAMES = {
    "english": "en",
    "japanese": "ja",
    "spanish": "es",
    "french": "fr",
    "german": "de",
    "chinese": "zh",
    "mandarin": "zh",
    "korean": "ko",
    "italian": "it",
    "portuguese": "pt",
    "russian": "ru",
    "dutch": "nl",
    "polish": "pl",
    "turkish": "tr",
    "arabic": "ar",
    "hindi": "hi",
    "swedish": "sv",
    "norwegian": "no",
    "danish": "da",
    "finnish": "fi",
    "czech": "cs",
    "greek": "el",
    "hebrew": "he",
    "indonesian": "id",
    "malay": "ms",
    "romanian": "ro",
    "thai": "th",
    "ukrainian": "uk",
    "vietnamese": "vi",
}


def map_model_variant(
    model_path: str,
    ov_device: str = "",
    env: Mapping[str, str] | None = None,
) -> tuple[str, str]:
    """Map a GGML Whisper model path to its OpenVINO IR repository and folder."""
    src = os.environ if env is None else env
    name = os.path.basename(model_path or "").lower()
    if "q5_0" in name or "int4" in name:
        return "OpenVINO/whisper-large-v3-turbo-int4-ov", "ov-whisper-large-v3-turbo-int4"
    if "int8" in name:
        return "OpenVINO/whisper-large-v3-turbo-int8-ov", "ov-whisper-large-v3-turbo-int8"
    if "q8_0" in name:
        prefer_fp16 = src.get("HOLYARRS_OV_PREFER_FP16", "").strip().lower() in (
            "1",
            "true",
            "yes",
            "on",
        )
        if (ov_device or "").upper().startswith("GPU") and prefer_fp16:
            return "OpenVINO/whisper-large-v3-turbo-fp16-ov", "ov-whisper-large-v3-turbo-fp16"
        return "OpenVINO/whisper-large-v3-turbo-int8-ov", "ov-whisper-large-v3-turbo-int8"
    return "OpenVINO/whisper-large-v3-turbo-fp16-ov", "ov-whisper-large-v3-turbo-fp16"


def resolve_ir_dir(
    model_path: str,
    env: Mapping[str, str] | None = None,
    downloader: Callable[..., Any] | None = None,
    ov_device: str = "",
) -> str:
    """Resolve or download the OpenVINO IR directory for the requested model."""
    src = os.environ if env is None else env
    override = src.get("HOLYARRS_OV_MODEL_DIR", "").strip()
    if override:
        encoder_xml = os.path.join(override, "openvino_encoder_model.xml")
        if not os.path.isfile(encoder_xml):
            raise FileNotFoundError(f"OpenVINO model not found in HOLYARRS_OV_MODEL_DIR: {override}")
        return override

    repo_id, variant_dir = map_model_variant(model_path, ov_device=ov_device, env=src)
    base_dir = os.path.dirname(os.path.abspath(model_path)) if model_path else "/config/models"
    ir_dir = os.path.join(base_dir, "openvino", variant_dir)
    encoder_xml = os.path.join(ir_dir, "openvino_encoder_model.xml")
    if not os.path.isfile(encoder_xml):
        os.makedirs(ir_dir, exist_ok=True)
        lock_path = os.path.join(base_dir, "openvino", ".download.lock")
        with _file_lock(lock_path):
            if not os.path.isfile(encoder_xml):
                if downloader is None:
                    from huggingface_hub import snapshot_download

                    downloader = snapshot_download
                log_err(f"whisper_openvino: downloading {repo_id} to {ir_dir}")
                downloader(repo_id=repo_id, local_dir=ir_dir)
    return ir_dir


def create_pipeline(
    ir_dir: str,
    ov_device: str,
    cache_dir: str,
    threads: int = 0,
    pipeline_cls: Any = None,
    env: dict[str, str] | None = None,
) -> Any:
    """Compile an OpenVINO GenAI WhisperPipeline with compatibility fallbacks."""
    if pipeline_cls is None:
        import openvino_genai

        pipeline_cls = openvino_genai.WhisperPipeline

    src = os.environ if env is None else env
    cap = _default_thread_cap(threads)
    enable_word_ts = src.get("HOLYARRS_OV_WORD_TIMESTAMPS", "").strip().lower() in (
        "1",
        "true",
        "yes",
        "on",
    )

    def _call_pipeline(dev: str, props: dict[str, Any]) -> Any:
        if not enable_word_ts:
            return pipeline_cls(ir_dir, dev, **props)
        try:
            return pipeline_cls(ir_dir, dev, word_timestamps=True, **props)
        except TypeError:
            return pipeline_cls(ir_dir, dev, **props)
        except RuntimeError as exc:
            if "word_timestamps" in str(exc).lower():
                return pipeline_cls(ir_dir, dev, **props)
            raise

    def _build(dev: str, use_cache: bool = True) -> Any:
        if dev.startswith("GPU"):
            base_props: dict[str, Any] = {
                "COMPILATION_NUM_THREADS": cap,
                "PERFORMANCE_HINT": "LATENCY",
                "NUM_STREAMS": "1",
            }
            if use_cache and cache_dir:
                prop_candidates = [
                    {"CACHE_DIR": cache_dir, **base_props},
                    {"CACHE_DIR": cache_dir},
                ]
            else:
                prop_candidates = [base_props, {}]
        else:
            base_props = {
                "INFERENCE_NUM_THREADS": cap,
                "PERFORMANCE_HINT": "LATENCY",
                "NUM_STREAMS": "1",
            }
            if use_cache and cache_dir:
                prop_candidates = [
                    {"CACHE_DIR": cache_dir, **base_props},
                    {"CACHE_DIR": cache_dir, "INFERENCE_NUM_THREADS": cap},
                    {"CACHE_DIR": cache_dir},
                ]
            else:
                prop_candidates = [
                    base_props,
                    {"INFERENCE_NUM_THREADS": cap},
                    {},
                ]

        for idx, props in enumerate(prop_candidates):
            try:
                return _call_pipeline(dev, props)
            except (TypeError, RuntimeError) as exc:
                if _is_cache_fs_error(exc):
                    raise
                extra_keys = [k for k in props if k != "CACHE_DIR"]
                if idx < len(prop_candidates) - 1 and _is_unsupported_prop_error(exc, extra_keys):
                    continue
                raise

    def _build_with_cache(dev: str) -> Any:
        if not cache_dir:
            return _build(dev, use_cache=False)
        lock_ctx = (
            _file_lock(os.path.join(cache_dir, ".compile.lock"))
            if os.path.isdir(cache_dir)
            else nullcontext()
        )
        try:
            with lock_ctx:
                return _build(dev, use_cache=True)
        except Exception as exc:
            if not _is_cache_fs_error(exc):
                raise
            first_line = next(
                (line.strip() for line in str(exc).splitlines() if line.strip()),
                type(exc).__name__,
            )
            log_err(f"whisper_openvino: cache error ({first_line}), retrying without cache")
            return _build(dev, use_cache=False)

    try:
        return _build_with_cache(ov_device)
    except Exception as exc:
        fallback = src.get("HOLYARRS_OV_FALLBACK_CPU", "").strip().lower() in ("1", "true", "yes", "on")
        if ov_device.startswith("GPU") and fallback:
            log_err(f"whisper_openvino: GPU init failed ({exc}), falling back to CPU")
            return _build_with_cache("CPU")
        raise


def parse_content_type_boundary(content_type: str) -> bytes | None:
    """Extract the multipart boundary bytes from a Content-Type header."""
    if not content_type:
        return None
    msg = Message()
    msg["content-type"] = content_type
    if msg.get_content_maintype() != "multipart":
        return None
    boundary = msg.get_param("boundary")
    if not boundary:
        return None
    if isinstance(boundary, tuple):
        boundary = boundary[2]
    return str(boundary).encode("latin-1")


def parse_multipart(content_type: str, body: bytes) -> tuple[dict[str, str], dict[str, bytes]]:
    """Parse multipart/form-data into form fields and uploaded file payloads."""
    boundary = parse_content_type_boundary(content_type)
    if not boundary:
        raise ValueError("missing multipart boundary")

    marker = b"--" + boundary
    fields: dict[str, str] = {}
    files: dict[str, bytes] = {}

    pos = body.find(marker)
    if pos < 0:
        raise ValueError("multipart boundary not found in body")

    header_parser = BytesParser()
    while pos >= 0:
        pos += len(marker)
        if body[pos : pos + 2] == b"--":
            break
        if body[pos : pos + 2] == b"\r\n":
            pos += 2
        elif body[pos : pos + 1] == b"\n":
            pos += 1

        next_pos = body.find(b"\r\n" + marker, pos)
        eol_len = 2
        if next_pos < 0:
            next_pos = body.find(b"\n" + marker, pos)
            eol_len = 1
        if next_pos < 0:
            break

        part = body[pos:next_pos]
        pos = next_pos + eol_len

        sep_idx = part.find(b"\r\n\r\n")
        sep_len = 4
        if sep_idx < 0:
            sep_idx = part.find(b"\n\n")
            sep_len = 2
        if sep_idx < 0:
            continue

        raw_headers = part[:sep_idx]
        payload = part[sep_idx + sep_len :]
        headers = header_parser.parsebytes(raw_headers)
        name = headers.get_param("name", header="content-disposition")
        if not name:
            continue
        if isinstance(name, tuple):
            name = name[2]
        filename = headers.get_param("filename", header="content-disposition")
        if filename is not None:
            files[str(name)] = payload
        else:
            fields[str(name)] = payload.decode("utf-8", errors="replace")

    return fields, files


def _resample_linear(samples: Any, src_rate: int, dst_rate: int) -> Any:
    """Resample 1D float32 audio linearly from src_rate to dst_rate."""
    if src_rate == dst_rate or len(samples) == 0:
        return samples
    new_len = max(1, int(round(len(samples) * dst_rate / src_rate)))
    if np is not None:
        old_idx = np.linspace(0.0, len(samples) - 1, num=len(samples), dtype=np.float64)
        new_idx = np.linspace(0.0, len(samples) - 1, num=new_len, dtype=np.float64)
        return np.interp(new_idx, old_idx, samples).astype(np.float32)
    out: list[float] = []
    scale = (len(samples) - 1) / max(new_len - 1, 1)
    for i in range(new_len):
        pos = i * scale
        lo = int(pos)
        hi = min(lo + 1, len(samples) - 1)
        frac = pos - lo
        out.append(float(samples[lo] * (1.0 - frac) + samples[hi] * frac))
    return out


def decode_wav_pcm16(wav_bytes: bytes) -> Any:
    """Decode PCM16 WAV bytes into mono 16 kHz float32 samples in [-1.0, 1.0]."""
    try:
        with wave.open(io.BytesIO(wav_bytes), "rb") as wf:
            channels = wf.getnchannels()
            sampwidth = wf.getsampwidth()
            framerate = wf.getframerate()
            nframes = wf.getnframes()
            raw = wf.readframes(nframes)
    except wave.Error as exc:
        raise ValueError(f"invalid WAV audio: {exc}") from exc

    if sampwidth != 2:
        raise ValueError(f"unsupported WAV sample width: {sampwidth * 8}-bit (expected 16-bit PCM)")
    if channels < 1 or framerate <= 0:
        raise ValueError("invalid WAV channel count or sample rate")

    if np is not None:
        samples = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
        if channels > 1:
            usable = (len(samples) // channels) * channels
            samples = samples[:usable].reshape(-1, channels).mean(axis=1).astype(np.float32)
        if framerate != SAMPLE_RATE:
            samples = _resample_linear(samples, framerate, SAMPLE_RATE)
        return samples

    pcm = array("h")
    pcm.frombytes(raw[: (len(raw) // 2) * 2])
    if sys.byteorder != "little":
        pcm.byteswap()
    floats = [v / 32768.0 for v in pcm]
    if channels > 1:
        usable = (len(floats) // channels) * channels
        floats = [
            sum(floats[i : i + channels]) / channels
            for i in range(0, usable, channels)
        ]
    if framerate != SAMPLE_RATE:
        floats = _resample_linear(floats, framerate, SAMPLE_RATE)
    return floats


def _frame_rms(audio: Any, frame_len: int) -> list[float]:
    """Compute RMS energy for non-overlapping frames of frame_len samples."""
    total = len(audio)
    if total == 0:
        return []
    n_frames = max(1, (total + frame_len - 1) // frame_len)
    if np is not None:
        arr = np.asarray(audio, dtype=np.float32)
        pad = n_frames * frame_len - total
        if pad > 0:
            arr = np.pad(arr, (0, pad))
        frames = arr.reshape(n_frames, frame_len)
        return [float(x) for x in np.sqrt(np.mean(frames * frames, axis=1))]

    rms_list: list[float] = []
    for i in range(n_frames):
        chunk = audio[i * frame_len : (i + 1) * frame_len]
        if not chunk:
            rms_list.append(0.0)
        else:
            rms_list.append(math.sqrt(sum(x * x for x in chunk) / len(chunk)))
    return rms_list


def _percentile(sorted_vals: list[float], pct: float) -> float:
    if not sorted_vals:
        return 0.0
    pos = (len(sorted_vals) - 1) * (pct / 100.0)
    lo = int(math.floor(pos))
    hi = min(lo + 1, len(sorted_vals) - 1)
    frac = pos - lo
    return sorted_vals[lo] * (1.0 - frac) + sorted_vals[hi] * frac


def _pack_regions(
    regions: list[tuple[int, int]],
    step_sec: float,
    max_span_sec: float,
    max_gap_sec: float,
) -> list[tuple[int, int]]:
    """Greedily merge adjacent speech regions when gap and merged duration stay within bounds."""
    if not regions or max_gap_sec < 0:
        return list(regions)
    packed: list[tuple[int, int]] = [regions[0]]
    for next_start, next_end in regions[1:]:
        cur_start, cur_end = packed[-1]
        if (
            (next_start - cur_end) * step_sec <= max_gap_sec
            and (next_end - cur_start) * step_sec <= max_span_sec
        ):
            packed[-1] = (cur_start, max(cur_end, next_end))
        else:
            packed.append((next_start, next_end))
    return packed


def _split_long_span(
    audio: Any,
    rms: list[float],
    start_sample: int,
    end_sample: int,
    sample_rate: int,
    frame_len: int,
    max_span_sec: float,
    min_split_sec: float,
) -> list[tuple[float, float, Any]]:
    """Split a speech span exceeding max_span_sec at the quietest interior point."""
    spans: list[tuple[float, float, Any]] = []
    max_samples = max(1, int(round(max_span_sec * sample_rate)))
    min_samples = max(1, int(round(min_split_sec * sample_rate)))
    if min_samples >= max_samples:
        min_samples = max(1, max_samples // 2)

    cur = start_sample
    while end_sample - cur > max_samples:
        lo_sample = cur + min_samples
        hi_sample = min(cur + max_samples, end_sample)
        lo_frame = min(len(rms) - 1, lo_sample // frame_len)
        hi_frame = min(len(rms) - 1, max(lo_frame, hi_sample // frame_len))

        best_frame = hi_frame
        best_rms = rms[hi_frame] if rms else 0.0
        for f_idx in range(lo_frame, hi_frame + 1):
            if rms[f_idx] <= best_rms:
                best_rms = rms[f_idx]
                best_frame = f_idx

        split_sample = min(hi_sample, max(lo_sample, best_frame * frame_len + frame_len // 2))
        if split_sample <= cur:
            split_sample = cur + max_samples
        spans.append((cur / sample_rate, split_sample / sample_rate, audio[cur:split_sample]))
        cur = split_sample

    if cur < end_sample:
        spans.append((cur / sample_rate, end_sample / sample_rate, audio[cur:end_sample]))
    return spans


def segment_audio_vad(
    audio: Any,
    sample_rate: int = SAMPLE_RATE,
    vad_enabled: bool = True,
    frame_ms: int = VAD_FRAME_MS,
    pad_ms: int = VAD_PAD_MS,
    min_silence_ms: int = VAD_MIN_SILENCE_MS,
    min_speech_ms: int = VAD_MIN_SPEECH_MS,
    max_span_sec: float = VAD_MAX_SPAN_SEC,
    max_pack_gap_sec: float = VAD_PACK_GAP_SEC,
) -> list[tuple[float, float, Any]]:
    """Split audio into speech spans with exact original start/end seconds."""
    total_samples = len(audio)
    if total_samples == 0:
        return []

    max_span = max(1.0, float(max_span_sec))
    min_split = min(VAD_MIN_SPLIT_SEC, max_span * 0.6)
    frame_len = max(1, int(round(sample_rate * frame_ms / 1000.0)))
    rms = _frame_rms(audio, frame_len)
    if not rms:
        return []

    if not vad_enabled:
        return _split_long_span(
            audio, rms, 0, total_samples, sample_rate, frame_len, max_span, min_split
        )

    if max(rms) < 1e-4:
        return []

    sorted_rms = sorted(rms)
    noise_floor = _percentile(sorted_rms, 15.0)
    # When most of a short clip is speech, percentile 15 reflects speech energy.
    effective_noise = 0.003 if noise_floor > 0.015 else min(noise_floor, 0.01)
    low_thresh = max(0.005, effective_noise * 2.0)
    high_thresh = max(0.012, effective_noise * 3.5)

    active = [False] * len(rms)
    in_speech = False
    for idx, energy in enumerate(rms):
        if not in_speech:
            if energy >= high_thresh:
                in_speech = True
                active[idx] = True
        else:
            if energy >= low_thresh:
                active[idx] = True
            else:
                in_speech = False

    min_silence_frames = max(0, int(round(min_silence_ms / frame_ms)))
    if min_silence_frames > 0:
        last_true = -1
        for idx, is_act in enumerate(active):
            if is_act:
                if last_true >= 0 and idx - last_true - 1 <= min_silence_frames:
                    for fill in range(last_true + 1, idx):
                        active[fill] = True
                last_true = idx

    min_speech_frames = max(1, int(round(min_speech_ms / frame_ms)))
    pad_frames = max(0, int(round(pad_ms / frame_ms)))

    regions: list[tuple[int, int]] = []
    idx = 0
    while idx < len(active):
        if not active[idx]:
            idx += 1
            continue
        end = idx
        while end < len(active) and active[end]:
            end += 1
        if end - idx >= min_speech_frames:
            padded_start = max(0, idx - pad_frames)
            padded_end = min(len(active), end + pad_frames)
            if regions and padded_start <= regions[-1][1]:
                regions[-1] = (regions[-1][0], max(regions[-1][1], padded_end))
            else:
                regions.append((padded_start, padded_end))
        idx = end

    regions = _pack_regions(regions, frame_len / sample_rate, max_span, max_pack_gap_sec)

    spans: list[tuple[float, float, Any]] = []
    for f_start, f_end in regions:
        s_start = f_start * frame_len
        s_end = min(total_samples, f_end * frame_len)
        if s_end <= s_start:
            continue
        spans.extend(
            _split_long_span(
                audio, rms, s_start, s_end, sample_rate, frame_len, max_span, min_split
            )
        )
    return spans


def _port_name(port: Any) -> str:
    if hasattr(port, "get_any_name"):
        try:
            return str(port.get_any_name())
        except Exception:
            pass
    name = getattr(port, "any_name", "")
    return str(name) if name else ""


def _port_shape(port: Any) -> Any:
    if hasattr(port, "get_partial_shape"):
        try:
            return port.get_partial_shape()
        except Exception:
            pass
    return getattr(port, "partial_shape", getattr(port, "shape", None))


def _shape_rank(shape: Any) -> int | None:
    if shape is None:
        return None
    try:
        return len(shape)
    except (TypeError, RuntimeError):
        return None


def _dim_static_length(dim: Any) -> int | None:
    if isinstance(dim, int):
        return dim if dim >= 0 else None
    is_static = getattr(dim, "is_static", False)
    if callable(is_static):
        is_static = is_static()
    if is_static:
        get_len = getattr(dim, "get_length", None)
        if callable(get_len):
            return int(get_len())
    return None


def _lookup_output(res: Any, port: Any, name: str, idx: int) -> Any:
    if isinstance(res, dict):
        if port is not None and port in res:
            return res[port]
        if name and name in res:
            return res[name]
        if idx in res:
            return res[idx]
        vals = list(res.values())
        if idx < len(vals):
            return vals[idx]
    elif isinstance(res, (list, tuple)) and idx < len(res):
        return res[idx]
    raise KeyError(f"missing Silero VAD output {name or idx}")


def _bind_key(port: Any, fallback_name: str) -> Any:
    if port is not None and not _port_name(port):
        return port
    return fallback_name


def _load_silero_onnx_static(onnx_path: str) -> Any | None:
    """Load Silero VAD ONNX via FrontEndManager with static shapes so OpenVINO can fold dynamic ops."""
    if not isinstance(onnx_path, str) or not onnx_path.lower().endswith(".onnx"):
        return None
    try:
        import openvino as ov
        from openvino.frontend import FrontEndManager

        fem = FrontEndManager()
        fe = fem.load_by_framework("onnx")
        im = fe.load(onnx_path)
        specs = (
            ("input", [1, SILERO_CONTEXT_SAMPLES + SILERO_WINDOW_SAMPLES], ov.Type.f32),
            ("state", [2, 1, 128], ov.Type.f32),
            ("sr", [], ov.Type.i64),
        )
        for tensor_name, dims, elem_type in specs:
            place = im.get_place_by_tensor_name(tensor_name)
            if place is not None:
                im.set_partial_shape(place, ov.PartialShape(dims))
                im.set_element_type(place, elem_type)
        return fe.convert(im)
    except Exception:
        return None


class SileroVADOvino:
    """Silero VAD v5/v6 ONNX runner compiled on OpenVINO CPU."""

    def __init__(
        self,
        onnx_path: Any,
        threads: int = 0,
        core: Any = None,
        frontend_loader: Callable[[str], Any] | None = None,
    ) -> None:
        if np is None:
            raise RuntimeError("numpy is required for Silero VAD")
        use_static_frontend = core is None or frontend_loader is not None
        if core is None:
            import openvino as ov

            core = ov.Core()

        model_src: Any = onnx_path
        if use_static_frontend and isinstance(onnx_path, str):
            loader = frontend_loader or _load_silero_onnx_static
            loaded = loader(onnx_path)
            if loaded is not None:
                model_src = loaded

        compile_configs: tuple[dict[str, Any] | None, ...] = (
            {"INFERENCE_NUM_THREADS": 1, "NUM_STREAMS": "1", "PERFORMANCE_HINT": "LATENCY"},
            {"INFERENCE_NUM_THREADS": 1},
            None,
        )
        compiled = None
        for idx, cfg in enumerate(compile_configs):
            try:
                if cfg is not None:
                    compiled = core.compile_model(model_src, "CPU", cfg)
                else:
                    compiled = core.compile_model(model_src, "CPU")
                break
            except (TypeError, RuntimeError):
                if idx == len(compile_configs) - 1:
                    raise
        self._compiled = compiled

        inputs = list(getattr(self._compiled, "inputs", []) or [])
        outputs = list(getattr(self._compiled, "outputs", []) or [])

        self._input_name = "input"
        self._state_name = "state"
        self._sr_name = "sr"
        self._input_port = None
        self._state_in_port = None
        self._sr_port = None
        self._use_context = True
        self._sr_scalar = True

        for idx, port in enumerate(inputs):
            name = _port_name(port)
            shape = _port_shape(port)
            rank = _shape_rank(shape)
            lower = name.lower()
            if lower == "input" or (idx == 0 and not lower):
                self._input_name = name or "input"
                self._input_port = port
                if rank is not None and rank >= 2:
                    if _dim_static_length(shape[1]) == SILERO_WINDOW_SAMPLES:
                        self._use_context = False
            elif lower == "state":
                self._state_name = name
                self._state_in_port = port
            elif lower == "sr":
                self._sr_name = name
                self._sr_port = port
                if rank is not None:
                    self._sr_scalar = rank == 0

        self._prob_port = outputs[0] if len(outputs) > 0 else None
        self._state_port = outputs[1] if len(outputs) > 1 else None
        self._prob_name = _port_name(self._prob_port) if self._prob_port is not None else "output"
        self._state_out_name = (
            _port_name(self._state_port) if self._state_port is not None else "stateN"
        )
        for port in outputs:
            pname = _port_name(port)
            if pname.lower() in ("output", "prob"):
                self._prob_port = port
                self._prob_name = pname
            elif pname.lower() in ("staten", "state_n"):
                self._state_port = port
                self._state_out_name = pname

    def probabilities(self, audio: Any) -> list[float]:
        """Compute per-512-sample-window speech probabilities with fresh state."""
        arr = np.asarray(audio, dtype=np.float32).reshape(-1)
        total = len(arr)
        if total == 0:
            return []

        n_windows = (total + SILERO_WINDOW_SAMPLES - 1) // SILERO_WINDOW_SAMPLES
        padded_len = n_windows * SILERO_WINDOW_SAMPLES
        if total < padded_len:
            padded = np.zeros(padded_len, dtype=np.float32)
            padded[:total] = arr
            arr = padded
        else:
            arr = np.ascontiguousarray(arr, dtype=np.float32)
        windows = arr.reshape(n_windows, SILERO_WINDOW_SAMPLES)

        width = (
            SILERO_CONTEXT_SAMPLES + SILERO_WINDOW_SAMPLES
            if self._use_context
            else SILERO_WINDOW_SAMPLES
        )
        x_buf = np.zeros((1, width), dtype=np.float32)
        state_in = np.zeros((2, 1, 128), dtype=np.float32)
        sr_val = (
            np.array(SAMPLE_RATE, dtype=np.int64)
            if self._sr_scalar
            else np.array([SAMPLE_RATE], dtype=np.int64)
        )
        infer_request = self._compiled.create_infer_request()
        probs: list[float] = []

        x_tensor = None
        state_tensor = None
        out_prob_tensor = None
        out_state_tensor = None
        if hasattr(infer_request, "set_tensor") and hasattr(infer_request, "get_tensor"):
            try:
                import openvino as ov

                x_tensor = ov.Tensor(x_buf, shared_memory=True)
                state_tensor = ov.Tensor(state_in, shared_memory=True)
                sr_tensor = ov.Tensor(sr_val, shared_memory=True)
                infer_request.set_tensor(
                    _bind_key(self._input_port, self._input_name), x_tensor
                )
                infer_request.set_tensor(
                    _bind_key(self._state_in_port, self._state_name), state_tensor
                )
                infer_request.set_tensor(
                    _bind_key(self._sr_port, self._sr_name), sr_tensor
                )
                out_prob_tensor = infer_request.get_tensor(
                    _bind_key(self._prob_port, self._prob_name)
                )
                out_state_tensor = infer_request.get_tensor(
                    _bind_key(self._state_port, self._state_out_name)
                )
            except Exception:
                x_tensor = None
                state_tensor = None
                out_prob_tensor = None
                out_state_tensor = None

        if (
            x_tensor is not None
            and state_tensor is not None
            and out_prob_tensor is not None
            and out_state_tensor is not None
        ):
            x_view = x_tensor.data
            state_view = state_tensor.data
            for i in range(n_windows):
                if self._use_context:
                    x_view[0, :SILERO_CONTEXT_SAMPLES] = x_view[0, SILERO_WINDOW_SAMPLES:]
                    x_view[0, SILERO_CONTEXT_SAMPLES:] = windows[i]
                else:
                    x_view[0, :] = windows[i]
                infer_request.infer()
                prob = float(out_prob_tensor.data.flat[0])
                probs.append(min(1.0, max(0.0, prob)))
                np.copyto(state_view, out_state_tensor.data)
            return probs

        feeds: dict[str, Any] = {
            self._input_name: x_buf,
            self._state_name: state_in,
            self._sr_name: sr_val,
        }
        for i in range(n_windows):
            if self._use_context:
                x_buf[0, :SILERO_CONTEXT_SAMPLES] = x_buf[0, SILERO_WINDOW_SAMPLES:]
                x_buf[0, SILERO_CONTEXT_SAMPLES:] = windows[i]
            else:
                x_buf[0, :] = windows[i]
            res = infer_request.infer(feeds)
            raw_prob = _lookup_output(res, self._prob_port, self._prob_name, 0)
            prob = float(np.asarray(raw_prob, dtype=np.float32).flat[0])
            probs.append(min(1.0, max(0.0, prob)))

            raw_state = _lookup_output(res, self._state_port, self._state_out_name, 1)
            next_state = np.asarray(raw_state, dtype=np.float32)
            if next_state.shape == state_in.shape:
                np.copyto(state_in, next_state)
            else:
                state_in = next_state
                feeds[self._state_name] = state_in

        return probs


def segment_audio_silero(
    audio: Any,
    probs: Sequence[float],
    sample_rate: int = SAMPLE_RATE,
    threshold: float = 0.5,
    neg_threshold: float = 0.35,
    min_speech_ms: int = 250,
    min_silence_ms: int = 400,
    pad_ms: int = 250,
    max_span_sec: float = VAD_MAX_SPAN_SEC,
    max_pack_gap_sec: float = VAD_PACK_GAP_SEC,
) -> list[tuple[float, float, Any]]:
    """Split audio into speech spans using Silero VAD probabilities."""
    total_samples = len(audio)
    if total_samples == 0 or not probs:
        return []

    max_span = max(1.0, float(max_span_sec))
    min_split = min(VAD_MIN_SPLIT_SEC, max_span * 0.6)
    win_len = SILERO_WINDOW_SAMPLES
    win_ms = (win_len / sample_rate) * 1000.0

    active = [False] * len(probs)
    in_speech = False
    for idx, prob in enumerate(probs):
        if not in_speech:
            if prob >= threshold:
                in_speech = True
                active[idx] = True
        else:
            if prob >= neg_threshold:
                active[idx] = True
            else:
                in_speech = False

    min_silence_frames = max(0, int(round(min_silence_ms / win_ms)))
    if min_silence_frames > 0:
        last_true = -1
        for idx, is_act in enumerate(active):
            if is_act:
                if last_true >= 0 and idx - last_true - 1 <= min_silence_frames:
                    for fill in range(last_true + 1, idx):
                        active[fill] = True
                last_true = idx

    min_speech_frames = max(1, int(round(min_speech_ms / win_ms)))
    pad_frames = max(0, int(round(pad_ms / win_ms)))

    regions: list[tuple[int, int]] = []
    idx = 0
    while idx < len(active):
        if not active[idx]:
            idx += 1
            continue
        end = idx
        while end < len(active) and active[end]:
            end += 1
        if end - idx >= min_speech_frames:
            padded_start = max(0, idx - pad_frames)
            padded_end = min(len(active), end + pad_frames)
            if regions and padded_start <= regions[-1][1]:
                regions[-1] = (regions[-1][0], max(regions[-1][1], padded_end))
            else:
                regions.append((padded_start, padded_end))
        idx = end

    regions = _pack_regions(regions, win_len / sample_rate, max_span, max_pack_gap_sec)

    prob_list = list(probs)
    spans: list[tuple[float, float, Any]] = []
    for f_start, f_end in regions:
        s_start = f_start * win_len
        s_end = min(total_samples, f_end * win_len)
        if s_end <= s_start:
            continue
        spans.extend(
            _split_long_span(
                audio, prob_list, s_start, s_end, sample_rate, win_len, max_span, min_split
            )
        )
    return spans


def load_silero_vad(
    vad_arg: str = "",
    threads: int = 0,
    env: dict[str, str] | None = None,
    vad_cls: Any = None,
) -> Any | None:
    """Locate and compile Silero VAD ONNX on OpenVINO CPU, or return None."""
    src = os.environ if env is None else env
    candidates: list[str] = []

    override = src.get("HOLYARRS_OV_VAD_MODEL", "").strip()
    if override:
        if os.path.isfile(override):
            candidates.append(override)
        else:
            log_err(f"whisper_openvino: HOLYARRS_OV_VAD_MODEL not found: {override}")

    arg_path = (vad_arg or "").strip()
    if arg_path.lower().endswith(".onnx") and os.path.isfile(arg_path):
        candidates.append(arg_path)

    if os.path.isfile(DEFAULT_SILERO_ONNX):
        candidates.append(DEFAULT_SILERO_ONNX)

    if not candidates:
        return None

    cls = vad_cls or SileroVADOvino
    for path in candidates:
        try:
            model = cls(path, threads=threads)
            log_err(f"whisper_openvino: loaded Silero VAD from {path} on CPU")
            return model
        except Exception as exc:
            first_line = next(
                (line.strip() for line in str(exc).splitlines() if line.strip()),
                type(exc).__name__,
            )
            log_err(f"whisper_openvino: failed to load Silero VAD ({path}): {first_line}")
    return None


def normalize_language(raw: str | None) -> str:
    """Normalize a Whisper language tag or name into a 2-3 letter code."""
    if not raw:
        return ""
    text = str(raw).strip().lower()
    m = _LANG_TOKEN_RE.search(text)
    if m:
        return m.group(1)
    cleaned = text.strip("<|> ").replace("_", "-")
    if cleaned in _LANG_NAMES:
        return _LANG_NAMES[cleaned]
    if _LANG_CODE_RE.match(cleaned):
        return cleaned
    return ""


def extract_detected_language(result: Any) -> str:
    """Extract the detected language code from an OpenVINO Whisper result."""
    if isinstance(result, dict):
        lang = normalize_language(result.get("language"))
        if lang:
            return lang
        texts = result.get("texts") or ([result["text"]] if result.get("text") else [])
    else:
        lang = normalize_language(getattr(result, "language", None))
        if lang:
            return lang
        texts = getattr(result, "texts", None)
        if texts is None and getattr(result, "text", None):
            texts = [getattr(result, "text")]

    if texts:
        for item in texts:
            m = _LANG_TOKEN_RE.search(str(item))
            if m:
                return m.group(1)
    return ""


def _read_attr(obj: Any, *names: str, default: Any = None) -> Any:
    for name in names:
        if isinstance(obj, dict):
            if name in obj and obj[name] is not None:
                return obj[name]
        elif hasattr(obj, name):
            val = getattr(obj, name)
            if val is not None:
                return val
    return default


def _format_word(
    word_obj: Any, orig_start_sec: float, span_dur: float | None = None
) -> dict[str, Any] | None:
    """Format one word entry with a leading space separator for HolyArrs segment.words()."""
    raw_text = str(_read_attr(word_obj, "word", "text", default=""))
    stripped = raw_text.strip()
    if not stripped:
        return None
    # HolyArrs segment.words() splits words on a leading ASCII space and merges
    # tokens without one onto the previous word, so keep attachments unspaced.
    if stripped[0] in ".,!?;:'\")}]-" or (
        len(stripped) > 1 and stripped.startswith("'")
    ):
        formatted_text = stripped
    else:
        formatted_text = " " + stripped
    w_start = max(0.0, float(_read_attr(word_obj, "start_ts", "start", default=0.0)))
    raw_w_end = _read_attr(word_obj, "end_ts", "end", default=None)
    if raw_w_end is None or float(raw_w_end) < 0:
        if span_dur is not None:
            w_end = max(w_start, min(w_start + 0.2, span_dur))
        else:
            w_end = w_start
    else:
        w_end = max(w_start, float(raw_w_end))
    return {
        "word": formatted_text,
        "start": round(orig_start_sec + w_start, 3),
        "end": round(orig_start_sec + w_end, 3),
    }


_WORD_TOKEN_RE = re.compile(r"[^\W_]+", flags=re.UNICODE)
_OPTIONAL_GENERATE_KWARGS = (
    "compression_ratio_threshold",
    "no_speech_threshold",
    "logprob_threshold",
    "max_new_tokens",
    "initial_prompt",
    "return_timestamps",
    "language",
    "task",
    "temperature",
    "do_sample",
    "num_beams",
)
_MIN_PROMPT_SPAN_SEC = 1.5
_MIN_DETECT_SPAN_SEC = 0.5


def is_repetition_hallucination(text: str) -> bool:
    """Detect Whisper decoding repetition loops while preserving normal emphatic speech."""
    if not text:
        return False
    tokens = _WORD_TOKEN_RE.findall(text.lower())
    total = len(tokens)
    if total < 4:
        return False

    for n in range(1, 7):
        min_repeats = 5 if n == 1 else 4
        needed = n * min_repeats
        if total < needed:
            continue
        for start in range(total - needed + 1):
            gram = tokens[start : start + n]
            repeats = 1
            pos = start + n
            while pos + n <= total and tokens[pos : pos + n] == gram:
                repeats += 1
                if repeats >= min_repeats:
                    return True
                pos += n

    if total >= 14 and (len(set(tokens)) / total) <= 0.25:
        return True
    return False


def collect_span_segments(
    result: Any,
    orig_start_sec: float,
    orig_end_sec: float,
    next_id: int,
) -> tuple[list[dict[str, Any]], int]:
    """Convert one OpenVINO Whisper result into HolyArrs JSON segments."""
    segments: list[dict[str, Any]] = []
    chunks = _read_attr(result, "chunks", default=None)
    top_words = _read_attr(result, "words", default=None) or []
    span_dur = max(0.05, orig_end_sec - orig_start_sec)

    if chunks:
        chunk_list = list(chunks)
        for idx, chunk in enumerate(chunk_list):
            text = str(_read_attr(chunk, "text", default=""))
            if not text.strip() or is_repetition_hallucination(text):
                continue
            c_start = max(0.0, float(_read_attr(chunk, "start_ts", "start", default=0.0)))
            raw_end = _read_attr(chunk, "end_ts", "end", default=None)
            is_open_end = raw_end is None or float(raw_end) < 0

            chunk_cap = span_dur
            for next_chunk in chunk_list[idx + 1 :]:
                next_text = str(_read_attr(next_chunk, "text", default=""))
                if not next_text.strip():
                    continue
                raw_next_start = _read_attr(next_chunk, "start_ts", "start", default=None)
                if raw_next_start is not None:
                    next_start = float(raw_next_start)
                    if next_start > c_start:
                        chunk_cap = min(span_dur, next_start)
                        break

            chunk_words_raw = _read_attr(chunk, "words", default=None)
            if chunk_words_raw is None and top_words:
                match_end = chunk_cap if is_open_end else max(c_start, float(raw_end))
                matched = []
                for w in top_words:
                    ws = float(_read_attr(w, "start_ts", "start", default=0.0))
                    raw_we = _read_attr(w, "end_ts", "end", default=None)
                    we = ws if raw_we is None or float(raw_we) < 0 else float(raw_we)
                    mid = (ws + we) * 0.5
                    if c_start - 0.05 <= mid <= match_end + 0.05:
                        matched.append(w)
                chunk_words_raw = matched

            seg_words: list[dict[str, Any]] = []
            for w in chunk_words_raw or []:
                formatted = _format_word(w, orig_start_sec, span_dur=chunk_cap)
                if formatted is not None:
                    seg_words.append(formatted)

            if is_open_end:
                if seg_words:
                    max_word_end = max(w["end"] - orig_start_sec for w in seg_words)
                    c_end = max(c_start + 0.05, min(chunk_cap, max_word_end))
                else:
                    c_end = max(c_start + 0.05, chunk_cap)
            else:
                c_end = max(c_start, float(raw_end))

            abs_start = round(orig_start_sec + c_start, 3)
            abs_end = round(orig_start_sec + c_end, 3)
            max_abs_end = round(orig_end_sec + 0.5, 3)
            if abs_start < max_abs_end:
                abs_end = min(abs_end, max_abs_end)
            if seg_words:
                abs_end = max(abs_end, min(max_abs_end, max(w["end"] for w in seg_words)))
            if abs_end <= abs_start:
                abs_end = round(abs_start + 0.05, 3)

            seg_obj: dict[str, Any] = {
                "id": next_id,
                "text": text,
                "start": abs_start,
                "end": abs_end,
            }
            if seg_words:
                seg_obj["words"] = seg_words
            segments.append(seg_obj)
            next_id += 1
        return segments, next_id

    texts = _read_attr(result, "texts", default=None)
    if texts is None:
        single = _read_attr(result, "text", default="")
        texts = [single] if single else []
    joined = " ".join(str(t).strip() for t in texts if str(t).strip())
    if joined and not is_repetition_hallucination(joined):
        abs_start = round(orig_start_sec, 3)
        abs_end = round(max(orig_end_sec, orig_start_sec + 0.05), 3)
        segments.append({"id": next_id, "text": joined, "start": abs_start, "end": abs_end})
        next_id += 1
    return segments, next_id


def _generate_with_compat(pipeline: Any, audio_chunk: Any, **kwargs: Any) -> Any:
    """Call pipeline.generate while dropping optional kwargs unsupported by older builds."""
    opts = {k: v for k, v in kwargs.items() if v is not None}
    while True:
        try:
            return pipeline.generate(audio_chunk, **opts)
        except TypeError as exc:
            msg = str(exc)
            dropped = False
            for key in _OPTIONAL_GENERATE_KWARGS:
                if key in opts and key in msg:
                    opts.pop(key)
                    dropped = True
                    break
            if not dropped:
                for key in _OPTIONAL_GENERATE_KWARGS:
                    if key in opts:
                        opts.pop(key)
                        dropped = True
                        break
            if not dropped:
                raise


def select_detect_audio(
    audio: Any,
    spans: Sequence[tuple[float, float, Any]],
    max_seconds: float = MAX_DETECT_SECONDS,
    sample_rate: int = SAMPLE_RATE,
    min_span_sec: float = _MIN_DETECT_SPAN_SEC,
) -> Any:
    """Select speech spans for language detection, skipping brief blips when longer speech exists."""
    max_samples = max(0, int(max_seconds * sample_rate))
    if not spans:
        return audio[:max_samples]

    chosen = [chunk for start, end, chunk in spans if (end - start) >= min_span_sec]
    if not chosen:
        chosen = [chunk for _, _, chunk in spans]

    if np is not None:
        combined = chosen[0] if len(chosen) == 1 else np.concatenate(chosen)
        return np.ascontiguousarray(combined[:max_samples], dtype=np.float32)

    combined_list: list[float] = []
    for chunk in chosen:
        combined_list.extend(chunk)
        if len(combined_list) >= max_samples:
            break
    return combined_list[:max_samples]


class WhisperService:
    """Thread-safe inference wrapper around an OpenVINO Whisper pipeline."""

    def __init__(
        self,
        pipeline: Any = None,
        max_span_sec: float = VAD_MAX_SPAN_SEC,
        vad_model: Any = None,
    ) -> None:
        self._lock = threading.Lock()
        self._ready = threading.Event()
        self._pipeline = pipeline
        self._vad_model = vad_model
        self._load_error: str | None = None
        self._max_span_sec = max(1.0, float(max_span_sec))
        if pipeline is not None:
            self._ready.set()

    def set_pipeline(self, pipeline: Any) -> None:
        self._pipeline = pipeline
        self._ready.set()

    def set_vad_model(self, vad_model: Any) -> None:
        self._vad_model = vad_model

    def set_error(self, message: str) -> None:
        self._load_error = message

    def is_ready(self) -> bool:
        return self._ready.is_set() and self._pipeline is not None

    def load_error(self) -> str | None:
        return self._load_error

    def _segment_audio(
        self, audio: Any, vad_enabled: bool, threshold: float = 0.5
    ) -> list[tuple[float, float, Any]]:
        if vad_enabled and self._vad_model is not None:
            try:
                probs = self._vad_model.probabilities(audio)
                return segment_audio_silero(
                    audio,
                    probs,
                    sample_rate=SAMPLE_RATE,
                    threshold=threshold,
                    neg_threshold=max(0.05, threshold - 0.15),
                    max_span_sec=self._max_span_sec,
                )
            except Exception as exc:
                log_err(f"whisper_openvino: Silero VAD failed ({exc}), falling back to RMS VAD")
        return segment_audio_vad(
            audio,
            sample_rate=SAMPLE_RATE,
            vad_enabled=vad_enabled,
            max_span_sec=self._max_span_sec,
        )

    def infer(self, wav_bytes: bytes, fields: dict[str, str]) -> dict[str, Any]:
        """Run language detection or transcription on a WAV payload."""
        if not self.is_ready():
            raise RuntimeError(self._load_error or "model is still loading")

        audio = decode_wav_pcm16(wav_bytes)
        detect_only = fields.get("detect_language", "").strip().lower() == "true"

        threshold = 0.5
        raw_thresh = fields.get("vad_threshold", "").strip()
        if raw_thresh:
            try:
                threshold = min(0.95, max(0.05, float(raw_thresh)))
            except ValueError:
                threshold = 0.5

        with self._lock:
            if detect_only:
                spans = self._segment_audio(audio, vad_enabled=True, threshold=threshold)
                sample_audio = select_detect_audio(audio, spans)
                if len(sample_audio) == 0:
                    return {"text": "", "language": ""}
                res = _generate_with_compat(
                    self._pipeline,
                    sample_audio,
                    task="transcribe",
                    return_timestamps=False,
                    max_new_tokens=1,
                )
                lang = extract_detected_language(res)
                if lang:
                    log_err(f"whisper_openvino: auto-detected language: {lang}")
                return {"text": "", "language": lang}

            vad_enabled = fields.get("vad", "true").strip().lower() != "false"
            lang = normalize_language(fields.get("language"))
            prompt = fields.get("prompt", "").strip()
            beam_size = fields.get("beam_size", "").strip()
            temperature_inc = fields.get("temperature_inc", "").strip()
            fast_mode = beam_size == "1" or temperature_inc in ("0", "0.0")

            spans = self._segment_audio(audio, vad_enabled=vad_enabled, threshold=threshold)
            if not spans:
                log_err("whisper_openvino: progress = 100%")
                return {"language": lang or "en", "segments": []}

            all_segments: list[dict[str, Any]] = []
            next_id = 0
            total_spans = len(spans)
            ov_lang = f"<|{lang}|>" if lang and lang != "auto" else None

            for idx, (orig_start, orig_end, span_audio) in enumerate(spans):
                gen_opts: dict[str, Any] = {
                    "task": "transcribe",
                    "return_timestamps": True,
                    "max_new_tokens": 224,
                    "compression_ratio_threshold": 2.4,
                    "no_speech_threshold": 0.6,
                    "logprob_threshold": -1.0,
                }
                if fast_mode:
                    gen_opts["do_sample"] = False
                    gen_opts["num_beams"] = 1
                if ov_lang:
                    gen_opts["language"] = ov_lang
                if prompt and (orig_end - orig_start) >= _MIN_PROMPT_SPAN_SEC:
                    gen_opts["initial_prompt"] = prompt

                res = _generate_with_compat(self._pipeline, span_audio, **gen_opts)
                if not lang:
                    detected = extract_detected_language(res)
                    if detected:
                        lang = detected
                        ov_lang = f"<|{lang}|>"

                segs, next_id = collect_span_segments(res, orig_start, orig_end, next_id)
                all_segments.extend(segs)
                pct = int(round((idx + 1) * 100 / total_spans))
                log_err(f"whisper_openvino: progress = {pct:3d}%")

            return {"language": lang or "en", "segments": all_segments}


def make_handler(service: WhisperService) -> type[BaseHTTPRequestHandler]:
    """Build an HTTP handler class bound to a WhisperService instance."""

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _send_json(self, status: int, payload: dict[str, Any]) -> None:
            raw = json.dumps(payload).encode("utf-8")
            try:
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(raw)
            except (BrokenPipeError, ConnectionResetError):
                return

        def do_GET(self) -> None:
            if self.path.split("?", 1)[0] == "/health":
                if service.is_ready():
                    self._send_json(200, {"status": "ok"})
                else:
                    self._send_json(503, {"status": "loading"})
                return
            self._send_json(404, {"error": "not found"})

        def do_POST(self) -> None:
            if self.path.split("?", 1)[0] != "/inference":
                self._send_json(404, {"error": "not found"})
                return
            if not service.is_ready():
                self._send_json(503, {"error": service.load_error() or "model is loading"})
                return

            try:
                body = read_http_body(self.headers, self.rfile)
                content_type = self.headers.get("Content-Type", "")
                fields, files = parse_multipart(content_type, body)
            except Exception as exc:
                self._send_json(400, {"error": f"invalid request: {exc}"})
                return

            wav_bytes = files.get("file")
            if wav_bytes is None:
                self._send_json(400, {"error": "no 'file' field in the request"})
                return

            try:
                result = service.infer(wav_bytes, fields)
            except ValueError as exc:
                self._send_json(400, {"error": str(exc)})
                return
            except Exception as exc:
                log_err(f"whisper_openvino: inference error: {exc}")
                self._send_json(500, {"error": f"failed to process audio: {exc}"})
                return

            self._send_json(200, result)

    return Handler


def build_arg_parser() -> argparse.ArgumentParser:
    """Create a CLI parser compatible with HolyArrs whisper-server flags."""
    parser = argparse.ArgumentParser(description="OpenVINO GenAI Whisper HTTP server")
    parser.add_argument("-m", "--model", default="", help="GGML model path used to select OpenVINO IR variant")
    parser.add_argument("-vm", "--vad-model", default="", help="Accepted for CLI compatibility")
    parser.add_argument(
        "-vmsd",
        "--vad-max-speech-duration-s",
        type=float,
        default=VAD_MAX_SPAN_SEC,
        help="Maximum speech span duration in seconds",
    )
    parser.add_argument("-t", "--threads", type=int, default=0, help="CPU thread count")
    parser.add_argument("--host", default=DEFAULT_HOST, help="Bind host")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help="Bind port")
    parser.add_argument("-dev", "--device", type=int, default=0, help="Intel GPU device index")
    parser.add_argument("-ng", "--no-gpu", action="store_true", help="Run on CPU instead of GPU")
    parser.add_argument("-pp", "--print-progress", action="store_true", help="Print progress to stderr")
    parser.add_argument("-fa", "--flash-attn", action="store_true", help="Accepted for CLI compatibility")
    parser.add_argument("-nfa", "--no-flash-attn", action="store_true", help="Accepted for CLI compatibility")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    """Start the HTTP server, load the OpenVINO Whisper pipeline, and serve until stopped."""
    args, _ = build_arg_parser().parse_known_args(argv)
    _configure_thread_env(args.threads)
    service = WhisperService(max_span_sec=args.vad_max_speech_duration_s)
    server = ThreadingHTTPServer((args.host, args.port), make_handler(service))

    def _shutdown(_signum: int, _frame: Any) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    def _load_model() -> None:
        try:
            avail = query_available_devices()
            dev_types = query_device_types(avail)
            ov_device = select_device(
                args.device,
                no_gpu=args.no_gpu,
                available_devices=avail,
                device_types=dev_types,
            )
            ir_dir = resolve_ir_dir(args.model, ov_device=ov_device)
            cache_dir = get_cache_dir()
            log_err(f"whisper_openvino: loading {ir_dir} on {ov_device} (cache {cache_dir})")
            pipe = create_pipeline(ir_dir, ov_device, cache_dir, threads=args.threads)
            vad_model = load_silero_vad(args.vad_model, threads=args.threads)
            if vad_model is not None:
                service.set_vad_model(vad_model)
            service.set_pipeline(pipe)
            log_err(f"whisper_openvino: ready on {args.host}:{args.port}")
        except Exception as exc:
            service.set_error(str(exc))
            log_err(f"whisper_openvino: failed to load model: {exc}")
            threading.Thread(target=server.shutdown, daemon=True).start()

    loader = threading.Thread(target=_load_model, daemon=True)
    loader.start()

    try:
        server.serve_forever()
    finally:
        server.server_close()

    if service.load_error():
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
