"""Private JSONL adapter for the pinned official Laya SDK.

The Go parent validates local policy/model/SDK artifacts and supplies offline
environment values. This process verifies those values before importing Laya,
loads exactly one local multilingual checkpoint, and emits JSONL only on stdout.
"""

import importlib.metadata
import inspect
import json
import math
import os
import re
import sys
import time


MAX_LINE_BYTES = 64 * 1024
ROUTES = [
    "CHAT", "PLAN", "ANALYZE", "OPS", "RESEARCH", "CODE",
    "CODE1", "CODE2", "CODE3", "CODE4", "WILD",
]
SDK_SOURCE_URL = "https://github.com/NandhaKishorM/laya"
# Third-party distributions imported by initialize must always have exact pins.
REQUIRED_SDK_PACKAGE_PINS = frozenset({"laya", "torch"})
REQUIRED_OFFLINE_ENVIRONMENT = {
    "HF_HUB_OFFLINE": "1",
    "TRANSFORMERS_OFFLINE": "1",
    "HF_DATASETS_OFFLINE": "1",
    "HF_HUB_DISABLE_TELEMETRY": "1",
    "PYTHONNOUSERSITE": "1",
    "PYTHONUTF8": "1",
    "PYTHONIOENCODING": "utf-8",
    "PYTHONDONTWRITEBYTECODE": "1",
    "TOKENIZERS_PARALLELISM": "false",
}


class WorkerError(Exception):
    def __init__(self, reason_code):
        super().__init__(reason_code)
        self.reason_code = reason_code


def emit(value):
    line = json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n"
    encoded = line.encode("utf-8")
    if len(encoded) > MAX_LINE_BYTES:
        raise WorkerError("INVALID_MODEL_OUTPUT")
    sys.stdout.write(line)
    sys.stdout.flush()


def read_line(limit=MAX_LINE_BYTES):
    raw = sys.stdin.buffer.readline(limit + 1)
    if len(raw) > limit:
        raise WorkerError("INVALID_REQUEST")
    if raw == b"":
        return None
    if not raw.endswith(b"\n"):
        raise WorkerError("INVALID_REQUEST")
    try:
        value = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise WorkerError("INVALID_REQUEST") from exc
    if not isinstance(value, dict):
        raise WorkerError("INVALID_REQUEST")
    return value


def normalize_package_name(name):
    return re.sub(r"[-_.]+", "-", name).lower()


def require_exact_keys(value, keys):
    if not isinstance(value, dict) or set(value) != set(keys):
        raise WorkerError("INVALID_REQUEST")


def check_pinned_environment(config):
    if sys.prefix == sys.base_prefix:
        raise WorkerError("SDK_ERROR")

    manifest = config.get("manifest") if isinstance(config, dict) else None
    sdk = manifest.get("sdk") if isinstance(manifest, dict) else None
    if not isinstance(sdk, dict):
        raise WorkerError("SDK_ERROR")

    version = sdk.get("version")
    packages = sdk.get("packages")
    if (
        sdk.get("distribution") != "laya"
        or sdk.get("source_url") != SDK_SOURCE_URL
        or not isinstance(version, str)
        or not version
        or not isinstance(packages, dict)
        or not packages
        or not REQUIRED_SDK_PACKAGE_PINS.issubset(packages)
        or packages.get("laya") != version
    ):
        raise WorkerError("SDK_ERROR")

    for package_name, expected_version in packages.items():
        if (
            not isinstance(package_name, str)
            or not package_name
            or normalize_package_name(package_name) != package_name
            or not isinstance(expected_version, str)
            or not expected_version
        ):
            raise WorkerError("SDK_ERROR")
        try:
            actual_version = importlib.metadata.version(package_name)
        except Exception as exc:
            raise WorkerError("SDK_ERROR") from exc
        if actual_version != expected_version:
            raise WorkerError("SDK_ERROR")

    try:
        actual_laya_version = importlib.metadata.version("laya")
    except Exception as exc:
        raise WorkerError("SDK_ERROR") from exc
    if actual_laya_version != version:
        raise WorkerError("SDK_ERROR")


def verify_offline_environment():
    observed = {key: os.environ.get(key) for key in REQUIRED_OFFLINE_ENVIRONMENT}
    if observed != REQUIRED_OFFLINE_ENVIRONMENT:
        raise WorkerError("SDK_ERROR")
    return observed


