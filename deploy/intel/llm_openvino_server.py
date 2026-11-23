"""OpenVINO GenAI LLM HTTP server compatible with HolyArrs llama-server calls."""

from __future__ import annotations

import argparse, hmac, json, os, re, signal, sys, threading, time
from collections import deque
from collections.abc import Mapping, Sequence
from contextlib import nullcontext
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable

_SCRIPT_DIR = str(Path(__file__).resolve().parent)
if _SCRIPT_DIR not in sys.path:
    sys.path.insert(0, _SCRIPT_DIR)

from openvino_common import (  # noqa: E402
    _configure_thread_env, _default_thread_cap, _file_lock, _is_cache_fs_error,
    _is_unsupported_prop_error, _order_devices_by_type, get_cache_dir, log_err,
    parse_bool_env, query_available_devices, query_device_types, read_http_body, select_device,
)

if __name__ == "__main__":
    _pre_parser = argparse.ArgumentParser(add_help=False)
    _pre_parser.add_argument("-t", "--threads", type=int, default=0)
    try:
        _pre_args, _ = _pre_parser.parse_known_args(sys.argv[1:])
        _configure_thread_env(_pre_args.threads)
    except SystemExit:
        pass

DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 8080
_DEV_INDEX_RE = re.compile(r"^(?:[A-Za-z]+)?(\d+)$")


class ContextTooBigError(ValueError):
    """Raised when a prompt exceeds the configured per-slot context window."""


class RequestCancelledError(RuntimeError):
    """Raised when an in-flight generation request is cancelled."""


class _LLMHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def parse_device_index(device_str: str) -> int | None:
    """Parse a device string such as GPU0, Vulkan0, or 1 into an index."""
    m = _DEV_INDEX_RE.match((device_str or "").strip())
    return int(m.group(1)) if m else None


