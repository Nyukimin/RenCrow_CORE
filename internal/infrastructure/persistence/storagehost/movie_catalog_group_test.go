package storagehost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	moviecatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	_ "modernc.org/sqlite"
)

type movieCatalogContractOwner struct{}

func (movieCatalogContractOwner) Lookup(context.Context, moviecatalog.LookupRequest) (moviecatalog.LookupResult, error) {
	return moviecatalog.LookupResult{}, nil
}
func (movieCatalogContractOwner) ViewerStats(context.Context) (map[string]int, error) {
	return map[string]int{}, nil
}
func (movieCatalogContractOwner) ViewerMovies(context.Context, moviecatalog.QueryParams, int, int) (int, []moviecatalog.MovieItem, error) {
	return 0, []moviecatalog.MovieItem{}, nil
}
func (movieCatalogContractOwner) ViewerPeople(context.Context, moviecatalog.QueryParams, int, int) (int, []moviecatalog.PersonItem, error) {
	return 0, []moviecatalog.PersonItem{}, nil
}
func (movieCatalogContractOwner) ViewerCards(context.Context, int, int) (int, []moviecatalog.Card, error) {
	return 0, []moviecatalog.Card{}, nil
}
func (movieCatalogContractOwner) ViewerMovie(context.Context, string) (map[string]any, error) {
	return nil, errors.New("not found")
}
func (movieCatalogContractOwner) ViewerPerson(context.Context, string) (map[string]any, error) {
	return nil, errors.New("not found")
}
func (movieCatalogContractOwner) FindPreferenceCandidateByID(context.Context, string, string) (moviecatalog.StorageHostPreferenceCandidate, bool, error) {
	return moviecatalog.StorageHostPreferenceCandidate{}, false, nil
}
func (movieCatalogContractOwner) FindPreferenceCandidateByRequestID(context.Context, string, string) (moviecatalog.StorageHostPreferenceCandidate, bool, error) {
	return moviecatalog.StorageHostPreferenceCandidate{}, false, nil
}
func (movieCatalogContractOwner) SavePreferenceCandidate(context.Context, moviecatalog.StorageHostOperationIdentity, moviecatalog.StorageHostPreferenceCandidate) (moviecatalog.StorageHostCandidateResult, error) {
	return moviecatalog.StorageHostCandidateResult{}, nil
}
func (movieCatalogContractOwner) FindStorageHostOperationReceipt(context.Context, string) (moviecatalog.StorageHostOperationReceipt, bool, error) {
	return moviecatalog.StorageHostOperationReceipt{}, false, nil
}

