package musiccatalog

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func seedMusicCatalog(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY,category TEXT,item_type TEXT,title TEXT,normalized_title TEXT,subtitle TEXT,canonical_source TEXT,canonical_url TEXT,metadata_json TEXT);
CREATE TABLE hobby_relations(relation_id TEXT PRIMARY KEY,from_item_id TEXT,to_item_id TEXT,relation_type TEXT,source TEXT,evidence_url TEXT);
CREATE TABLE hobby_music_lyrics(lyrics_id TEXT PRIMARY KEY,song_item_id TEXT,source TEXT,source_record_id TEXT,canonical_url TEXT,language TEXT,rights_status TEXT,license_reference TEXT,storage_mode TEXT,lyrics_text TEXT,content_sha256 TEXT,fetched_at TEXT,updated_at TEXT);
CREATE TABLE hobby_music_syntax_features(feature_id TEXT PRIMARY KEY,song_item_id TEXT,lyrics_source TEXT,language TEXT,analyzer TEXT,analyzer_version TEXT,feature_schema TEXT,token_count INTEGER,line_count INTEGER,vocabulary_size INTEGER,features_json TEXT,source_content_sha256 TEXT,non_reconstructable INTEGER,generated_at TEXT);
CREATE INDEX idx_hobby_music_items_type_title ON hobby_items(category,item_type,normalized_title);
CREATE INDEX idx_hobby_music_lyrics_song_language ON hobby_music_lyrics(song_item_id,language,source);
CREATE INDEX idx_hobby_music_syntax_song_language ON hobby_music_syntax_features(song_item_id,language,analyzer);
INSERT INTO hobby_items VALUES('a1','music','artist','歌手A','歌手a','','provider','https://example.test/a1','{}');
INSERT INTO hobby_items VALUES('a2','music','artist','歌手B','歌手b','','provider','https://example.test/a2','{}');
INSERT INTO hobby_items VALUES('s1','music','song','青い鳥','青い鳥','','provider','https://example.test/s1','{}');
INSERT INTO hobby_items VALUES('s2','music','song','青い鳥','青い鳥','','provider','https://example.test/s2','{}');
INSERT INTO hobby_relations VALUES('r1','a1','s1','performed','provider','https://example.test/s1');
INSERT INTO hobby_relations VALUES('r2','a2','s2','performed','provider','https://example.test/s2');
INSERT INTO hobby_music_lyrics VALUES('l1','s1','licensed','rec1','https://example.test/l1','ja','licensed','contract:1','full_text','許諾済み本文','hash1','2026-08-13T00:00:00Z','2026-08-13T00:00:00Z');
INSERT INTO hobby_music_lyrics VALUES('l2','s1','reference','rec2','https://example.test/l2','ja','unknown','','hash_only',NULL,'hash2','2026-08-13T00:00:00Z','2026-08-13T00:00:00Z');
INSERT INTO hobby_music_syntax_features VALUES('f1','s1','licensed','ja','syntax','1','rencrow.music.syntax.v1',10,3,8,'{"unique_ratio":0.8}','hash1',1,'2026-08-13T00:00:00Z');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEnsureIndexedLookupSchemaCreatesRequiredRelationIndexes(t *testing.T) {
	db := seedMusicCatalog(t)
	if err := EnsureIndexedLookupSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_hobby_music_relations_from", "idx_hobby_music_relations_to"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", name, count, err)
		}
	}
}

func TestLookupCatalogRequiresArtistToResolveSameTitle(t *testing.T) {
	db := seedMusicCatalog(t)
	ambiguous, err := LookupCatalog(context.Background(), db, CatalogRequest{Kind: "song", Name: "青い鳥"})
	if err != nil || ambiguous.Status != "ambiguous" || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous=%#v err=%v", ambiguous, err)
	}
	resolved, err := LookupCatalog(context.Background(), db, CatalogRequest{Kind: "song", Name: "青い鳥", Artist: "歌手A"})
	if err != nil || resolved.Status != "ok" || len(resolved.Items) != 1 || resolved.Items[0].ItemID != "s1" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}

func TestLookupLyricsSeparatesRightsSyntaxAndLicensedText(t *testing.T) {
	db := seedMusicCatalog(t)
	rights, err := LookupLyrics(context.Background(), db, LyricsRequest{Song: "青い鳥", Artist: "歌手A", Information: "rights"})
	if err != nil || len(rights.Lyrics) != 2 {
		t.Fatalf("rights=%#v err=%v", rights, err)
	}
	for _, entry := range rights.Lyrics {
		if entry.LyricsText != "" {
			t.Fatal("rights lookup leaked lyrics text")
		}
	}
	full, err := LookupLyrics(context.Background(), db, LyricsRequest{Song: "青い鳥", Artist: "歌手A", Information: "full_text"})
	if err != nil || len(full.Lyrics) != 1 || full.Lyrics[0].LyricsText != "許諾済み本文" || full.Lyrics[0].RightsStatus != "licensed" {
		t.Fatalf("full=%#v err=%v", full, err)
	}
	syntax, err := LookupLyrics(context.Background(), db, LyricsRequest{Song: "青い鳥", Artist: "歌手A", Information: "syntax"})
	if err != nil || len(syntax.Syntax) != 1 || syntax.Syntax[0].TokenCount != 10 {
		t.Fatalf("syntax=%#v err=%v", syntax, err)
	}
}
