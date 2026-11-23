"""Shared OpenVINO device, cache, locking, thread, and HTTP helpers."""

from __future__ import annotations

from collections.abc import Iterator, Mapping, MutableMapping, Sequence
from contextlib import contextmanager
import os
import sys
from typing import Any

_THREAD_ENV_VARS = (
    "OMP_NUM_THREADS",
    "OPENBLAS_NUM_THREADS",
    "MKL_NUM_THREADS",
    "VECLIB_MAXIMUM_THREADS",
    "NUMEXPR_NUM_THREADS",
    "TBB_NUM_THREADS",
    "OV_CPU_THREADS_NUM",
)

_TRUTHY_ENV = ("1", "true", "yes", "on")
_FALSY_ENV = ("0", "false", "no", "off")


def _default_thread_cap(threads: int) -> int:
    """Return the thread limit, defaulting to a small cap on high-core hosts."""
    return threads if threads > 0 else max(1, min(4, os.cpu_count() or 2))


def _configure_thread_env(threads: int, env: MutableMapping[str, str] | None = None) -> int:
    """Cap OpenVINO, oneTBB, and BLAS thread pools without overriding user env vars."""
    cap = _default_thread_cap(threads)
    target = os.environ if env is None else env
    cap_str = str(cap)
    for var in _THREAD_ENV_VARS:
        target.setdefault(var, cap_str)
    return cap


def parse_bool_env(key: str, env: Mapping[str, str] | None = None) -> tuple[bool, bool]:
    """Parse an env var as (value, is_set) for 1/true/yes/on and 0/false/no/off."""
    src = os.environ if env is None else env
    raw = str(src.get(key, "")).strip().lower()
    if raw in _TRUTHY_ENV:
        return True, True
    if raw in _FALSY_ENV:
        return False, True
    return False, False


def log_err(message: str) -> None:
    """Write one flushed line to stderr for HolyArrs log capture."""
    sys.stderr.write(message.rstrip("\n") + "\n")
    sys.stderr.flush()


def get_cache_dir(env: Mapping[str, str] | None = None) -> str:
    """Return and create the persistent OpenVINO compiled blob cache folder."""
    src = os.environ if env is None else env
    xdg = src.get("XDG_CACHE_HOME", "").strip() or "/tmp"
    cache_dir = os.path.join(xdg, "openvino")
    os.makedirs(cache_dir, exist_ok=True)
    return cache_dir


@contextmanager
def _file_lock(lock_path: str) -> Iterator[None]:
    """Acquire an exclusive advisory lock, degrading to a no-op if unavailable."""
    try:
        import fcntl
    except ImportError:
        yield
        return
    fd: int | None = None
    try:
        parent = os.path.dirname(lock_path)
        if parent:
            os.makedirs(parent, exist_ok=True)
        fd = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o666)
        fcntl.flock(fd, fcntl.LOCK_EX)
    except OSError:
        if fd is not None:
            try:
                os.close(fd)
            except OSError:
                pass
        yield
        return
    try:
        yield
    finally:
        try:
            fcntl.flock(fd, fcntl.LOCK_UN)
        except OSError:
            pass
        try:
            os.close(fd)
        except OSError:
            pass


def _is_discrete_device_type(dtype: Any) -> bool:
    """Return True when an OpenVINO DEVICE_TYPE value indicates a discrete GPU."""
    if dtype is None:
        return False
    text = str(dtype).upper()
    return text.endswith("DISCRETE") or "DISCRETE" in text


def query_device_types(devices: Sequence[str] | None, core: Any | None = None) -> dict[str, str]:
    """Query OpenVINO DEVICE_TYPE for GPU devices, returning a mapping when available."""
    if not devices:
        return {}
    gpu_devs = [d for d in devices if d == "GPU" or d.startswith("GPU.")]
    if not gpu_devs:
        return {}
    if core is None:
        try:
            import openvino as ov

            core = ov.Core()
        except Exception:
            return {}
    types: dict[str, str] = {}
    for dev in gpu_devs:
        try:
            dtype = core.get_property(dev, "DEVICE_TYPE")
        except Exception:
            continue
        if dtype is not None:
            types[dev] = str(dtype)
    return types


