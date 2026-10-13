package stt

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseControlIgnoresOversizedPayloads(t *testing.T) {
	small := `{"type":"stop","pad":"` + strings.Repeat("x", 200) + `"}`
	if _, ok := ParseControl([]byte(small)); !ok {
		t.Fatal("a control message of a few hundred bytes must still parse")
	}
	big := `{"type":"stop","pad":"` + strings.Repeat("x", MaxControlMessageBytes) + `"}`
	if len(big) <= MaxControlMessageBytes {
		t.Fatal("test payload must exceed the limit")
	}
	if c, ok := ParseControl([]byte(big)); ok {
		t.Fatalf("a payload over %d bytes is audio, not control; parsed as %+v", MaxControlMessageBytes, c)
	}
}

func TestWAVToPCM16RejectsADataSizeLargerThanThePayload(t *testing.T) {
	pcm := pcmTone(100, 500)
	for _, claimed := range []uint32{uint32(len(pcm)) + 1, uint32(len(pcm)) + 1000, 0x7FFFFFFF, 0xFFFFFFFE} {
		wav := PCM16LEToWAV(pcm, 16000)
		binary.LittleEndian.PutUint32(wav[40:44], claimed)
		if out, err := WAVToPCM16(wav); err == nil {
			t.Fatalf("data size %d claims more than the %d bytes sent but %d bytes were accepted", claimed, len(pcm), len(out))
		}
	}
	// The two streaming sentinels stay valid: writers that cannot seek leave them in the header.
	for _, sentinel := range []uint32{0, 0xFFFFFFFF} {
		wav := PCM16LEToWAV(pcm, 16000)
		binary.LittleEndian.PutUint32(wav[40:44], sentinel)
		out, err := WAVToPCM16(wav)
		if err != nil || !bytes.Equal(out, pcm) {
			t.Fatalf("streaming sentinel %#x: err=%v equal=%t", sentinel, err, bytes.Equal(out, pcm))
		}
	}
	exact := PCM16LEToWAV(pcm, 16000)
	if out, err := WAVToPCM16(exact); err != nil || !bytes.Equal(out, pcm) {
		t.Fatalf("a consistent header must be accepted: err=%v", err)
	}
}

func TestWAVToPCM16NeverTrustsHeaderSizes(t *testing.T) {
	pcm := pcmTone(50, 500)
	hostile := map[string]func([]byte) []byte{
		"fmt chunk claims 4 GiB": func(w []byte) []byte {
			binary.LittleEndian.PutUint32(w[16:20], 0xFFFFFFFF)
			return w
		},
		"unknown chunk claims 4 GiB before data": func(w []byte) []byte {
			out := append([]byte{}, w[:36]...)
			out = append(out, []byte("junk\xff\xff\xff\xff")...)
			return append(out, w[36:]...)
		},
		"RIFF size claims 4 GiB": func(w []byte) []byte {
			binary.LittleEndian.PutUint32(w[4:8], 0xFFFFFFFF)
			return w
		},
	}
	for name, mutate := range hostile {
		wav := mutate(PCM16LEToWAV(pcm, 16000))
		out, err := WAVToPCM16(wav) // must neither panic nor allocate from the claimed size
		if err == nil && len(out) > len(wav) {
			t.Fatalf("%s: %d bytes returned from a %d byte payload", name, len(out), len(wav))
		}
	}
}
