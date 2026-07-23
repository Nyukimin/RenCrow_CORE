package viewer

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleMovieCatalogMoviesSearchAndLimit(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})

	req := httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movies&q=ケビン&limit=99", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool                    `json:"available"`
		Total     int                     `json:"total"`
		Limit     int                     `json:"limit"`
		Items     []movieCatalogMovieItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.Available {
		t.Fatal("expected available catalog")
	}
	if out.Limit != maxMovieCatalogLimit {
		t.Fatalf("expected capped limit %d, got %d", maxMovieCatalogLimit, out.Limit)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Title != "マージン・コール" {
		t.Fatalf("unexpected movie search result: %+v", out)
	}
}

func TestHandleMovieCatalogPersonDetailReturnsMovieLinks(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})

	req := httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=person&id=30003", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Detail struct {
			Person movieCatalogPersonItem `json:"person"`
			Links  []movieCatalogEdgeItem `json:"links"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Detail.Person.Name != "ケビン・スペイシー" {
		t.Fatalf("unexpected person: %+v", out.Detail.Person)
	}
	if len(out.Detail.Links) != 1 || out.Detail.Links[0].MovieTitle != "マージン・コール" {
		t.Fatalf("expected linked movie edge, got %+v", out.Detail.Links)
	}
	if !out.Detail.Links[0].MovieFetched || !out.Detail.Links[0].PersonFetched {
		t.Fatalf("expected fetched flags on linked edge, got %+v", out.Detail.Links[0])
	}
}

func TestHandleMovieCatalogMovieDetailMarksUnfetchedPersonLinks(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('57573','99999','出演','movie_cast','マージン・コール','未取得の人物','https://eiga.com/movie/57573/','https://eiga.com/person/99999/');
`); err != nil {
		db.Close()
		t.Fatalf("seed unfetched edge: %v", err)
	}
	db.Close()
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})

	req := httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movie&id=57573", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Detail struct {
			Links []movieCatalogEdgeItem `json:"links"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	var found bool
	for _, link := range out.Detail.Links {
		if link.PersonID == "99999" {
			found = true
			if !link.MovieFetched || link.PersonFetched {
				t.Fatalf("expected fetched movie and unfetched person flags, got %+v", link)
			}
		}
	}
	if !found {
		t.Fatalf("expected unfetched person edge, got %+v", out.Detail.Links)
	}
}

func TestHandleMovieCatalogReturnsWatchEventsAndWatchedCounts(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE movie_watch_events(
  event_id TEXT PRIMARY KEY,
  movie_id TEXT,
  original_title TEXT NOT NULL,
  watched_at TEXT,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO movie_watch_events(event_id,movie_id,original_title,watched_at,source,source_batch_id,note)
VALUES('watch_1','57573','マージン・コール','2026-06-03','user_list','batch_today','');
`); err != nil {
		db.Close()
		t.Fatalf("seed watch events: %v", err)
	}
	db.Close()
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})

	moviesRec := httptest.NewRecorder()
	h(moviesRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movies&q=マージン", nil))
	if moviesRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", moviesRec.Code, moviesRec.Body.String())
	}
	var moviesOut struct {
		Items []movieCatalogMovieItem `json:"items"`
	}
	if err := json.Unmarshal(moviesRec.Body.Bytes(), &moviesOut); err != nil {
		t.Fatalf("invalid movies json: %v", err)
	}
	if len(moviesOut.Items) != 1 || !moviesOut.Items[0].Watched || moviesOut.Items[0].WatchCount != 1 {
		t.Fatalf("expected watched movie row, got %+v", moviesOut.Items)
	}

	movieRec := httptest.NewRecorder()
	h(movieRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movie&id=57573", nil))
	if movieRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", movieRec.Code, movieRec.Body.String())
	}
	var movieOut struct {
		Detail struct {
			Movie       movieCatalogMovieItem        `json:"movie"`
			WatchEvents []movieCatalogWatchEventItem `json:"watch_events"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(movieRec.Body.Bytes(), &movieOut); err != nil {
		t.Fatalf("invalid movie json: %v", err)
	}
	if !movieOut.Detail.Movie.Watched || movieOut.Detail.Movie.WatchCount != 1 || len(movieOut.Detail.WatchEvents) != 1 {
		t.Fatalf("expected watched movie detail, got %+v", movieOut.Detail)
	}

	personRec := httptest.NewRecorder()
	h(personRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=person&id=30003", nil))
	if personRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", personRec.Code, personRec.Body.String())
	}
	var personOut struct {
		Detail struct {
			Person movieCatalogPersonItem `json:"person"`
			Links  []movieCatalogEdgeItem `json:"links"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(personRec.Body.Bytes(), &personOut); err != nil {
		t.Fatalf("invalid person json: %v", err)
	}
	if personOut.Detail.Person.WatchedMovieCount != 1 || len(personOut.Detail.Links) != 1 || !personOut.Detail.Links[0].MovieWatched {
		t.Fatalf("expected watched person link, got %+v", personOut.Detail)
	}
}

func TestHandleMovieCatalogReturnsFavoritePeople(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE movie_preference_signals(
  signal_id TEXT PRIMARY KEY,
  signal_type TEXT NOT NULL,
  target_id TEXT,
  target_label TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1.0,
  evidence_json TEXT NOT NULL,
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO movie_preference_signals(signal_id,signal_type,target_id,target_label,weight,evidence_json,generated_by)
VALUES('pref_1','actor_affinity','30003','ケビン・スペイシー',1.0,'{}','user');
`); err != nil {
		db.Close()
		t.Fatalf("seed preference signals: %v", err)
	}
	db.Close()
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})

	peopleRec := httptest.NewRecorder()
	h(peopleRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=people&q=ケビン", nil))
	if peopleRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", peopleRec.Code, peopleRec.Body.String())
	}
	var peopleOut struct {
		Items []movieCatalogPersonItem `json:"items"`
	}
	if err := json.Unmarshal(peopleRec.Body.Bytes(), &peopleOut); err != nil {
		t.Fatalf("invalid people json: %v", err)
	}
	if len(peopleOut.Items) != 1 || !peopleOut.Items[0].Favorite || peopleOut.Items[0].PreferenceCount != 1 {
		t.Fatalf("expected favorite person row, got %+v", peopleOut.Items)
	}

	personRec := httptest.NewRecorder()
	h(personRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=person&id=30003", nil))
	if personRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", personRec.Code, personRec.Body.String())
	}
	var personOut struct {
		Detail struct {
			Person movieCatalogPersonItem `json:"person"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(personRec.Body.Bytes(), &personOut); err != nil {
		t.Fatalf("invalid person json: %v", err)
	}
	if !personOut.Detail.Person.Favorite || personOut.Detail.Person.PreferenceCount != 1 {
		t.Fatalf("expected favorite person detail, got %+v", personOut.Detail.Person)
	}

	movieRec := httptest.NewRecorder()
	h(movieRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movie&id=57573", nil))
	if movieRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", movieRec.Code, movieRec.Body.String())
	}
	var movieOut struct {
		Detail struct {
			Links []movieCatalogEdgeItem `json:"links"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(movieRec.Body.Bytes(), &movieOut); err != nil {
		t.Fatalf("invalid movie json: %v", err)
	}
	if len(movieOut.Detail.Links) != 1 || !movieOut.Detail.Links[0].PersonFavorite {
		t.Fatalf("expected favorite person edge, got %+v", movieOut.Detail.Links)
	}
}

func TestHandleMovieCatalogPreferenceTogglesPersonFavorite(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	h := HandleMovieCatalogPreference(MovieCatalogOptions{DBPath: dbPath})

	onRec := httptest.NewRecorder()
	h(onRec, httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/preference", strings.NewReader(`{
		"kind":"person",
		"target_id":"30003",
		"target_label":"ケビン・スペイシー",
		"favorite":true
	}`)))
	if onRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", onRec.Code, onRec.Body.String())
	}
	var onOut movieCatalogResponse
	if err := json.Unmarshal(onRec.Body.Bytes(), &onOut); err != nil {
		t.Fatalf("invalid on json: %v", err)
	}
	detail, ok := onOut.Detail.(map[string]any)
	if !ok || detail["favorite"] != true || int(detail["preference_count"].(float64)) != 1 {
		t.Fatalf("expected favorite on response, got %+v", onOut.Detail)
	}

	read := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})
	personRec := httptest.NewRecorder()
	read(personRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=person&id=30003", nil))
	if personRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", personRec.Code, personRec.Body.String())
	}
	var personOut struct {
		Detail struct {
			Person movieCatalogPersonItem `json:"person"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(personRec.Body.Bytes(), &personOut); err != nil {
		t.Fatalf("invalid person json: %v", err)
	}
	if !personOut.Detail.Person.Favorite || personOut.Detail.Person.PreferenceCount != 1 {
		t.Fatalf("expected favorite person after toggle on, got %+v", personOut.Detail.Person)
	}

	offRec := httptest.NewRecorder()
	h(offRec, httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/preference", strings.NewReader(`{
		"kind":"person",
		"target_id":"30003",
		"target_label":"ケビン・スペイシー",
		"favorite":false
	}`)))
	if offRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", offRec.Code, offRec.Body.String())
	}

	personRec = httptest.NewRecorder()
	read(personRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=person&id=30003", nil))
	if personRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", personRec.Code, personRec.Body.String())
	}
	if err := json.Unmarshal(personRec.Body.Bytes(), &personOut); err != nil {
		t.Fatalf("invalid person json after off: %v", err)
	}
	if personOut.Detail.Person.Favorite || personOut.Detail.Person.PreferenceCount != 0 {
		t.Fatalf("expected favorite off after toggle off, got %+v", personOut.Detail.Person)
	}
}

func TestHandleMovieCatalogPreferenceStoresGridAssessment(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	write := HandleMovieCatalogPreference(MovieCatalogOptions{DBPath: dbPath})

	rec := httptest.NewRecorder()
	write(rec, httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/preference", strings.NewReader(`{
		"kind":"movie",
		"target_id":"57573",
		"target_label":"マージン・コール",
		"dimension":"familiarity",
		"value":"unseen",
		"generated_by":"viewer"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	read := HandleMovieCatalog(MovieCatalogOptions{DBPath: dbPath})
	listRec := httptest.NewRecorder()
	read(listRec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=movies&q=マージン", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var out struct {
		Items []movieCatalogMovieItem `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid list json: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Familiarity != "unseen" {
		t.Fatalf("expected unseen assessment, got %+v", out.Items)
	}

	badRec := httptest.NewRecorder()
	write(badRec, httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/preference", strings.NewReader(`{
		"kind":"movie",
		"target_id":"57573",
		"dimension":"familiarity",
		"value":"known"
	}`)))
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid movie familiarity, got %d: %s", badRec.Code, badRec.Body.String())
	}
}

func TestHandleMovieCatalogMissingDBIsSoftUnavailable(t *testing.T) {
	h := HandleMovieCatalog(MovieCatalogOptions{DBPath: filepath.Join(t.TempDir(), "missing.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog?action=stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out movieCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Available {
		t.Fatal("missing catalog should be available=false")
	}
}

func TestResolveMovieCatalogFetchTargetByMovieName(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	url, candidates, err := resolveMovieCatalogFetchTarget(db, movieCatalogFetchRequest{Kind: "movie", Query: "マージン・コール"})
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if url != "https://eiga.com/movie/57573/" {
		t.Fatalf("unexpected url: %s", url)
	}
	if len(candidates) != 1 || candidates[0].Title != "マージン・コール" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestResolveMovieCatalogFetchTargetUsesUnfetchedEdgeMovieName(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('103262','30003','出演','person_filmography','爆弾','ケビン・スペイシー','https://eiga.com/movie/103262/','https://eiga.com/person/30003/');
`); err != nil {
		t.Fatalf("seed edge movie: %v", err)
	}

	url, candidates, err := resolveMovieCatalogFetchTarget(db, movieCatalogFetchRequest{Kind: "movie", Query: "爆弾"})
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if url != "https://eiga.com/movie/103262/" {
		t.Fatalf("unexpected url: %s", url)
	}
	if len(candidates) != 1 || candidates[0].Title != "爆弾" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestHandleMovieCatalogFetchNoCandidatesReturnsStructuredHint(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	h := HandleMovieCatalogFetch(MovieCatalogOptions{DBPath: dbPath})

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/fetch", strings.NewReader(`{"kind":"movie","query":"爆弾","max_pages":1}`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var out movieCatalogFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Status != "candidates" || out.Kind != "movie" || out.Query != "爆弾" {
		t.Fatalf("unexpected structured hint response: %+v", out)
	}
	if len(out.Candidates) != 0 {
		t.Fatalf("expected no local candidates, got %+v", out.Candidates)
	}
}

func TestResolveMovieCatalogFetchTargetRejectsKindMismatch(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, _, err = resolveMovieCatalogFetchTarget(db, movieCatalogFetchRequest{Kind: "person", URL: "https://eiga.com/movie/57573/"})
	if err == nil {
		t.Fatal("expected kind mismatch error")
	}
}

func seedMovieCatalogTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eiga_catalog.sqlite")
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT NOT NULL, url TEXT NOT NULL, synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, profile_json TEXT, biography TEXT);
CREATE TABLE movie_people(
  movie_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  role TEXT NOT NULL,
  source TEXT NOT NULL,
  movie_title TEXT,
  person_name TEXT,
  movie_url TEXT,
  person_url TEXT,
  PRIMARY KEY(movie_id, person_id, role, source)
);
CREATE TABLE fetch_log(url TEXT PRIMARY KEY, status TEXT NOT NULL);
INSERT INTO movies(movie_id,title,url,synopsis) VALUES('57573','マージン・コール','https://eiga.com/movie/57573/','金融危機を描く社会派サスペンス。');
INSERT INTO people(person_id,name,url,profile_json,biography) VALUES('30003','ケビン・スペイシー','https://eiga.com/person/30003/','{"英語表記":"Kevin Spacey"}','映画俳優。');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('57573','30003','出演','movie_cast','マージン・コール','ケビン・スペイシー','https://eiga.com/movie/57573/','https://eiga.com/person/30003/');
INSERT INTO fetch_log(url,status) VALUES('https://eiga.com/movie/57573/','ok');
`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	return path
}
