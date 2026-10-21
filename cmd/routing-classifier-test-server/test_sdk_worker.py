"""Focused regression tests for the isolated Laya SDK worker."""

import builtins
import importlib.metadata
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest import mock


SERVER_DIR = Path(__file__).resolve().parent
REPO_ROOT = SERVER_DIR.parents[1]
RUNTIME_ROOT = REPO_ROOT / "Tmp" / "test-runtime"
if str(SERVER_DIR) not in sys.path:
    sys.path.insert(0, str(SERVER_DIR))

import sdk_worker


SDK_VERSIONS = {
    "laya": "0.4.11",
    "torch": "2.4.0",
    "huggingface-hub": "0.26.2",
}


def make_config(model_directory):
    return {
        "kind": "initialize",
        "manifest": {
            "policy_sha256": "a" * 64,
            "model": {
                "family": "laya-multilingual",
                "source_id": "convaiinnovations/laya-multilingual",
                "revision": "b" * 40,
                "device": "cpu",
                "dtype": "float32",
                "files": [],
                "files_sha256": "c" * 64,
            },
            "sdk": {
                "distribution": "laya",
                "source_url": sdk_worker.SDK_SOURCE_URL,
                "version": SDK_VERSIONS["laya"],
                "lock_sha256": "d" * 64,
                "packages": dict(SDK_VERSIONS),
            },
        },
        "model_directory": str(model_directory),
        "policy": {
            "semantic_review_status": "verified",
            "routes": list(sdk_worker.ROUTES),
            "criteria": {route: "criterion" for route in sdk_worker.ROUTES},
            "policy_version": "rencrow-route-policy-v1",
            "instructions": "Choose one route.",
        },
        "reference_worker": {
            "offline_required": True,
            "hard_timeout_ms": 10000,
            "model_revision": "b" * 40,
            "model_directory": str(model_directory),
            "device": "cpu",
            "dtype": "float32",
            "intra_op_threads": 1,
            "inter_op_threads": 1,
            "max_len": 512,
            "head_max_len": 32,
        },
    }


