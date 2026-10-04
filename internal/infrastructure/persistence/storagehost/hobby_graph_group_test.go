package storagehost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	musiccatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	_ "modernc.org/sqlite"
)

const hobbyGraphTestToken = "hobby-graph-test-token"

type hobbyGraphRemoteFixture struct {
	root    string
	db      *sql.DB
	owner   *musiccatalog.StorageHostSQLiteStore
	handler *Handler
	server  *httptest.Server
	client  *Client
}

func openHobbyGraphRemoteFixture(t *testing.T, root string, wrap func(*musiccatalog.StorageHostSQLiteStore) HobbyGraphGroupOwner) *hobbyGraphRemoteFixture {
	t.Helper()
	dbPath := filepath.Join(root, "hobby-graph.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(hobbyGraphTestSchema); err != nil {
		_ = db.Close()
		t.Fatalf("seed hobby graph database: %v", err)
	}
	owner, err := musiccatalog.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open hobby graph owner: %v", err)
	}
	handler, err := NewHandler(HandlerConfig{Token: hobbyGraphTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = db.Close()
		t.Fatalf("open hobby graph handler: %v", err)
	}
	groupOwner := HobbyGraphGroupOwner(owner)
	if wrap != nil {
		groupOwner = wrap(owner)
	}
	if err := RegisterHobbyGraphGroup(handler, groupOwner); err != nil {
		_ = handler.Close()
		_ = db.Close()
		t.Fatalf("register hobby graph group: %v", err)
	}
	server := httptest.NewServer(handler)
	client := newHobbyGraphClient(t, server)
	fixture := &hobbyGraphRemoteFixture{root: root, db: db, owner: owner, handler: handler, server: server, client: client}
	t.Cleanup(fixture.close)
	return fixture
}

func newHobbyGraphClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: hobbyGraphTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("hobby graph client handshake: %v", err)
	}
	return client
}

func (f *hobbyGraphRemoteFixture) close() {
	if f == nil {
		return
	}
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.handler != nil {
		_ = f.handler.Close()
		f.handler = nil
	}
	if f.db != nil {
		_ = f.db.Close()
		f.db = nil
	}
}

