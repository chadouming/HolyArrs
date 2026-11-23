"""Unit tests for the OpenVINO GenAI LLM HTTP server."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
import io
import json
import os
from pathlib import Path
import re
import socket
import sys
import tempfile
import threading
import types
from typing import Any
import unittest
from unittest import mock
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parent))
import llm_openvino_server as srv  # noqa: E402
import openvino_common as ovc  # noqa: E402


class FakeGenerationStatus(Enum):
    RUNNING = 0
    FINISHED = 1
    STOP = 2
    CANCEL = 3
    IGNORED = 4


class FakeGenerationFinishReason(Enum):
    NONE = 0
    STOP = 1
    LENGTH = 2


class FakeSchedulerConfig:
    def __init__(self) -> None:
        self.max_num_seqs = 0
        self.max_num_batched_tokens = 0
        self.dynamic_split_fuse = True
        self.enable_prefix_caching = False
        self.cache_size = 0


class FakeGenerationConfig:
    def __init__(self) -> None:
        self.apply_chat_template = True
        self.max_new_tokens = 0
        self.do_sample = False
        self.temperature = 0.0
        self.top_p = 1.0
        self.top_k = 0
        self.repetition_penalty = 1.0
        self.stop_strings: set[str] = set()
        self.include_stop_str_in_output = True


class FakeGenAIModule:
    GenerationStatus = FakeGenerationStatus
    GenerationFinishReason = FakeGenerationFinishReason
    SchedulerConfig = FakeSchedulerConfig
    GenerationConfig = FakeGenerationConfig


@dataclass
class FakeGenerationOutput:
    generated_ids: list[int]
    finish_reason: FakeGenerationFinishReason = FakeGenerationFinishReason.STOP


class FakeEncodedInputs:
    def __init__(self, ids: list[int]) -> None:
        self.input_ids = _FakeTensor([ids])


class _FakeTensor:
    def __init__(self, rows: list[list[int]]) -> None:
        self._rows = rows
        self.shape = (len(rows), len(rows[0]) if rows else 0)


class FakeTokenizer:
    def __init__(self, support_extra_context: bool = True) -> None:
        self.support_extra_context = support_extra_context
        self.template_calls: list[dict[str, Any]] = []

    def apply_chat_template(
        self,
        messages: list[dict[str, str]],
        add_generation_prompt: bool = True,
        **kwargs: Any,
    ) -> str:
        if "extra_context" in kwargs and not self.support_extra_context:
            raise TypeError("unexpected keyword argument 'extra_context'")
        self.template_calls.append(
            {
                "messages": messages,
                "add_generation_prompt": add_generation_prompt,
                "extra_context": kwargs.get("extra_context"),
            }
        )
        return "|".join(f"{m['role']}:{m['content']}" for m in messages)

    def encode(self, text: str, add_special_tokens: bool = False) -> FakeEncodedInputs:
        tokens = [idx + 1 for idx, _ in enumerate(text.split())]
        if not tokens:
            tokens = [1]
        return FakeEncodedInputs(tokens)

    def decode(self, token_ids: list[int]) -> str:
        if token_ids == [99]:
            return '{"lines":[{"n":1,"text":"Bonjour"}]}'
        if token_ids == [77, 78]:
            return "partial"
        return "decoded:" + ",".join(str(x) for x in token_ids)


class FakeGenerationHandle:
    def __init__(
        self,
        req_id: int,
        prompt_text: str,
        gen_config: FakeGenerationConfig,
        steps_needed: int = 2,
        finish_reason: FakeGenerationFinishReason = FakeGenerationFinishReason.STOP,
        output_ids: list[int] | None = None,
    ) -> None:
        self.req_id = req_id
        self.prompt_text = prompt_text
        self.gen_config = gen_config
        self.steps_remaining = steps_needed
        self.finish_reason = finish_reason
        self.output_ids = output_ids if output_ids is not None else [99]
        self.cancelled = False
        self.cancel_event = threading.Event()

    def get_status(self) -> FakeGenerationStatus:
        if self.cancelled:
            return FakeGenerationStatus.CANCEL
        if self.steps_remaining > 0:
            return FakeGenerationStatus.RUNNING
        return FakeGenerationStatus.FINISHED

    def cancel(self) -> None:
        self.cancelled = True
        self.cancel_event.set()

    def read_all(self) -> list[FakeGenerationOutput]:
        return [FakeGenerationOutput(generated_ids=list(self.output_ids), finish_reason=self.finish_reason)]


class FakeContinuousBatchingPipeline:
    def __init__(
        self,
        tokenizer: FakeTokenizer | None = None,
        steps_needed: int = 2,
        finish_reason: FakeGenerationFinishReason = FakeGenerationFinishReason.STOP,
        output_ids: list[int] | None = None,
        on_add: Any = None,
        on_step: Any = None,
    ) -> None:
        self.tokenizer = tokenizer or FakeTokenizer()
        self.steps_needed = steps_needed
        self.finish_reason = finish_reason
        self.output_ids = output_ids
        self.on_add = on_add
        self.on_step = on_step
        self.handles: list[FakeGenerationHandle] = []
        self.step_snapshots: list[list[int]] = []
        self.last_configs: list[FakeGenerationConfig] = []

    def get_tokenizer(self) -> FakeTokenizer:
        return self.tokenizer

    def add_request(
        self,
        req_id: int,
        input_ids: Any,
        gen_config: FakeGenerationConfig,
    ) -> FakeGenerationHandle:
        self.last_configs.append(gen_config)
        handle = FakeGenerationHandle(
            req_id=req_id,
            prompt_text="",
            gen_config=gen_config,
            steps_needed=self.steps_needed,
            finish_reason=self.finish_reason,
            output_ids=self.output_ids,
        )
        self.handles.append(handle)
        if self.on_add is not None:
            self.on_add(req_id, handle)
        return handle

    def has_non_finished_requests(self) -> bool:
        return any(h.get_status() == FakeGenerationStatus.RUNNING for h in self.handles)

    def step(self) -> None:
        active_ids = [h.req_id for h in self.handles if h.get_status() == FakeGenerationStatus.RUNNING]
        self.step_snapshots.append(active_ids)
        if self.on_step is not None:
            self.on_step(active_ids, self.handles)
        for h in self.handles:
            if h.get_status() == FakeGenerationStatus.RUNNING:
                h.steps_remaining -= 1


class CliAndRoutingTests(unittest.TestCase):
    def test_cli_parses_all_runner_flags_and_device_index(self) -> None:
        argv = [
            "--model", "/config/models/Hy-MT2-1.8B-Q8_0.gguf", "--host", "127.0.0.1", "--port", "18080",
            "--api-key", "secret123", "--threads", "6", "--ctx-size", "16384", "--parallel", "8",
            "--reasoning-budget", "0", "--gpu-layers", "999", "--device", "Vulkan1",
            "--batch-size", "4096", "--ubatch-size", "512", "--flash-attn", "on",
            "--cache-type-k", "q8_0", "--cache-type-v", "q8_0", "--offline", "--no-webui", "--no-slots",
        ]
        args, unknown = srv.build_arg_parser().parse_known_args(argv)
        self.assertEqual(unknown, [])
        self.assertEqual(args.model, "/config/models/Hy-MT2-1.8B-Q8_0.gguf")
        self.assertEqual((args.port, args.api_key, args.threads), (18080, "secret123", 6))
        self.assertEqual((args.ctx_size, args.parallel, args.reasoning_budget), (16384, 8, 0))
        self.assertEqual((args.gpu_layers, args.device, args.batch_size, args.ubatch_size), (999, "Vulkan1", 4096, 512))
        self.assertEqual((args.flash_attn, args.cache_type_k, args.cache_type_v), ("on", "q8_0", "q8_0"))
        self.assertTrue(args.offline and args.no_webui and args.no_slots)
        self.assertEqual(srv.parse_device_index("GPU0"), 0)
        self.assertEqual(srv.parse_device_index("GPU1"), 1)
        self.assertEqual(srv.parse_device_index("Vulkan0"), 0)
        self.assertEqual(srv.parse_device_index("Vulkan1"), 1)
        self.assertEqual(srv.parse_device_index("0"), 0)
        self.assertIsNone(srv.parse_device_index(""))

    def test_map_llm_variant(self) -> None:
        self.assertEqual(
            srv.map_llm_variant("/models/Hy-MT2-1.8B-Q8_0.gguf", env={}),
            ("chadouming/Hy-MT2-1.8B-int8-ov", "ov-hy-mt2-1.8b-int8"),
        )
        self.assertEqual(
            srv.map_llm_variant("/models/Hy-MT2-1.8B-Q8_0.gguf", env={"HOLYARRS_OV_HYMT_INT4": "1"}),
            ("chadouming/Hy-MT2-1.8B-int4-ov", "ov-hy-mt2-1.8b-int4"),
        )
        self.assertEqual(
            srv.map_llm_variant("/models/Hy-MT2-1.8B-int4.gguf", env={}),
            ("chadouming/Hy-MT2-1.8B-int4-ov", "ov-hy-mt2-1.8b-int4"),
        )
        self.assertEqual(
            srv.map_llm_variant("/models/gemma-4-E4B-it-Q4_0.gguf", env={}),
            ("OpenVINO/gemma-4-E4B-it-int4-ov", "ov-gemma-4-e4b-it-int4"),
        )
        self.assertIsNone(srv.map_llm_variant("/models/custom-qwen.gguf", env={}))

    def test_list_openvino_devices_and_main_list_devices(self) -> None:
        go_re = re.compile(r"^\s*([A-Za-z]+[0-9]+):\s+(.+?)\s+\((\d+) MiB, (\d+) MiB free\)\s*$")

        class FakeCore:
            def __init__(self, devs: list[str], props: dict[tuple[str, str], Any]) -> None:
                self.available_devices = devs
                self._props = props

            def get_property(self, dev: str, prop: str) -> Any:
                if (dev, prop) not in self._props:
                    raise RuntimeError(f"missing {prop}")
                return self._props[(dev, prop)]

        # Multiple discrete GPUs.
        dual_core = FakeCore(
            ["CPU", "GPU.0", "GPU.1"],
            {
                ("GPU.0", "DEVICE_TYPE"): "Type.DISCRETE",
                ("GPU.0", "FULL_DEVICE_NAME"): "Intel(R) Arc(TM) B580 Graphics (dGPU)",
                ("GPU.0", "GPU_DEVICE_TOTAL_MEM_SIZE"): 12168933376,
                ("GPU.1", "DEVICE_TYPE"): "Type.DISCRETE",
                ("GPU.1", "FULL_DEVICE_NAME"): "Intel(R) Arc(TM) B580 Graphics (dGPU)",
                ("GPU.1", "GPU_DEVICE_TOTAL_MEM_SIZE"): 12168933376,
            },
        )
        lines = srv.list_openvino_devices(core=dual_core)
        self.assertEqual(len(lines), 2)
        for idx, line in enumerate(lines):
            m = go_re.match(line)
            self.assertIsNotNone(m)
            assert m is not None
            self.assertEqual(m.group(1), f"GPU{idx}")
            self.assertEqual(m.group(2), "Intel(R) Arc(TM) B580 Graphics (dGPU)")
            self.assertEqual(m.group(3), "11605")

        # iGPU + dGPU orders discrete GPU ahead of integrated GPU.
        mixed_core = FakeCore(
            ["CPU", "GPU.0", "GPU.1"],
            {
                ("GPU.0", "DEVICE_TYPE"): "Type.INTEGRATED",
                ("GPU.0", "FULL_DEVICE_NAME"): "Intel(R) Graphics (iGPU)",
                ("GPU.0", "GPU_DEVICE_TOTAL_MEM_SIZE"): 2048 * 1024 * 1024,
                ("GPU.1", "DEVICE_TYPE"): "Type.DISCRETE",
                ("GPU.1", "FULL_DEVICE_NAME"): "Intel(R) Arc(TM) B580 Graphics (dGPU)",
                ("GPU.1", "GPU_DEVICE_TOTAL_MEM_SIZE"): 12216 * 1024 * 1024,
            },
        )
        mixed_lines = srv.list_openvino_devices(core=mixed_core)
        self.assertEqual(len(mixed_lines), 2)
        self.assertIn("GPU0: Intel(R) Arc(TM) B580 Graphics (dGPU)", mixed_lines[0])
        self.assertIn("GPU1: Intel(R) Graphics (iGPU)", mixed_lines[1])

        # No GPU returns empty list.
        self.assertEqual(srv.list_openvino_devices(core=FakeCore(["CPU"], {})), [])

        # main(["--list-devices"]) prints formatted lines and returns 0.
        out_buf = io.StringIO()
        with mock.patch.object(srv, "list_openvino_devices", return_value=mixed_lines):
            with mock.patch.object(sys, "stdout", out_buf):
                rc = srv.main(["--list-devices"])
        self.assertEqual(rc, 0)
        self.assertIn("Available devices:\n", out_buf.getvalue())
        self.assertIn("GPU0: Intel(R) Arc(TM) B580 Graphics (dGPU)", out_buf.getvalue())

        # Custom unmapped existing GGUF returns directly; missing unmapped raises FileNotFoundError.
        with tempfile.TemporaryDirectory() as tmp:
            custom_gguf = os.path.join(tmp, "custom-qwen.gguf")
            with self.assertRaises(FileNotFoundError):
                srv.resolve_llm_ir_dir(custom_gguf, env={})
            Path(custom_gguf).write_bytes(b"GGUF")
            self.assertEqual(srv.resolve_llm_ir_dir(custom_gguf, env={}), custom_gguf)

    def test_resolve_llm_ir_dir_override_and_locked_download(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            override = os.path.join(tmp, "ov-override")
            os.makedirs(override)
            with self.assertRaises(FileNotFoundError):
                srv.resolve_llm_ir_dir("/models/m.gguf", env={"HOLYARRS_OV_LLM_MODEL_DIR": override})
            Path(override, "openvino_model.xml").write_text("<net/>", encoding="utf-8")
            self.assertEqual(
                srv.resolve_llm_ir_dir("/models/m.gguf", env={"HOLYARRS_OV_LLM_MODEL_DIR": override}),
                override,
            )

            gguf = os.path.join(tmp, "Hy-MT2-1.8B-Q8_0.gguf")
            downloads: list[tuple[str, str]] = []

            def fake_dl(repo_id: str, local_dir: str) -> None:
                downloads.append((repo_id, local_dir))
                Path(local_dir, "openvino_model.xml").write_text("<net/>", encoding="utf-8")

            ir_dir = srv.resolve_llm_ir_dir(gguf, env={}, downloader=fake_dl)
            self.assertEqual(downloads, [("chadouming/Hy-MT2-1.8B-int8-ov", ir_dir)])
            # Second call hits cache and does not re-download.
            self.assertEqual(srv.resolve_llm_ir_dir(gguf, env={}, downloader=fake_dl), ir_dir)
            self.assertEqual(len(downloads), 1)

            # Re-check inside .download.lock skips download if peer completed it.
            gguf_gemma = os.path.join(tmp, "gemma-4-E4B-it-Q4_0.gguf")
            gemma_ir = os.path.join(tmp, "openvino", "ov-gemma-4-e4b-it-int4")
            os.makedirs(gemma_ir, exist_ok=True)
            lock_path = os.path.join(tmp, "openvino", ".download.lock")
            res_holder: list[str] = []
            with ovc._file_lock(lock_path):
                t = threading.Thread(
                    target=lambda: res_holder.append(
                        srv.resolve_llm_ir_dir(gguf_gemma, env={}, downloader=fake_dl)
                    )
                )
                t.start()
                t.join(timeout=0.05)
                self.assertTrue(t.is_alive())
                Path(gemma_ir, "openvino_model.xml").write_text("<net/>", encoding="utf-8")
            t.join(timeout=2.0)
            self.assertEqual(res_holder, [gemma_ir])
            self.assertEqual(len(downloads), 1)


class PipelineCreationTests(unittest.TestCase):
    def test_create_llm_pipeline_props_fallback_and_scheduler_config(self) -> None:
        recorded: list[tuple[Any, str, dict[str, Any]]] = []

        def fake_pipe(ir_dir: str, sc: FakeSchedulerConfig, dev: str, props: dict[str, Any]) -> str:
            recorded.append((sc, dev, dict(props)))
            if "KV_CACHE_PRECISION" in props:
                raise RuntimeError("Unsupported property KV_CACHE_PRECISION")
            if "COMPILATION_NUM_THREADS" in props:
                raise TypeError("unexpected COMPILATION_NUM_THREADS")
            return f"pipe:{dev}"

        with mock.patch("os.cpu_count", return_value=64):
            pipe = srv.create_llm_pipeline(
                "/ir",
                "GPU.1",
                "/cache",
                threads=0,
                parallel=8,
                batch_size=4096,
                cache_type_k="q8_0",
                pipeline_cls=fake_pipe,
                scheduler_config_cls=FakeSchedulerConfig,
                env={},
            )

        self.assertEqual(pipe, "pipe:GPU.1")
        sc = recorded[0][0]
        self.assertEqual(sc.max_num_seqs, 8)
        self.assertEqual(sc.max_num_batched_tokens, 4096)
        self.assertFalse(sc.dynamic_split_fuse)
        self.assertTrue(sc.enable_prefix_caching)
        self.assertEqual(sc.cache_size, 2)  # KV_CACHE_PRECISION='u8' defaults cache_size to 2
        self.assertEqual(
            [p for _, _, p in recorded],
            [
                {"CACHE_DIR": "/cache", "COMPILATION_NUM_THREADS": 4, "KV_CACHE_PRECISION": "u8"},
                {"CACHE_DIR": "/cache", "COMPILATION_NUM_THREADS": 4},
                {"CACHE_DIR": "/cache", "KV_CACHE_PRECISION": "u8"},
                {"CACHE_DIR": "/cache"},
            ],
        )

        # If only COMPILATION_NUM_THREADS is unsupported, KV_CACHE_PRECISION is still retained.
        recorded_no_threads: list[dict[str, Any]] = []

        def no_threads_pipe(ir_dir: str, sc: FakeSchedulerConfig, dev: str, props: dict[str, Any]) -> str:
            recorded_no_threads.append(dict(props))
            if "COMPILATION_NUM_THREADS" in props:
                raise TypeError("unexpected COMPILATION_NUM_THREADS")
            return f"pipe:{dev}"

        srv.create_llm_pipeline(
            "/ir",
            "GPU.1",
            "/cache",
            threads=4,
            parallel=8,
            cache_type_k="q8_0",
            pipeline_cls=no_threads_pipe,
            scheduler_config_cls=FakeSchedulerConfig,
            env={},
        )
        self.assertEqual(
            recorded_no_threads[-1],
            {"CACHE_DIR": "/cache", "KV_CACHE_PRECISION": "u8"},
        )

    def test_create_llm_pipeline_cache_fs_retry_and_compile_lock(self) -> None:
        attempts: list[dict[str, Any]] = []

        def fs_err_pipe(ir_dir: str, sc: FakeSchedulerConfig, dev: str, props: dict[str, Any]) -> str:
            attempts.append(dict(props))
            if "CACHE_DIR" in props:
                raise RuntimeError("filesystem error: cannot set permissions [/cache/abc.blob]")
            return "pipe:nocache"

        err_buf = io.StringIO()
        with mock.patch.object(sys, "stderr", err_buf):
            pipe = srv.create_llm_pipeline(
                "/ir",
                "GPU.0",
                "/cache",
                parallel=8,
                batch_size=1024,
                pipeline_cls=fs_err_pipe,
                scheduler_config_cls=FakeSchedulerConfig,
                env={"HOLYARRS_OV_LLM_SPLIT_FUSE": "1"},
            )
        self.assertEqual(pipe, "pipe:nocache")
        self.assertEqual(attempts[0]["CACHE_DIR"], "/cache")
        self.assertNotIn("CACHE_DIR", attempts[1])
        self.assertIn("retrying without cache", err_buf.getvalue())

        # Concurrent calls serialize on .compile.lock when cache_dir exists.
        with tempfile.TemporaryDirectory() as cache_dir:
            active = 0
            max_active = 0
            lock = threading.Lock()
            t1_in = threading.Event()
            release_t1 = threading.Event()

            def locked_pipe(ir_dir: str, sc: FakeSchedulerConfig, dev: str, props: dict[str, Any]) -> str:
                nonlocal active, max_active
                with lock:
                    active += 1
                    max_active = max(max_active, active)
                if dev == "GPU.0":
                    t1_in.set()
                    release_t1.wait(timeout=2.0)
                with lock:
                    active -= 1
                return dev

            t1 = threading.Thread(
                target=srv.create_llm_pipeline,
                args=("/ir", "GPU.0", cache_dir),
                kwargs={
                    "pipeline_cls": locked_pipe,
                    "scheduler_config_cls": FakeSchedulerConfig,
                    "env": {},
                },
            )
            t2 = threading.Thread(
                target=srv.create_llm_pipeline,
                args=("/ir", "GPU.1", cache_dir),
                kwargs={
                    "pipeline_cls": locked_pipe,
                    "scheduler_config_cls": FakeSchedulerConfig,
                    "env": {},
                },
            )
            t1.start()
            self.assertTrue(t1_in.wait(timeout=2.0))
            t2.start()
            t2.join(timeout=0.05)
            self.assertTrue(t2.is_alive())
            release_t1.set()
            t1.join(timeout=2.0)
            t2.join(timeout=2.0)
            self.assertEqual(max_active, 1)
            self.assertTrue(os.path.exists(os.path.join(cache_dir, ".compile.lock")))


class EngineAndHttpTests(unittest.TestCase):
    def test_concurrent_batching_in_shared_step_calls(self) -> None:
        both_added = threading.Event()

        def wait_in_first_add(req_id: int, handle: FakeGenerationHandle) -> None:
            if req_id == 1:
                both_added.wait(timeout=2.0)

        pipe = FakeContinuousBatchingPipeline(steps_needed=2, on_add=wait_in_first_add)
        engine = srv.ContinuousBatchEngine(
            pipeline=pipe, ctx_size=8192, parallel=4, reasoning_budget=0, genai_mod=FakeGenAIModule
        )
        try:
            results: list[dict[str, Any]] = []
            errors: list[Exception] = []

            def run_req(idx: int) -> None:
                try:
                    results.append(engine.chat_completion({
                        "messages": [{"role": "user", "content": f"hello world {idx}"}],
                        "temperature": 0.7,
                        "top_p": 0.6,
                        "top_k": 20,
                        "repeat_penalty": 1.05,
                        "stop": ["END"],
                        "max_tokens": 128,
                    }))
                except Exception as exc:
                    errors.append(exc)

            t1 = threading.Thread(target=run_req, args=(1,))
            t1.start()
            while len(pipe.handles) < 1:
                threading.Event().wait(0.005)
            t2 = threading.Thread(target=run_req, args=(2,))
            t2.start()
            while len(engine._pending) < 1 and len(pipe.handles) < 2:
                threading.Event().wait(0.005)
            both_added.set()

            t1.join(timeout=3.0)
            t2.join(timeout=3.0)
            self.assertEqual(errors, [])
            self.assertEqual(len(results), 2)
            self.assertTrue(any(len(snap) == 2 for snap in pipe.step_snapshots))
            cfg = pipe.last_configs[0]
            self.assertFalse(cfg.apply_chat_template)
            self.assertEqual(cfg.max_new_tokens, 128)
            self.assertTrue(cfg.do_sample)
            self.assertAlmostEqual(cfg.temperature, 0.7)
            self.assertAlmostEqual(cfg.top_p, 0.6)
            self.assertEqual(cfg.top_k, 20)
            self.assertAlmostEqual(cfg.repetition_penalty, 1.05)
            self.assertEqual(cfg.stop_strings, {"END"})
            self.assertFalse(cfg.include_stop_str_in_output)
            self.assertEqual(pipe.tokenizer.template_calls[0]["extra_context"], {"enable_thinking": False})
        finally:
            engine.stop()

    def test_finish_reason_length_enable_thinking_override_and_cancel(self) -> None:
        # FinishReason.LENGTH + explicit enable_thinking + extra_context TypeError fallback.
        tok = FakeTokenizer(support_extra_context=False)
        pipe = FakeContinuousBatchingPipeline(
            tokenizer=tok,
            steps_needed=1,
            finish_reason=FakeGenerationFinishReason.LENGTH,
            output_ids=[77, 78],
        )
        engine = srv.ContinuousBatchEngine(
            pipeline=pipe, ctx_size=8192, parallel=2, reasoning_budget=0, genai_mod=FakeGenAIModule
        )
        try:
            res = engine.chat_completion(
                {
                    "messages": [{"role": "user", "content": "translate line"}],
                    "chat_template_kwargs": {"enable_thinking": True},
                    "max_tokens": None,
                    "max_completion_tokens": 16000,
                }
            )
            self.assertEqual(res["finish_reason"], "length")
            self.assertEqual(res["text"], "partial")
            self.assertEqual(res["completion_tokens"], 2)
            # slot_ctx = 8192 // 2 = 4096; prompt_tokens = 2 -> 16000 clamped to 4094.
            self.assertEqual(pipe.last_configs[-1].max_new_tokens, 4094)
        finally:
            engine.stop()

        # Cancellation invokes handle.cancel() and unblocks caller.
        step_entered = threading.Event()
        release_step = threading.Event()

        def slow_step(active_ids: list[int], handles: list[FakeGenerationHandle]) -> None:
            step_entered.set()
            release_step.wait(timeout=2.0)

        cancel_pipe = FakeContinuousBatchingPipeline(steps_needed=100, on_step=slow_step)
        cancel_engine = srv.ContinuousBatchEngine(
            pipeline=cancel_pipe, ctx_size=8192, parallel=1, genai_mod=FakeGenAIModule
        )
        try:
            job = cancel_engine.submit([{"role": "user", "content": "long text"}])
            self.assertTrue(step_entered.wait(timeout=2.0))
            cancel_engine.cancel(job.req_id)
            release_step.set()
            self.assertTrue(job.done.wait(timeout=2.0))
            self.assertIsInstance(job.error, srv.RequestCancelledError)
            self.assertTrue(cancel_pipe.handles[0].cancelled)
        finally:
            release_step.set()
            cancel_engine.stop()

    def test_http_endpoints_auth_context_too_big_and_health(self) -> None:
        pipe = FakeContinuousBatchingPipeline(steps_needed=1, output_ids=[99])
        engine = srv.ContinuousBatchEngine(
            pipeline=None, ctx_size=2048, parallel=4, reasoning_budget=0, model_id="hy-mt2-1.8b-q8",
            genai_mod=FakeGenAIModule,
        )
        server = srv._LLMHTTPServer(("127.0.0.1", 0), srv.make_handler(engine, api_key="topsecret"))
        port = server.server_address[1]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            # 1. /health returns 503 while loading without auth.
            with self.assertRaises(urllib.error.HTTPError) as ctx:
                urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2.0)
            self.assertEqual(ctx.exception.code, 503)
            ctx.exception.close()

            # 2. Mark ready -> /health 200.
            engine.set_pipeline(pipe, model_id="hy-mt2-1.8b-q8")
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2.0) as resp:
                self.assertEqual(resp.status, 200)
                self.assertEqual(json.loads(resp.read().decode("utf-8")), {"status": "ok"})

            # 3. /v1/models requires auth.
            with self.assertRaises(urllib.error.HTTPError) as ctx_m:
                urllib.request.urlopen(f"http://127.0.0.1:{port}/v1/models", timeout=2.0)
            self.assertEqual(ctx_m.exception.code, 401)
            ctx_m.exception.close()

            req_models = urllib.request.Request(
                f"http://127.0.0.1:{port}/v1/models",
                headers={"Authorization": "Bearer topsecret"},
            )
            with urllib.request.urlopen(req_models, timeout=2.0) as resp:
                self.assertEqual(resp.status, 200)
                models_json = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(models_json["data"][0]["id"], "hy-mt2-1.8b-q8")

            # 4. /v1/chat/completions rejects wrong key and succeeds with valid key.
            payload = json.dumps({"messages": [{"role": "user", "content": "Hello"}]}).encode("utf-8")
            bad_req = urllib.request.Request(
                f"http://127.0.0.1:{port}/v1/chat/completions",
                data=payload,
                headers={"Authorization": "Bearer wrong", "Content-Type": "application/json"},
                method="POST",
            )
            with self.assertRaises(urllib.error.HTTPError) as ctx_401:
                urllib.request.urlopen(bad_req, timeout=2.0)
            self.assertEqual(ctx_401.exception.code, 401)
            ctx_401.exception.close()

            good_req = urllib.request.Request(
                f"http://127.0.0.1:{port}/v1/chat/completions",
                data=payload,
                headers={"Authorization": "Bearer topsecret", "Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(good_req, timeout=2.0) as resp:
                self.assertEqual(resp.status, 200)
                body = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(body["object"], "chat.completion")
                self.assertEqual(
                    body["choices"][0]["message"]["content"],
                    '{"lines":[{"n":1,"text":"Bonjour"}]}',
                )
                self.assertEqual(body["choices"][0]["finish_reason"], "stop")
                self.assertGreaterEqual(body["usage"]["prompt_tokens"], 1)

            # 5. ContextTooBigError -> HTTP 400 with "exceeds the available context size".
            huge_words = " ".join(["word"] * 600)
            huge_payload = json.dumps({"messages": [{"role": "user", "content": huge_words}]}).encode("utf-8")
            huge_req = urllib.request.Request(
                f"http://127.0.0.1:{port}/v1/chat/completions",
                data=huge_payload,
                headers={"Authorization": "Bearer topsecret", "Content-Type": "application/json"},
                method="POST",
            )
            with self.assertRaises(urllib.error.HTTPError) as ctx_400:
                urllib.request.urlopen(huge_req, timeout=2.0)
            self.assertEqual(ctx_400.exception.code, 400)
            err_body = json.loads(ctx_400.exception.read().decode("utf-8"))
            ctx_400.exception.close()
            self.assertIn("exceeds the available context size", err_body["error"]["message"])
        finally:
            engine.stop()
            server.shutdown()
            server.server_close()
            thread.join(timeout=2.0)

    def test_main_returns_1_on_load_failure_and_selects_cpu_mode(self) -> None:
        sock_probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock_probe.bind(("127.0.0.1", 0))
        port = sock_probe.getsockname()[1]
        sock_probe.close()

        argv = [
            "-m", "/models/Hy-MT2-1.8B-Q8_0.gguf",
            "--host", "127.0.0.1",
            "--port", str(port),
            "-ngl", "0",
        ]
        seen_devices: list[tuple[int | None, bool]] = []

        def fake_select(dev_idx: int | None, no_gpu: bool = False, **kwargs: Any) -> str:
            seen_devices.append((dev_idx, no_gpu))
            return "CPU"

        err_buf = io.StringIO()
        with (
            mock.patch.object(os, "execv", side_effect=AssertionError("os.execv must not be called")),
            mock.patch.object(srv, "select_device", side_effect=fake_select),
            mock.patch.object(srv, "resolve_llm_ir_dir", side_effect=RuntimeError("boom")),
            mock.patch.object(sys, "stderr", err_buf),
        ):
            rc = srv.main(argv)

        self.assertEqual(rc, 1)
        self.assertEqual(seen_devices, [(None, True)])
        self.assertIn("failed to load model: boom", err_buf.getvalue())
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("127.0.0.1", port))


if __name__ == "__main__":
    unittest.main()
