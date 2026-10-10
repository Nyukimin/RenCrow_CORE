#!/usr/bin/env python3
import argparse
import importlib.util
import struct
import sys
import tempfile
import types
import unittest
import wave
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).with_name("vds_e2e_probe.py")
SPEC = importlib.util.spec_from_file_location("vds_e2e_probe", MODULE_PATH)
probe = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = probe
SPEC.loader.exec_module(probe)


class VDSE2EProbeTest(unittest.TestCase):
    def write_wav(self, path: Path, channels: int, sample_width: int, sample_rate: int, samples):
        with wave.open(str(path), "wb") as wav:
            wav.setnchannels(channels)
            wav.setsampwidth(sample_width)
            wav.setframerate(sample_rate)
            if sample_width == 2:
                wav.writeframes(b"".join(struct.pack("<h", s) for s in samples))
            else:
                wav.writeframes(bytes(samples))

    def test_load_pcm16_chunks_extracts_raw_pcm_without_wav_header(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "sample.wav"
            samples = [0, 1000, -1000, 2000, -2000, 3000, -3000, 0]
            self.write_wav(path, 1, 2, 16000, samples)

            sample_rate, channels, chunks = probe.load_pcm16_chunks(path, 1)

            self.assertEqual(sample_rate, 16000)
            self.assertEqual(channels, 1)
            self.assertEqual(b"".join(chunks), b"".join(struct.pack("<h", s) for s in samples))
            self.assertNotIn(b"RIFF", b"".join(chunks))

    def test_build_session_start_payload_matches_contract(self):
        payload = probe.build_session_start_payload(
            utterance_id="utt-1",
            sample_rate=16000,
            channels=1,
            prompt="要約して",
        )
        self.assertEqual(payload["type"], "session.start")
        self.assertEqual(payload["format"], "pcm16le")
        self.assertEqual(payload["model"], "Chat")
        self.assertEqual(payload["voice_input_mode"], "vds_sub")
        self.assertEqual(payload["utterance_id"], "utt-1")

    def test_summarize_vds_messages_extracts_commit_timings(self):
        messages = [
            {"type": "session.ready", "_at": 1.05},
            {"type": "llm.delta", "text": "お", "_at": 1.20},
            {
                "type": "llm.final",
                "text": "おはよう",
                "_at": 1.50,
                "metrics": {"commit_to_first_token_ms": 150.0, "commit_to_final_ms": 450.0},
            },
        ]
        timings, metrics, delta_text, final_text, error_code = probe.summarize_vds_messages(
            messages,
            commit_at=1.0,
        )
        self.assertEqual(delta_text, "お")
        self.assertEqual(final_text, "おはよう")
        self.assertEqual(error_code, "")
        self.assertEqual(timings["commit_to_first_delta_ms"], 200.0)
        self.assertEqual(timings["commit_to_final_ms"], 500.0)
        self.assertEqual(metrics["commit_to_final_ms"], 450.0)

    def test_meets_phase1_gate_ignores_first_round_by_default(self):
        passed, reasons = probe.meets_phase1_gate({}, wav_duration_sec=25.0, warm=False)
        self.assertTrue(passed)
        self.assertEqual(reasons, [])

    def test_meets_phase1_gate_fails_when_warm_round_is_slow(self):
        passed, reasons = probe.meets_phase1_gate(
            {"commit_to_first_delta_ms": 20_000.0, "commit_to_final_ms": 30_000.0},
            wav_duration_sec=25.0,
            warm=True,
        )
        self.assertFalse(passed)
        self.assertTrue(any("commit_to_first_delta_ms" in reason for reason in reasons))

    def test_result_exit_code_requires_llm_final_when_requested(self):
        args = argparse.Namespace(require_llm_final=True, require_phase1_gate=False)
        result = {
            "results": [
                {"ok": True, "i": 1},
                {"ok": False, "i": 2},
            ]
        }
        self.assertEqual(probe.result_exit_code(args, result), 2)

    def test_result_exit_code_checks_phase1_gate(self):
        args = argparse.Namespace(require_llm_final=False, require_phase1_gate=True, max_delta_events=0)
        result = {"results": [], "phase1_gate": [{"passed": False, "reasons": ["slow"]}]}
        self.assertEqual(probe.result_exit_code(args, result), 3)

    def test_build_result_counts_delta_events(self):
        args = argparse.Namespace(
            warm_gate_first=True,
            max_delta_events=1,
            ws_url="ws://example/voice-chat",
            base_url="http://example",
        )
        rec = probe.VDSRoundResult(i=1, ok=True, events=["session.ready", "llm.delta", "llm.delta", "llm.final"])
        with tempfile.TemporaryDirectory() as tmp:
            wav_path = Path(tmp) / "sample.wav"
            self.write_wav(wav_path, 1, 2, 16000, [0, 1, 2, 3])
            result = probe.build_result(args, wav_path, [rec])

        self.assertEqual(result["results"][0]["delta_event_count"], 2)
        self.assertEqual(result["delta_event_gate"][0]["passed"], False)
        self.assertIn("delta_event_count=2", result["delta_event_gate"][0]["reasons"][0])

    def test_result_exit_code_checks_delta_event_gate(self):
        args = argparse.Namespace(require_llm_final=False, require_phase1_gate=False, max_delta_events=1)
        result = {"results": [], "phase1_gate": [], "delta_event_gate": [{"passed": False, "reasons": ["too many"]}]}
        self.assertEqual(probe.result_exit_code(args, result), 4)

    def test_sse_events_request_tails_without_last_event_id(self):
        url, headers = probe.sse_events_request("http://example:18790/")
        self.assertEqual(url, "http://example:18790/viewer/events?from=now")
        self.assertNotIn("Last-Event-ID", {key.title(): value for key, value in headers.items()})

    def fake_requests(self, status_code, lines=(), error=None):
        calls = []

        class FakeResponse:
            def __init__(self):
                self.status_code = status_code

            def __enter__(self):
                return self

            def __exit__(self, *exc_info):
                return False

            def iter_lines(self, decode_unicode=True):
                return iter(lines)

        def get(url, headers=None, stream=False, timeout=None):
            calls.append({"url": url, "headers": dict(headers or {})})
            if error is not None:
                raise error
            return FakeResponse()

        return types.SimpleNamespace(get=get), calls

    def test_sse_collector_collects_live_events_from_a_tail_subscription(self):
        fake, calls = self.fake_requests(200, ['data: {"type": "agent.response", "content": "hi"}', ""])
        with mock.patch.dict(sys.modules, {"requests": fake}):
            collector = probe.SSECollector("http://example:18790", timeout=5)
            collector.start()
            collector.stop()
        self.assertEqual(collector.error, "")
        self.assertEqual([event["type"] for event in collector.events], ["agent.response"])
        self.assertEqual(calls[0]["url"], "http://example:18790/viewer/events?from=now")
        self.assertNotIn("last-event-id", {key.lower() for key in calls[0]["headers"]})

    def test_sse_collector_start_fails_on_non_200(self):
        fake, _ = self.fake_requests(409)
        with mock.patch.dict(sys.modules, {"requests": fake}):
            collector = probe.SSECollector("http://example:18790", timeout=5)
            with self.assertRaises(RuntimeError) as raised:
                collector.start()
        self.assertIn("409", str(raised.exception))
        self.assertIn("409", collector.error)

    def test_sse_collector_start_fails_when_the_connection_fails(self):
        fake, _ = self.fake_requests(200, error=ConnectionError("refused"))
        with mock.patch.dict(sys.modules, {"requests": fake}):
            collector = probe.SSECollector("http://example:18790", timeout=5)
            with self.assertRaises(RuntimeError) as raised:
                collector.start()
        self.assertIn("refused", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
