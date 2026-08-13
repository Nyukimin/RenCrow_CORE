package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	_ "modernc.org/sqlite"
)

func seedRuntimeMusicCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hobby.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY,category TEXT,item_type TEXT,title TEXT,normalized_title TEXT,subtitle TEXT,canonical_source TEXT,canonical_url TEXT,metadata_json TEXT);
CREATE TABLE hobby_relations(relation_id TEXT PRIMARY KEY,from_item_id TEXT,to_item_id TEXT,relation_type TEXT,source TEXT,evidence_url TEXT);
CREATE TABLE hobby_music_lyrics(lyrics_id TEXT PRIMARY KEY,song_item_id TEXT,source TEXT,source_record_id TEXT,canonical_url TEXT,language TEXT,rights_status TEXT,license_reference TEXT,storage_mode TEXT,lyrics_text TEXT,content_sha256 TEXT,fetched_at TEXT,updated_at TEXT);
CREATE TABLE hobby_music_syntax_features(feature_id TEXT PRIMARY KEY,song_item_id TEXT,lyrics_source TEXT,language TEXT,analyzer TEXT,analyzer_version TEXT,feature_schema TEXT,token_count INTEGER,line_count INTEGER,vocabulary_size INTEGER,features_json TEXT,source_content_sha256 TEXT,non_reconstructable INTEGER,generated_at TEXT);
CREATE INDEX idx_hobby_music_items_type_title ON hobby_items(category,item_type,normalized_title);
CREATE INDEX idx_hobby_music_lyrics_song_language ON hobby_music_lyrics(song_item_id,language,source);
CREATE INDEX idx_hobby_music_syntax_song_language ON hobby_music_syntax_features(song_item_id,language,analyzer);
INSERT INTO hobby_items VALUES('a1','music','artist','歌手A','歌手a','','provider','','{}');
INSERT INTO hobby_items VALUES('s1','music','song','青い鳥','青い鳥','','provider','','{}');
INSERT INTO hobby_relations VALUES('r1','a1','s1','performed','provider','');
INSERT INTO hobby_music_lyrics VALUES('l1','s1','provider','rec1','https://example.test/l1','ja','licensed','contract:1','full_text','許諾済み本文','hash','','2026-08-13T00:00:00Z');`)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return path
}

func TestBuildToolRuntimeRegistersMusicAndLyricsForChatWorkerAndSnapshot(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.HobbyGraph = seedRuntimeMusicCatalog(t)
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	for name, runner := range map[string]domaintool.RunnerV2{"chat": runtime.ChatRunnerV2, "worker": runtime.WorkerRunnerV2} {
		metadata, err := runner.ListTools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, toolID := range []string{"music_catalog.lookup", "lyrics_catalog.lookup"} {
			if !hasToolMetadata(metadata, toolID) {
				t.Fatalf("%s missing from %s metadata", toolID, name)
			}
		}
		response, err := runner.ExecuteV2(context.Background(), "lyrics_catalog.lookup", map[string]any{"song": "青い鳥", "artist": "歌手A", "information": "full_text"})
		if err != nil || response.IsError() {
			t.Fatalf("%s lyrics execution response=%#v err=%v", name, response, err)
		}
	}
	workerMetadata, _ := runtime.WorkerRunnerV2.ListTools(context.Background())
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(workerMetadata, nil, nil, nil))
	for _, toolID := range []string{"music_catalog.lookup", "lyrics_catalog.lookup"} {
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, toolID)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Fatalf("snapshot missing %s: %#v", toolID, snapshot.Entries)
		}
	}
	lookup, err := prepareRuntimeMusicCatalogLookup(context.Background(), cfg.Storage.Databases.HobbyGraph)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openRuntimeMusicCatalogReadOnly(lookup.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE forbidden_write(id INTEGER)`); err == nil {
		t.Fatal("read-only music runtime connection accepted a schema write")
	}
}

func TestBuildToolRuntimeLeavesUnreadyMusicSchemaUnregistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.HobbyGraph = path
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "music_catalog.lookup") || hasToolMetadata(metadata, "lyrics_catalog.lookup") {
		t.Fatal("unready music schema must fail closed")
	}
}
