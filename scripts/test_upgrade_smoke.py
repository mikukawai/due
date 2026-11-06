"""Failure-path regressions for the controlled upgrade smoke tool (stdlib only)."""
import importlib.util
import json
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("upgrade_smoke", Path(__file__).with_name("upgrade_smoke.py"))
assert SPEC is not None and SPEC.loader is not None
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)
REAL_POPEN = subprocess.Popen


class RedactionTests(unittest.TestCase):
    def test_redact_json_encoded_mcp_text_without_mutating_rpc_data(self):
        payload = {"accessToken": "ACCESS_SENTINEL_123456", "refresh_token": "REFRESH_SENTINEL_123456",
                   "player_tokens": {"0": "PLAYER_SENTINEL_123456"}, "code": 0,
                   "nested": json.dumps({"token": "NESTED_SENTINEL_123456"})}
        response = {"result": {"content": [{"type": "text", "text": json.dumps(payload)}]}}
        original = json.loads(json.dumps(response))
        redacted = smoke.redact(response)
        saved = json.dumps(redacted)
        for value in ("ACCESS_SENTINEL_123456", "REFRESH_SENTINEL_123456",
                      "PLAYER_SENTINEL_123456", "NESTED_SENTINEL_123456"):
            self.assertNotIn(value, saved)
        self.assertEqual(response, original, "log sanitization must not alter actual RPC results")
        parsed = json.loads(redacted["result"]["content"][0]["text"])
        self.assertEqual(parsed["code"], 0)
        self.assertEqual(smoke.redact("ordinary diagnostic text"), "ordinary diagnostic text")


