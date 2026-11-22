"""Unit tests for the OpenVINO GenAI Whisper HTTP server."""

from __future__ import annotations

from dataclasses import dataclass
from http.server import ThreadingHTTPServer
import io
import json
import math
import os
from pathlib import Path
import socket
import struct
import sys
import tempfile
import threading
import types
from typing import Any
import unittest
from unittest import mock
import urllib.error
import urllib.request
import wave

sys.path.insert(0, str(Path(__file__).resolve().parent))
import whisper_openvino_server as srv  # noqa: E402


def make_pcm16_wav(
    samples: list[int],
    sample_rate: int = 16000,
    channels: int = 1,
    sampwidth: int = 2,
) -> bytes:
    """Build an in-memory WAV byte buffer from integer sample values."""
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(sampwidth)
        wf.setframerate(sample_rate)
        if sampwidth == 2:
            raw = struct.pack(f"<{len(samples)}h", *samples)
        elif sampwidth == 1:
            raw = bytes(max(0, min(255, s)) for s in samples)
        else:
            raw = b"\x00" * (len(samples) * sampwidth)
        wf.writeframes(raw)
    return buf.getvalue()


def sine_samples(seconds: float, freq: float = 440.0, amplitude: int = 12000, rate: int = 16000) -> list[int]:
    """Generate integer PCM16 sine wave samples."""
    total = int(round(seconds * rate))
    return [
        int(round(amplitude * math.sin(2.0 * math.pi * freq * i / rate)))
        for i in range(total)
    ]


def build_multipart(fields: dict[str, str], files: dict[str, tuple[str, bytes]], boundary: str = "testboundary123") -> tuple[str, bytes]:
    """Encode a multipart/form-data payload matching Go multipart.Writer."""
    chunks: list[bytes] = []
    for k, v in fields.items():
        chunks.append(f"--{boundary}\r\n".encode("ascii"))
        chunks.append(f'Content-Disposition: form-data; name="{k}"\r\n\r\n'.encode("utf-8"))
        chunks.append(v.encode("utf-8"))
        chunks.append(b"\r\n")
    for k, (filename, data) in files.items():
        chunks.append(f"--{boundary}\r\n".encode("ascii"))
        chunks.append(
            f'Content-Disposition: form-data; name="{k}"; filename="{filename}"\r\n'
            "Content-Type: application/octet-stream\r\n\r\n".encode("utf-8")
        )
        chunks.append(data)
        chunks.append(b"\r\n")
    chunks.append(f"--{boundary}--\r\n".encode("ascii"))
    return f"multipart/form-data; boundary={boundary}", b"".join(chunks)


@dataclass
class FakeWord:
    word: str
    start_ts: float
    end_ts: float


@dataclass
class FakeChunk:
    text: str
    start_ts: float
    end_ts: float
    words: list[FakeWord]


@dataclass
class FakeResult:
    language: str = ""
    chunks: list[FakeChunk] | None = None
    texts: list[str] | None = None


class FakeWhisperPipeline:
    """In-process stand-in for openvino_genai.WhisperPipeline."""

    def __init__(self) -> None:
        self.calls: list[tuple[int, dict[str, object]]] = []

    def generate(self, audio_chunk: object, **kwargs: object) -> FakeResult:
        length = len(audio_chunk)  # type: ignore[arg-type]
        self.calls.append((length, dict(kwargs)))
        if kwargs.get("return_timestamps") is False:
            return FakeResult(language="<|ja|>", texts=["<|ja|>"])
        return FakeResult(
            language="en",
            chunks=[
                FakeChunk(
                    text=" Hello world.",
                    start_ts=0.2,
                    end_ts=1.4,
                    words=[
                        FakeWord("Hello", 0.2, 0.7),
                        FakeWord("world", 0.8, 1.3),
                        FakeWord(".", 1.3, 1.4),
                    ],
                )
            ],
        )


