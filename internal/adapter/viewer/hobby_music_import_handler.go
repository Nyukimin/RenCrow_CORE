package viewer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type hobbyMusicArtifact struct {
	Format           string `json:"format"`
	Provider         string `json:"provider"`
	SourceRecordID   string `json:"source_record_id"`
	CanonicalURL     string `json:"canonical_url"`
	Artist           string `json:"artist"`
	Song             string `json:"song"`
	Language         string `json:"language"`
	RightsStatus     string `json:"rights_status"`
	LicenseReference string `json:"license_reference"`
	StorageMode      string `json:"storage_mode"`
	LyricsText       string `json:"lyrics_text"`
	ContentSHA256    string `json:"content_sha256"`
	ParsedAt         string `json:"parsed_at"`
	Syntax           struct {
		TokenCount           int     `json:"token_count"`
		LineCount            int     `json:"line_count"`
		VocabularySize       int     `json:"vocabulary_size"`
		UniqueRatio          float64 `json:"unique_ratio"`
		RepeatedLineCount    int     `json:"repeated_line_count"`
		QuestionLineCount    int     `json:"question_line_count"`
		ExclamationLineCount int     `json:"exclamation_line_count"`
		NonReconstructable   bool    `json:"non_reconstructable"`
	} `json:"syntax"`
}

func HandleHobbyMusicImport(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Apply   bool                 `json:"apply"`
			Records []hobbyMusicArtifact `json:"records"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&req) != nil || len(req.Records) == 0 || len(req.Records) > 100 {
			http.Error(w, "invalid music import request", http.StatusBadRequest)
			return
		}
		for _, a := range req.Records {
			if err := validateHobbyMusicArtifact(a); err != nil {
				http.Error(w, "music import rejected: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if !req.Apply {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "applied": false, "validated": len(req.Records)})
			return
		}
		dbPath := resolveHobbyGraphWritableDBPath(opts.DBPath)
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "music database unavailable", 500)
			return
		}
		defer db.Close()
		if err = ensureHobbyGraphTables(r.Context(), db); err != nil {
			http.Error(w, "music schema unavailable", 500)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "music import failed", 500)
			return
		}
		defer tx.Rollback()
		for _, a := range req.Records {
			if err = importHobbyMusicArtifact(r.Context(), tx, a); err != nil {
				http.Error(w, "music import failed", 500)
				return
			}
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, "music import failed", 500)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "applied": true, "imported": len(req.Records)})
	}
}
func validateHobbyMusicArtifact(a hobbyMusicArtifact) error {
	if a.Format != "rencrow.music_catalog.v1" || strings.TrimSpace(a.Provider) == "" || strings.TrimSpace(a.SourceRecordID) == "" || strings.TrimSpace(a.Artist) == "" || strings.TrimSpace(a.Song) == "" {
		return fmt.Errorf("format, provider, source_record_id, artist, and song are required")
	}
	if a.StorageMode == "full_text" {
		if a.RightsStatus != "licensed" && a.RightsStatus != "public_domain" && a.RightsStatus != "user_owned" {
			return fmt.Errorf("full text rights are not allowed")
		}
		if strings.TrimSpace(a.LicenseReference) == "" || a.LyricsText == "" {
			return fmt.Errorf("full text license evidence is required")
		}
	} else if a.LyricsText != "" {
		return fmt.Errorf("non-full-text artifact contains lyrics")
	}
	if !a.Syntax.NonReconstructable {
		return fmt.Errorf("syntax features must be non-reconstructable")
	}
	return nil
}
func musicID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}
func importHobbyMusicArtifact(ctx context.Context, tx *sql.Tx, a hobbyMusicArtifact) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	artistID := "music:artist:" + musicID(a.Provider, a.Artist)
	songID := "music:song:" + musicID(a.Provider, a.SourceRecordID)
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO hobby_items(item_id,category,item_type,title,normalized_title,canonical_source,canonical_url,created_at,updated_at) VALUES (?, 'music','artist',?,?,?, '',?,?)`, artistID, a.Artist, strings.ToLower(a.Artist), a.Provider, now, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hobby_items(item_id,category,item_type,title,normalized_title,canonical_source,canonical_url,created_at,updated_at) VALUES (?, 'music','song',?,?,?,?,?,?)`, songID, a.Song, strings.ToLower(a.Song), a.Provider, a.CanonicalURL, now, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hobby_relations(relation_id,from_item_id,to_item_id,relation_type,source,evidence_url) VALUES (?,?,?,?,?,?)`, "music:relation:"+musicID(artistID, songID, "performed"), artistID, songID, "performed", a.Provider, a.CanonicalURL)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO hobby_music_lyrics(lyrics_id,song_item_id,source,source_record_id,canonical_url,language,rights_status,license_reference,storage_mode,lyrics_text,content_sha256,fetched_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, "music:lyrics:"+musicID(a.Provider, a.SourceRecordID, a.Language), songID, a.Provider, a.SourceRecordID, a.CanonicalURL, a.Language, a.RightsStatus, a.LicenseReference, a.StorageMode, nullIfEmpty(a.LyricsText), a.ContentSHA256, a.ParsedAt, now)
	if err != nil {
		return err
	}
	featureJSON, _ := json.Marshal(a.Syntax)
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO hobby_music_syntax_features(feature_id,song_item_id,lyrics_source,language,analyzer,analyzer_version,feature_schema,token_count,line_count,vocabulary_size,features_json,source_content_sha256,non_reconstructable,generated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?)`, "music:syntax:"+musicID(songID, a.Provider, a.ContentSHA256), songID, a.Provider, a.Language, "lyrics_catalog", "1", "rencrow.music.syntax.v1", a.Syntax.TokenCount, a.Syntax.LineCount, a.Syntax.VocabularySize, string(featureJSON), a.ContentSHA256, now)
	return err
}
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
