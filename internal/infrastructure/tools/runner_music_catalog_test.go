package tools

import (
	"context"
	"testing"
)

type musicLookupStub struct{ called string }

func (s *musicLookupStub) LookupMusic(_ context.Context, kind, name, artist string, limit int) (any, error) {
	s.called = "music"
	return map[string]any{"status": "ok", "kind": kind, "name": name, "artist": artist, "limit": limit}, nil
}

func (s *musicLookupStub) LookupLyrics(_ context.Context, song, artist, language, information string, limit int) (any, error) {
	s.called = "lyrics"
	return map[string]any{"status": "ok", "song": song, "information": information, "limit": limit}, nil
}

func TestMusicAndLyricsToolsRegisterValidateAndExecute(t *testing.T) {
	stub := &musicLookupStub{}
	runner := NewToolRunner(ToolRunnerConfig{DisableWebSearch: true, MusicCatalogLookup: stub, LyricsCatalogLookup: stub})
	for _, toolID := range []string{"music_catalog.lookup", "lyrics_catalog.lookup"} {
		if _, ok := runner.toolsV2[toolID]; !ok {
			t.Fatalf("%s not registered", toolID)
		}
	}
	response, err := runner.ExecuteV2(context.Background(), "music_catalog.lookup", map[string]any{"kind": "song", "name": "青い鳥", "artist": "歌手A"})
	if err != nil || response.IsError() || stub.called != "music" {
		t.Fatalf("music response=%#v err=%v called=%s", response, err, stub.called)
	}
	response, err = runner.ExecuteV2(context.Background(), "lyrics_catalog.lookup", map[string]any{"song": "青い鳥", "information": "rights"})
	if err != nil || response.IsError() || stub.called != "lyrics" {
		t.Fatalf("lyrics response=%#v err=%v called=%s", response, err, stub.called)
	}
	response, err = runner.ExecuteV2(context.Background(), "lyrics_catalog.lookup", map[string]any{"song": "青い鳥", "information": "raw_sql"})
	if err != nil || !response.IsError() {
		t.Fatalf("invalid information was accepted response=%#v err=%v", response, err)
	}
}