def list_openvino_devices(core: Any | None = None) -> list[str]:
    """List OpenVINO GPU devices formatted to match HolyArrs devices.go."""
    try:
        if core is None:
            import openvino as ov

            core = ov.Core()
        devs = list(core.available_devices)
    except Exception:
        return []
    gpu_devs = [d for d in devs if d == "GPU" or d.startswith("GPU.")]
    if not gpu_devs:
        return []
    dev_types = query_device_types(gpu_devs, core=core)
    ordered = _order_devices_by_type(gpu_devs, dev_types)
    lines: list[str] = []
    for idx, dev in enumerate(ordered):
        try:
            name = str(core.get_property(dev, "FULL_DEVICE_NAME") or dev).strip() or dev
        except Exception:
            name = dev
        try:
            total_bytes = core.get_property(dev, "GPU_DEVICE_TOTAL_MEM_SIZE")
            total_mib = max(1, int(total_bytes) // (1024 * 1024))
        except Exception:
            total_mib = 0
        lines.append(f"  GPU{idx}: {name} ({total_mib} MiB, {total_mib} MiB free)")
    return lines


def map_llm_variant(model_path: str, env: Mapping[str, str] | None = None) -> tuple[str, str] | None:
    """Map a GGUF model path to its (repo_id, variant_dir) OpenVINO IR target."""
    name = os.path.basename(model_path or "").lower()
    if "hy-mt2-1.8b" in name:
        int4_val, int4_set = parse_bool_env("HOLYARRS_OV_HYMT_INT4", env)
        if "int4" in name or (int4_set and int4_val):
            return "chadouming/Hy-MT2-1.8B-int4-ov", "ov-hy-mt2-1.8b-int4"
        return "chadouming/Hy-MT2-1.8B-int8-ov", "ov-hy-mt2-1.8b-int8"
    if "gemma-4-e4b" in name:
        return "OpenVINO/gemma-4-E4B-it-int4-ov", "ov-gemma-4-e4b-it-int4"
    return None


def resolve_variant_info(
    model_path: str, env: Mapping[str, str] | None = None
) -> tuple[str, str, str] | None:
    """Return (repo_id, variant_dir, ir_dir) for a mapped model, or None."""
    mapped = map_llm_variant(model_path, env=env)
    if mapped is None:
        return None
    repo_id, variant_dir = mapped
    base_dir = os.path.dirname(os.path.abspath(model_path)) if model_path else "/config/models"
    return repo_id, variant_dir, os.path.join(base_dir, "openvino", variant_dir)


def resolve_llm_ir_dir(
    model_path: str, env: Mapping[str, str] | None = None, downloader: Callable[..., Any] | None = None
) -> str:
    """Resolve or download the OpenVINO IR directory for the requested LLM."""
    src = os.environ if env is None else env
    override = src.get("HOLYARRS_OV_LLM_MODEL_DIR", "").strip()
    if override:
        if not os.path.isfile(os.path.join(override, "openvino_model.xml")):
            raise FileNotFoundError(f"OpenVINO model not found in HOLYARRS_OV_LLM_MODEL_DIR: {override}")
        return override

    info = resolve_variant_info(model_path, env=src)
    if info is None:
        if model_path and os.path.isfile(model_path):
            return model_path
        raise FileNotFoundError(f"no OpenVINO IR mapping or local model file for: {model_path}")
    repo_id, _, ir_dir = info
    model_xml = os.path.join(ir_dir, "openvino_model.xml")
    if not os.path.isfile(model_xml):
        offline_val, _ = parse_bool_env("HOLYARRS_OV_TRANSLATE_OFFLINE", src)
        if offline_val:
            raise FileNotFoundError(f"OpenVINO IR not found in offline mode: {ir_dir}")
        os.makedirs(ir_dir, exist_ok=True)
        with _file_lock(os.path.join(os.path.dirname(ir_dir), ".download.lock")):
            if not os.path.isfile(model_xml):
                if downloader is None:
                    from huggingface_hub import snapshot_download

                    downloader = snapshot_download
                log_err(f"llm_openvino: downloading {repo_id} to {ir_dir}")
                downloader(repo_id=repo_id, local_dir=ir_dir)
    return ir_dir


def _dedupe_dicts(items: Sequence[dict[str, Any]]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for item in items:
        if item not in out:
            out.append(item)
    return out


def create_llm_pipeline(
    ir_dir: str, ov_device: str, cache_dir: str, threads: int = 0, parallel: int = 1,
    batch_size: int = 2048, cache_type_k: str = "", pipeline_cls: Any = None,
    scheduler_config_cls: Any = None, env: Mapping[str, str] | None = None,
) -> Any:
    """Create an OpenVINO GenAI ContinuousBatchingPipeline with compatibility fallbacks."""
    if pipeline_cls is None or scheduler_config_cls is None:
        import openvino_genai as ovg

        pipeline_cls = pipeline_cls or ovg.ContinuousBatchingPipeline
        scheduler_config_cls = scheduler_config_cls or ovg.SchedulerConfig

    src = os.environ if env is None else env
    cap = _default_thread_cap(threads)
    seqs = max(int(parallel), 1)
    ctk = (cache_type_k or "").strip().lower()
    kv_u8_val, kv_u8_set = parse_bool_env("HOLYARRS_OV_LLM_KV_U8", src)
    use_u8_kv = kv_u8_val if kv_u8_set else ctk.startswith(("q8", "u8", "int8"))

    cache_gb = 2 if (use_u8_kv or seqs <= 6) else 3
    raw_cache_gb = src.get("HOLYARRS_OV_LLM_CACHE_GB", "").strip()
    if raw_cache_gb:
        try:
            if int(raw_cache_gb) > 0:
                cache_gb = int(raw_cache_gb)
        except ValueError:
            pass

    split_val, split_set = parse_bool_env("HOLYARRS_OV_LLM_SPLIT_FUSE", src)
    sc = scheduler_config_cls()
    sc.max_num_seqs = seqs
    sc.max_num_batched_tokens = max(int(batch_size), 2048)
    sc.dynamic_split_fuse = split_val if split_set else False
    sc.enable_prefix_caching = True
    sc.cache_size = cache_gb

    def _prop_candidates(dev: str, use_cache: bool) -> list[dict[str, Any]]:
        cbase = {"CACHE_DIR": cache_dir} if (use_cache and cache_dir) else {}
        if dev.startswith("GPU"):
            kv = {"KV_CACHE_PRECISION": "u8"} if use_u8_kv else {}
            th = {"COMPILATION_NUM_THREADS": cap}
            return _dedupe_dicts([{**cbase, **th, **kv}, {**cbase, **th}, {**cbase, **kv}, dict(cbase), {}])
        return _dedupe_dicts([{**cbase, "INFERENCE_NUM_THREADS": cap}, dict(cbase), {}])

    def _build(dev: str, use_cache: bool = True) -> Any:
        candidates = _prop_candidates(dev, use_cache)
        for idx, props in enumerate(candidates):
            try:
                return pipeline_cls(ir_dir, sc, dev, props)
            except (TypeError, RuntimeError) as exc:
                if _is_cache_fs_error(exc):
                    raise
                extra_keys = [k for k in props if k != "CACHE_DIR"]
                if idx < len(candidates) - 1 and _is_unsupported_prop_error(exc, extra_keys):
                    continue
                raise

    def _build_with_cache(dev: str) -> Any:
        if not cache_dir:
            return _build(dev, use_cache=False)
        lock_ctx = _file_lock(os.path.join(cache_dir, ".compile.lock")) if os.path.isdir(cache_dir) else nullcontext()
        try:
            with lock_ctx:
                return _build(dev, use_cache=True)
        except Exception as exc:
            if not _is_cache_fs_error(exc):
                raise
            first_line = next((line.strip() for line in str(exc).splitlines() if line.strip()), type(exc).__name__)
            log_err(f"llm_openvino: cache error ({first_line}), retrying without cache")
            return _build(dev, use_cache=False)

    try:
        return _build_with_cache(ov_device)
    except Exception as exc:
        if ov_device.startswith("GPU") and parse_bool_env("HOLYARRS_OV_FALLBACK_CPU", src)[0]:
            log_err(f"llm_openvino: GPU init failed ({exc}), falling back to CPU")
            return _build_with_cache("CPU")
        raise


def _extract_message_content(raw: Any) -> str:
    if isinstance(raw, str):
        return raw
    if isinstance(raw, list):
        parts = [
            item if isinstance(item, str) else str(item.get("text", ""))
            for item in raw
            if isinstance(item, str) or (isinstance(item, Mapping) and item.get("type") == "text")
        ]
        return "".join(parts)
    return "" if raw is None else str(raw)


def _count_tokens(input_ids: Any) -> int:
    shape = getattr(input_ids, "shape", None)
    if shape is not None:
        return int(shape[1]) if len(shape) >= 2 else (int(shape[0]) if len(shape) == 1 else 0)
    if isinstance(input_ids, Sequence):
        if input_ids and isinstance(input_ids[0], Sequence) and not isinstance(input_ids[0], (str, bytes)):
            return len(input_ids[0])
        return len(input_ids)
    return 0


@dataclass
class _PendingJob:
    req_id: int
    messages: list[dict[str, str]]
    params: dict[str, Any]
    done: threading.Event = field(default_factory=threading.Event)
    cancelled: bool = False
    handle: Any = None
    prompt_tokens: int = 0
    result: dict[str, Any] | None = None
    error: Exception | None = None

    def fail(self, exc: Exception) -> None:
        self.error = exc
        self.done.set()

    def succeed(self, result: dict[str, Any]) -> None:
        self.result = result
        self.done.set()


class ContinuousBatchEngine:
    """Thread-safe OpenAI chat completion engine backed by ContinuousBatchingPipeline."""

    def __init__(
        self, pipeline: Any = None, ctx_size: int = 8192, parallel: int = 1,
        reasoning_budget: int = -1, model_id: str = "openvino-llm", genai_mod: Any = None,
    ) -> None:
        self._cond, self._stop_event, self._ready = threading.Condition(), threading.Event(), threading.Event()
        self._pipeline = pipeline
        self._tokenizer = pipeline.get_tokenizer() if pipeline is not None else None
        self._ovg = genai_mod
        self._ctx_size, self._parallel = int(ctx_size), max(int(parallel), 1)
        self._reasoning_budget, self._model_id = int(reasoning_budget), model_id
        self._load_error: str | None = None
        self._pending: deque[_PendingJob] = deque()
        self._jobs_by_id: dict[int, _PendingJob] = {}
        self._next_req_id, self._stepper = 1, None
        if pipeline is not None:
            self._ready.set()
            self._start_stepper()

    @property
    def model_id(self) -> str:
        return self._model_id

    def is_ready(self) -> bool:
        return self._ready.is_set() and self._pipeline is not None

    def load_error(self) -> str | None:
        return self._load_error

    def set_error(self, message: str) -> None:
        self._load_error = message

    def set_pipeline(self, pipeline: Any, model_id: str | None = None) -> None:
        with self._cond:
            self._pipeline = pipeline
            self._tokenizer = pipeline.get_tokenizer()
            if model_id:
                self._model_id = model_id
            self._ready.set()
            if self._stepper is None and not self._stop_event.is_set():
                self._start_stepper()
            self._cond.notify_all()

    def _start_stepper(self) -> None:
        self._stepper = threading.Thread(target=self._run_loop, name="ov-cb-stepper", daemon=True)
        self._stepper.start()

    def _ensure_ovg(self) -> Any:
        if self._ovg is None:
            import openvino_genai as ovg

            self._ovg = ovg
        return self._ovg

    def _retire_job(self, job: _PendingJob, exc: Exception | None = None) -> None:
        with self._cond:
            self._jobs_by_id.pop(job.req_id, None)
        if exc is not None:
            job.fail(exc)

    def _build_generation_config(self, params: Mapping[str, Any], prompt_tokens: int) -> Any:
        gc = self._ensure_ovg().GenerationConfig()
        gc.apply_chat_template = False

        raw_max = params.get("max_tokens")
        if raw_max is None:
            raw_max = params.get("max_completion_tokens")
        if self._ctx_size > 0:
            slot_ctx = max(self._ctx_size // self._parallel, 512)
            max_avail = max(1, slot_ctx - prompt_tokens)
            gc.max_new_tokens = min(max(1, int(raw_max)), max_avail) if raw_max is not None else min(2048, max_avail)
        else:
            gc.max_new_tokens = max(1, int(raw_max)) if raw_max is not None else 2048

        temp = params.get("temperature")
        if temp is not None and float(temp) > 0.0:
            gc.do_sample = True
            gc.temperature = float(temp)
            if params.get("top_p") is not None:
                gc.top_p = float(params["top_p"])
            if params.get("top_k") is not None and int(params["top_k"]) > 0:
                gc.top_k = int(params["top_k"])
        else:
            gc.do_sample = False

        rep = params.get("repeat_penalty", params.get("repetition_penalty"))
        if rep is not None:
            gc.repetition_penalty = float(rep)

        stop = params.get("stop")
        if stop:
            stops = [stop] if isinstance(stop, str) else ([str(s) for s in stop if s] if isinstance(stop, Sequence) else [])
            if stops:
                gc.stop_strings = set(stops)
                gc.include_stop_str_in_output = False
        return gc

    def _prepare_job(self, job: _PendingJob) -> bool:
        """Tokenize, validate context window, and register job on the pipeline."""
        tok, pipe = self._tokenizer, self._pipeline
        if tok is None or pipe is None:
            job.fail(RuntimeError("pipeline is not initialized"))
            return False

        raw_kwargs = job.params.get("chat_template_kwargs")
        extra_ctx: dict[str, Any] = dict(raw_kwargs) if isinstance(raw_kwargs, Mapping) else {}
        if self._reasoning_budget == 0 and "enable_thinking" not in extra_ctx:
            extra_ctx["enable_thinking"] = False

        if extra_ctx:
            try:
                rendered = tok.apply_chat_template(job.messages, add_generation_prompt=True, extra_context=extra_ctx)
            except TypeError:
                rendered = tok.apply_chat_template(job.messages, add_generation_prompt=True)
        else:
            rendered = tok.apply_chat_template(job.messages, add_generation_prompt=True)

        enc = tok.encode(rendered, add_special_tokens=False)
        input_ids = enc.input_ids if hasattr(enc, "input_ids") else enc
        prompt_tokens = _count_tokens(input_ids)

        if self._ctx_size > 0:
            slot_ctx = max(self._ctx_size // self._parallel, 512)
            if prompt_tokens >= slot_ctx:
                job.fail(ContextTooBigError(f"the request ({prompt_tokens} tokens) exceeds the available context size ({slot_ctx} tokens)"))
                return False

        job.prompt_tokens = prompt_tokens
        job.handle = pipe.add_request(job.req_id, input_ids, self._build_generation_config(job.params, prompt_tokens))
        return True

    def _abort_all(self, active: list[_PendingJob]) -> None:
        for job in [*self._pending, *active]:
            self._jobs_by_id.pop(job.req_id, None)
            if job.handle is not None:
                try:
                    job.handle.cancel()
                except Exception:
                    pass
            job.fail(RequestCancelledError("server shutting down"))
        self._pending.clear()

    def _finish_job(self, job: _PendingJob, length_reason: Any) -> None:
        outs = job.handle.read_all()
        if outs:
            out = outs[0]
            gen_ids = list(out.generated_ids)
            text = self._tokenizer.decode(gen_ids)
            finish_reason = "length" if out.finish_reason == length_reason else "stop"
            c_tok = len(gen_ids)
        else:
            text, finish_reason, c_tok = "", "stop", 0
        job.succeed({
            "req_id": job.req_id, "text": text, "finish_reason": finish_reason,
            "prompt_tokens": job.prompt_tokens, "completion_tokens": c_tok,
        })
        self._retire_job(job)

    def _run_loop(self) -> None:
        active: list[_PendingJob] = []
        ovg = self._ensure_ovg()
        running_status, length_reason = ovg.GenerationStatus.RUNNING, ovg.GenerationFinishReason.LENGTH

        while True:
            to_prepare: list[_PendingJob] = []
            with self._cond:
                while not self._stop_event.is_set() and not active and not self._pending:
                    self._cond.wait()
                if self._stop_event.is_set():
                    self._abort_all(active)
                    return
                while self._pending:
                    to_prepare.append(self._pending.popleft())

            for job in to_prepare:
                if job.cancelled:
                    self._retire_job(job, RequestCancelledError("request cancelled"))
                    continue
                try:
                    if self._prepare_job(job):
                        active.append(job)
                    else:
                        self._retire_job(job)
                except Exception as exc:
                    self._retire_job(job, exc)

            kept: list[_PendingJob] = []
            for job in active:
                if job.cancelled:
                    try:
                        job.handle.cancel()
                    except Exception:
                        pass
                    self._retire_job(job, RequestCancelledError("request cancelled"))
                else:
                    kept.append(job)
            active = kept
            if not active and not self._pipeline.has_non_finished_requests():
                continue
            try:
                self._pipeline.step()
            except Exception as exc:
                log_err(f"llm_openvino: pipeline step error: {exc}")
                for job in active:
                    self._retire_job(job, exc)
                active = []
                continue

            next_active: list[_PendingJob] = []
            for job in active:
                try:
                    if job.handle.get_status() == running_status:
                        next_active.append(job)
                    else:
                        self._finish_job(job, length_reason)
                except Exception as exc:
                    self._retire_job(job, exc)
            active = next_active

    def submit(self, messages: list[dict[str, str]], params: Mapping[str, Any] | None = None) -> _PendingJob:
        """Queue a chat completion job and return its handle."""
        if not self.is_ready():
            raise RuntimeError(self._load_error or "model is still loading")
        if self._stop_event.is_set():
            raise RequestCancelledError("engine stopped")
        with self._cond:
            req_id = self._next_req_id
            self._next_req_id += 1
            job = _PendingJob(req_id=req_id, messages=messages, params=dict(params or {}))
            self._pending.append(job)
            self._jobs_by_id[req_id] = job
            self._cond.notify_all()
            return job

    def cancel(self, req_id: int) -> bool:
        """Cancel a queued or active request by id."""
        with self._cond:
            job = self._jobs_by_id.get(req_id)
            if job is None:
                return False
            job.cancelled = True
            self._cond.notify_all()
            return True

    def chat_completion(self, payload: Mapping[str, Any]) -> dict[str, Any]:
        """Run a synchronous OpenAI chat completion request through the batch engine."""
        raw_msgs = payload.get("messages")
        if not isinstance(raw_msgs, list) or not raw_msgs:
            raise ValueError("messages must be a non-empty list")
        messages = [
            {"role": str(m.get("role", "")) if isinstance(m, Mapping) else "", "content": _extract_message_content(m.get("content")) if isinstance(m, Mapping) else ""}
            for m in raw_msgs
        ]
        job = self.submit(messages, params=payload)
        while not job.done.wait(timeout=0.25):
            if self._stop_event.is_set():
                self.cancel(job.req_id)
                break
        if job.error is not None:
            raise job.error
        if job.result is None:
            raise RequestCancelledError("request cancelled")
        return job.result

    def stop(self) -> None:
        """Signal the stepper thread to cancel active work and exit."""
        with self._cond:
            self._stop_event.set()
            for job in self._jobs_by_id.values():
                job.cancelled = True
            self._cond.notify_all()
        if self._stepper is not None and self._stepper.is_alive():
            self._stepper.join(timeout=2.0)


def _is_authorized(headers: Any, api_key: str) -> bool:
    if not api_key:
        return True
    parts = str(headers.get("Authorization", "")).strip().split(None, 1)
    return len(parts) == 2 and parts[0].lower() == "bearer" and hmac.compare_digest(parts[1], api_key)


def make_handler(engine: ContinuousBatchEngine, api_key: str = "") -> type[BaseHTTPRequestHandler]:
    """Build an OpenAI-compatible HTTP handler bound to a ContinuousBatchEngine."""

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

        def _send_error(self, status: int, message: str) -> None:
            self._send_json(status, {"error": {"message": message}})

        def _require_auth(self) -> bool:
            if _is_authorized(self.headers, api_key):
                return True
            self._send_error(401, "unauthorized")
            return False

        def do_GET(self) -> None:
            path = self.path.split("?", 1)[0]
            if path == "/health":
                self._send_json(200 if engine.is_ready() else 503, {"status": "ok" if engine.is_ready() else "loading"})
            elif path == "/v1/models":
                if self._require_auth():
                    self._send_json(200, {"object": "list", "data": [{"id": engine.model_id, "object": "model"}]})
            else:
                self._send_error(404, "not found")

        def do_POST(self) -> None:
            if self.path.split("?", 1)[0] != "/v1/chat/completions":
                return self._send_error(404, "not found")
            if not self._require_auth():
                return
            if not engine.is_ready():
                return self._send_error(503, engine.load_error() or "model is loading")
            try:
                payload = json.loads(read_http_body(self.headers, self.rfile).decode("utf-8"))
            except Exception as exc:
                return self._send_error(400, f"invalid JSON request: {exc}")
            if not isinstance(payload, dict):
                return self._send_error(400, "request body must be a JSON object")
            if payload.get("stream") is True:
                return self._send_error(400, "streaming is not supported")

            try:
                res = engine.chat_completion(payload)
            except (ContextTooBigError, ValueError) as exc:
                self._send_error(400, str(exc))
            except RequestCancelledError as exc:
                self._send_error(503, str(exc))
            except Exception as exc:
                log_err(f"llm_openvino: completion error: {exc}")
                self._send_error(500, f"completion failed: {exc}")
            else:
                p_tok, c_tok = int(res["prompt_tokens"]), int(res["completion_tokens"])
                self._send_json(200, {
                    "id": f"chatcmpl-{res['req_id']}",
                    "object": "chat.completion",
                    "created": int(time.time()),
                    "model": str(payload.get("model") or engine.model_id),
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": res["text"]}, "finish_reason": res["finish_reason"]}],
                    "usage": {"prompt_tokens": p_tok, "completion_tokens": c_tok, "total_tokens": p_tok + c_tok},
                })

    return Handler


def build_arg_parser() -> argparse.ArgumentParser:
    """Create a CLI parser compatible with HolyArrs llama-server flags."""
    p = argparse.ArgumentParser(description="OpenVINO GenAI LLM HTTP server")
    for flags, kw in (
        (("-m", "--model"), {"default": "", "help": "GGUF model path"}),
        (("--host",), {"default": DEFAULT_HOST, "help": "Bind host"}),
        (("--port",), {"type": int, "default": DEFAULT_PORT, "help": "Bind port"}),
        (("--api-key",), {"default": "", "help": "Bearer API key"}),
        (("-t", "--threads"), {"type": int, "default": 0, "help": "CPU thread count"}),
        (("-c", "--ctx-size"), {"type": int, "default": 8192, "help": "Total context size"}),
        (("-np", "--parallel"), {"type": int, "default": 1, "help": "Parallel request slots"}),
        (("-dev", "--device"), {"default": "", "help": "Device id such as GPU0 or Vulkan0"}),
        (("-ngl", "--gpu-layers"), {"type": int, "default": -1, "help": "GPU layers (0 for CPU)"}),
        (("-b", "--batch-size"), {"type": int, "default": 2048, "help": "Batch token limit"}),
        (("-ub", "--ubatch-size"), {"type": int, "default": 512, "help": "Accepted for compatibility"}),
        (("--reasoning-budget",), {"type": int, "default": -1, "help": "Reasoning token budget"}),
        (("-fa", "--flash-attn"), {"nargs": "?", "const": "on", "default": "", "help": "Compatibility flag"}),
        (("-ctk", "--cache-type-k"), {"default": "", "help": "KV cache K type"}),
        (("-ctv", "--cache-type-v"), {"default": "", "help": "KV cache V type"}),
        (("--list-devices",), {"action": "store_true", "help": "List available OpenVINO GPU devices"}),
        (("--offline",), {"action": "store_true", "help": "Accepted for compatibility"}),
        (("--no-webui",), {"action": "store_true", "help": "Accepted for compatibility"}),
        (("--no-slots",), {"action": "store_true", "help": "Accepted for compatibility"}),
    ):
        p.add_argument(*flags, **kw)
    return p


def main(argv: Sequence[str] | None = None) -> int:
    """Start the HTTP server, load the OpenVINO LLM pipeline, and serve until stopped."""
    raw_argv = list(sys.argv[1:] if argv is None else argv)
    args, _ = build_arg_parser().parse_known_args(raw_argv)
    _configure_thread_env(args.threads)

    if args.list_devices:
        lines = list_openvino_devices()
        if lines:
            sys.stdout.write("Available devices:\n" + "\n".join(lines) + "\n")
            sys.stdout.flush()
        return 0

    model_id = os.path.basename(args.model) or "openvino-llm"
    engine = ContinuousBatchEngine(
        ctx_size=args.ctx_size, parallel=args.parallel,
        reasoning_budget=args.reasoning_budget, model_id=model_id,
    )
    server = _LLMHTTPServer((args.host, args.port), make_handler(engine, api_key=args.api_key))

    def _shutdown(_signum: int, _frame: Any) -> None:
        engine.stop()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    def _load_model() -> None:
        try:
            avail = query_available_devices()
            no_gpu = (args.gpu_layers == 0) or not (args.device or "").strip()
            ov_device = select_device(
                parse_device_index(args.device), no_gpu=no_gpu,
                available_devices=avail, device_types=query_device_types(avail),
            )
            ir_dir, cache_dir = resolve_llm_ir_dir(args.model), get_cache_dir()
            log_err(f"llm_openvino: loading {ir_dir} on {ov_device} (cache {cache_dir})")
            pipe = create_llm_pipeline(
                ir_dir, ov_device, cache_dir, threads=args.threads, parallel=args.parallel,
                batch_size=args.batch_size, cache_type_k=args.cache_type_k,
            )
            engine.set_pipeline(pipe, model_id=os.path.basename(args.model) or os.path.basename(ir_dir))
            log_err(f"llm_openvino: ready on {args.host}:{args.port}")
        except Exception as exc:
            engine.set_error(str(exc))
            log_err(f"llm_openvino: failed to load model: {exc}")
            threading.Thread(target=server.shutdown, daemon=True).start()

    threading.Thread(target=_load_model, daemon=True).start()
    try:
        server.serve_forever()
    finally:
        engine.stop()
        server.server_close()

    return 1 if engine.load_error() else 0


if __name__ == "__main__":
    sys.exit(main())