class SmokeCleanupTests(unittest.TestCase):
    def stubborn_executable(self, directory):
        executable = directory / "stubborn-mcp"
        pid_file = directory / "child.pid"
        executable.write_text(
            "#!" + sys.executable + "\n"
            "import os, signal, time\n"
            "from pathlib import Path\n"
            "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
            "Path(" + repr(str(pid_file)) + ").write_text(str(os.getpid()))\n"
            "while True: time.sleep(60)\n"
        )
        executable.chmod(0o755)
        return executable, pid_file

    def wait_started(self, pid_file):
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            if pid_file.exists():
                return
            time.sleep(0.01)
        self.fail("test child did not initialize its SIGTERM handler")

    def reap_for_test(self, process):
        if process is not None:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=3)
            for pipe in (process.stdin, process.stdout):
                if pipe is not None:
                    pipe.close()

    def test_constructor_failure_reaps_process_and_closes_descriptors(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            executable, pid_file = self.stubborn_executable(directory)
            processes = []
            instances = []

            def spawn(*args, **kwargs):
                process = REAL_POPEN(*args, **kwargs)
                processes.append(process)
                return process

            def failed_initialize(instance, *args, **kwargs):
                instances.append(instance)
                self.wait_started(pid_file)
                raise TimeoutError("injected initialize failure")

            try:
                with mock.patch.object(smoke.subprocess, "Popen", side_effect=spawn), \
                     mock.patch.object(smoke.MCP, "rpc", autospec=True, side_effect=failed_initialize), \
                     mock.patch.object(smoke.MCP, "CLOSE_TIMEOUT", 0.05, create=True):
                    with self.assertRaisesRegex(TimeoutError, "injected initialize failure"):
                        smoke.MCP(executable, directory, [])
                self.assertEqual(len(processes), 1)
                self.assertIsNotNone(processes[0].poll(), "failed constructor leaked the real child process")
                self.assertTrue(processes[0].stdin.closed)
                self.assertTrue(processes[0].stdout.closed)
                self.assertTrue(instances[0].err.closed)
                self.assertIsNone(instances[0].selector.get_map())
            finally:
                for process in processes:
                    self.reap_for_test(process)
                for instance in instances:
                    instance.selector.close()
                    instance.err.close()

    def test_close_force_kills_stubborn_process_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            executable, pid_file = self.stubborn_executable(directory)
            instance = smoke.MCP.__new__(smoke.MCP)
            instance.err = (directory / "stderr.log").open("w")
            instance.proc = REAL_POPEN([str(executable)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=instance.err)
            assert instance.proc.stdin is not None and instance.proc.stdout is not None
            instance.selector = selectors.DefaultSelector()
            instance.selector.register(instance.proc.stdout, selectors.EVENT_READ)
            instance.clients = []
            instance._closed = False
            self.wait_started(pid_file)
            try:
                with mock.patch.object(smoke.MCP, "CLOSE_TIMEOUT", 0.05, create=True):
                    instance.close()
                    instance.close()
                self.assertIsNotNone(instance.proc.poll())
                self.assertTrue(instance.proc.stdin.closed)
                self.assertTrue(instance.proc.stdout.closed)
                self.assertTrue(instance.err.closed)
                self.assertIsNone(instance.selector.get_map())
            finally:
                self.reap_for_test(instance.proc)
                instance.selector.close()
                instance.err.close()

    def test_constructor_preserves_original_error_without_exception_notes(self):
        class LegacyInitializeError(Exception):
            def __getattribute__(self, name):
                if name == "add_note":  # Python 3.10 has no exception notes.
                    raise AttributeError(name)
                return super().__getattribute__(name)

        instances = []

        def fail_initialize(instance, *args, **kwargs):
            instances.append(instance)
            raise LegacyInitializeError("original initialize error")

        with tempfile.TemporaryDirectory() as temp:
            try:
                with mock.patch.object(smoke.subprocess, "Popen"), \
                     mock.patch.object(smoke.selectors, "DefaultSelector"), \
                     mock.patch.object(smoke.MCP, "rpc", autospec=True, side_effect=fail_initialize), \
                     mock.patch.object(smoke.MCP, "close", side_effect=RuntimeError("injected cleanup error")), \
                     mock.patch.object(sys, "stderr"):
                    with self.assertRaisesRegex(LegacyInitializeError, "original initialize error"):
                        smoke.MCP(Path("unused"), Path(temp), [])
            finally:
                for instance in instances:
                    instance.err.close()

    def test_main_cleans_other_resources_when_mcp_close_raises(self):
        with tempfile.TemporaryDirectory() as temp:
            work = Path(temp) / "run"
            processes = [mock.Mock(name="node"), mock.Mock(name="gate")]
            for process in processes:
                process.wait.return_value = 0
                process.poll.return_value = 0
            ds = mock.Mock()
            ds.server_address = ("127.0.0.1", 19001)
            ds.actions = []
            mcp = mock.Mock()
            mcp.rpc.side_effect = RuntimeError("injected failure before tools/list")
            mcp.close.side_effect = subprocess.TimeoutExpired("fixture-mcp", 0.05)
            arguments = ["upgrade_smoke.py", "--bin-dir", str(Path(temp)),
                         "--consul-addr", "127.0.0.1:19002", "--redis-addr", "127.0.0.1:19003",
                         "--work-dir", str(work), "--disposable-middleware"]
            with mock.patch.object(sys, "argv", arguments), \
                 mock.patch.object(smoke, "DSStub", return_value=ds), \
                 mock.patch.object(smoke, "MCP", return_value=mcp), \
                 mock.patch.object(smoke, "wait_until", return_value=True), \
                 mock.patch.object(smoke.subprocess, "Popen", side_effect=processes):
                result = smoke.main()
            self.assertEqual(result, 1)
            for process in processes:
                process.send_signal.assert_called_once_with(signal.SIGTERM)
                process.wait.assert_called()
            ds.shutdown.assert_called_once()
            ds.server_close.assert_called_once()
            report = json.loads((work / "summary.json").read_text())
            self.assertFalse(report["passed"])
            self.assertIn("injected failure", report["failure"])
            self.assertTrue(report.get("teardown_warnings"))
            self.assertTrue((work / "mcp-transcript.json").exists())
            self.assertEqual((work / "mcp-transcript.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual((work / "summary.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(work.stat().st_mode & 0o777, 0o700)


if __name__ == "__main__":
    unittest.main()