def resolve_dtype(torch, name):
    values = {
        "float32": torch.float32,
        "float16": torch.float16,
        "bfloat16": torch.bfloat16,
    }
    try:
        return values[name]
    except KeyError as exc:
        raise WorkerError("SDK_ERROR") from exc


def actual_runtime(router, torch, config):
    agents = getattr(router, "_agents", None)
    if not isinstance(agents, dict) or set(agents) != {"multilingual"}:
        raise WorkerError("SDK_ERROR")
    agent = agents["multilingual"]
    actual_device = str(getattr(agent, "device", ""))
    expected_device = config["reference_worker"]["device"]
    if actual_device != expected_device:
        raise WorkerError("SDK_ERROR")
    actual_dtype = getattr(agent, "dtype", None)
    expected_dtype = resolve_dtype(torch, config["reference_worker"]["dtype"])
    if actual_dtype != expected_dtype:
        raise WorkerError("SDK_ERROR")
    if getattr(agent, "cpu_fallback_count", 0) != 0:
        raise WorkerError("SDK_ERROR")
    return agent, actual_device, config["reference_worker"]["dtype"]


def initialize(config):
    require_exact_keys(config, {"kind", "manifest", "model_directory", "policy", "reference_worker"})
    if config["kind"] != "initialize":
        raise WorkerError("INVALID_REQUEST")

    manifest = config["manifest"]
    policy = config["policy"]
    reference = config["reference_worker"]
    if not isinstance(manifest, dict) or not isinstance(policy, dict) or not isinstance(reference, dict):
        raise WorkerError("INVALID_REQUEST")
    if policy.get("semantic_review_status") != "verified":
        raise WorkerError("POLICY_UNDEFINED")
    if policy.get("routes") != ROUTES or set(policy.get("criteria", {})) != set(ROUTES):
        raise WorkerError("POLICY_UNDEFINED")
    if policy.get("policy_version") != "rencrow-route-policy-v1":
        raise WorkerError("POLICY_UNDEFINED")
    if reference.get("offline_required") is not True or reference.get("hard_timeout_ms") != 10000:
        raise WorkerError("SDK_ERROR")
    offline_environment = verify_offline_environment()
    check_pinned_environment(config)

    model = manifest.get("model")
    sdk = manifest.get("sdk")
    if not isinstance(model, dict) or not isinstance(sdk, dict):
        raise WorkerError("SDK_ERROR")
    if model.get("family") != "laya-multilingual" or model.get("source_id") != "convaiinnovations/laya-multilingual":
        raise WorkerError("SDK_ERROR")
    if model.get("revision") != reference.get("model_revision"):
        raise WorkerError("SDK_ERROR")
    if model.get("device") != reference.get("device") or model.get("dtype") != reference.get("dtype"):
        raise WorkerError("SDK_ERROR")
    if config["model_directory"] != reference.get("model_directory"):
        raise WorkerError("SDK_ERROR")

    if not os.path.isabs(config["model_directory"]) or not os.path.isdir(config["model_directory"]):
        raise WorkerError("SDK_ERROR")

    import torch
    from laya import Router

    router_parameters = set(inspect.signature(Router.__init__).parameters)
    predict_parameters = set(inspect.signature(Router.predict).parameters)
    if not {"models", "device", "revisions", "max_loaded", "default", "sha256_digests"}.issubset(router_parameters):
        raise WorkerError("SDK_ERROR")
    if not {"model", "max_len", "head_max_len"}.issubset(predict_parameters):
        raise WorkerError("SDK_ERROR")

    torch.set_num_threads(reference["intra_op_threads"])
    torch.set_num_interop_threads(reference["inter_op_threads"])
    expected_digests = {item["path"]: item["sha256"] for item in model["files"]}
    router = Router(
        models={"multilingual": config["model_directory"]},
        device=reference["device"],
        revisions={"multilingual": model["revision"]},
        max_loaded=1,
        default="multilingual",
        auto_task_detection=False,
        sha256_digests={"multilingual": expected_digests},
    )
    router.preload(["multilingual"])
    _, actual_device, actual_dtype = actual_runtime(router, torch, config)

    ready = {
        "kind": "ready",
        "model": {
            "family": model["family"],
            "source_id": model["source_id"],
            "revision": model["revision"],
            "sdk_version": sdk["version"],
            "device": actual_device,
            "dtype": actual_dtype,
        },
        "policy_sha256": manifest["policy_sha256"],
        "model_files_sha256": model["files_sha256"],
        "sdk_lock_sha256": sdk["lock_sha256"],
        "effective_max_len": reference["max_len"],
        "effective_head_max_len": reference["head_max_len"],
        "offline_configuration_verified": True,
        "offline_attestation": {
            "offline_environment_checked_before_sdk_import": True,
            "environment": offline_environment,
            "model_source": "prepared_local_directory",
            "model_revision": model["revision"],
            "model_files_sha256": model["files_sha256"],
            "sdk_lock_sha256": sdk["lock_sha256"],
            "preloaded_models": ["multilingual"],
        },
    }
    return router, torch, config, ready