const hobbyGraphTestSchema = `
CREATE TABLE IF NOT EXISTS hobby_items(
 item_id TEXT PRIMARY KEY,category TEXT,item_type TEXT,title TEXT,normalized_title TEXT,
 subtitle TEXT,canonical_source TEXT,canonical_url TEXT,metadata_json TEXT,created_at TEXT,updated_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_relations(
 relation_id TEXT PRIMARY KEY,from_item_id TEXT,to_item_id TEXT,relation_type TEXT,source TEXT,evidence_url TEXT,created_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_music_lyrics(
 lyrics_id TEXT PRIMARY KEY,song_item_id TEXT,source TEXT,source_record_id TEXT,canonical_url TEXT,language TEXT,
 rights_status TEXT,license_reference TEXT,storage_mode TEXT,lyrics_text TEXT,content_sha256 TEXT,fetched_at TEXT,updated_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_music_syntax_features(
 feature_id TEXT PRIMARY KEY,song_item_id TEXT,lyrics_source TEXT,language TEXT,analyzer TEXT,analyzer_version TEXT,
 feature_schema TEXT,token_count INTEGER,line_count INTEGER,vocabulary_size INTEGER,features_json TEXT,
 source_content_sha256 TEXT,non_reconstructable INTEGER,generated_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_interactions(
 interaction_id TEXT PRIMARY KEY,item_id TEXT,original_title TEXT,category TEXT,interaction_type TEXT,source TEXT,created_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_title_observations(observation_id TEXT PRIMARY KEY,item_id TEXT,title TEXT);
CREATE TABLE IF NOT EXISTS hobby_preference_signals(signal_id TEXT PRIMARY KEY,item_id TEXT,signal_type TEXT);
CREATE TABLE IF NOT EXISTS hobby_topic_candidates(
 candidate_id TEXT PRIMARY KEY,category TEXT,topic_type TEXT,target_item_id TEXT,title TEXT,reason TEXT,status TEXT,generated_by TEXT,generated_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_collection_runs(run_id TEXT PRIMARY KEY,status TEXT);
CREATE TABLE IF NOT EXISTS hobby_collection_targets(target_id TEXT PRIMARY KEY,run_id TEXT,target TEXT);
CREATE TABLE IF NOT EXISTS hobby_music_collection_receipts(receipt_id TEXT PRIMARY KEY,run_id TEXT,status TEXT);
INSERT OR IGNORE INTO hobby_items VALUES
 ('a1','music','artist','Artist One','artist one','','fixture','https://example.test/a1','{"kind":"artist"}','2026-10-01T00:00:00Z','2026-10-02T00:00:00Z'),
 ('a2','music','artist','Artist Two','artist two','','fixture','https://example.test/a2','{"kind":"artist"}','2026-10-01T00:00:00Z','2026-10-02T00:00:00Z'),
 ('s1','music','song','Silver Song','silver song','First edition','fixture','https://example.test/s1','{"edition":1}','2026-10-01T00:00:00Z','2026-10-03T00:00:00Z'),
 ('s2','music','song','Silver Song','silver song','Second edition','fixture','https://example.test/s2','{"edition":2}','2026-10-01T00:00:00Z','2026-10-02T00:00:00Z');
INSERT OR IGNORE INTO hobby_relations VALUES
 ('r1','a1','s1','performed','fixture','https://example.test/r1','2026-10-01T00:00:00Z'),
 ('r2','a2','s2','performed','fixture','https://example.test/r2','2026-10-02T00:00:00Z');
INSERT OR IGNORE INTO hobby_music_lyrics VALUES
 ('ly-licensed','s1','licensed','rec-licensed','https://example.test/ly1','ja','licensed','license:one','full_text','licensed verse','sha-1','2026-10-01T00:00:00Z','2026-10-02T00:00:00Z'),
 ('ly-reference','s1','reference','rec-reference','https://example.test/ly2','ja','unknown','','hash_only',NULL,'sha-2','2026-10-01T00:00:00Z','2026-10-03T00:00:00Z');
INSERT OR IGNORE INTO hobby_music_syntax_features VALUES
 ('syntax-1','s1','licensed','ja','fixture-analyzer','1','music.syntax.v1',12,3,9,'{"unique_ratio":0.75}','sha-1',1,'2026-10-03T00:00:00Z');
INSERT OR IGNORE INTO hobby_interactions VALUES ('interaction-1','s1','Silver Song','music','played','fixture','2026-10-03T00:00:00Z');
INSERT OR IGNORE INTO hobby_title_observations VALUES ('observation-1','s1','Silver Song');
INSERT OR IGNORE INTO hobby_preference_signals VALUES ('signal-1','s1','like');
INSERT OR IGNORE INTO hobby_topic_candidates VALUES ('topic-1','music','song','s1','Silver Song topic','fixture reason','candidate','fixture','2026-10-03T00:00:00Z');
INSERT OR IGNORE INTO hobby_collection_runs VALUES ('run-1','complete');
INSERT OR IGNORE INTO hobby_collection_targets VALUES ('target-1','run-1','fixture target');
INSERT OR IGNORE INTO hobby_music_collection_receipts VALUES ('music-receipt-1','run-1','complete');`

func TestHobbyGraphGroupRegistersExactClosedContract(t *testing.T) {
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), nil)
	contract, err := f.client.Contract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"catalog_lookup": false, "lyrics_lookup": false,
		"viewer_stats": false, "viewer_overview": false,
		"candidate_by_id": false, "candidate_by_request": false,
		"candidate_save": true,
	}
	got := map[string]bool{}
	for _, spec := range contract.Operations {
		if spec.Group == GroupHobbyGraph {
			got[spec.Op] = spec.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("hobby graph operations=%v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if actual, ok := got[operation]; !ok || actual != mutating {
			t.Fatalf("hobby graph operation %q mutating=%v present=%v, want %v", operation, actual, ok, mutating)
		}
	}
}

