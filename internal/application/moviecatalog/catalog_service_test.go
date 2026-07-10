package moviecatalog

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:?_time_format=sqlite")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	schema := `
CREATE TABLE movies (
	movie_id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	url TEXT NOT NULL,
	synopsis TEXT
);

CREATE TABLE people (
	person_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	url TEXT NOT NULL,
	profile_json TEXT,
	biography TEXT
);

CREATE TABLE movie_people (
	movie_id TEXT NOT NULL,
	person_id TEXT NOT NULL,
	movie_title TEXT,
	movie_url TEXT,
	person_name TEXT,
	person_url TEXT,
	role TEXT,
	source TEXT
);

CREATE TABLE fetch_log (
	log_id INTEGER PRIMARY KEY AUTOINCREMENT,
	fetched_at TEXT NOT NULL
);

CREATE TABLE movie_watch_events (
	event_id TEXT PRIMARY KEY,
	movie_id TEXT NOT NULL,
	original_title TEXT,
	watched_at TEXT,
	source TEXT,
	source_batch_id TEXT,
	note TEXT,
	created_at TEXT
);

CREATE TABLE movie_preference_signals (
	signal_id TEXT PRIMARY KEY,
	signal_type TEXT NOT NULL,
	target_id TEXT,
	target_label TEXT NOT NULL,
	weight REAL NOT NULL DEFAULT 1.0,
	evidence_json TEXT NOT NULL,
	generated_by TEXT NOT NULL,
	generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_movie_preference_signals_target ON movie_preference_signals(target_id);
CREATE INDEX idx_movie_preference_signals_type ON movie_preference_signals(signal_type);
`

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create test schema: %v", err)
	}

	return db
}

func seedTestData(t *testing.T, db *sql.DB) {
	// Insert test movies
	_, err := db.Exec(`
INSERT INTO movies (movie_id, title, url, synopsis) VALUES
	('m1', 'Test Movie 1', 'http://example.com/m1', 'Synopsis 1'),
	('m2', 'Test Movie 2', 'http://example.com/m2', 'Synopsis 2'),
	('m3', 'Another Movie', 'http://example.com/m3', 'Synopsis 3')
`)
	if err != nil {
		t.Fatalf("failed to seed movies: %v", err)
	}

	// Insert test people
	_, err = db.Exec(`
INSERT INTO people (person_id, name, url, profile_json, biography) VALUES
	('p1', 'Test Person 1', 'http://example.com/p1', '{}', 'Bio 1'),
	('p2', 'Test Person 2', 'http://example.com/p2', '{}', 'Bio 2'),
	('p3', 'Another Person', 'http://example.com/p3', '{}', 'Bio 3')
`)
	if err != nil {
		t.Fatalf("failed to seed people: %v", err)
	}

	// Insert test movie_people relationships
	_, err = db.Exec(`
INSERT INTO movie_people (movie_id, person_id, movie_title, person_name, role, source) VALUES
	('m1', 'p1', 'Test Movie 1', 'Test Person 1', 'Actor', 'test'),
	('m1', 'p2', 'Test Movie 1', 'Test Person 2', 'Director', 'test'),
	('m2', 'p1', 'Test Movie 2', 'Test Person 1', 'Actor', 'test')
`)
	if err != nil {
		t.Fatalf("failed to seed movie_people: %v", err)
	}

	// Insert test watch events
	_, err = db.Exec(`
INSERT INTO movie_watch_events (event_id, movie_id, original_title, watched_at, source, created_at) VALUES
	('e1', 'm1', 'Test Movie 1', '2024-01-01', 'test', '2024-01-01'),
	('e2', 'm1', 'Test Movie 1', '2024-01-02', 'test', '2024-01-02')
`)
	if err != nil {
		t.Fatalf("failed to seed watch events: %v", err)
	}

	// Insert test fetch_log
	_, err = db.Exec(`INSERT INTO fetch_log (fetched_at) VALUES ('2024-01-01')`)
	if err != nil {
		t.Fatalf("failed to seed fetch_log: %v", err)
	}
}

func TestStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	stats, err := Stats(db)
	if err != nil {
		t.Fatalf("Stats() failed: %v", err)
	}

	expected := map[string]int{
		"movies":             3,
		"people":             3,
		"movie_people":       3,
		"fetch_log":          1,
		"movie_watch_events": 2,
	}

	for table, expectedCount := range expected {
		if count, ok := stats[table]; !ok {
			t.Errorf("Stats() missing table %q", table)
		} else if count != expectedCount {
			t.Errorf("Stats() for table %q: got %d, want %d", table, count, expectedCount)
		}
	}
}

func TestMovies(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	tests := []struct {
		name      string
		params    QueryParams
		limit     int
		offset    int
		wantTotal int
		wantCount int
		wantFirst string
	}{
		{
			name:      "list all movies",
			params:    QueryParams{},
			limit:     10,
			offset:    0,
			wantTotal: 3,
			wantCount: 3,
			wantFirst: "Another Movie",
		},
		{
			name:      "search by title",
			params:    QueryParams{Query: "Test"},
			limit:     10,
			offset:    0,
			wantTotal: 2,
			wantCount: 2,
			wantFirst: "Test Movie 1",
		},
		{
			name:      "filter by role",
			params:    QueryParams{Role: "Actor"},
			limit:     10,
			offset:    0,
			wantTotal: 2,
			wantCount: 2,
		},
		{
			name:      "pagination",
			params:    QueryParams{},
			limit:     2,
			offset:    1,
			wantTotal: 3,
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, items, err := Movies(db, tt.params, tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("Movies() failed: %v", err)
			}
			if total != tt.wantTotal {
				t.Errorf("Movies() total: got %d, want %d", total, tt.wantTotal)
			}
			if len(items) != tt.wantCount {
				t.Errorf("Movies() count: got %d, want %d", len(items), tt.wantCount)
			}
			if tt.wantFirst != "" && len(items) > 0 {
				if items[0].Title != tt.wantFirst {
					t.Errorf("Movies() first title: got %q, want %q", items[0].Title, tt.wantFirst)
				}
			}
			// Verify watched flag
			for _, item := range items {
				if item.MovieID == "m1" {
					if !item.Watched {
						t.Errorf("Movie m1 should be marked as watched")
					}
					if item.WatchCount != 2 {
						t.Errorf("Movie m1 watch count: got %d, want 2", item.WatchCount)
					}
				}
			}
		})
	}
}

func TestPeople(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	tests := []struct {
		name      string
		params    QueryParams
		limit     int
		offset    int
		wantTotal int
		wantCount int
	}{
		{
			name:      "list all people",
			params:    QueryParams{},
			limit:     10,
			offset:    0,
			wantTotal: 3,
			wantCount: 3,
		},
		{
			name:      "search by name",
			params:    QueryParams{Query: "Test"},
			limit:     10,
			offset:    0,
			wantTotal: 2,
			wantCount: 2,
		},
		{
			name:      "pagination",
			params:    QueryParams{},
			limit:     2,
			offset:    1,
			wantTotal: 3,
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, items, err := People(db, tt.params, tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("People() failed: %v", err)
			}
			if total != tt.wantTotal {
				t.Errorf("People() total: got %d, want %d", total, tt.wantTotal)
			}
			if len(items) != tt.wantCount {
				t.Errorf("People() count: got %d, want %d", len(items), tt.wantCount)
			}

			// Verify movie counts for p1
			for _, item := range items {
				if item.PersonID == "p1" {
					if item.MovieCount != 2 {
						t.Errorf("Person p1 movie count: got %d, want 2", item.MovieCount)
					}
				}
			}
		})
	}
}

func TestMovieDetail(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	detail, err := MovieDetail(db, "m1")
	if err != nil {
		t.Fatalf("MovieDetail() failed: %v", err)
	}

	movie, ok := detail["movie"].(MovieItem)
	if !ok {
		t.Fatalf("MovieDetail() movie not found or wrong type")
	}
	if movie.MovieID != "m1" {
		t.Errorf("MovieDetail() movie ID: got %q, want %q", movie.MovieID, "m1")
	}
	if movie.Title != "Test Movie 1" {
		t.Errorf("MovieDetail() title: got %q, want %q", movie.Title, "Test Movie 1")
	}
	if !movie.Watched {
		t.Error("MovieDetail() movie should be marked as watched")
	}
	if movie.WatchCount != 2 {
		t.Errorf("MovieDetail() watch count: got %d, want 2", movie.WatchCount)
	}

	links, ok := detail["links"].([]EdgeItem)
	if !ok {
		t.Fatalf("MovieDetail() links not found or wrong type")
	}
	if len(links) != 2 {
		t.Errorf("MovieDetail() links count: got %d, want 2", len(links))
	}

	watchEvents, ok := detail["watch_events"].([]WatchEventItem)
	if !ok {
		t.Fatalf("MovieDetail() watch_events not found or wrong type")
	}
	if len(watchEvents) != 2 {
		t.Errorf("MovieDetail() watch_events count: got %d, want 2", len(watchEvents))
	}
}

func TestPersonDetail(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	detail, err := PersonDetail(db, "p1")
	if err != nil {
		t.Fatalf("PersonDetail() failed: %v", err)
	}

	person, ok := detail["person"].(PersonItem)
	if !ok {
		t.Fatalf("PersonDetail() person not found or wrong type")
	}
	if person.PersonID != "p1" {
		t.Errorf("PersonDetail() person ID: got %q, want %q", person.PersonID, "p1")
	}
	if person.Name != "Test Person 1" {
		t.Errorf("PersonDetail() name: got %q, want %q", person.Name, "Test Person 1")
	}
	if person.MovieCount != 2 {
		t.Errorf("PersonDetail() movie count: got %d, want 2", person.MovieCount)
	}

	links, ok := detail["links"].([]EdgeItem)
	if !ok {
		t.Fatalf("PersonDetail() links not found or wrong type")
	}
	if len(links) != 2 {
		t.Errorf("PersonDetail() links count: got %d, want 2", len(links))
	}
}

func TestSetPersonFavorite(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Test setting favorite
	req := PreferenceRequest{
		Kind:        "person",
		TargetID:    "p1",
		TargetLabel: "Test Person 1",
		Favorite:    true,
		SignalType:  "person_affinity",
		Weight:      1.0,
		GeneratedBy: "viewer",
	}

	if err := SetPersonFavorite(db, req); err != nil {
		t.Fatalf("SetPersonFavorite() failed: %v", err)
	}

	// Verify preference was set
	count, err := PersonPreferenceCount(db, "p1")
	if err != nil {
		t.Fatalf("PersonPreferenceCount() failed: %v", err)
	}
	if count != 1 {
		t.Errorf("PersonPreferenceCount() after set: got %d, want 1", count)
	}

	// Test unsetting favorite
	req.Favorite = false
	if err := SetPersonFavorite(db, req); err != nil {
		t.Fatalf("SetPersonFavorite() unset failed: %v", err)
	}

	// Verify preference was unset
	count, err = PersonPreferenceCount(db, "p1")
	if err != nil {
		t.Fatalf("PersonPreferenceCount() after unset failed: %v", err)
	}
	if count != 0 {
		t.Errorf("PersonPreferenceCount() after unset: got %d, want 0", count)
	}
}

func TestWatchEvents(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	events, err := WatchEvents(db, "m1", 10)
	if err != nil {
		t.Fatalf("WatchEvents() failed: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("WatchEvents() count: got %d, want 2", len(events))
	}

	// Verify ordering (most recent first)
	if len(events) >= 2 {
		if events[0].EventID != "e2" {
			t.Errorf("WatchEvents() first event: got %q, want e2", events[0].EventID)
		}
	}
}

func TestEdges(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Test edges for movie
	edges, err := Edges(db, "movie_id", "m1", 100)
	if err != nil {
		t.Fatalf("Edges() for movie failed: %v", err)
	}
	if len(edges) != 2 {
		t.Errorf("Edges() for movie count: got %d, want 2", len(edges))
	}

	// Test edges for person
	edges, err = Edges(db, "person_id", "p1", 100)
	if err != nil {
		t.Fatalf("Edges() for person failed: %v", err)
	}
	if len(edges) != 2 {
		t.Errorf("Edges() for person count: got %d, want 2", len(edges))
	}

	// Test invalid column
	_, err = Edges(db, "invalid_column", "xxx", 100)
	if err == nil {
		t.Error("Edges() with invalid column should return error")
	}
}

func TestMovieDetailErrors(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Test empty ID
	_, err := MovieDetail(db, "")
	if err == nil {
		t.Error("MovieDetail() with empty ID should return error")
	}

	// Test non-existent ID
	_, err = MovieDetail(db, "non-existent")
	if err == nil {
		t.Error("MovieDetail() with non-existent ID should return error")
	}
}

func TestPersonDetailErrors(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Test empty ID
	_, err := PersonDetail(db, "")
	if err == nil {
		t.Error("PersonDetail() with empty ID should return error")
	}

	// Test non-existent ID
	_, err = PersonDetail(db, "non-existent")
	if err == nil {
		t.Error("PersonDetail() with non-existent ID should return error")
	}
}
