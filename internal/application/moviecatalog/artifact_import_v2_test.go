package moviecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportArtifactVerifiesReceiptBeforeTransactionalImport(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-receipt?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	artifact := strings.Join([]string{
		`{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"receipt-1","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Receipt Movie","root_url":"https://eiga.com/movie/1/","validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/1/"]}`,
		`{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Receipt Movie","url":"https://eiga.com/movie/1/","depth":0,"validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "artifact.jsonl")
	if err := os.WriteFile(path, []byte(artifact), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	sum := sha256.Sum256([]byte(artifact))
	good := ArtifactReceipt{Path: path, SourceURL: "https://eiga.com/movie/1/", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(artifact))}
	if _, err := ImportArtifact(context.Background(), db, good); err != nil {
		t.Fatalf("valid receipt import: %v", err)
	}
	var roots int
	if err := db.QueryRow("SELECT COUNT(*) FROM movie_catalog_roots").Scan(&roots); err != nil {
		t.Fatalf("count imported roots: %v", err)
	}
	if roots != 1 {
		t.Fatalf("successful receipt import roots=%d", roots)
	}

	badDB, err := sql.Open("sqlite", "file:artifact-receipt-bad?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open bad sqlite: %v", err)
	}
	defer badDB.Close()
	bad := good
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := ImportArtifact(context.Background(), badDB, bad); err == nil {
		t.Fatal("hash mismatch must reject before import")
	}
	if tableExists(badDB, "movies") {
		t.Fatal("hash mismatch must not initialize or mutate import tables")
	}
	bad = good
	bad.Bytes++
	if _, err := ImportArtifact(context.Background(), badDB, bad); err == nil {
		t.Fatal("size mismatch must reject before import")
	}
}

func TestImportJSONLToolsSchemaTwoPreservesPartialUnresolvedCredits(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-tools-v2?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	artifact := strings.NewReader(`{"schema_version":"2","record_type":"manifest","artifact_kind":"movie_catalog","status":"complete","max_entity_depth":1,"root":{"kind":"movie","id":"1","label":"Tools Movie","url":"https://eiga.com/movie/1/"},"provenance":{"resolver":"tools","index_url":"https://example.test/index"},"counts":{"nodes":4,"edges":3,"depth0":1,"depth1":3}}
{"schema_version":"2","record_type":"node","node_id":"movie:1","kind":"movie","entity_id":"1","entity_depth":0,"is_d0":true,"title":"Tools Movie","url":"https://eiga.com/movie/1/"}
{"schema_version":"2","record_type":"node","node_id":"person:2","kind":"person","entity_id":"2","entity_depth":1,"name":"Tools Person","url":"https://eiga.com/person/2/"}
{"schema_version":"2","record_type":"node","node_id":"music:3","kind":"music","entity_id":"Theme text","entity_depth":1,"title":"Theme text","url":"https://example.test/music/theme"}
{"schema_version":"2","record_type":"node","node_id":"credit:4","kind":"unresolved_credit","entity_id":"Original string","entity_depth":1,"title":"Original string"}
{"schema_version":"2","record_type":"edge","from":"movie:1","to":"person:2","relation":"cast","entity_depth":1,"source_page_url":"https://eiga.com/movie/1/"}
{"schema_version":"2","record_type":"edge","from":"movie:1","to":"music:3","relation":"music","entity_depth":1,"source_page_url":"https://eiga.com/movie/1/"}
{"schema_version":"2","record_type":"edge","from":"movie:1","to":"credit:4","relation":"source_work","entity_depth":1,"source_page_url":"https://eiga.com/movie/1/"}`)
	result, err := ImportJSONL(context.Background(), db, artifact, "")
	if err != nil {
		t.Fatalf("Tools v2 import: %v", err)
	}
	if result.Movies != 1 || result.People != 1 || result.Edges != 3 || result.Records != 8 {
		t.Fatalf("unexpected Tools v2 result: %+v", result)
	}
	var unresolvedID sql.NullString
	if err := db.QueryRow(`SELECT target_id FROM movie_related_credits WHERE target_kind='unresolved_credit'`).Scan(&unresolvedID); err != nil {
		t.Fatalf("read unresolved credit: %v", err)
	}
	if unresolvedID.Valid || unresolvedID.String != "" {
		t.Fatalf("unresolved credit target_id must remain empty: %+v", unresolvedID)
	}
	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards after Tools import: %v", err)
	}
	var foundMusic, foundUnresolved bool
	for _, card := range cards {
		if card.Kind == "music" && card.TargetLabel == "Theme text" {
			foundMusic = card.Depth == 1 && card.ValidationState == "partial" && card.TargetID == ""
		}
		if card.Kind == "unresolved_credit" && card.TargetLabel == "Original string" {
			foundUnresolved = card.Depth == 1 && card.ValidationState == "unresolved" && card.TargetID == ""
		}
	}
	if !foundMusic || !foundUnresolved {
		t.Fatalf("Tools partial/unresolved cards missing: %+v", cards)
	}
}

func TestImportJSONLCanonicalToolsWriterShape(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-tools-canonical?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	artifact := strings.NewReader(`{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest:movie:1","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Canonical Tools Movie","root_url":"https://eiga.com/movie/1/","source_url":"https://eiga.com/movie/1/","validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/1/"],"node_count":2,"edge_count":1}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Canonical Tools Movie","url":"https://eiga.com/movie/1/","depth":0,"validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"credit:1","node_kind":"unresolved_credit","label":"Unresolved credit","url":"","depth":1,"validation_state":"unresolved","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge:credit:1","from_node_id":"movie:1","to_node_id":"credit:1","from_kind":"movie","to_kind":"unresolved_credit","relation_type":"staff_credit","source":"eiga","validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}`)
	result, err := ImportJSONL(context.Background(), db, artifact, "")
	if err != nil {
		t.Fatalf("canonical Tools writer import: %v", err)
	}
	if result.Movies != 1 || result.Edges != 1 || result.Records != 4 {
		t.Fatalf("unexpected canonical Tools writer result: %+v", result)
	}
	var state, label string
	if err := db.QueryRow(`SELECT validation_state,target_label FROM movie_related_credits WHERE target_kind='unresolved_credit'`).Scan(&state, &label); err != nil {
		t.Fatalf("read canonical unresolved credit: %v", err)
	}
	if state != "unresolved" || label != "Unresolved credit" {
		t.Fatalf("canonical unresolved credit state=%q label=%q", state, label)
	}
}

func TestImportJSONLAcceptsPartialPersonWithoutIdentityAndProjectsEdge(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-partial-person?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	artifact := strings.NewReader(`{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest:partial-person","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Root Movie","root_url":"https://eiga.com/movie/1/","validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/1/"],"node_count":2,"edge_count":1}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Root Movie","url":"https://eiga.com/movie/1/","depth":0,"validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"person:partial","node_kind":"person","target_id":"","label":"Partial Person","url":"","depth":1,"validation_state":"partial","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge:partial-person","from_node_id":"movie:1","to_node_id":"person:partial","from_kind":"movie","to_kind":"person","relation_type":"cast","source":"eiga","validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}`)
	result, err := ImportJSONL(context.Background(), db, artifact, "")
	if err != nil {
		t.Fatalf("partial person import: %v", err)
	}
	if result.Movies != 1 || result.People != 0 || result.Edges != 1 {
		t.Fatalf("partial person must not fabricate people identity: %+v", result)
	}
	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards after partial person import: %v", err)
	}
	var found bool
	for _, card := range cards {
		if card.Kind == "person" && card.TargetID == "" && card.TargetLabel == "Partial Person" {
			found = card.Depth == 1 && card.TargetURL == "" && card.ValidationState == "partial"
		}
	}
	if !found {
		t.Fatalf("partial person D1 card missing: %+v", cards)
	}
}

func TestImportJSONLV2StoresManifestRootAndDirectCredits(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-v2-import?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest-1","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Root Movie","root_url":"https://eiga.com/movie/1/","source_url":"https://eiga.com/movie/1/","validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Root Movie","url":"https://eiga.com/movie/1/","depth":0,"validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"person:2","node_kind":"person","target_id":"2","label":"Person","url":"https://eiga.com/person/2/","depth":1,"validation_state":"validated","provenance_urls":["https://eiga.com/person/2/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"music:3","node_kind":"music","target_id":"music-3","label":"Theme Song","url":"https://example.test/music/3","depth":1,"validation_state":"validated","provenance_urls":["https://example.test/music/3"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge-person","from_node_id":"movie:1","to_node_id":"person:2","from_kind":"movie","to_kind":"person","relation_type":"music","source":"eiga","validation_state":"validated","provenance_urls":["https://eiga.com/movie/1/"]}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge-music","from_node_id":"movie:1","to_node_id":"music:3","from_kind":"movie","to_kind":"music","relation_type":"theme_song","source":"eiga","validation_state":"validated","provenance_urls":["https://example.test/music/3"]}
`)
	result, err := ImportJSONL(context.Background(), db, artifact, "")
	if err != nil {
		t.Fatalf("v2 import: %v", err)
	}
	if result.Movies != 1 || result.People != 1 || result.Edges != 2 || result.Records != 6 {
		t.Fatalf("unexpected v2 import result: %+v", result)
	}
	var movies, people, personEdges, credits, roots int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM movies":                &movies,
		"SELECT COUNT(*) FROM people":                &people,
		"SELECT COUNT(*) FROM movie_people":          &personEdges,
		"SELECT COUNT(*) FROM movie_related_credits": &credits,
		"SELECT COUNT(*) FROM movie_catalog_roots":   &roots,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
	}
	if movies != 1 || people != 1 || personEdges != 1 || credits != 1 || roots != 1 {
		t.Fatalf("unexpected v2 counts movies=%d people=%d person_edges=%d credits=%d roots=%d", movies, people, personEdges, credits, roots)
	}
	var rootID, rootKind, targetID, rootURL string
	if err := db.QueryRow(`SELECT root_id, kind, target_id, target_url FROM movie_catalog_roots`).Scan(&rootID, &rootKind, &targetID, &rootURL); err != nil {
		t.Fatalf("read root: %v", err)
	}
	if rootID != "manifest-1" || rootKind != "movie" || targetID != "1" || rootURL != "https://eiga.com/movie/1/" {
		t.Fatalf("unexpected root: id=%q kind=%q target=%q url=%q", rootID, rootKind, targetID, rootURL)
	}
	var creditKind, creditID, creditRelation string
	if err := db.QueryRow(`SELECT target_kind, target_id, relation_type FROM movie_related_credits`).Scan(&creditKind, &creditID, &creditRelation); err != nil {
		t.Fatalf("read credit: %v", err)
	}
	if creditKind != "music" || creditID != "music-3" || creditRelation != "theme_song" {
		t.Fatalf("unexpected credit: kind=%q id=%q relation=%q", creditKind, creditID, creditRelation)
	}
}

func TestImportJSONLV2RejectsDuplicateNodesAndRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-v2-duplicate?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest-duplicate","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Root","root_url":"https://eiga.com/movie/1/"}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Root","url":"https://eiga.com/movie/1/","depth":0}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1-copy","node_kind":"movie","target_id":"1","label":"Duplicate","url":"https://eiga.com/movie/1/","depth":0}
`)
	if _, err := ImportJSONL(context.Background(), db, artifact, ""); err == nil {
		t.Fatal("expected duplicate node error")
	}
	for _, query := range []string{"SELECT COUNT(*) FROM movies", "SELECT COUNT(*) FROM movie_catalog_roots"} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		if count != 0 {
			t.Fatalf("failed v2 import must rollback %q, count=%d", query, count)
		}
	}
}