class SDKWorkerPinValidationTests(unittest.TestCase):
    def setUp(self):
        RUNTIME_ROOT.mkdir(parents=True, exist_ok=True)
        self.model_directory = tempfile.TemporaryDirectory(dir=RUNTIME_ROOT)
        self.addCleanup(self.model_directory.cleanup)
        self.config = make_config(self.model_directory.name)
        self.environment = mock.patch.dict(os.environ, sdk_worker.REQUIRED_OFFLINE_ENVIRONMENT)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def deny_sdk_imports(self):
        real_import = builtins.__import__

        def guarded_import(name, globals=None, locals=None, fromlist=(), level=0):
            if name in {"torch", "laya"}:
                raise AssertionError("SDK modules were imported before their pins were checked")
            return real_import(name, globals, locals, fromlist, level)

        return mock.patch("builtins.__import__", side_effect=guarded_import)

    def assert_initialize_rejects_before_sdk_import(self, configure, versions=None):
        config = make_config(self.model_directory.name)
        configure(config)
        observed = SDK_VERSIONS if versions is None else versions

        def installed_version(package_name):
            try:
                return observed[package_name]
            except KeyError as exc:
                raise importlib.metadata.PackageNotFoundError(package_name) from exc

        with self.subTest(configure=configure):
            with mock.patch.object(sdk_worker.importlib.metadata, "version", side_effect=installed_version):
                with self.deny_sdk_imports(), mock.patch.object(
                    sdk_worker.sys, "prefix", "/tmp/rencrow-test-venv"
                ), mock.patch.object(sdk_worker.sys, "base_prefix", "/usr"):
                    with self.assertRaises(sdk_worker.WorkerError) as raised:
                        sdk_worker.initialize(config)
            self.assertEqual(raised.exception.reason_code, "SDK_ERROR")

    def test_initialize_rejects_missing_or_mismatched_sdk_pins_before_import(self):
        cases = [
            lambda config: config["manifest"].pop("sdk"),
            lambda config: config["manifest"].__setitem__("sdk", None),
            lambda config: config["manifest"]["sdk"].pop("distribution"),
            lambda config: config["manifest"]["sdk"].__setitem__("distribution", "other"),
            lambda config: config["manifest"]["sdk"].pop("source_url"),
            lambda config: config["manifest"]["sdk"].__setitem__("source_url", "https://example.invalid/laya"),
            lambda config: config["manifest"]["sdk"].pop("version"),
            lambda config: config["manifest"]["sdk"].pop("packages"),
            lambda config: config["manifest"]["sdk"].__setitem__("packages", {}),
            lambda config: config["manifest"]["sdk"]["packages"].pop("laya"),
            lambda config: config["manifest"]["sdk"]["packages"].__setitem__("laya", "0.4.10"),
            lambda config: config["manifest"]["sdk"]["packages"].__setitem__("huggingface-hub", "0.26.1"),
        ]
        for configure in cases:
            with self.subTest(configure=configure):
                self.assert_initialize_rejects_before_sdk_import(configure)

    def test_initialize_rejects_uninstalled_pinned_package_before_import(self):
        self.assert_initialize_rejects_before_sdk_import(
            lambda config: None,
            versions={"laya": "0.4.11", "torch": "2.4.0"},
        )

    def test_initialize_rejects_missing_torch_pin_before_import(self):
        self.assert_initialize_rejects_before_sdk_import(
            lambda config: config["manifest"]["sdk"]["packages"].pop("torch")
        )

    def test_initialize_rejects_incorrect_laya_distribution_version_before_import(self):
        config = make_config(self.model_directory.name)
        observed_laya_versions = iter(["0.4.11", "0.4.12"])

        def installed_version(package_name):
            if package_name == "laya":
                return next(observed_laya_versions)
            return SDK_VERSIONS[package_name]

        with mock.patch.object(sdk_worker.importlib.metadata, "version", side_effect=installed_version):
            with self.deny_sdk_imports(), mock.patch.object(
                sdk_worker.sys, "prefix", "/tmp/rencrow-test-venv"
            ), mock.patch.object(sdk_worker.sys, "base_prefix", "/usr"):
                with self.assertRaises(sdk_worker.WorkerError) as raised:
                    sdk_worker.initialize(config)
        self.assertEqual(raised.exception.reason_code, "SDK_ERROR")

    def test_initialize_rejects_non_venv_before_metadata_lookup_or_import(self):
        with mock.patch.object(sdk_worker.importlib.metadata, "version") as version:
            with self.deny_sdk_imports(), mock.patch.object(sdk_worker.sys, "prefix", "/usr"), mock.patch.object(
                sdk_worker.sys, "base_prefix", "/usr"
            ):
                with self.assertRaises(sdk_worker.WorkerError) as raised:
                    sdk_worker.initialize(self.config)
        self.assertEqual(raised.exception.reason_code, "SDK_ERROR")
        version.assert_not_called()

    def test_initialize_checks_every_package_and_laya_version_before_import(self):
        events = []

        def installed_version(package_name):
            events.append(("version", package_name))
            return SDK_VERSIONS[package_name]

        real_import = builtins.__import__

        def guarded_import(name, globals=None, locals=None, fromlist=(), level=0):
            if name in {"torch", "laya"}:
                events.append(("import", name))
            return real_import(name, globals, locals, fromlist, level)

        fake_torch = types.ModuleType("torch")
        fake_torch.float32 = object()
        fake_torch.float16 = object()
        fake_torch.bfloat16 = object()
        fake_torch.set_num_threads = lambda _: None
        fake_torch.set_num_interop_threads = lambda _: None

        class FakeAgent:
            device = "cpu"
            dtype = fake_torch.float32
            cpu_fallback_count = 0

        class FakeRouter:
            def __init__(self, models, device, revisions, max_loaded, default, auto_task_detection, sha256_digests):
                self._agents = {"multilingual": FakeAgent()}

            def predict(self, model, max_len, head_max_len):
                return {}

            def preload(self, models):
                return None

        fake_laya = types.ModuleType("laya")
        fake_laya.Router = FakeRouter

        with mock.patch.object(sdk_worker.importlib.metadata, "version", side_effect=installed_version):
            with mock.patch.dict(sys.modules, {"torch": fake_torch, "laya": fake_laya}):
                with mock.patch("builtins.__import__", side_effect=guarded_import):
                    with mock.patch.object(sdk_worker.sys, "prefix", "/tmp/rencrow-test-venv"), mock.patch.object(
                        sdk_worker.sys, "base_prefix", "/usr"
                    ):
                        _, _, _, ready = sdk_worker.initialize(self.config)

        self.assertEqual(ready["kind"], "ready")
        import_positions = [index for index, event in enumerate(events) if event[0] == "import"]
        self.assertTrue(import_positions)
        last_pin_position = max(index for index, event in enumerate(events) if event[0] == "version")
        self.assertLess(last_pin_position, min(import_positions))
        checked_packages = {name for kind, name in events if kind == "version"}
        self.assertTrue(set(SDK_VERSIONS).issubset(checked_packages))
        self.assertEqual(events.count(("version", "laya")), 2)


if __name__ == "__main__":
    unittest.main()
