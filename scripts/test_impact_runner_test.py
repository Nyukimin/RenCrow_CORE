"""Real owner PowerShell coverage contract, independent of live services."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class ImpactRunnerContract(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        (self.repo / "scripts").mkdir()
        shutil.copyfile(Path(__file__).with_name("test-local.ps1"), self.repo / "scripts" / "test-local.ps1")
        (self.repo / ".gitignore").write_text("Tmp/\n", encoding="utf-8")
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True, capture_output=True)
        (self.repo / "one.test.mjs").write_text("// coverage fixture\n", encoding="utf-8")
        self.plan = {"version": 2, "steps": [{"name": "node", "filePath": "node", "arguments": ["--test", "one.test.mjs"], "testFiles": ["*.test.mjs"], "tier": "related", "impact": {"paths": ["*.mjs"]}, "resourceLocks": [], "timeoutSeconds": 10}]}

    def invoke(self, *args):
        (self.repo / "scripts" / "test-local.plan.json").write_text(json.dumps(self.plan), encoding="utf-8")
        pwsh = os.environ.get("RENCROW_TEST_PWSH", "pwsh")
        return subprocess.run([pwsh, "-NoProfile", "-File", str(self.repo / "scripts" / "test-local.ps1"), *args], capture_output=True, text=True, timeout=20, env={**os.environ})

    def test_v1_and_v2_keep_full_registration(self):
        for version in (1, 2):
            self.plan["version"] = version
            result = self.invoke("-SelfTest")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("tracked tests: 1", result.stdout)

    def test_new_matching_node_test_must_actually_execute(self):
        (self.repo / "two.test.mjs").write_text("// another test\n", encoding="utf-8")
        result = self.invoke("-SelfTest", "-Step", "node")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("registers but does not execute", result.stderr)

    def test_unregistered_test_still_blocks_selected_step(self):
        (self.repo / "test_unknown.py").write_text("# unregistered\n", encoding="utf-8")
        result = self.invoke("-SelfTest", "-Step", "node")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not cover tracked test", result.stderr)

    def test_execution_plan_cannot_bypass_selection_contract(self):
        result = self.invoke("-ExecutionPlan", "anything.json", "-Step", "node")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot be combined", result.stderr)

    def test_ci_uses_canonical_owner_instead_of_duplicate_test_commands(self):
        root = Path(__file__).resolve().parent.parent
        workflow = (root / ".github" / "workflows" / "go-test.yml").read_text(encoding="utf-8")
        self.assertIn("run-test-impact-ci.ps1", workflow)
        self.assertIn("test-local.ps1 -SelfTest", workflow)
        for command in ("go test ", "go vet ", "go generate "):
            self.assertNotIn(command, workflow)
        self.assertFalse((root / ".github" / "workflows" / "pr.yml").exists())
        release = (root / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
        self.assertIn("needs: regression", release)

    def test_go_build_output_stays_in_owner_run_directory(self):
        (self.repo / "main.go").write_text("package main\nfunc main() {}\n", encoding="utf-8")
        self.plan["steps"].append({"name": "build", "filePath": "go", "arguments": ["build", "main.go"], "tier": "fast", "impact": {"paths": ["*.go"]}, "resourceLocks": [], "timeoutSeconds": 30})
        result = self.invoke("-Step", "build")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.repo / "main").exists())
        self.assertFalse((self.repo / "main.exe").exists())
        self.assertIn("build-output", result.stdout)

    def test_generation_gate_detects_new_untracked_output(self):
        (self.repo / "go.mod").write_text("module generationfixture\n\ngo 1.24\n", encoding="utf-8")
        (self.repo / "main.go").write_text(
            'package main\n//go:generate go run ./generator\nfunc main() {}\n', encoding="utf-8")
        (self.repo / "generator").mkdir()
        (self.repo / "generator" / "main.go").write_text(
            'package main\nimport "os"\nfunc main() { if err := os.WriteFile("generated.txt", []byte("generated"), 0600); err != nil { panic(err) } }\n',
            encoding="utf-8")
        for arguments in (["add", "."], ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"]):
            subprocess.run(["git", *arguments], cwd=self.repo, check=True, capture_output=True)
        result = subprocess.run(
            [os.environ.get("RENCROW_TEST_PWSH", "pwsh"), "-NoProfile", "-File", str(Path(__file__).with_name("check-generated.ps1").resolve())],
            cwd=self.repo, capture_output=True, text=True, timeout=30, env={**os.environ})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Generated files changed", result.stderr)
        self.assertTrue((self.repo / "generated.txt").is_file())

    def test_owner_resolves_python_and_its_own_powershell(self):
        for executable, arguments in (("python", ["-c", "print('interpreter-ok')"]), ("pwsh", ["-NoProfile", "-Command", "Write-Output interpreter-ok"])):
            self.plan["steps"] = [{"name": "interpreter", "filePath": executable, "arguments": arguments, "testFiles": ["*.test.mjs"], "tier": "fast", "impact": {"paths": ["*.mjs"]}, "resourceLocks": [], "timeoutSeconds": 30}]
            result = self.invoke("-Step", "interpreter")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("interpreter-ok", result.stdout)

    def test_format_gate_includes_untracked_patch_source(self):
        (self.repo / "new.go").write_text("package sample\nfunc f( ){ }\n", encoding="utf-8")
        result = subprocess.run(
            [os.environ.get("RENCROW_TEST_PWSH", "pwsh"), "-NoProfile", "-File", str(Path(__file__).with_name("check-format.ps1").resolve())],
            cwd=self.repo, capture_output=True, text=True, timeout=20, env={**os.environ})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Go formatting check failed: new.go", result.stderr)


if __name__ == "__main__":
    unittest.main()
