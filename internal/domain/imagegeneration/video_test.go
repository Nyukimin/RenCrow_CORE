package imagegeneration

import (
	"encoding/binary"
	"testing"
)

func pcmWaveFixture() []byte {
	wav := make([]byte, 46)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], 2)
	return wav
}

func TestIsValidVideoPCMWave(t *testing.T) {
	t.Run("accepts bounded PCM WAVE without interpreting duration", func(t *testing.T) {
		wav := pcmWaveFixture()
		if !IsValidVideoPCMWave(wav) {
			t.Fatal("expected structurally valid PCM WAVE")
		}
	})

	for _, testCase := range []struct {
		name  string
		edit  func([]byte) []byte
		input []byte
	}{
		{name: "not RIFF", edit: func(wav []byte) []byte { copy(wav[:4], "NOPE"); return wav }},
		{name: "not PCM", edit: func(wav []byte) []byte { binary.LittleEndian.PutUint16(wav[20:22], 3); return wav }},
		{name: "truncated data chunk", edit: func(wav []byte) []byte { binary.LittleEndian.PutUint32(wav[40:44], 200); return wav }},
		{name: "incorrect RIFF length", edit: func(wav []byte) []byte { binary.LittleEndian.PutUint32(wav[4:8], 37); return wav }},
		{name: "empty", input: []byte{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wav := testCase.input
			if testCase.edit != nil {
				wav = testCase.edit(pcmWaveFixture())
			}
			if IsValidVideoPCMWave(wav) {
				t.Fatal("expected malformed WAVE rejection")
			}
		})
	}

	if IsValidVideoPCMWave(make([]byte, MaxVideoAudioBytes+1)) {
		t.Fatal("expected oversized audio rejection")
	}
}

func TestIsValidVideoOptionsBindsLipSyncTask(t *testing.T) {
	options := VideoOptionsResponse{
		Task: VideoTaskLipSync, Model: "lip-sync-v1", Mode: "t2v",
		AspectRatios: []string{"16:9"}, DefaultAspectRatio: "16:9",
		Megapixels: 0.2, DurationSeconds: 10, Steps: 8,
	}
	if !IsValidVideoOptions(options, "t2v", VideoTaskLipSync) {
		t.Fatal("expected lip-sync options with matching task to pass")
	}
	if IsValidVideoOptions(options, "t2v", "") {
		t.Fatal("taskless H3 options must reject a lip-sync response")
	}
	options.Task = ""
	if !IsValidVideoOptions(options, "t2v", "") {
		t.Fatal("existing taskless H3 options must remain valid")
	}
}

func TestV2VLipSyncOptionsUseOnlyVideoContract(t *testing.T) {
	options := VideoOptionsResponse{
		Task: VideoTaskLipSync, Model: "MuseTalk 1.5", Mode: VideoModeV2V,
		DurationSeconds: 10, MaxVideoBytes: MaxVideoV2VBytes, MaxAudioBytes: MaxVideoAudioBytes,
	}
	if !IsValidVideoOptions(options, VideoModeV2V, VideoTaskLipSync) {
		t.Fatal("expected bounded V2V lip-sync options to pass")
	}
	if IsValidVideoOptions(options, VideoModeV2V, "") || IsValidVideoOptions(options, "t2v", VideoTaskLipSync) {
		t.Fatal("V2V options must require task=lip_sync and mode=v2v")
	}

	options.AspectRatios = []string{"16:9"}
	if IsValidVideoOptions(options, VideoModeV2V, VideoTaskLipSync) {
		t.Fatal("V2V options must not include aspect ratio controls")
	}
	options.AspectRatios = nil
	options.Steps = 8
	if IsValidVideoOptions(options, VideoModeV2V, VideoTaskLipSync) {
		t.Fatal("V2V options must not include steps")
	}
	options.Steps = 0
	options.MaxVideoBytes = MaxVideoV2VBytes + 1
	if IsValidVideoOptions(options, VideoModeV2V, VideoTaskLipSync) {
		t.Fatal("V2V max video bytes must remain within the 100 MiB hard cap")
	}
}

func TestIsValidVideoMP4ChecksOnlyTheBasicFileTypeBox(t *testing.T) {
	mp4 := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0}
	if !IsValidVideoMP4(mp4) {
		t.Fatal("expected a basic MP4 ftyp header to pass")
	}
	for _, malformed := range [][]byte{
		{},
		[]byte("not an mp4 file"),
		{0, 0, 0, 15, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0},
		{0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0},
	} {
		if IsValidVideoMP4(malformed) {
			t.Fatalf("expected malformed MP4 header rejection: %x", malformed)
		}
	}
}
