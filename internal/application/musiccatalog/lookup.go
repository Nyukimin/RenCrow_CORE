package musiccatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const defaultLimit = 10

type CatalogRequest struct {
	Kind   string
	Name   string
	Artist string
	Limit  int
}

type LyricsRequest struct {
	Song        string
	Artist      string
	Language    string
	Information string
	Limit       int
}

type CatalogItem struct {
	ItemID          string         `json:"item_id"`
	Kind            string         `json:"kind"`
	Title           string         `json:"title"`
	Subtitle        string         `json:"subtitle,omitempty"`
	CanonicalSource string         `json:"canonical_source,omitempty"`
	CanonicalURL    string         `json:"canonical_url,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Relations       []Relation     `json:"relations,omitempty"`
}

type Relation struct {
	Type        string `json:"type"`
	ItemID      string `json:"item_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Source      string `json:"source,omitempty"`
	EvidenceURL string `json:"evidence_url,omitempty"`
}

type CatalogResult struct {
	Status     string        `json:"status"`
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	Artist     string        `json:"artist,omitempty"`
	Items      []CatalogItem `json:"items,omitempty"`
	Candidates []CatalogItem `json:"candidates,omitempty"`
}

type LyricsEntry struct {
	LyricsID         string `json:"lyrics_id"`
	Source           string `json:"source"`
	SourceRecordID   string `json:"source_record_id"`
	CanonicalURL     string `json:"canonical_url"`
	Language         string `json:"language"`
	RightsStatus     string `json:"rights_status"`
	LicenseReference string `json:"license_reference,omitempty"`
	StorageMode      string `json:"storage_mode"`
	ContentSHA256    string `json:"content_sha256"`
	LyricsText       string `json:"lyrics_text,omitempty"`
	FetchedAt        string `json:"fetched_at,omitempty"`
	UpdatedAt        string `json:"updated_at"`
}

type SyntaxFeature struct {
	FeatureID       string         `json:"feature_id"`
	LyricsSource    string         `json:"lyrics_source"`
	Language        string         `json:"language"`
	Analyzer        string         `json:"analyzer"`
	AnalyzerVersion string         `json:"analyzer_version"`
	FeatureSchema   string         `json:"feature_schema"`
	TokenCount      int            `json:"token_count"`
	LineCount       int            `json:"line_count"`
	VocabularySize  int            `json:"vocabulary_size"`
	Features        map[string]any `json:"features"`
	GeneratedAt     string         `json:"generated_at"`
}

type LyricsResult struct {
	Status      string          `json:"status"`
	Song        CatalogItem     `json:"song"`
	Artist      string          `json:"artist,omitempty"`
	Information string          `json:"information"`
	Lyrics      []LyricsEntry   `json:"lyrics,omitempty"`
	Syntax      []SyntaxFeature `json:"syntax,omitempty"`
	Candidates  []CatalogItem   `json:"candidates,omitempty"`
}

func ValidateIndexedSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("music catalog database is nil")
	}
	for _, object := range []string{
		"hobby_items", "hobby_relations", "hobby_music_lyrics", "hobby_music_syntax_features",
		"idx_hobby_music_items_type_title", "idx_hobby_music_relations_from", "idx_hobby_music_relations_to",
		"idx_hobby_music_lyrics_song_language", "idx_hobby_music_syntax_song_language",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, object).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required music catalog schema object %s is missing", object)
		}
	}
	return nil
}

// EnsureIndexedLookupSchema is the startup-only migration boundary. Runtime
// lookup opens the database read-only after these fixed named indexes exist.
func EnsureIndexedLookupSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("music catalog database is nil")
	}
	if _, err := db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_hobby_music_items_type_title
  ON hobby_items(category, item_type, normalized_title)
  WHERE category = 'music' AND item_type IN ('artist','song');
CREATE INDEX IF NOT EXISTS idx_hobby_music_relations_from
  ON hobby_relations(from_item_id, relation_type, to_item_id);
CREATE INDEX IF NOT EXISTS idx_hobby_music_relations_to
  ON hobby_relations(to_item_id, relation_type, from_item_id);