func TestImportJSONLV2RejectsD1OutboundEdgeAndKindURLMismatch(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
	}{
		{
			name: "d1 outbound",
			artifact: `{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest-d1","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Root","root_url":"https://eiga.com/movie/1/"}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Root","url":"https://eiga.com/movie/1/","depth":0}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"person:2","node_kind":"person","target_id":"2","label":"Person","url":"https://eiga.com/person/2/","depth":1}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:3","node_kind":"movie","target_id":"3","label":"Nested","url":"https://eiga.com/movie/3/","depth":1}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge-root","from_node_id":"movie:1","to_node_id":"person:2","relation_type":"cast","source":"eiga"}
{"schema_version":"rencrow.movie_catalog.v2","kind":"edge","edge_id":"edge-nested","from_node_id":"person:2","to_node_id":"movie:3","relation_type":"filmography","source":"eiga"}`,
		},
		{
			name: "kind url mismatch",
			artifact: `{"schema_version":"rencrow.movie_catalog.v2","kind":"manifest","manifest_id":"manifest-kind","root_node_id":"movie:1","root_kind":"movie","root_id":"1","root_label":"Root","root_url":"https://eiga.com/movie/1/"}
{"schema_version":"rencrow.movie_catalog.v2","kind":"node","node_id":"movie:1","node_kind":"movie","target_id":"1","label":"Root","url":"https://eiga.com/person/1/","depth":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:artifact-v2-invalid-"+strings.ReplaceAll(tt.name, " ", "-")+"?mode=memory&cache=shared&_time_format=sqlite")
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			defer db.Close()
			if _, err := ImportJSONL(context.Background(), db, strings.NewReader(tt.artifact), ""); err == nil {
				t.Fatal("expected v2 invariant error")
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM movies").Scan(&count); err != nil {
				t.Fatalf("count movies: %v", err)
			}
			if count != 0 {
				t.Fatalf("failed import must rollback, movies=%d", count)
			}
		})
	}
}

func TestImportJSONLV2CanonicalRecordTypeDerivesDepthAndIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:artifact-v2-canonical?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifactText := `{"record_type":"manifest","schema_version":"movie-catalog-graph/v2","artifact_id":"artifact-canonical","input":{"kind":"movie","query":"Root Movie","seed_url":""},"root_node_ids":["movie:101"],"node_count":2,"edge_count":1,"validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/101/"]}
{"record_type":"node","schema_version":"movie-catalog-graph/v2","node_id":"movie:101","kind":"movie","label":"Root Movie","url":"https://eiga.com/movie/101/","validation_state":"validated","provenance_urls":["https://eiga.com/movie/101/"]}
{"record_type":"node","schema_version":"movie-catalog-graph/v2","node_id":"person:7","kind":"person","label":"Person","url":"https://eiga.com/person/7/","validation_state":"validated","provenance_urls":["https://eiga.com/movie/101/"]}
{"record_type":"edge","schema_version":"movie-catalog-graph/v2","edge_id":"movie:101-person:7-director","from_node_id":"movie:101","to_node_id":"person:7","relation_type":"director","validation_state":"validated","provenance_urls":["https://eiga.com/movie/101/"]}`
	first, err := ImportJSONL(context.Background(), db, strings.NewReader(artifactText), "")
	if err != nil {
		t.Fatalf("canonical import: %v", err)
	}
	if first.Movies != 1 || first.People != 1 || first.Edges != 1 || first.Records != 4 {
		t.Fatalf("unexpected canonical import result: %+v", first)
	}
	second, err := ImportJSONL(context.Background(), db, strings.NewReader(artifactText), "")
	if err != nil {
		t.Fatalf("canonical reimport: %v", err)
	}
	if second != first {
		t.Fatalf("reimport result changed: first=%+v second=%+v", first, second)
	}
	var movies, people, edges, roots int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM movies":              &movies,
		"SELECT COUNT(*) FROM people":              &people,
		"SELECT COUNT(*) FROM movie_people":        &edges,
		"SELECT COUNT(*) FROM movie_catalog_roots": &roots,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
	}
	if movies != 1 || people != 1 || edges != 1 || roots != 1 {
		t.Fatalf("reimport must be idempotent: movies=%d people=%d edges=%d roots=%d", movies, people, edges, roots)
	}
	for _, table := range []string{"movie_catalog_roots", "movie_related_credits"} {
		columns, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		for columns.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt interface{}
			if err := columns.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				columns.Close()
				t.Fatal(err)
			}
			if name == "depth" {
				columns.Close()
				t.Fatalf("derived depth must not be materialized in %s", table)
			}
		}
		if err := columns.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