func TestMovieCatalogGroupRegistersExactClosedContract(t *testing.T) {
	root := t.TempDir()
	handler, err := NewHandler(HandlerConfig{Token: "movie-catalog-test-token", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := RegisterMovieCatalogGroup(handler, movieCatalogContractOwner{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: "movie-catalog-test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	contract, err := client.Contract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"lookup":       false,
		"viewer_stats": false, "viewer_movies": false, "viewer_people": false,
		"viewer_cards": false, "viewer_movie": false, "viewer_person": false,
		"candidate_by_id": false, "candidate_by_request": false, "candidate_save": true,
	}
	got := map[string]bool{}
	for _, operation := range contract.Operations {
		if operation.Group == GroupMovieCatalog {
			got[operation.Op] = operation.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("movie catalog operations=%v, want exactly %v", got, want)
	}
	for name, mutating := range want {
		if got[name] != mutating {
			t.Fatalf("movie catalog operation %q mutating=%v, want %v", name, got[name], mutating)
		}
	}
}

type movieCatalogRemoteFixture struct {
	db      *sql.DB
	owner   *moviecatalog.StorageHostSQLiteStore
	handler *Handler
	server  *httptest.Server
	client  *Client
}

func openMovieCatalogRemoteFixture(t *testing.T, root string) *movieCatalogRemoteFixture {
	return openMovieCatalogRemoteFixtureWithOwner(t, root, nil)
}

func openMovieCatalogRemoteFixtureWithOwner(t *testing.T, root string, adapt func(*moviecatalog.StorageHostSQLiteStore) MovieCatalogGroupOwner) *movieCatalogRemoteFixture {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "movie-catalog.db")
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS movies(movie_id TEXT PRIMARY KEY,title TEXT NOT NULL,url TEXT NOT NULL,synopsis TEXT);
CREATE TABLE IF NOT EXISTS people(person_id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,profile_json TEXT,biography TEXT);
CREATE TABLE IF NOT EXISTS movie_people(movie_id TEXT NOT NULL,person_id TEXT NOT NULL,role TEXT NOT NULL,source TEXT NOT NULL,movie_title TEXT,person_name TEXT,movie_url TEXT,person_url TEXT);
CREATE TABLE IF NOT EXISTS fetch_log(source TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS movie_catalog_assessments(kind TEXT NOT NULL,target_id TEXT NOT NULL,target_label TEXT NOT NULL,familiarity TEXT NOT NULL DEFAULT '',sentiment TEXT NOT NULL DEFAULT '',updated_by TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(kind,target_id));
INSERT OR IGNORE INTO movies(movie_id,title,url,synopsis) VALUES('m1','Heat','https://example.test/m1','A crime drama');
INSERT OR IGNORE INTO people(person_id,name,url,profile_json,biography) VALUES('p1','Al Pacino','https://example.test/p1','{}','Profile');
INSERT OR IGNORE INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES('m1','p1','actor','test','Heat','Al Pacino','https://example.test/m1','https://example.test/p1');
INSERT OR IGNORE INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by,updated_at) VALUES('movie','m1','Heat','known','like','test','2026-10-03T00:00:00Z');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	owner, err := moviecatalog.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open movie SQLite owner: %v", err)
	}
	groupOwner := MovieCatalogGroupOwner(owner)
	if adapt != nil {
		groupOwner = adapt(owner)
	}
	handler, err := NewHandler(HandlerConfig{Token: "movie-catalog-test-token", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := RegisterMovieCatalogGroup(handler, groupOwner); err != nil {
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: "movie-catalog-test-token", HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	return &movieCatalogRemoteFixture{db: db, owner: owner, handler: handler, server: server, client: client}
}

func (f *movieCatalogRemoteFixture) close() {
	if f == nil {
		return
	}
	if f.server != nil {
		f.server.Close()
	}
	if f.handler != nil {
		_ = f.handler.Close()
	}
	if f.db != nil {
		_ = f.db.Close()
	}
}

func TestMovieCatalogClientPreservesTypedLookupAndViewerDTOs(t *testing.T) {
	fixture := openMovieCatalogRemoteFixture(t, t.TempDir())
	defer fixture.close()
	ctx := context.Background()
	remote := NewMovieCatalogClient(fixture.client)
	lookup, err := remote.Lookup(ctx, moviecatalog.LookupRequest{Kind: "movie", Name: "Heat"})
	if err != nil {
		t.Fatal(err)
	}
	movie, ok := lookup.Detail["movie"].(moviecatalog.MovieItem)
	if !ok || movie.MovieID != "m1" {
		t.Fatalf("lookup movie DTO=%T %#v", lookup.Detail["movie"], lookup.Detail)
	}
	links, ok := lookup.Detail["links"].([]moviecatalog.EdgeItem)
	if !ok || len(links) != 1 || links[0].PersonID != "p1" {
		t.Fatalf("lookup links DTO=%T %#v", lookup.Detail["links"], lookup.Detail)
	}
	stats, err := remote.ViewerStats(ctx)
	if err != nil || stats["movies"] != 1 {
		t.Fatalf("ViewerStats=%v err=%v", stats, err)
	}
	total, movies, err := remote.ViewerMovies(ctx, moviecatalog.QueryParams{Query: "Heat"}, 10, 0)
	if err != nil || total != 1 || len(movies) != 1 || movies[0].MovieID != "m1" {
		t.Fatalf("ViewerMovies total=%d items=%+v err=%v", total, movies, err)
	}
	total, people, err := remote.ViewerPeople(ctx, moviecatalog.QueryParams{Query: "Al Pacino"}, 10, 0)
	if err != nil || total != 1 || len(people) != 1 || people[0].PersonID != "p1" {
		t.Fatalf("ViewerPeople total=%d items=%+v err=%v", total, people, err)
	}
	total, cards, err := remote.ViewerCards(ctx, 10, 0)
	if err != nil || total < 1 || len(cards) < 1 || cards[0].Depth != 0 {
		t.Fatalf("ViewerCards total=%d items=%+v err=%v", total, cards, err)
	}
	movieDetail, err := remote.ViewerMovie(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := movieDetail["movie"].(moviecatalog.MovieItem); !ok {
		t.Fatalf("ViewerMovie movie DTO=%T", movieDetail["movie"])
	}
	if _, ok := movieDetail["links"].([]moviecatalog.EdgeItem); !ok {
		t.Fatalf("ViewerMovie links DTO=%T", movieDetail["links"])
	}
	personDetail, err := remote.ViewerPerson(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := personDetail["person"].(moviecatalog.PersonItem); !ok {
		t.Fatalf("ViewerPerson person DTO=%T", personDetail["person"])
	}
	if _, ok := personDetail["links"].([]moviecatalog.EdgeItem); !ok {
		t.Fatalf("ViewerPerson links DTO=%T", personDetail["links"])
	}
}

func TestMovieCatalogCandidateReceiptSurvivesOwnerAndHandlerReopen(t *testing.T) {
	root := t.TempDir()
	first := openMovieCatalogRemoteFixture(t, root)
	candidate := moviecatalog.StorageHostPreferenceCandidate{ID: "movie-preference-candidate/sha256:test", RequestID: "request-1", UserID: "user-1", ActorID: "agent:mio", PayloadHash: strings.Repeat("a", 64), TargetKind: "movie", TargetID: "m1", Familiarity: "known", Sentiment: "like", Note: "", State: "candidate", CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)}
	const opID = "movie-candidate-recovery"
	first.client.opID = opID
	first.handler.crashAfterCommitFor = opID
	_, err := NewMovieCatalogClient(first.client).SavePreferenceCandidate(context.Background(), candidate)
	expectMovieCatalogError(t, err, ErrorCodeOutcomeUnknown)
	first.close()
	second := openMovieCatalogRemoteFixture(t, root)
	defer second.close()
	second.client.opID = opID
	result, err := NewMovieCatalogClient(second.client).SavePreferenceCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatalf("SavePreferenceCandidate after reopen: %v", err)
	}
	if result.Candidate.ID != candidate.ID || result.Replay {
		t.Fatalf("recovered result=%+v, want exact original non-replay receipt", result)
	}
	var count int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM movie_preference_candidate WHERE request_id=?`, candidate.RequestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidate count=%d err=%v, want exactly one", count, err)
	}
	remote := NewMovieCatalogClient(second.client)
	byID, found, err := remote.FindPreferenceCandidateByID(context.Background(), candidate.UserID, candidate.ID)
	if err != nil || !found || byID != candidate {
		t.Fatalf("candidate by id=%+v found=%v err=%v", byID, found, err)
	}
	byRequest, found, err := remote.FindPreferenceCandidateByRequestID(context.Background(), candidate.UserID, candidate.RequestID)
	if err != nil || !found || byRequest != candidate {
		t.Fatalf("candidate by request=%+v found=%v err=%v", byRequest, found, err)
	}
	if _, found, err := remote.FindPreferenceCandidateByID(context.Background(), "another-user", candidate.ID); err != nil || found {
		t.Fatalf("cross-user candidate read found=%v err=%v, want absent", found, err)
	}
	if _, found, err := remote.FindPreferenceCandidateByRequestID(context.Background(), "another-user", candidate.RequestID); err != nil || found {
		t.Fatalf("cross-user request read found=%v err=%v, want absent", found, err)
	}
	changed := candidate
	changed.Note = "different payload"
	second.client.opID = opID
	expectMovieCatalogError(t, func() error { _, err := remote.SavePreferenceCandidate(context.Background(), changed); return err }(), ErrorCodeDuplicateConflict)
}

type movieCatalogCommitThenErrorOwner struct {
	*moviecatalog.StorageHostSQLiteStore
	saveCalls int
}

func (owner *movieCatalogCommitThenErrorOwner) SavePreferenceCandidate(ctx context.Context, identity moviecatalog.StorageHostOperationIdentity, candidate moviecatalog.StorageHostPreferenceCandidate) (moviecatalog.StorageHostCandidateResult, error) {
	owner.saveCalls++
	if _, err := owner.StorageHostSQLiteStore.SavePreferenceCandidate(ctx, identity, candidate); err != nil {
		return moviecatalog.StorageHostCandidateResult{}, err
	}
	return moviecatalog.StorageHostCandidateResult{}, errors.New("owner response lost after commit")
}

func TestMovieCatalogArbitraryOwnerErrorAfterCommitRecoversWithoutRerun(t *testing.T) {
	root := t.TempDir()
	var injected *movieCatalogCommitThenErrorOwner
	first := openMovieCatalogRemoteFixtureWithOwner(t, root, func(store *moviecatalog.StorageHostSQLiteStore) MovieCatalogGroupOwner {
		injected = &movieCatalogCommitThenErrorOwner{StorageHostSQLiteStore: store}
		return injected
	})
	candidate := movieCatalogCandidateForTest("owner-error")
	const opID = "movie-owner-error-after-commit"
	first.client.opID = opID
	expectMovieCatalogError(t, func() error {
		_, err := NewMovieCatalogClient(first.client).SavePreferenceCandidate(context.Background(), candidate)
		return err
	}(), ErrorCodeOutcomeUnknown)
	if injected.saveCalls != 1 {
		t.Fatalf("owner Save calls=%d, want one initial execution", injected.saveCalls)
	}
	first.close()
	second := openMovieCatalogRemoteFixture(t, root)
	defer second.close()
	second.client.opID = opID
	result, err := NewMovieCatalogClient(second.client).SavePreferenceCandidate(context.Background(), candidate)
	if err != nil || result.Candidate != candidate || result.Replay {
		t.Fatalf("recovered result=%+v err=%v", result, err)
	}
	var count int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM movie_preference_candidate WHERE request_id=?`, candidate.RequestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidate count=%d err=%v, want exactly one", count, err)
	}
}

func TestMovieCatalogTamperedCandidateReceiptStaysUnknown(t *testing.T) {
	root := t.TempDir()
	first := openMovieCatalogRemoteFixture(t, root)
	candidate := movieCatalogCandidateForTest("tamper")
	const opID = "movie-tampered-receipt"
	first.client.opID = opID
	first.handler.crashAfterCommitFor = opID
	expectMovieCatalogError(t, func() error {
		_, err := NewMovieCatalogClient(first.client).SavePreferenceCandidate(context.Background(), candidate)
		return err
	}(), ErrorCodeOutcomeUnknown)
	first.close()
	db, err := sql.Open("sqlite", filepath.Join(root, "movie-catalog.db")+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE movie_catalog_storagehost_receipt SET result_json=? WHERE op_id=?`, []byte(`{"candidate":{},"replay":true}`), opID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	second := openMovieCatalogRemoteFixture(t, root)
	defer second.close()
	second.client.opID = opID
	expectMovieCatalogError(t, func() error {
		_, err := NewMovieCatalogClient(second.client).SavePreferenceCandidate(context.Background(), candidate)
		return err
	}(), ErrorCodeOutcomeUnknown)
	var count int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM movie_preference_candidate WHERE request_id=?`, candidate.RequestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidate count=%d err=%v, want committed row preserved without rerun", count, err)
	}
}

func TestMovieCatalogSubstitutedReplayFlagCannotBorrowAnotherOperationResult(t *testing.T) {
	root := t.TempDir()
	fixture := openMovieCatalogRemoteFixture(t, root)
	candidate := movieCatalogCandidateForTest("replay-binding")
	firstOpID := "movie-candidate-first"
	fixture.client.opID = firstOpID
	if _, err := NewMovieCatalogClient(fixture.client).SavePreferenceCandidate(context.Background(), candidate); err != nil {
		t.Fatalf("first save: %v", err)
	}
	const replayOpID = "movie-candidate-replay"
	fixture.client.opID = replayOpID
	fixture.handler.crashAfterCommitFor = replayOpID
	expectMovieCatalogError(t, func() error {
		_, err := NewMovieCatalogClient(fixture.client).SavePreferenceCandidate(context.Background(), candidate)
		return err
	}(), ErrorCodeOutcomeUnknown)
	fixture.close()
	db, err := sql.Open("sqlite", filepath.Join(root, "movie-catalog.db")+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	var payloadHash string
	var generation int64
	if err := db.QueryRow(`SELECT payload_hash,writer_generation FROM movie_catalog_storagehost_receipt WHERE op_id=?`, replayOpID).Scan(&payloadHash, &generation); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	// Substitute an otherwise canonical result from the first operation and
	// recompute both unkeyed digests. The candidate-row effect marker must still
	// reject Replay=false for the second operation.
	resultJSON, err := json.Marshal(moviecatalog.StorageHostCandidateResult{Candidate: candidate, Replay: false})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	resultDigest := sha256.Sum256(resultJSON)
	resultHash := hex.EncodeToString(resultDigest[:])
	identityJSON, err := json.Marshal(struct {
		OpID             string `json:"op_id"`
		Operation        string `json:"operation"`
		PayloadHash      string `json:"payload_hash"`
		WriterGeneration int64  `json:"writer_generation"`
		ResultSHA256     string `json:"result_sha256"`
	}{OpID: replayOpID, Operation: "candidate_save", PayloadHash: payloadHash, WriterGeneration: generation, ResultSHA256: resultHash})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(identityJSON)
	if _, err := db.Exec(`UPDATE movie_catalog_storagehost_receipt SET result_json=?,result_sha256=?,receipt_sha256=? WHERE op_id=?`, resultJSON, resultHash, hex.EncodeToString(receiptDigest[:]), replayOpID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	reopened := openMovieCatalogRemoteFixture(t, root)
	defer reopened.close()
	reopened.client.opID = replayOpID
	expectMovieCatalogError(t, func() error {
		_, err := NewMovieCatalogClient(reopened.client).SavePreferenceCandidate(context.Background(), candidate)
		return err
	}(), ErrorCodeOutcomeUnknown)
	var createdBy string
	if err := reopened.db.QueryRow(`SELECT created_by_storagehost_op_id FROM movie_preference_candidate WHERE request_id=?`, candidate.RequestID).Scan(&createdBy); err != nil || createdBy != firstOpID {
		t.Fatalf("candidate effect marker=%q err=%v, want original op %q", createdBy, err, firstOpID)
	}
}

func movieCatalogCandidateForTest(suffix string) moviecatalog.StorageHostPreferenceCandidate {
	return moviecatalog.StorageHostPreferenceCandidate{ID: "movie-preference-candidate/sha256:" + suffix, RequestID: "request-" + suffix, UserID: "user-1", ActorID: "agent:mio", PayloadHash: strings.Repeat("a", 64), TargetKind: "movie", TargetID: "m1", Familiarity: "known", Sentiment: "like", State: "candidate", CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)}
}

func expectMovieCatalogError(t *testing.T, err error, code string) {
	t.Helper()
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != code {
		t.Fatalf("error=%v, want storage-host code %q", err, code)
	}
}