CREATE INDEX IF NOT EXISTS idx_hobby_music_lyrics_song_language
  ON hobby_music_lyrics(song_item_id, language, source);
CREATE INDEX IF NOT EXISTS idx_hobby_music_syntax_song_language
  ON hobby_music_syntax_features(song_item_id, language, analyzer);`); err != nil {
		return err
	}
	return ValidateIndexedSchema(ctx, db)
}

func LookupCatalog(ctx context.Context, db *sql.DB, req CatalogRequest) (CatalogResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Artist = strings.TrimSpace(req.Artist)
	if req.Kind != "artist" && req.Kind != "song" {
		return CatalogResult{}, fmt.Errorf("kind must be artist or song")
	}
	if req.Name == "" {
		return CatalogResult{}, fmt.Errorf("name is required")
	}
	limit := boundedLimit(req.Limit)
	query := `SELECT i.item_id,i.item_type,i.title,COALESCE(i.subtitle,''),COALESCE(i.canonical_source,''),COALESCE(i.canonical_url,''),COALESCE(i.metadata_json,'{}')
FROM hobby_items i
WHERE i.category='music' AND i.item_type=? AND i.normalized_title=?`
	args := []any{req.Kind, normalizeTitle(req.Name)}
	if req.Kind == "song" && req.Artist != "" {
		query += ` AND EXISTS (
SELECT 1 FROM hobby_relations r JOIN hobby_items a ON a.item_id=r.from_item_id
WHERE r.to_item_id=i.item_id AND r.relation_type='performed' AND a.category='music' AND a.item_type='artist' AND a.normalized_title=?)`
		args = append(args, normalizeTitle(req.Artist))
	}
	query += ` ORDER BY i.title,i.item_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return CatalogResult{}, err
	}
	defer rows.Close()
	items := make([]CatalogItem, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanCatalogItem(rows)
		if scanErr != nil {
			return CatalogResult{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return CatalogResult{}, err
	}
	if err := rows.Close(); err != nil {
		return CatalogResult{}, err
	}
	for index := range items {
		items[index].Relations, err = lookupRelations(ctx, db, items[index].ItemID, limit)
		if err != nil {
			return CatalogResult{}, err
		}
	}
	result := CatalogResult{Status: "ok", Kind: req.Kind, Name: req.Name, Artist: req.Artist, Items: items}
	if len(items) == 0 {
		result.Status = "not_found"
	} else if len(items) > 1 {
		result.Status = "ambiguous"
		result.Candidates = items
		result.Items = nil
	}
	return result, nil
}

func LookupLyrics(ctx context.Context, db *sql.DB, req LyricsRequest) (LyricsResult, error) {
	catalog, err := LookupCatalog(ctx, db, CatalogRequest{Kind: "song", Name: req.Song, Artist: req.Artist, Limit: req.Limit})
	if err != nil {
		return LyricsResult{}, err
	}
	result := LyricsResult{Status: catalog.Status, Artist: strings.TrimSpace(req.Artist), Information: req.Information, Candidates: catalog.Candidates}
	if catalog.Status != "ok" || len(catalog.Items) != 1 {
		return result, nil
	}
	result.Song = catalog.Items[0]
	limit := boundedLimit(req.Limit)
	language := strings.TrimSpace(req.Language)
	switch req.Information {
	case "rights", "full_text":
		query := `SELECT lyrics_id,source,source_record_id,canonical_url,language,rights_status,COALESCE(license_reference,''),storage_mode,content_sha256,COALESCE(fetched_at,''),updated_at`
		if req.Information == "full_text" {
			query += `,COALESCE(lyrics_text,'')`
		}
		query += ` FROM hobby_music_lyrics WHERE song_item_id=?`
		args := []any{result.Song.ItemID}
		if language != "" {
			query += ` AND language=?`
			args = append(args, language)
		}
		if req.Information == "full_text" {
			query += ` AND storage_mode='full_text' AND rights_status IN ('licensed','public_domain','user_owned') AND TRIM(COALESCE(license_reference,''))<>''`
		}
		query += ` ORDER BY language,source LIMIT ?`
		args = append(args, limit)
		rows, queryErr := db.QueryContext(ctx, query, args...)
		if queryErr != nil {
			return LyricsResult{}, queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var entry LyricsEntry
			if req.Information == "full_text" {
				err = rows.Scan(&entry.LyricsID, &entry.Source, &entry.SourceRecordID, &entry.CanonicalURL, &entry.Language, &entry.RightsStatus, &entry.LicenseReference, &entry.StorageMode, &entry.ContentSHA256, &entry.FetchedAt, &entry.UpdatedAt, &entry.LyricsText)
			} else {
				err = rows.Scan(&entry.LyricsID, &entry.Source, &entry.SourceRecordID, &entry.CanonicalURL, &entry.Language, &entry.RightsStatus, &entry.LicenseReference, &entry.StorageMode, &entry.ContentSHA256, &entry.FetchedAt, &entry.UpdatedAt)
			}
			if err != nil {
				return LyricsResult{}, err
			}
			result.Lyrics = append(result.Lyrics, entry)
		}
		if err := rows.Err(); err != nil {
			return LyricsResult{}, err
		}
	case "syntax":
		query := `SELECT feature_id,lyrics_source,language,analyzer,analyzer_version,feature_schema,token_count,line_count,vocabulary_size,features_json,generated_at
FROM hobby_music_syntax_features WHERE song_item_id=? AND non_reconstructable=1`
		args := []any{result.Song.ItemID}
		if language != "" {
			query += ` AND language=?`
			args = append(args, language)
		}
		query += ` ORDER BY language,lyrics_source,analyzer LIMIT ?`
		args = append(args, limit)
		rows, queryErr := db.QueryContext(ctx, query, args...)
		if queryErr != nil {
			return LyricsResult{}, queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var feature SyntaxFeature
			var raw string
			if err := rows.Scan(&feature.FeatureID, &feature.LyricsSource, &feature.Language, &feature.Analyzer, &feature.AnalyzerVersion, &feature.FeatureSchema, &feature.TokenCount, &feature.LineCount, &feature.VocabularySize, &raw, &feature.GeneratedAt); err != nil {
				return LyricsResult{}, err
			}
			_ = json.Unmarshal([]byte(raw), &feature.Features)
			result.Syntax = append(result.Syntax, feature)
		}
		if err := rows.Err(); err != nil {
			return LyricsResult{}, err
		}
	default:
		return LyricsResult{}, fmt.Errorf("information must be rights, syntax, or full_text")
	}
	return result, nil
}

type rowScanner interface{ Scan(...any) error }

func scanCatalogItem(row rowScanner) (CatalogItem, error) {
	var item CatalogItem
	var raw string
	if err := row.Scan(&item.ItemID, &item.Kind, &item.Title, &item.Subtitle, &item.CanonicalSource, &item.CanonicalURL, &raw); err != nil {
		return CatalogItem{}, err
	}
	_ = json.Unmarshal([]byte(raw), &item.Metadata)
	return item, nil
}

func lookupRelations(ctx context.Context, db *sql.DB, itemID string, limit int) ([]Relation, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.relation_type,o.item_id,o.item_type,o.title,r.source,COALESCE(r.evidence_url,'')
FROM hobby_relations r JOIN hobby_items o ON o.item_id=CASE WHEN r.from_item_id=? THEN r.to_item_id ELSE r.from_item_id END
WHERE r.from_item_id=? OR r.to_item_id=? ORDER BY r.relation_type,o.title LIMIT ?`, itemID, itemID, itemID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var relations []Relation
	for rows.Next() {
		var relation Relation
		if err := rows.Scan(&relation.Type, &relation.ItemID, &relation.Kind, &relation.Title, &relation.Source, &relation.EvidenceURL); err != nil {
			return nil, err
		}
		relations = append(relations, relation)
	}
	return relations, rows.Err()
}

func boundedLimit(limit int) int {
	if limit < 1 {
		return defaultLimit
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func normalizeTitle(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
