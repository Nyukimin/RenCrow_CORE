package viewer

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHobbyMusicImportDryRunAndApply(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby.sqlite")
	h := HandleHobbyMusicImport(HobbyGraphOptions{DBPath: dbPath})
	body := `{"apply":false,"records":[{"format":"rencrow.music_catalog.v1","provider":"lyricfind","source_record_id":"song-1","canonical_url":"https://example.test/song","artist":"歌手A","song":"青い鳥","language":"ja","rights_status":"unknown","storage_mode":"hash_only","content_sha256":"abc","parsed_at":"2026-08-13T00:00:00Z","syntax":{"token_count":10,"line_count":3,"vocabulary_size":8,"non_reconstructable":true}}]}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/music/import", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run=%d %s", rec.Code, rec.Body.String())
	}
	body = strings.Replace(body, `"apply":false`, `"apply":true`, 1)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/music/import", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("apply=%d %s", rec.Code, rec.Body.String())
	}
	db, _ := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	defer db.Close()
	var items, lyrics, features int
	db.QueryRow(`SELECT COUNT(*) FROM hobby_items`).Scan(&items)
	db.QueryRow(`SELECT COUNT(*) FROM hobby_music_lyrics`).Scan(&lyrics)
	db.QueryRow(`SELECT COUNT(*) FROM hobby_music_syntax_features`).Scan(&features)
	if items != 2 || lyrics != 1 || features != 1 {
		t.Fatalf("items=%d lyrics=%d features=%d", items, lyrics, features)
	}
}

func TestHobbyMusicImportRejectsLyricsOnReferenceOnly(t *testing.T) {
	h := HandleHobbyMusicImport(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby.sqlite")})
	body := `{"apply":true,"records":[{"format":"rencrow.music_catalog.v1","provider":"lyricfind","source_record_id":"s","canonical_url":"https://example.test/s","artist":"a","song":"s","language":"ja","rights_status":"unknown","storage_mode":"reference_only","lyrics_text":"空へ","content_sha256":"abc","parsed_at":"2026-08-13T00:00:00Z","syntax":{"non_reconstructable":true}}]}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/music/import", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHobbyMusicImportReferenceOnlyStoresNullLyrics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby.sqlite")
	h := HandleHobbyMusicImport(HobbyGraphOptions{DBPath: dbPath})
	body := `{"apply":true,"records":[{"format":"rencrow.music_catalog.v1","provider":"lyricfind","source_record_id":"song-1","canonical_url":"https://example.test/song","artist":"歌手A","song":"青い鳥","language":"ja","rights_status":"unknown","storage_mode":"reference_only","content_sha256":"abc","parsed_at":"2026-08-13T00:00:00Z","syntax":{"token_count":0,"line_count":0,"vocabulary_size":0,"non_reconstructable":true}}]}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/music/import", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	db, _ := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	defer db.Close()
	var n int
	var mode string
	var lyrics sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*), storage_mode, lyrics_text FROM hobby_music_lyrics`).Scan(&n, &mode, &lyrics); err != nil {
		t.Fatal(err)
	}
	if n != 1 || mode != "reference_only" || lyrics.Valid {
		t.Fatalf("n=%d mode=%s lyrics=%v", n, mode, lyrics)
	}
}
