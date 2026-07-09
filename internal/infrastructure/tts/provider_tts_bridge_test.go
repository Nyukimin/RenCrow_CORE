package tts

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestProviderTTSBridgeSplitsLongTextBeforeSynthesis(t *testing.T) {
	provider := &recordingProvider{}
	sink := &recordingSink{}
	var readyTexts []string
	var readyIndexes []int
	var readyAudioPaths []string
	var readyAudioURLs []string
	bridge := NewProviderTTSBridge(ProviderTTSBridgeConfig{
		Provider:  provider,
		Sink:      sink,
		OutputDir: t.TempDir(),
		OnChunkReady: func(_, _ string, chunkIndex int, _, text, _, audioPath, audioURL string) {
			readyIndexes = append(readyIndexes, chunkIndex)
			readyTexts = append(readyTexts, text)
			readyAudioPaths = append(readyAudioPaths, audioPath)
			readyAudioURLs = append(readyAudioURLs, audioURL)
		},
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "s1",
		CharacterID: "mio",
		VoiceID:     "female_01",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}

	err := bridge.PushText(context.Background(), "s1", "今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。", nil)
	if err != nil {
		t.Fatalf("push text: %v", err)
	}

	if len(provider.texts) != 2 {
		t.Fatalf("expected 2 provider calls, got %d: %#v", len(provider.texts), provider.texts)
	}
	if provider.texts[0] != "😊今日はいい天気ですね。" || provider.texts[1] != "😊少し歩いてから、温かいお茶を飲みましょう。" {
		t.Fatalf("unexpected provider texts: %#v", provider.texts)
	}
	if len(readyTexts) != 2 || len(sink.chunks) != 2 {
		t.Fatalf("expected 2 ready/sink chunks, got ready=%d sink=%d", len(readyTexts), len(sink.chunks))
	}
	if readyIndexes[0] != 0 || readyIndexes[1] != 1 {
		t.Fatalf("unexpected chunk indexes: %#v", readyIndexes)
	}
	if readyAudioPaths[0] != "01.wav" || readyAudioPaths[1] != "02.wav" {
		t.Fatalf("unexpected viewer audio paths: %#v", readyAudioPaths)
	}
	if readyAudioURLs[0] != "http://tts.local/audio/01.wav" || readyAudioURLs[1] != "http://tts.local/audio/02.wav" {
		t.Fatalf("unexpected viewer audio urls: %#v", readyAudioURLs)
	}
}

func TestDisplayChunkForSpeechChunkDoesNotIndependentlySplitDifferentText(t *testing.T) {
	speechChunks := []string{"一つ目の音声です。", "二つ目の音声です。"}
	displayText := "表示側だけがまったく違う境界で分割される長い文字列です。"
	speechText := strings.Join(speechChunks, "")

	if got := displayChunkForSpeechChunk(speechText, displayText, speechChunks, 0); got != speechChunks[0] {
		t.Fatalf("first display chunk = %q, want speech chunk %q", got, speechChunks[0])
	}
	if got := displayChunkForSpeechChunk(speechText, displayText, speechChunks, 1); got != speechChunks[1] {
		t.Fatalf("second display chunk = %q, want speech chunk %q", got, speechChunks[1])
	}
}

type recordingProvider struct {
	texts []string
}

func (p *recordingProvider) Name() string {
	return "recording"
}

func (p *recordingProvider) Synthesize(_ context.Context, in SynthesisInput) (SynthesisOutput, error) {
	p.texts = append(p.texts, in.Text)
	path := fmt.Sprintf("%s/%02d.wav", in.OutputDir, len(p.texts))
	if err := writeTestWAV(path); err != nil {
		return SynthesisOutput{}, err
	}
	return SynthesisOutput{
		Provider:      p.Name(),
		AudioFilePath: path,
		AudioURL:      fmt.Sprintf("http://tts.local/audio/%02d.wav", len(p.texts)),
	}, nil
}

func writeTestWAV(path string) error {
	pcm := make([]byte, 960)
	for i := 0; i+1 < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:i+2], uint16(1200))
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+len(pcm)))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], 48000)
	binary.LittleEndian.PutUint32(header[28:32], 96000)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(len(pcm)))
	return os.WriteFile(path, append(header, pcm...), 0o644)
}

type recordingSink struct {
	chunks []audioChunk
}

func (s *recordingSink) SubmitChunk(_ context.Context, _ string, ch audioChunk) error {
	s.chunks = append(s.chunks, ch)
	return nil
}

func (s *recordingSink) CompleteSession(context.Context, string) error {
	return nil
}
