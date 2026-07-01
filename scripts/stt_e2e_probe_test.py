#!/usr/bin/env python3
import importlib.util
import argparse
import struct
import tempfile
import unittest
import wave
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("stt_e2e_probe.py")
SPEC = importlib.util.spec_from_file_location("stt_e2e_probe", MODULE_PATH)
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


class STTE2EProbeTest(unittest.TestCase):
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

    def test_load_pcm16_chunks_can_append_tail_silence(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "sample.wav"
            samples = [1000, -1000]
            self.write_wav(path, 1, 2, 1000, samples)

            sample_rate, channels, chunks = probe.load_pcm16_chunks(path, 1, tail_silence_ms=2)

            self.assertEqual(sample_rate, 1000)
            self.assertEqual(channels, 1)
            self.assertEqual(b"".join(chunks), b"".join(struct.pack("<h", s) for s in samples) + b"\x00\x00\x00\x00")

    def test_load_pcm16_chunks_rejects_non_mono_or_non_pcm16_wav(self):
        with tempfile.TemporaryDirectory() as tmp:
            stereo_path = Path(tmp) / "stereo.wav"
            self.write_wav(stereo_path, 2, 2, 16000, [0, 0, 1, 1])
            with self.assertRaisesRegex(ValueError, "mono"):
                probe.load_pcm16_chunks(stereo_path, 200)

            pcm8_path = Path(tmp) / "pcm8.wav"
            self.write_wav(pcm8_path, 1, 1, 16000, [0, 1, 2, 3])
            with self.assertRaisesRegex(ValueError, "PCM16"):
                probe.load_pcm16_chunks(pcm8_path, 200)
            with self.assertRaisesRegex(ValueError, "tail_silence_ms"):
                probe.load_pcm16_chunks(pcm8_path, 200, tail_silence_ms=-1)

    def test_result_exit_code_requires_ws_final_when_requested(self):
        args = argparse.Namespace(
            require_ws_final=True,
            require_provider_text=False,
            require_chat_input_text=False,
            chat_input_url="",
        )
        result = {"ws": [{"ok": True}, {"ok": False}], "inference": [], "chat_input": []}
        self.assertEqual(probe.result_exit_code(args, result), 2)

    def test_result_exit_code_allows_partial_only_when_not_required(self):
        args = argparse.Namespace(
            require_ws_final=False,
            require_provider_text=False,
            require_chat_input_text=False,
            chat_input_url="",
        )
        result = {"ws": [{"ok": False, "partial": "テスト"}], "inference": [], "chat_input": []}
        self.assertEqual(probe.result_exit_code(args, result), 0)


if __name__ == "__main__":
    unittest.main()