class ModelAndDeviceTests(unittest.TestCase):
    def test_map_model_variant(self) -> None:
        self.assertEqual(
            srv.map_model_variant("/config/models/whisper-large-v3-turbo-q8_0.gguf"),
            ("OpenVINO/whisper-large-v3-turbo-int8-ov", "ov-whisper-large-v3-turbo-int8"),
        )
        self.assertEqual(
            srv.map_model_variant("/config/models/whisper-large-v3-turbo-q5_0.bin"),
            ("OpenVINO/whisper-large-v3-turbo-int4-ov", "ov-whisper-large-v3-turbo-int4"),
        )
        self.assertEqual(
            srv.map_model_variant("/config/models/ggml-large-v3-turbo-int4.gguf"),
            ("OpenVINO/whisper-large-v3-turbo-int4-ov", "ov-whisper-large-v3-turbo-int4"),
        )
        self.assertEqual(
            srv.map_model_variant("/config/models/whisper-large-v3-turbo-fp16.gguf"),
            ("OpenVINO/whisper-large-v3-turbo-fp16-ov", "ov-whisper-large-v3-turbo-fp16"),
        )

    def test_select_device(self) -> None:
        self.assertEqual(srv.select_device(0, no_gpu=True), "CPU")
        self.assertEqual(srv.select_device(-1, no_gpu=False), "CPU")
        self.assertEqual(srv.select_device(0, available_devices=["CPU", "GPU"]), "GPU")
        self.assertEqual(srv.select_device(0, available_devices=["CPU", "GPU.0", "GPU.1"]), "GPU.0")
        self.assertEqual(srv.select_device(1, available_devices=["CPU", "GPU.0", "GPU.1"]), "GPU.1")

    def test_cli_flags_include_vmsd_and_whisper_server_flags(self) -> None:
        args, _ = srv.build_arg_parser().parse_known_args(
            [
                "-m",
                "/config/models/whisper-large-v3-turbo-q8_0.gguf",
                "-vm",
                "/config/models/vad.bin",
                "-vmsd",
                "18",
                "-t",
                "4",
                "--host",
                "127.0.0.1",
                "--port",
                "9090",
                "-dev",
                "1",
                "-pp",
                "-nfa",
            ]
        )
        self.assertEqual(args.model, "/config/models/whisper-large-v3-turbo-q8_0.gguf")
        self.assertEqual(args.vad_model, "/config/models/vad.bin")
        self.assertEqual(args.vad_max_speech_duration_s, 18.0)
        self.assertEqual(args.threads, 4)
        self.assertEqual(args.port, 9090)
        self.assertEqual(args.device, 1)
        self.assertTrue(args.print_progress)
        self.assertTrue(args.no_flash_attn)

    def test_resolve_ir_dir_uses_override_and_cache(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            override = os.path.join(tmp, "custom-ov")
            os.makedirs(override)
            Path(override, "openvino_encoder_model.xml").write_text("<net/>", encoding="utf-8")
            got = srv.resolve_ir_dir("/config/models/m.gguf", env={"HOLYARRS_OV_MODEL_DIR": override})
            self.assertEqual(got, override)

            gguf = os.path.join(tmp, "whisper-large-v3-turbo-q8_0.gguf")
            downloads: list[tuple[str, str]] = []

            def fake_dl(repo_id: str, local_dir: str) -> None:
                downloads.append((repo_id, local_dir))
                Path(local_dir, "openvino_encoder_model.xml").write_text("<net/>", encoding="utf-8")

            ir_dir = srv.resolve_ir_dir(gguf, env={}, downloader=fake_dl)
            self.assertEqual(len(downloads), 1)
            self.assertEqual(downloads[0][0], "OpenVINO/whisper-large-v3-turbo-int8-ov")
            # Second call finds cached openvino_encoder_model.xml and skips download.
            ir_dir_2 = srv.resolve_ir_dir(gguf, env={}, downloader=fake_dl)
            self.assertEqual(ir_dir_2, ir_dir)
            self.assertEqual(len(downloads), 1)

    def test_configure_thread_env_defaults_and_preserves_existing(self) -> None:
        env: dict[str, str] = {"OMP_NUM_THREADS": "16", "TBB_NUM_THREADS": "8"}
        with mock.patch("os.cpu_count", return_value=64):
            cap = srv._configure_thread_env(0, env=env)
        self.assertEqual(cap, 4)
        self.assertEqual(env["OMP_NUM_THREADS"], "16")
        self.assertEqual(env["TBB_NUM_THREADS"], "8")
        for var in (
            "OPENBLAS_NUM_THREADS",
            "MKL_NUM_THREADS",
            "VECLIB_MAXIMUM_THREADS",
            "NUMEXPR_NUM_THREADS",
            "OV_CPU_THREADS_NUM",
        ):
            self.assertEqual(env[var], "4")

        explicit_env: dict[str, str] = {}
        cap_explicit = srv._configure_thread_env(2, env=explicit_env)
        self.assertEqual(cap_explicit, 2)
        for var in srv._THREAD_ENV_VARS:
            self.assertEqual(explicit_env[var], "2")

    def test_create_pipeline_fallbacks(self) -> None:
        seen: list[tuple[str, dict[str, object]]] = []

        def fake_cls(ir_dir: str, device: str, **kwargs: object) -> str:
            seen.append((device, kwargs))
            if "word_timestamps" in kwargs:
                raise TypeError("unexpected keyword argument 'word_timestamps'")
            if device == "GPU":
                raise RuntimeError("zeInit failed")
            return f"pipe:{device}"

        pipe = srv.create_pipeline("/ir", "GPU", "/cache", pipeline_cls=fake_cls, env={"HOLYARRS_OV_FALLBACK_CPU": "1"})
        self.assertEqual(pipe, "pipe:CPU")
        self.assertEqual(seen[0][0], "GPU")
        self.assertEqual(seen[-1][0], "CPU")

    def test_create_pipeline_gpu_and_cpu_property_fallbacks(self) -> None:
        gpu_calls: list[dict[str, object]] = []

        def gpu_prop_fallback(ir_dir: str, device: str, **kwargs: object) -> str:
            gpu_calls.append(dict(kwargs))
            if "COMPILATION_NUM_THREADS" in kwargs:
                raise TypeError("unexpected keyword argument 'COMPILATION_NUM_THREADS'")
            return f"gpu:{device}"

        with mock.patch("os.cpu_count", return_value=64):
            pipe = srv.create_pipeline("/ir", "GPU.0", "/cache", pipeline_cls=gpu_prop_fallback)
        self.assertEqual(pipe, "gpu:GPU.0")
        self.assertEqual(
            gpu_calls[0],
            {
                "word_timestamps": True,
                "CACHE_DIR": "/cache",
                "COMPILATION_NUM_THREADS": 4,
                "PERFORMANCE_HINT": "LATENCY",
                "NUM_STREAMS": 1,
            },
        )
        self.assertEqual(gpu_calls[-1], {"word_timestamps": True, "CACHE_DIR": "/cache"})

        cpu_calls: list[dict[str, object]] = []

        def cpu_prop_fallback(ir_dir: str, device: str, **kwargs: object) -> str:
            cpu_calls.append(dict(kwargs))
            if "PERFORMANCE_HINT" in kwargs:
                raise RuntimeError("Unsupported property PERFORMANCE_HINT")
            return f"cpu:{device}"

        pipe_cpu = srv.create_pipeline("/ir", "CPU", "/cache", threads=3, pipeline_cls=cpu_prop_fallback)
        self.assertEqual(pipe_cpu, "cpu:CPU")
        self.assertEqual(
            cpu_calls[0],
            {
                "word_timestamps": True,
                "CACHE_DIR": "/cache",
                "INFERENCE_NUM_THREADS": 3,
                "PERFORMANCE_HINT": "LATENCY",
                "NUM_STREAMS": 1,
            },
        )
        self.assertEqual(
            cpu_calls[1],
            {
                "word_timestamps": True,
                "CACHE_DIR": "/cache",
                "INFERENCE_NUM_THREADS": 3,
            },
        )

    def test_generate_with_compat_drops_unsupported_max_new_tokens(self) -> None:
        calls: list[dict[str, object]] = []

        class OldPipeline:
            def generate(self, audio_chunk: object, task: str = "transcribe", return_timestamps: bool = False) -> FakeResult:
                calls.append({"task": task, "return_timestamps": return_timestamps})
                return FakeResult(language="en")

        res = srv._generate_with_compat(
            OldPipeline(),
            [0.0],
            task="transcribe",
            return_timestamps=False,
            max_new_tokens=1,
        )
        self.assertEqual(res.language, "en")
        self.assertEqual(calls, [{"task": "transcribe", "return_timestamps": False}])

    def test_generate_with_compat_drops_threshold_kwargs_and_generic_typeerror(self) -> None:
        calls: list[dict[str, object]] = []

        class MidPipeline:
            def generate(
                self,
                audio_chunk: object,
                task: str = "transcribe",
                return_timestamps: bool = True,
                max_new_tokens: int = 224,
                initial_prompt: str = "",
            ) -> FakeResult:
                calls.append(
                    {
                        "task": task,
                        "return_timestamps": return_timestamps,
                        "max_new_tokens": max_new_tokens,
                        "initial_prompt": initial_prompt,
                    }
                )
                return FakeResult(language="en")

        res = srv._generate_with_compat(
            MidPipeline(),
            [0.0],
            task="transcribe",
            return_timestamps=True,
            max_new_tokens=224,
            compression_ratio_threshold=2.4,
            no_speech_threshold=0.6,
            logprob_threshold=-1.0,
            initial_prompt="Show Name",
        )
        self.assertEqual(res.language, "en")
        self.assertEqual(
            calls,
            [
                {
                    "task": "transcribe",
                    "return_timestamps": True,
                    "max_new_tokens": 224,
                    "initial_prompt": "Show Name",
                }
            ],
        )

        # Generic TypeError without naming the kwarg still drops optional keys in order.
        class GenericBindPipeline:
            def generate(self, audio_chunk: object, **kwargs: object) -> FakeResult:
                if "no_speech_threshold" in kwargs or "compression_ratio_threshold" in kwargs:
                    raise TypeError("generate(): incompatible function arguments")
                return FakeResult(language="en", texts=[str(sorted(kwargs.keys()))])

        res2 = srv._generate_with_compat(
            GenericBindPipeline(),
            [0.0],
            task="transcribe",
            return_timestamps=True,
            max_new_tokens=224,
            compression_ratio_threshold=2.4,
            no_speech_threshold=0.6,
            logprob_threshold=-1.0,
        )
        self.assertEqual(res2.language, "en")
        self.assertEqual(res2.texts, ["['logprob_threshold', 'return_timestamps', 'task']"])

        class AlwaysFailPipeline:
            def generate(self, audio_chunk: object, **kwargs: object) -> FakeResult:
                raise TypeError("invalid audio buffer")

        with self.assertRaises(TypeError):
            srv._generate_with_compat(AlwaysFailPipeline(), [0.0], task="transcribe")


class AudioAndMultipartTests(unittest.TestCase):
    def test_parse_multipart_binary_and_fields(self) -> None:
        wav = make_pcm16_wav([0, 12000, -12000, 0x0D0A, 0x2D2D])
        ctype, body = build_multipart(
            {"language": "en", "vad": "true", "prompt": "Alice, Bob"},
            {"file": ("audio.wav", wav)},
            boundary='"quotedBoundary99"'.strip('"'),
        )
        fields, files = srv.parse_multipart(f'multipart/form-data; boundary="quotedBoundary99"', body)
        self.assertEqual(fields["language"], "en")
        self.assertEqual(fields["vad"], "true")
        self.assertEqual(fields["prompt"], "Alice, Bob")
        self.assertEqual(files["file"], wav)

        fields2, files2 = srv.parse_multipart(ctype, body)
        self.assertEqual(fields2["language"], "en")
        self.assertEqual(files2["file"], wav)

    def test_decode_wav_pcm16_and_validation(self) -> None:
        wav = make_pcm16_wav([16384, -16384, 0], sample_rate=16000, channels=1, sampwidth=2)
        audio = srv.decode_wav_pcm16(wav)
        self.assertEqual(len(audio), 3)
        self.assertAlmostEqual(float(audio[0]), 0.5, places=4)
        self.assertAlmostEqual(float(audio[1]), -0.5, places=4)

        bad_wav = make_pcm16_wav([128, 129], sample_rate=16000, channels=1, sampwidth=1)
        with self.assertRaises(ValueError):
            srv.decode_wav_pcm16(bad_wav)

    def test_vad_skips_silence_and_preserves_timestamps(self) -> None:
        # 5.0s silence, 2.0s speech, 5.0s silence, 2.0s speech.
        s1 = [0] * (5 * 16000)
        t1 = sine_samples(2.0, amplitude=10000)
        s2 = [0] * (5 * 16000)
        t2 = sine_samples(2.0, amplitude=10000)
        audio = srv.decode_wav_pcm16(make_pcm16_wav(s1 + t1 + s2 + t2))

        spans = srv.segment_audio_vad(audio, sample_rate=16000, vad_enabled=True)
        self.assertEqual(len(spans), 2)
        self.assertGreater(spans[0][0], 4.5)
        self.assertLess(spans[0][1], 7.5)
        self.assertGreater(spans[1][0], 11.5)
        self.assertLess(spans[1][1], 14.5)

        silent = srv.decode_wav_pcm16(make_pcm16_wav([0] * (3 * 16000)))
        self.assertEqual(srv.segment_audio_vad(silent, sample_rate=16000, vad_enabled=True), [])

    def test_vad_splits_long_speech_using_max_span_sec(self) -> None:
        long_tone = sine_samples(28.0, amplitude=9000)
        audio = srv.decode_wav_pcm16(make_pcm16_wav(long_tone))
        spans = srv.segment_audio_vad(audio, sample_rate=16000, vad_enabled=True, max_span_sec=12.0)
        self.assertGreaterEqual(len(spans), 3)
        for start, end, chunk in spans:
            self.assertLessEqual(end - start, 12.05)
            self.assertGreater(len(chunk), 0)


class HttpServerTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fake_pipe = FakeWhisperPipeline()
        self.service = srv.WhisperService(pipeline=None, max_span_sec=22.0)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), srv.make_handler(self.service))
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2.0)

    def test_health_transitions_from_503_to_200(self) -> None:
        url = f"http://127.0.0.1:{self.port}/health"
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(url, timeout=2.0)
        self.assertEqual(ctx.exception.code, 503)
        ctx.exception.close()

        self.service.set_pipeline(self.fake_pipe)
        with urllib.request.urlopen(url, timeout=2.0) as resp:
            self.assertEqual(resp.status, 200)
            self.assertEqual(json.loads(resp.read().decode("utf-8")), {"status": "ok"})

    def test_detect_language_and_chunked_transcription(self) -> None:
        self.service.set_pipeline(self.fake_pipe)

        # 1. Language detection request with Content-Length.
        wav_detect = make_pcm16_wav(sine_samples(1.0, amplitude=10000))
        ctype, body = build_multipart(
            {"detect_language": "true", "response_format": "json", "no_timestamps": "true"},
            {"file": ("sample0.wav", wav_detect)},
        )
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/inference",
            data=body,
            headers={"Content-Type": ctype},
            method="POST",
        )
        err_buf = io.StringIO()
        old_stderr = sys.stderr
        sys.stderr = err_buf
        try:
            with urllib.request.urlopen(req, timeout=5.0) as resp:
                self.assertEqual(resp.status, 200)
                detect_json = json.loads(resp.read().decode("utf-8"))
        finally:
            sys.stderr = old_stderr

        self.assertEqual(detect_json, {"text": "", "language": "ja"})
        self.assertIn("auto-detected language: ja", err_buf.getvalue())
        self.assertEqual(self.fake_pipe.calls[-1][1].get("max_new_tokens"), 1)

        # 2. Multi-span transcription sent via HTTP/1.1 Transfer-Encoding: chunked (matching Go io.Pipe).
        # Layout: 4s silence, 1.5s speech, 3s silence, 1.5s speech.
        wav_trans = make_pcm16_wav(
            ([0] * (4 * 16000))
            + sine_samples(1.5, amplitude=10000)
            + ([0] * (3 * 16000))
            + sine_samples(1.5, amplitude=10000)
        )
        ctype2, body2 = build_multipart(
            {
                "language": "en",
                "response_format": "verbose_json",
                "vad": "true",
                "token_timestamps": "true",
                "prompt": "Aoi Kisaragi",
            },
            {"file": ("audio.wav", wav_trans)},
        )

        # Split body2 into arbitrary chunks over a raw socket.
        chunked_payload = bytearray()
        mid = len(body2) // 2
        for part in (body2[:mid], body2[mid:]):
            chunked_payload.extend(f"{len(part):x}\r\n".encode("ascii"))
            chunked_payload.extend(part)
            chunked_payload.extend(b"\r\n")
        chunked_payload.extend(b"0\r\n\r\n")

        raw_req = (
            f"POST /inference HTTP/1.1\r\n"
            f"Host: 127.0.0.1:{self.port}\r\n"
            f"Content-Type: {ctype2}\r\n"
            f"Transfer-Encoding: chunked\r\n"
            f"Connection: close\r\n\r\n"
        ).encode("ascii") + bytes(chunked_payload)

        err_buf2 = io.StringIO()
        sys.stderr = err_buf2
        try:
            with socket.create_connection(("127.0.0.1", self.port), timeout=5.0) as sock:
                sock.sendall(raw_req)
                chunks: list[bytes] = []
                while True:
                    data = sock.recv(4096)
                    if not data:
                        break
                    chunks.append(data)
        finally:
            sys.stderr = old_stderr

        raw_resp = b"".join(chunks)
        head, resp_body = raw_resp.split(b"\r\n\r\n", 1)
        self.assertIn(b"200 OK", head)
        out = json.loads(resp_body.decode("utf-8"))
        self.assertEqual(out["language"], "en")
        self.assertEqual(len(out["segments"]), 2)

        # Both VAD spans must receive initial_prompt.
        transcribe_calls = self.fake_pipe.calls[1:]
        self.assertEqual(len(transcribe_calls), 2)
        for _, call_kwargs in transcribe_calls:
            self.assertEqual(call_kwargs.get("initial_prompt"), "Aoi Kisaragi")

        seg0, seg1 = out["segments"]
        # First speech starts near 4.0s (minus 250ms padding = ~3.75s).
        self.assertGreater(seg0["start"], 3.8)
        self.assertGreater(seg0["end"], seg0["start"])
        # Second speech starts near 8.5s (minus 250ms padding = ~8.25s).
        self.assertGreater(seg1["start"], 8.3)
        words = seg0["words"]
        self.assertEqual([w["word"] for w in words], [" Hello", " world", "."])
        self.assertGreater(words[0]["start"], 3.8)
        self.assertIn("progress =  50%", err_buf2.getvalue())
        self.assertIn("progress = 100%", err_buf2.getvalue())


