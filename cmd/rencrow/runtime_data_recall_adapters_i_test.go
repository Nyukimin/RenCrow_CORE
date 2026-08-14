package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestDataRecallHobbyGraphPublicMusicRoutesUseFixedKindsAndSafeCatalogProjection(t *testing.T) {
	path := seedRuntimeHobbyGraphOwnerDatabase(t)
	lookup, err := prepareRuntimeMusicCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallHobbyGraph(registry, lookup); err != nil {
		t.Fatalf("register hobby graph recall: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 4 {
		t.Fatalf("hobby graph recall routes = %#v", routes)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	public := runtimeHobbyGraphPublicContext(t, "hobby-public-recall")
	artist := runtimeHobbyGraphExecuteRecall(t, worker, public, "music_artist", "Artist One")
	if len(artist.Records) != 1 {
		t.Fatalf("artist records = %#v", artist)
	}
	assertHobbyGraphCatalogRecord(t, artist.Records[0], "artist", "Artist One", "artist-1")
	song := runtimeHobbyGraphExecuteRecall(t, worker, public, "music_song", "Blue Bird")
	if len(song.Records) != 1 {
		t.Fatalf("song records = %#v", song)
	}
	assertHobbyGraphCatalogRecord(t, song.Records[0], "song", "Blue Bird", "song-1")
	if _, found := artist.Records[0]["lyrics_text"]; found {
		t.Fatal("public catalog projection leaked lyrics_text")
	}
	if _, found := song.Records[0]["db_path"]; found {
		t.Fatal("public catalog projection leaked database path")
	}
	if response, err := worker.ExecuteV2(public, "data.recall", map[string]any{
		"store": "hobby_graph", "operation": "lyrics_metadata", "query": "Blue Bird", "limit": 1,
	}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("unregistered lyrics_metadata route response=%#v err=%v", response, err)
	}
}

func assertHobbyGraphCatalogRecord(t *testing.T, record map[string]any, kind, name, itemID string) {
	t.Helper()
	if record["status"] != "ok" || record["kind"] != kind || record["name"] != name || record["artist"] != "" {
		t.Fatalf("catalog record header = %#v", record)
	}
	items, ok := record["items"].([]musiccatalog.CatalogItem)
	if !ok || len(items) != 1 || items[0].ItemID != itemID || items[0].Kind != kind || items[0].Title != name {
		t.Fatalf("catalog record items = %#v (ok=%v)", record["items"], ok)
	}
}