func TestHobbyGraphRejectsArbitraryQueriesAndRecallLyricsRoute(t *testing.T) {
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), nil)
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		group   string
		op      string
		payload any
		want    string
	}{
		{name: "sql-shaped catalog field", group: GroupHobbyGraph, op: "catalog_lookup", payload: map[string]any{"kind": "song", "name": "Silver Song", "sql": "SELECT * FROM hobby_items"}, want: ErrorCodeSchemaRejected},
		{name: "out-of-bound catalog limit", group: GroupHobbyGraph, op: "catalog_lookup", payload: map[string]any{"kind": "song", "name": "Silver Song", "limit": hobbyGraphMaxPage + 1}, want: ErrorCodeSchemaRejected},
		{name: "lyrics recall selector", group: GroupHobbyGraph, op: "lyrics_lookup", payload: map[string]any{"song": "Silver Song", "artist": "Artist One", "information": "data.recall"}, want: ErrorCodeSchemaRejected},
		{name: "generic recall operation", group: GroupHobbyGraph, op: "data.recall", payload: map[string]any{"query": "lyrics"}, want: ErrorCodeOperationUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result json.RawMessage
			err := f.client.Call(ctx, test.group, test.op, test.payload, &result)
			if actual := hobbyGraphErrorCode(err); actual != test.want {
				t.Fatalf("error=%v code=%q, want %q", err, actual, test.want)
			}
		})
	}
}

func TestHobbyGraphClientPreservesTypedCatalogLyricsAndViewerDTOs(t *testing.T) {
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), nil)
	ctx := context.Background()
	client := NewHobbyGraphClient(f.client)

	catalog, err := client.LookupCatalog(ctx, musiccatalog.CatalogRequest{Kind: "song", Name: "Silver Song"})
	if err != nil || catalog.Status != "ambiguous" || len(catalog.Candidates) != 2 || len(catalog.Items) != 0 {
		t.Fatalf("typed catalog result=%#v err=%v", catalog, err)
	}
	var foundS1 bool
	for _, item := range catalog.Candidates {
		if item.ItemID == "s1" {
			foundS1 = item.Kind == "song" && item.Title == "Silver Song" && item.Subtitle == "First edition" && item.Metadata["edition"] == float64(1) && len(item.Relations) == 1 && item.Relations[0].ItemID == "a1" && item.Relations[0].EvidenceURL == "https://example.test/r1"
		}
	}
	if !foundS1 {
		t.Fatalf("concrete CatalogItem/relation DTO was not preserved: %#v", catalog.Candidates)
	}

	rights, err := client.LookupLyrics(ctx, musiccatalog.LyricsRequest{Song: "Silver Song", Artist: "Artist One", Language: "ja", Information: "rights"})
	if err != nil || rights.Status != "ok" || rights.Song.ItemID != "s1" || len(rights.Lyrics) != 2 || len(rights.Syntax) != 0 {
		t.Fatalf("typed rights result=%#v err=%v", rights, err)
	}
	for _, entry := range rights.Lyrics {
		if entry.LyricsText != "" {
			t.Fatalf("rights-only route leaked lyric text: %#v", entry)
		}
	}
	fullText, err := client.LookupLyrics(ctx, musiccatalog.LyricsRequest{Song: "Silver Song", Artist: "Artist One", Language: "ja", Information: "full_text"})
	if err != nil || len(fullText.Lyrics) != 1 || fullText.Lyrics[0].LyricsText != "licensed verse" || fullText.Lyrics[0].RightsStatus != "licensed" || fullText.Lyrics[0].LicenseReference != "license:one" {
		t.Fatalf("typed licensed full-text result=%#v err=%v", fullText, err)
	}
	syntax, err := client.LookupLyrics(ctx, musiccatalog.LyricsRequest{Song: "Silver Song", Artist: "Artist One", Language: "ja", Information: "syntax"})
	if err != nil || len(syntax.Syntax) != 1 || syntax.Syntax[0].Analyzer != "fixture-analyzer" || syntax.Syntax[0].TokenCount != 12 || syntax.Syntax[0].Features["unique_ratio"] != float64(0.75) || len(syntax.Lyrics) != 0 {
		t.Fatalf("typed syntax result=%#v err=%v", syntax, err)
	}

	stats, err := client.ViewerStats(ctx)
	if err != nil || len(stats) != len(hobbyGraphStatsKeys) || stats["hobby_items"] != 4 || stats["hobby_music_lyrics"] != 2 || stats["hobby_music_syntax_features"] != 1 {
		t.Fatalf("typed Viewer stats=%#v err=%v", stats, err)
	}
	overview, err := client.ViewerOverview(ctx, 5)
	if err != nil || len(overview.Stats) != len(hobbyGraphStatsKeys) || len(overview.Items) != 4 || len(overview.Relations) != 2 || len(overview.Interactions) != 1 || len(overview.TopicCandidates) != 1 {
		t.Fatalf("typed Viewer overview=%#v err=%v", overview, err)
	}
	if overview.Items[0].ItemID != "s1" || overview.Relations[0].RelationID == "" || overview.Interactions[0].InteractionType != "played" || overview.TopicCandidates[0].CandidateID != "topic-1" {
		t.Fatalf("Viewer overview projections lost their concrete fields: %#v", overview)
	}
}