class CutoffAndSileroVadTests(unittest.TestCase):
    def test_collect_span_segments_handles_negative_end_ts_cutoff(self) -> None:
        # Chunk without words and end_ts = -1.0 extends to orig_end_sec.
        res_no_words = FakeResult(
            language="en",
            chunks=[FakeChunk(text=" Cut off at end", start_ts=1.2, end_ts=-1.0, words=[])],
        )
        segs, next_id = srv.collect_span_segments(res_no_words, 10.0, 18.5, 0)
        self.assertEqual(next_id, 1)
        self.assertEqual(len(segs), 1)
        self.assertAlmostEqual(segs[0]["start"], 11.2, places=3)
        self.assertAlmostEqual(segs[0]["end"], 18.5, places=3)

        # Chunk with words (one valid end_ts, one -1.0 sentinel) uses formatted word ends
        # capped at chunk_cap so trailing cut-off words are never clipped by segment end.
        res_words = FakeResult(
            language="en",
            chunks=[
                FakeChunk(
                    text=" Hello again",
                    start_ts=0.5,
                    end_ts=-1.0,
                    words=[
                        FakeWord("Hello", 0.5, 1.1),
                        FakeWord("again", 1.2, -1.0),
                    ],
                )
            ],
        )
        segs2, _ = srv.collect_span_segments(res_words, 4.0, 9.0, 0)
        self.assertAlmostEqual(segs2[0]["start"], 4.5, places=3)
        self.assertAlmostEqual(segs2[0]["end"], 5.4, places=3)
        self.assertGreaterEqual(segs2[0]["end"], segs2[0]["words"][-1]["end"])
        self.assertGreaterEqual(segs2[0]["words"][1]["end"], segs2[0]["words"][1]["start"])

        # Open-ended non-final chunk without words caps at the next chunk's start_ts.
        res_multi = FakeResult(
            language="en",
            chunks=[
                FakeChunk(text=" First chunk", start_ts=0.2, end_ts=-1.0, words=[]),
                FakeChunk(text=" Second chunk", start_ts=2.5, end_ts=4.0, words=[]),
            ],
        )
        segs3, next_id3 = srv.collect_span_segments(res_multi, 10.0, 16.0, 0)
        self.assertEqual(next_id3, 2)
        self.assertEqual(len(segs3), 2)
        self.assertAlmostEqual(segs3[0]["start"], 10.2, places=3)
        self.assertAlmostEqual(segs3[0]["end"], 12.5, places=3)
        self.assertAlmostEqual(segs3[1]["start"], 12.5, places=3)
        self.assertAlmostEqual(segs3[1]["end"], 14.0, places=3)

    def test_silero_vad_segmenter_skips_loud_noise_and_splits_at_valley(self) -> None:
        # 4s loud non-speech + 2s speech + 4s silence.
        audio = [0.5] * (10 * 16000)
        win = srv.SILERO_WINDOW_SAMPLES
        total_windows = len(audio) // win
        # Windows in [4s, 6s) correspond to frames ~125..187.
        start_w = int(4.0 * 16000 // win)
        end_w = int(6.0 * 16000 // win)
        probs = [0.9 if start_w <= i < end_w else 0.05 for i in range(total_windows)]

        spans = srv.segment_audio_silero(audio, probs, sample_rate=16000)
        self.assertEqual(len(spans), 1)
        self.assertGreater(spans[0][0], 3.5)
        self.assertLess(spans[0][1], 6.5)

        # Long 26s speech with a clear probability valley at 16s splits cleanly at the valley.
        long_audio = [0.2] * (26 * 16000)
        n_win = len(long_audio) // win
        valley_w = int(16.0 * 16000 // win)
        long_probs = [0.9] * n_win
        long_probs[valley_w] = 0.40  # still above neg_threshold 0.35, lowest interior point
        long_spans = srv.segment_audio_silero(long_audio, long_probs, sample_rate=16000, max_span_sec=20.0)
        self.assertEqual(len(long_spans), 2)
        self.assertAlmostEqual(long_spans[0][1], (valley_w * win + win // 2) / 16000, delta=0.05)
        self.assertAlmostEqual(long_spans[1][0], long_spans[0][1], places=4)

    @unittest.skipIf(srv.np is None, "numpy not installed")
    def test_silero_vad_ovino_shapes_state_reset_and_service(self) -> None:
        np = srv.np
        recorded: list[dict[str, object]] = []

        class FakePort:
            def __init__(self, name: str, shape: tuple[int, ...]) -> None:
                self.any_name = name
                self.shape = shape

        class FakeCompiled:
            def __init__(self, input_shape: tuple[int, ...], sr_shape: tuple[int, ...]) -> None:
                self.inputs = [
                    FakePort("input", input_shape),
                    FakePort("state", (2, 1, 128)),
                    FakePort("sr", sr_shape),
                ]
                self.outputs = [FakePort("output", (1, 1)), FakePort("stateN", (2, 1, 128))]

            def create_infer_request(self) -> object:
                class Req:
                    def infer(self_req, feeds: dict[str, object]) -> dict[str, object]:
                        x = feeds["input"]
                        st = feeds["state"]
                        sr_val = feeds["sr"]
                        recorded.append(
                            {
                                "x": x.copy(),
                                "state_sum": float(st.sum()),
                                "sr_shape": sr_val.shape,
                                "sr_val": int(sr_val.reshape(-1)[0]),
                            }
                        )
                        next_state = st + 1.0
                        # High probability when chunk energy is negative-mean test marker? No, use max abs > 0.2.
                        prob = 0.92 if float(np.max(np.abs(x[:, -srv.SILERO_WINDOW_SAMPLES:]))) > 0.2 else 0.04
                        return {
                            "output": np.array([[prob]], dtype=np.float32),
                            "stateN": next_state,
                        }

                return Req()

        class FakeCore:
            def __init__(self, input_shape: tuple[int, ...], sr_shape: tuple[int, ...]) -> None:
                self.input_shape = input_shape
                self.sr_shape = sr_shape

            def compile_model(self, path: str, device: str, config: dict[str, object] | None = None) -> FakeCompiled:
                self.last_call = (path, device, config)
                return FakeCompiled(self.input_shape, self.sr_shape)

        core = FakeCore((-1, -1), ())
        vad = srv.SileroVADOvino("/opt/whisper/silero_vad.onnx", threads=2, core=core)
        self.assertEqual(
            core.last_call,
            (
                "/opt/whisper/silero_vad.onnx",
                "CPU",
                {"INFERENCE_NUM_THREADS": 1, "NUM_STREAMS": 1, "PERFORMANCE_HINT": "LATENCY"},
            ),
        )

        # Two windows of samples: window 1 is small (0.05), window 2 is strong (0.5).
        w1 = np.full(srv.SILERO_WINDOW_SAMPLES, 0.05, dtype=np.float32)
        w2 = np.full(srv.SILERO_WINDOW_SAMPLES, 0.5, dtype=np.float32)
        probs = vad.probabilities(np.concatenate((w1, w2)))
        self.assertEqual(len(probs), 2)
        self.assertAlmostEqual(probs[0], 0.04, places=4)
        self.assertAlmostEqual(probs[1], 0.92, places=4)

        # Window 1 had (1, 576) with 64 zeros of context; window 2 carried 64 samples from w1.
        self.assertEqual(recorded[0]["x"].shape, (1, 576))
        self.assertTrue(np.allclose(recorded[0]["x"][0, :64], 0.0))
        self.assertTrue(np.allclose(recorded[1]["x"][0, :64], 0.05))
        self.assertEqual(recorded[0]["state_sum"], 0.0)
        self.assertGreater(recorded[1]["state_sum"], 0.0)
        self.assertEqual(recorded[0]["sr_shape"], ())
        self.assertEqual(recorded[0]["sr_val"], 16000)

        # Second call resets state back to zero.
        vad.probabilities(w1)
        self.assertEqual(recorded[2]["state_sum"], 0.0)

        # Static 512 input + 1D sr shape variant.
        core_1d = FakeCore((1, 512), (1,))
        vad_1d = srv.SileroVADOvino("/opt/whisper/silero_vad.onnx", core=core_1d)
        vad_1d.probabilities(w1)
        self.assertEqual(recorded[-1]["x"].shape, (1, 512))
        self.assertEqual(recorded[-1]["sr_shape"], (1,))

        # Dynamic-rank port shapes raise RuntimeError on len() and keep safe defaults.
        class DynamicRankShape:
            def __len__(self) -> int:
                raise RuntimeError("get_length() cannot be called on dynamic rank")

        core_dyn = FakeCore(DynamicRankShape(), DynamicRankShape())  # type: ignore[arg-type]
        vad_dyn = srv.SileroVADOvino("/opt/whisper/silero_vad.onnx", core=core_dyn)
        self.assertTrue(vad_dyn._use_context)
        self.assertTrue(vad_dyn._sr_scalar)

    @unittest.skipIf(srv.np is None, "numpy not installed")
    def test_silero_vad_ovino_compile_fallback_and_zero_copy_tensors(self) -> None:
        np = srv.np
        compile_attempts: list[dict[str, object] | None] = []
        zc_recorded: list[dict[str, object]] = []

        class FakeTensor:
            def __init__(self, data: Any, shared_memory: bool = False) -> None:
                self.shared_memory = shared_memory
                self.data = data if shared_memory else data.copy()

        class FakePort:
            def __init__(self, name: str, shape: tuple[int, ...]) -> None:
                self.any_name = name
                self.shape = shape

        class ZeroCopyReq:
            def __init__(self, fail_bind: bool = False) -> None:
                self.fail_bind = fail_bind
                self.in_tensors: dict[str, FakeTensor] = {}
                self.out_tensors: dict[str, FakeTensor] = {
                    "output": FakeTensor(np.zeros((1, 1), dtype=np.float32)),
                    "stateN": FakeTensor(np.zeros((2, 1, 128), dtype=np.float32)),
                }
                self.zero_arg_calls = 0
                self.feed_calls = 0

            def set_tensor(self, name: str, tensor: FakeTensor) -> None:
                if self.fail_bind:
                    raise RuntimeError("set_tensor unsupported")
                self.in_tensors[name] = tensor

            def get_tensor(self, name: str) -> FakeTensor:
                return self.out_tensors[name]

            def infer(self, feeds: dict[str, object] | None = None) -> dict[str, object] | None:
                if feeds is None:
                    self.zero_arg_calls += 1
                    x = self.in_tensors["input"].data
                    st = self.in_tensors["state"].data
                    sr_val = self.in_tensors["sr"].data
                    zc_recorded.append(
                        {
                            "x": x.copy(),
                            "state_sum": float(st.sum()),
                            "sr_val": int(sr_val.reshape(-1)[0]),
                        }
                    )
                    prob = 0.92 if float(np.max(np.abs(x[:, -srv.SILERO_WINDOW_SAMPLES:]))) > 0.2 else 0.04
                    self.out_tensors["output"].data = np.array([[prob]], dtype=np.float32)
                    self.out_tensors["stateN"].data = np.array(st + 1.0, dtype=np.float32)
                    return None
                self.feed_calls += 1
                x = feeds["input"]
                st = feeds["state"]
                prob = 0.92 if float(np.max(np.abs(x[:, -srv.SILERO_WINDOW_SAMPLES:]))) > 0.2 else 0.04
                return {
                    "output": np.array([[prob]], dtype=np.float32),
                    "stateN": st + 1.0,
                }

        class FakeCompiledZC:
            def __init__(self, fail_bind: bool = False) -> None:
                self.inputs = [
                    FakePort("input", (-1, -1)),
                    FakePort("state", (2, 1, 128)),
                    FakePort("sr", ()),
                ]
                self.outputs = [FakePort("output", (1, 1)), FakePort("stateN", (2, 1, 128))]
                self.reqs: list[ZeroCopyReq] = []
                self.fail_bind = fail_bind

            def create_infer_request(self) -> ZeroCopyReq:
                req = ZeroCopyReq(fail_bind=self.fail_bind)
                self.reqs.append(req)
                return req

        class FallbackCore:
            def __init__(self, compiled_obj: object) -> None:
                self.compiled_obj = compiled_obj

            def compile_model(
                self, path: str, device: str, config: dict[str, object] | None = None
            ) -> object:
                compile_attempts.append(config)
                if config is not None:
                    raise TypeError("unsupported config")
                return self.compiled_obj

        compiled_zc = FakeCompiledZC(fail_bind=False)
        vad_zc = srv.SileroVADOvino(
            "/opt/whisper/silero_vad.onnx", core=FallbackCore(compiled_zc)
        )
        self.assertEqual(
            compile_attempts,
            [
                {"INFERENCE_NUM_THREADS": 1, "NUM_STREAMS": 1, "PERFORMANCE_HINT": "LATENCY"},
                {"INFERENCE_NUM_THREADS": 1},
                None,
            ],
        )

        # Include a partial trailing window (100 samples) to verify single pre-pad + reshape.
        w1 = np.full(srv.SILERO_WINDOW_SAMPLES, 0.05, dtype=np.float32)
        w2 = np.full(srv.SILERO_WINDOW_SAMPLES, 0.5, dtype=np.float32)
        w3_partial = np.full(100, 0.02, dtype=np.float32)
        audio = np.concatenate((w1, w2, w3_partial))

        fake_ov = types.SimpleNamespace(Tensor=FakeTensor)
        with mock.patch.dict(sys.modules, {"openvino": fake_ov}):
            probs_zc = vad_zc.probabilities(audio)
            # Second call resets state back to 0.
            probs_zc_2 = vad_zc.probabilities(w1)

        self.assertEqual(len(probs_zc), 3)
        self.assertAlmostEqual(probs_zc[0], 0.04, places=4)
        self.assertAlmostEqual(probs_zc[1], 0.92, places=4)
        self.assertAlmostEqual(probs_zc[2], 0.04, places=4)
        self.assertEqual(len(probs_zc_2), 1)
        self.assertAlmostEqual(probs_zc_2[0], 0.04, places=4)
        self.assertEqual(compiled_zc.reqs[0].zero_arg_calls, 3)
        self.assertEqual(compiled_zc.reqs[0].feed_calls, 0)
        for req in compiled_zc.reqs:
            self.assertTrue(all(t.shared_memory for t in req.in_tensors.values()))
        self.assertEqual(zc_recorded[0]["state_sum"], 0.0)
        self.assertGreater(zc_recorded[1]["state_sum"], 0.0)
        # Window 3 carries last 64 samples of w2 (0.5) as context and zero-pads tail.
        self.assertTrue(np.allclose(zc_recorded[2]["x"][0, :64], 0.5))
        self.assertTrue(np.allclose(zc_recorded[2]["x"][0, 64:164], 0.02))
        self.assertTrue(np.allclose(zc_recorded[2]["x"][0, 164:], 0.0))
        self.assertEqual(zc_recorded[3]["state_sum"], 0.0)

        # Compare outputs against dict-based fallback when set_tensor fails on bind.
        compiled_fb = FakeCompiledZC(fail_bind=True)
        vad_fb = srv.SileroVADOvino(
            "/opt/whisper/silero_vad.onnx", core=FallbackCore(compiled_fb)
        )
        with mock.patch.dict(sys.modules, {"openvino": fake_ov}):
            probs_fb = vad_fb.probabilities(audio)
        self.assertEqual(probs_fb, probs_zc)
        self.assertEqual(compiled_fb.reqs[0].zero_arg_calls, 0)
        self.assertEqual(compiled_fb.reqs[0].feed_calls, 3)

    def test_is_repetition_hallucination_and_segment_filtering(self) -> None:
        self.assertTrue(
            srv.is_repetition_hallucination(
                "For Richer Or For Poorer, For Poorer, For Poorer, For Poorer..."
            )
        )
        self.assertTrue(
            srv.is_repetition_hallucination(
                "For Richer Or For Poorer. For Richer Or For Poorer. "
                "For Richer Or For Poorer. For Richer Or For Poorer."
            )
        )
        self.assertTrue(
            srv.is_repetition_hallucination(
                "For Richer Or For Poorer, For Poorer, For Richer Or For Poorer, "
                "For Poorer, For Richer Or For Poorer, For Poorer"
            )
        )
        self.assertTrue(srv.is_repetition_hallucination("ha ha ha ha ha"))

        self.assertFalse(srv.is_repetition_hallucination("Yeah, yeah, yeah, I hear you."))
        self.assertFalse(srv.is_repetition_hallucination("No, no, no, wait!"))
        self.assertFalse(srv.is_repetition_hallucination("No, no, no, no!"))
        self.assertFalse(srv.is_repetition_hallucination("I really, really want to go."))
        self.assertFalse(
            srv.is_repetition_hallucination(
                "I know, I know, I know, I just don't think we should leave right now."
            )
        )

        res_chunks = FakeResult(
            language="en",
            chunks=[
                FakeChunk(
                    text="For Richer Or For Poorer, For Poorer, For Poorer, For Poorer...",
                    start_ts=0.0,
                    end_ts=1.0,
                    words=[],
                ),
                FakeChunk(text=" Let's get started.", start_ts=1.2, end_ts=2.4, words=[]),
            ],
        )
        segs, next_id = srv.collect_span_segments(res_chunks, 5.0, 8.0, 0)
        self.assertEqual(next_id, 1)
        self.assertEqual(len(segs), 1)
        self.assertEqual(segs[0]["id"], 0)
        self.assertEqual(segs[0]["text"], " Let's get started.")

        res_fallback_halluc = FakeResult(
            language="en",
            texts=["For Richer Or For Poorer, For Poorer, For Poorer, For Poorer..."],
        )
        segs_fb, next_id_fb = srv.collect_span_segments(res_fallback_halluc, 0.0, 2.0, 3)
        self.assertEqual(segs_fb, [])
        self.assertEqual(next_id_fb, 3)

    def test_whisper_service_skips_initial_prompt_on_short_spans(self) -> None:
        # Span 1 is ~0.8s (< 1.5s) -> no initial_prompt; Span 2 is ~2.0s (>= 1.5s) -> keeps initial_prompt.
        short_speech = sine_samples(0.8, amplitude=12000)
        long_speech = sine_samples(2.0, amplitude=12000)
        wav = make_pcm16_wav(short_speech + ([0] * (2 * 16000)) + long_speech)

        win = srv.SILERO_WINDOW_SAMPLES
        short_end_w = int(0.8 * 16000 // win)
        gap_end_w = int(2.8 * 16000 // win)
        total_w = (len(short_speech) + 2 * 16000 + len(long_speech)) // win

        class TwoSpanSilero:
            def probabilities(self, audio: object) -> list[float]:
                probs: list[float] = []
                for i in range(total_w):
                    if i < short_end_w or i >= gap_end_w:
                        probs.append(0.95)
                    else:
                        probs.append(0.05)
                return probs

        fake_pipe = FakeWhisperPipeline()
        service = srv.WhisperService(pipeline=fake_pipe, vad_model=TwoSpanSilero())
        out = service.infer(wav, {"language": "en", "vad": "true", "prompt": "90 Day Fiance"})
        self.assertEqual(len(out["segments"]), 2)
        self.assertEqual(len(fake_pipe.calls), 2)

        short_kwargs = fake_pipe.calls[0][1]
        long_kwargs = fake_pipe.calls[1][1]
        self.assertNotIn("initial_prompt", short_kwargs)
        self.assertEqual(long_kwargs.get("initial_prompt"), "90 Day Fiance")
        for kw in (short_kwargs, long_kwargs):
            self.assertEqual(kw.get("max_new_tokens"), 224)
            self.assertEqual(kw.get("compression_ratio_threshold"), 2.4)
            self.assertEqual(kw.get("no_speech_threshold"), 0.6)
            self.assertEqual(kw.get("logprob_threshold"), -1.0)

    def test_whisper_service_uses_silero_vad_to_skip_loud_non_speech_intro(self) -> None:
        # 4s loud music intro + 2s speech: RMS VAD would include the intro, Silero VAD skips it.
        intro = sine_samples(4.0, amplitude=14000)
        speech = sine_samples(2.0, amplitude=14000)
        wav = make_pcm16_wav(intro + speech)

        win = srv.SILERO_WINDOW_SAMPLES
        intro_windows = int(4.0 * 16000 // win)
        total_windows = (len(intro) + len(speech)) // win

        class FakeSilero:
            def probabilities(self, audio: object) -> list[float]:
                return [0.05 if i < intro_windows else 0.95 for i in range(total_windows)]

        fake_pipe = FakeWhisperPipeline()
        service = srv.WhisperService(pipeline=fake_pipe, vad_model=FakeSilero())
        out = service.infer(wav, {"language": "en", "vad": "true"})
        self.assertEqual(len(out["segments"]), 1)
        self.assertGreater(out["segments"][0]["start"], 3.8)
        self.assertEqual(fake_pipe.calls[0][1]["language"], "<|en|>")


if __name__ == "__main__":
    unittest.main()
