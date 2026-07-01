package audiorouter

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePlayer struct {
	plays []string
	err   error
}

func (f *fakePlayer) ListDevices(ctx context.Context) ([]Device, error) {
	_ = ctx
	return []Device{{ID: "mio-device", Name: "Audio-Out-Mio"}}, nil
}

func (f *fakePlayer) PlayWAV(ctx context.Context, deviceID string, wavData []byte, buffer time.Duration) error {
	_, _ = ctx, buffer
	f.plays = append(f.plays, deviceID+":"+string(wavData))
	return f.err
}

type fakeDownloader struct {
	data []byte
	err  error
}

func (f *fakeDownloader) Download(ctx context.Context, rawURL string) ([]byte, error) {
	_, _ = ctx, rawURL
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.data...), nil
}

func TestRouterEnqueueAndPlay(t *testing.T) {
	player := &fakePlayer{}
	router := NewRouter(RouterConfig{
		DeviceMap:       map[string]string{"mio": "mio-device"},
		Buffer:          10 * time.Millisecond,
		QueueDepth:      2,
		DownloadTimeout: time.Second,
	}, player, &fakeDownloader{data: []byte("wav")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router.Start(ctx)

	if err := router.Enqueue(Event{
		SessionID:   "s1",
		ChunkIndex:  0,
		CharacterID: "mio",
		AudioURL:    "http://example/audio.wav",
	}); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for len(player.plays) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(player.plays) != 1 {
		t.Fatalf("expected one play, got %d", len(player.plays))
	}
	if player.plays[0] != "mio-device:wav" {
		t.Fatalf("unexpected playback: %q", player.plays[0])
	}
}

func TestRouterDeduplicatesChunk(t *testing.T) {
	player := &fakePlayer{}
	router := NewRouter(RouterConfig{
		DeviceMap:       map[string]string{"mio": "mio-device"},
		DownloadTimeout: time.Second,
	}, player, &fakeDownloader{data: []byte("wav")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router.Start(ctx)

	ev := Event{SessionID: "s1", ChunkIndex: 1, CharacterID: "mio", AudioURL: "http://example/audio.wav"}
	if err := router.Enqueue(ev); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := router.Enqueue(ev); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for len(player.plays) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(player.plays) != 1 {
		t.Fatalf("expected one playback for duplicate event, got %d", len(player.plays))
	}
}

func TestRouterUnknownCharacter(t *testing.T) {
	router := NewRouter(RouterConfig{
		DeviceMap:       map[string]string{"mio": "mio-device"},
		DownloadTimeout: time.Second,
	}, &fakePlayer{}, &fakeDownloader{data: []byte("wav")})
	if err := router.Enqueue(Event{SessionID: "s1", ChunkIndex: 0, CharacterID: "shiro", AudioURL: "http://example/audio.wav"}); err == nil {
		t.Fatal("expected error for unknown character")
	}
}

func TestHTTPDownloader_StatusError(t *testing.T) {
	d := &fakeDownloader{err: errors.New("boom")}
	if _, err := d.Download(context.Background(), "http://example"); err == nil {
		t.Fatal("expected download error")
	}
}