def build_questions(policy):
    criteria = {route: policy["criteria"][route] for route in policy["routes"]}
    return {
        "route": {
            "type": "choice",
            "instructions": policy["instructions"],
            "criteria": criteria,
        }
    }


def finite_probability_map(value):
    if not isinstance(value, dict) or set(value) != set(ROUTES):
        raise WorkerError("INVALID_MODEL_OUTPUT")
    probabilities = {}
    total = 0.0
    for route in ROUTES:
        probability = value[route]
        if isinstance(probability, bool) or not isinstance(probability, (float, int)):
            raise WorkerError("INVALID_MODEL_OUTPUT")
        probability = float(probability)
        if not math.isfinite(probability) or probability < 0 or probability > 1:
            raise WorkerError("INVALID_MODEL_OUTPUT")
        probabilities[route] = probability
        total += probability
    if abs(total - 1.0) > 1e-4:
        raise WorkerError("INVALID_MODEL_OUTPUT")
    return probabilities


def classify(router, torch, config, request):
    require_exact_keys(request, {"kind", "input"})
    if request["kind"] != "classify" or not isinstance(request["input"], dict):
        raise WorkerError("INVALID_REQUEST")
    value = request["input"]
    require_exact_keys(value, {"schema_version", "request_id", "policy_sha256", "message"})
    request_id = value["request_id"]
    if value["schema_version"] != "routing-classifier-test/v1" or not isinstance(request_id, str):
        raise WorkerError("INVALID_REQUEST")
    if value["policy_sha256"] != config["manifest"]["policy_sha256"]:
        raise WorkerError("POLICY_MISMATCH")
    if not isinstance(value["message"], str) or not value["message"].strip():
        raise WorkerError("INVALID_REQUEST")

    reference = config["reference_worker"]
    started = time.perf_counter()
    questions = build_questions(config["policy"])
    inference_started = time.perf_counter()
    prepare_ms = (inference_started - started) * 1000.0
    result = router.predict(
        value["message"],
        questions,
        model="multilingual",
        max_len=reference["max_len"],
        head_max_len=reference["head_max_len"],
    )
    inference_ms = (time.perf_counter() - inference_started) * 1000.0
    postprocess_started = time.perf_counter()

    agent, actual_device, actual_dtype = actual_runtime(router, torch, config)
    if actual_device != config["manifest"]["model"]["device"] or actual_dtype != config["manifest"]["model"]["dtype"]:
        raise WorkerError("SDK_ERROR")
    if not isinstance(result, dict):
        raise WorkerError("INVALID_MODEL_OUTPUT")
    routing = result.get("routing")
    if not isinstance(routing, dict) or routing.get("model") != "multilingual":
        raise WorkerError("INVALID_MODEL_OUTPUT")
    answers = result.get("answers")
    if not isinstance(answers, dict) or set(answers) != {"route"}:
        raise WorkerError("INVALID_MODEL_OUTPUT")
    answer = answers["route"]
    if not isinstance(answer, dict) or answer.get("type") != "choice":
        raise WorkerError("INVALID_MODEL_OUTPUT")

    usage = result.get("usage")
    if not isinstance(usage, dict):
        raise WorkerError("INVALID_MODEL_OUTPUT")
    tokens = usage.get("input_tokens")
    if isinstance(tokens, bool) or not isinstance(tokens, int) or tokens < 1:
        raise WorkerError("INVALID_MODEL_OUTPUT")
    dropped = usage.get("state_tokens_dropped")
    truncated = usage.get("truncated")
    if isinstance(dropped, bool) or not isinstance(dropped, int) or dropped < 0 or not isinstance(truncated, bool):
        raise WorkerError("INVALID_MODEL_OUTPUT")
    if truncated or dropped > 0:
        emit({
            "kind": "response",
            "request_id": request_id,
            "result": {
                "schema_version": value["schema_version"],
                "request_id": request_id,
                "status": "abstain",
                "route": None,
                "native_confidence": None,
                "model": {
                    "family": config["manifest"]["model"]["family"],
                    "source_id": config["manifest"]["model"]["source_id"],
                    "revision": config["manifest"]["model"]["revision"],
                    "sdk_version": config["manifest"]["sdk"]["version"],
                    "device": actual_device,
                    "dtype": actual_dtype,
                },
                "policy_sha256": value["policy_sha256"],
                "reason_code": "INPUT_TOO_LONG",
            },
        })
        return
    truncated_questions = usage.get("truncated_questions", [])
    if not isinstance(truncated_questions, list) or any(not isinstance(item, str) for item in truncated_questions):
        raise WorkerError("INVALID_MODEL_OUTPUT")
    if truncated_questions:
        emit({
            "kind": "response",
            "request_id": request_id,
            "result": {
                "schema_version": value["schema_version"],
                "request_id": request_id,
                "status": "abstain",
                "route": None,
                "native_confidence": None,
                "model": {
                    "family": config["manifest"]["model"]["family"],
                    "source_id": config["manifest"]["model"]["source_id"],
                    "revision": config["manifest"]["model"]["revision"],
                    "sdk_version": config["manifest"]["sdk"]["version"],
                    "device": actual_device,
                    "dtype": actual_dtype,
                },
                "policy_sha256": value["policy_sha256"],
                "reason_code": "POLICY_TOO_LONG",
            },
        })
        return

    route = answer.get("choice")
    if route not in ROUTES:
        raise WorkerError("INVALID_MODEL_OUTPUT")
    probabilities = finite_probability_map(answer.get("probabilities"))
    confidence = answer.get("confidence")
    if confidence is not None:
        if isinstance(confidence, bool) or not isinstance(confidence, (int, float)):
            raise WorkerError("INVALID_MODEL_OUTPUT")
        confidence = float(confidence)
        if not math.isfinite(confidence) or confidence < 0 or confidence > 1:
            raise WorkerError("INVALID_MODEL_OUTPUT")

    sorted_probabilities = sorted(probabilities.values(), reverse=True)
    end = time.perf_counter()
    payload = {
        "schema_version": value["schema_version"],
        "request_id": request_id,
        "status": "classified",
        "route": route,
        "probabilities": probabilities,
        "selected_probability": probabilities[route],
        "top_two_margin": sorted_probabilities[0] - sorted_probabilities[1],
        "native_confidence": confidence,
        "probability_status": "upstream_default_not_validated_on_rencrow",
        "model": {
            "family": config["manifest"]["model"]["family"],
            "source_id": config["manifest"]["model"]["source_id"],
            "revision": config["manifest"]["model"]["revision"],
            "sdk_version": config["manifest"]["sdk"]["version"],
            "device": actual_device,
            "dtype": actual_dtype,
        },
        "policy_sha256": value["policy_sha256"],
        "input": {"effective_sequence_tokens": tokens, "truncated": False},
        "timing_ms": {
            "prepare": prepare_ms,
            "inference": inference_ms,
            "postprocess": (end - postprocess_started) * 1000.0,
            "server_total": (end - started) * 1000.0,
        },
        "reason_code": "OK",
    }
    emit({"kind": "response", "request_id": request_id, "result": payload})