def _order_devices_by_type(devs: list[str], device_types: Mapping[str, Any] | None) -> list[str]:
    """Order discrete GPU devices ahead of integrated GPUs while preserving relative order."""
    if not device_types:
        return devs
    gpu_devs = [d for d in devs if d == "GPU" or d.startswith("GPU.")]
    if not any(_is_discrete_device_type(device_types.get(d)) for d in gpu_devs):
        return devs
    ordered_gpus = sorted(
        gpu_devs,
        key=lambda d: 0 if _is_discrete_device_type(device_types.get(d)) else 1,
    )
    gpu_iter = iter(ordered_gpus)
    return [next(gpu_iter) if (d == "GPU" or d.startswith("GPU.")) else d for d in devs]


def select_device(
    device_index: int | None,
    no_gpu: bool = False,
    available_devices: Sequence[str] | None = None,
    device_types: Mapping[str, Any] | None = None,
) -> str:
    """Choose the OpenVINO target device matching HolyArrs -dev / -ng flags."""
    if no_gpu or (device_index is not None and device_index < 0):
        return "CPU"
    idx = 0 if device_index is None else device_index
    if available_devices is not None:
        devs = list(available_devices)
        gpu_devs = [d for d in devs if d == "GPU" or d.startswith("GPU.")]
        if device_types and any(_is_discrete_device_type(device_types.get(d)) for d in gpu_devs):
            ordered_gpus = [d for d in _order_devices_by_type(gpu_devs, device_types)]
            if 0 <= idx < len(ordered_gpus):
                return ordered_gpus[idx]
        indexed = f"GPU.{idx}"
        if indexed in devs:
            return indexed
        if idx == 0 and "GPU" in devs:
            return "GPU"
        if idx == 0 and len(gpu_devs) == 1:
            return gpu_devs[0]
    return "GPU" if idx == 0 else f"GPU.{idx}"


def query_available_devices() -> list[str] | None:
    """Query OpenVINO Core available devices, returning None if unavailable."""
    try:
        import openvino as ov

        core = ov.Core()
        devs = list(core.available_devices)
        dev_types = query_device_types(devs, core=core)
        return _order_devices_by_type(devs, dev_types)
    except Exception:
        return None


def _is_unsupported_prop_error(exc: Exception, extra_keys: Sequence[str]) -> bool:
    """Return True when exc indicates an unsupported OpenVINO property or kwarg."""
    if isinstance(exc, TypeError):
        return True
    if isinstance(exc, RuntimeError):
        msg = str(exc).lower()
        if any(
            tok in msg
            for tok in ("property", "unsupported", "not supported", "unknown", "invalid config")
        ):
            return True
        return any(k.lower() in msg for k in extra_keys)
    return False


def _is_cache_fs_error(exc: Exception) -> bool:
    """Return True when exc indicates an OpenVINO CACHE_DIR filesystem or permission error."""
    if not isinstance(exc, RuntimeError):
        return False
    msg = str(exc).lower()
    return any(
        tok in msg
        for tok in ("filesystem error", "cannot set permissions", "permission denied", ".blob")
    )


def _read_exact(rfile: Any, length: int) -> bytes:
    """Read up to length bytes cleanly from a stream."""
    chunks: list[bytes] = []
    remaining = length
    while remaining > 0:
        chunk = rfile.read(remaining)
        if not chunk:
            break
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_http_body(headers: Any, rfile: Any) -> bytes:
    """Read an HTTP request body supporting both Content-Length and chunked transfer."""
    transfer_encoding = str(headers.get("Transfer-Encoding", "")).lower()
    if "chunked" in transfer_encoding:
        parts: list[bytes] = []
        while True:
            size_line = rfile.readline()
            if not size_line:
                break
            size_token = size_line.split(b";", 1)[0].strip()
            if not size_token:
                continue
            chunk_size = int(size_token, 16)
            if chunk_size == 0:
                while True:
                    trailer = rfile.readline()
                    if not trailer or trailer in (b"\r\n", b"\n"):
                        break
                break
            parts.append(_read_exact(rfile, chunk_size))
            rfile.readline()
        return b"".join(parts)

    raw_len = headers.get("Content-Length")
    if raw_len is not None:
        length = int(raw_len)
        if length < 0:
            raise ValueError("negative Content-Length")
        return _read_exact(rfile, length)
    return rfile.read()