func TestHobbyGraphCandidateReadIsUserScopedAndTyped(t *testing.T) {
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), nil)
	client := NewHobbyGraphClient(f.client)
	candidate := hobbyGraphCandidateForTest("private-read")
	f.client.opID = "hobby-private-read-op"
	result, err := client.SavePreferenceCandidate(context.Background(), candidate)
	if err != nil || result.Candidate != candidate || result.Replay {
		t.Fatalf("candidate write=%#v err=%v", result, err)
	}
	byID, found, err := client.FindPreferenceCandidateByID(context.Background(), candidate.UserID, candidate.CandidateID)
	if err != nil || !found || byID != candidate {
		t.Fatalf("candidate by id=%#v found=%v err=%v", byID, found, err)
	}
	byRequest, found, err := client.FindPreferenceCandidateByRequestID(context.Background(), candidate.UserID, candidate.RequestID)
	if err != nil || !found || byRequest != candidate {
		t.Fatalf("candidate by request=%#v found=%v err=%v", byRequest, found, err)
	}
	otherUser, found, err := client.FindPreferenceCandidateByID(context.Background(), "user-b", candidate.CandidateID)
	if err != nil || found || otherUser != (musiccatalog.StorageHostPreferenceCandidate{}) {
		t.Fatalf("private candidate crossed user scope: %#v found=%v err=%v", otherUser, found, err)
	}
}

func TestHobbyGraphCandidateReceiptSurvivesOwnerAndHandlerReopen(t *testing.T) {
	root := t.TempDir()
	first := openHobbyGraphRemoteFixture(t, root, nil)
	candidate := hobbyGraphCandidateForTest("commit-gap")
	const opID = "hobby-commit-gap"
	first.client.opID = opID
	initialGeneration := first.handler.Generation()
	first.handler.crashAfterCommitFor = opID
	if _, err := NewHobbyGraphClient(first.client).SavePreferenceCandidate(context.Background(), candidate); hobbyGraphErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit response loss error=%v, want outcome_unknown", err)
	}
	assertHobbyGraphCandidateCount(t, first.db, candidate.RequestID, 1)
	first.close()

	second := openHobbyGraphRemoteFixture(t, root, nil)
	defer second.close()
	if second.handler.Generation() <= initialGeneration {
		t.Fatalf("reopened handler generation=%d, initial=%d", second.handler.Generation(), initialGeneration)
	}
	second.client.opID = opID
	result, err := NewHobbyGraphClient(second.client).SavePreferenceCandidate(context.Background(), candidate)
	if err != nil || result.Candidate != candidate || result.Replay {
		t.Fatalf("recovered exact candidate result=%#v err=%v", result, err)
	}
	assertHobbyGraphCandidateCount(t, second.db, candidate.RequestID, 1)
	var storedGeneration int64
	if err := second.db.QueryRow(`SELECT writer_generation FROM hobby_graph_storagehost_receipt WHERE op_id=?`, opID).Scan(&storedGeneration); err != nil || storedGeneration != initialGeneration {
		t.Fatalf("receipt generation=%d err=%v, want first journal generation %d", storedGeneration, err, initialGeneration)
	}
}