def main():
    router = None
    try:
        config = read_line(limit=MAX_LINE_BYTES)
        if config is None:
            return 2
        router, torch, config, ready = initialize(config)
        emit(ready)
        while True:
            request = read_line()
            if request is None:
                return 0
            request_id = None
            try:
                candidate = request.get("input", {})
                request_id = candidate.get("request_id") if isinstance(candidate, dict) else None
                classify(router, torch, config, request)
            except WorkerError as exc:
                emit({"kind": "error", "request_id": request_id or "", "reason_code": exc.reason_code})
                if exc.reason_code == "SDK_ERROR":
                    return 3
            except Exception:
                print("laya worker inference failed", file=sys.stderr)
                emit({"kind": "error", "request_id": request_id or "", "reason_code": "SDK_ERROR"})
                return 3
    except WorkerError as exc:
        print("laya worker initialization failed: " + exc.reason_code, file=sys.stderr)
        try:
            emit({"kind": "startup_error", "reason_code": exc.reason_code})
        except Exception:
            pass
        return 3
    except Exception:
        print("laya worker initialization failed", file=sys.stderr)
        try:
            emit({"kind": "startup_error", "reason_code": "SDK_ERROR"})
        except Exception:
            pass
        return 3
    finally:
        if router is not None:
            try:
                router.unload()
            except Exception:
                print("laya worker model cleanup failed", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
