#!/usr/bin/env python3
import argparse
import importlib.util
import struct
import sys
import tempfile
import unittest
import wave
from pathlib import Path


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


if __name__ == "__main__":
    unittest.main()