func TestHobbyGraphCandidateSameOperationDifferentPayloadConflicts(t *testing.T) {
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), nil)
	candidate := hobbyGraphCandidateForTest("payload-conflict")
	const opID = "hobby-payload-conflict"
	f.client.opID = opID
	client := NewHobbyGraphClient(f.client)
	if _, err := client.SavePreferenceCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	f.client = newHobbyGraphClient(t, f.server)
	f.client.opID = opID
	changed := candidate
	changed.Note = "different payload under same operation id"
	if _, err := NewHobbyGraphClient(f.client).SavePreferenceCandidate(context.Background(), changed); hobbyGraphErrorCode(err) != ErrorCodeDuplicateConflict {
		t.Fatalf("same operation id/different payload error=%v, want duplicate_conflict", err)
	}
	assertHobbyGraphCandidateCount(t, f.db, candidate.RequestID, 1)
}

func TestHobbyGraphSubstitutedReplayReceiptStaysUnknown(t *testing.T) {
	root := t.TempDir()
	first := openHobbyGraphRemoteFixture(t, root, nil)
	candidate := hobbyGraphCandidateForTest("receipt-substitution")
	first.client.opID = "hobby-first-create"
	if _, err := NewHobbyGraphClient(first.client).SavePreferenceCandidate(context.Background(), candidate); err != nil {
		t.Fatalf("first candidate write: %v", err)
	}
	const replayOpID = "hobby-second-replay"
	first.client.opID = replayOpID
	first.handler.crashAfterCommitFor = replayOpID
	if _, err := NewHobbyGraphClient(first.client).SavePreferenceCandidate(context.Background(), candidate); hobbyGraphErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("replay response loss error=%v, want outcome_unknown", err)
	}
	first.close()

	db, err := sql.Open("sqlite", filepath.Join(root, "hobby-graph.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	var payloadHash string
	var generation int64
	if err := db.QueryRow(`SELECT payload_hash,writer_generation FROM hobby_graph_storagehost_receipt WHERE op_id=?`, replayOpID).Scan(&payloadHash, &generation); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	// Substitute the first operation's Replay=false result and recompute both
	// unkeyed digests. The immutable candidate-row op marker must still reject it.
	resultJSON, err := json.Marshal(musiccatalog.StorageHostCandidateResult{Candidate: candidate, Replay: false})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	resultDigest := sha256.Sum256(resultJSON)
	resultHash := hex.EncodeToString(resultDigest[:])
	receiptBody, err := json.Marshal(struct {
		OpID             string `json:"op_id"`
		Operation        string `json:"operation"`
		PayloadHash      string `json:"payload_hash"`
		WriterGeneration int64  `json:"writer_generation"`
		ResultSHA256     string `json:"result_sha256"`
	}{replayOpID, "candidate_save", payloadHash, generation, resultHash})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBody)
	receiptHash := hex.EncodeToString(receiptDigest[:])
	if _, err := db.Exec(`UPDATE hobby_graph_storagehost_receipt SET result_json=?,result_sha256=?,receipt_sha256=? WHERE op_id=?`, resultJSON, resultHash, receiptHash, replayOpID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	second := openHobbyGraphRemoteFixture(t, root, nil)
	defer second.close()
	second.client.opID = replayOpID
	if _, err := NewHobbyGraphClient(second.client).SavePreferenceCandidate(context.Background(), candidate); hobbyGraphErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("substituted replay result error=%v, want outcome_unknown", err)
	}
	assertHobbyGraphCandidateCount(t, second.db, candidate.RequestID, 1)
}

type hobbyGraphErrorAfterCommitOwner struct {
	HobbyGraphGroupOwner
	saveCalls int
}

func (o *hobbyGraphErrorAfterCommitOwner) SavePreferenceCandidate(ctx context.Context, identity musiccatalog.StorageHostOperationIdentity, candidate musiccatalog.StorageHostPreferenceCandidate) (musiccatalog.StorageHostCandidateResult, error) {
	o.saveCalls++
	result, err := o.HobbyGraphGroupOwner.SavePreferenceCandidate(ctx, identity, candidate)
	if err != nil {
		return result, err
	}
	if o.saveCalls == 1 {
		return musiccatalog.StorageHostCandidateResult{}, errors.New("opaque owner failure after commit")
	}
	return result, nil
}

func TestHobbyGraphArbitraryOwnerErrorAfterCommitRecoversWithoutRerun(t *testing.T) {
	var wrapped *hobbyGraphErrorAfterCommitOwner
	f := openHobbyGraphRemoteFixture(t, t.TempDir(), func(owner *musiccatalog.StorageHostSQLiteStore) HobbyGraphGroupOwner {
		wrapped = &hobbyGraphErrorAfterCommitOwner{HobbyGraphGroupOwner: owner}
		return wrapped
	})
	candidate := hobbyGraphCandidateForTest("owner-error-after-commit")
	const opID = "hobby-owner-error-after-commit"
	f.client.opID = opID
	client := NewHobbyGraphClient(f.client)
	if _, err := client.SavePreferenceCandidate(context.Background(), candidate); hobbyGraphErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("owner error after commit=%v, want outcome_unknown", err)
	}
	if wrapped.saveCalls != 1 {
		t.Fatalf("first attempt owner save calls=%d, want 1", wrapped.saveCalls)
	}
	result, err := client.SavePreferenceCandidate(context.Background(), candidate)
	if err != nil || result.Candidate != candidate || result.Replay || wrapped.saveCalls != 1 {
		t.Fatalf("receipt recovery result=%#v err=%v owner save calls=%d", result, err, wrapped.saveCalls)
	}
	assertHobbyGraphCandidateCount(t, f.db, candidate.RequestID, 1)
}

func hobbyGraphCandidateForTest(suffix string) musiccatalog.StorageHostPreferenceCandidate {
	requestID := "hobby-request-" + suffix
	candidateDigest := sha256.Sum256([]byte(requestID))
	payloadDigest := sha256.Sum256([]byte("payload:" + suffix))
	return musiccatalog.StorageHostPreferenceCandidate{
		CandidateID: "hobby-preference-candidate/sha256:" + hex.EncodeToString(candidateDigest[:]),
		RequestID:   requestID,
		UserID:      "user-a",
		ActorID:     "Mio",
		PayloadHash: hex.EncodeToString(payloadDigest[:]),
		TargetID:    "s1",
		SignalType:  "like",
		Note:        "private preference candidate",
		State:       "candidate",
		CreatedAt:   time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
}

func assertHobbyGraphCandidateCount(t *testing.T, db *sql.DB, requestID string, want int) {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM hobby_agent_preference_candidate WHERE request_id=?`, requestID).Scan(&count)
	if err != nil || count != want {
		t.Fatalf("candidate row count=%d err=%v, want %d", count, err, want)
	}
}

func hobbyGraphErrorCode(err error) string {
	var storageError *Error
	if errors.As(err, &storageError) {
		return storageError.Code
	}
	return ""
}
