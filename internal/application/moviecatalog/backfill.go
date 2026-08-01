package moviecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultBackfillInterval = 5 * time.Minute
	defaultBackfillTimeout  = 90 * time.Second
	defaultInitialDelay     = 10 * time.Second
	defaultCrawlerDelaySec  = 2
	maxBackfillPages        = 3
)

type BackfillOptions struct {
	DBPath       string
	Interval     time.Duration
	InitialDelay time.Duration
	Timeout      time.Duration
	MaxPages     int
	CrawlerDelay time.Duration
	Crawler      Crawler
}

type BackfillService struct {
	dbPath       string
	interval     time.Duration
	initialDelay time.Duration
	timeout      time.Duration
	maxPages     int
	crawlerDelay time.Duration
	crawler      Crawler
}

type BackfillTarget struct {
	Kind  string
	ID    string
	Title string
	URL   string
}

type BackfillResult struct {
	Status         string
	Target         BackfillTarget
	Output         string
	JobID          string
	ImportedMovies int
	ImportedPeople int
	ImportedEdges  int
}

func NewBackfillService(opts BackfillOptions) *BackfillService {
	if opts.Interval <= 0 {
		opts.Interval = defaultBackfillInterval
	}
	if opts.InitialDelay < 0 {
		opts.InitialDelay = 0
	} else if opts.InitialDelay == 0 {
		opts.InitialDelay = defaultInitialDelay
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultBackfillTimeout
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 1
	}
	if opts.MaxPages > maxBackfillPages {
		opts.MaxPages = maxBackfillPages
	}
	if opts.CrawlerDelay <= 0 {
		opts.CrawlerDelay = defaultCrawlerDelaySec * time.Second
	}
	if opts.Crawler == nil {
		opts.Crawler = NewConfiguredCrawler(opts.Timeout)
	}
	return &BackfillService{
		dbPath:       strings.TrimSpace(opts.DBPath),
		interval:     opts.Interval,
		initialDelay: opts.InitialDelay,
		timeout:      opts.Timeout,
		maxPages:     opts.MaxPages,
		crawlerDelay: opts.CrawlerDelay,
		crawler:      opts.Crawler,
	}
}

func (s *BackfillService) Start(ctx context.Context) <-chan BackfillResult {
	results := make(chan BackfillResult, 1)
	go func() {
		defer close(results)
		if s.initialDelay > 0 {
			timer := time.NewTimer(s.initialDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		s.runAndPublish(ctx, results)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runAndPublish(ctx, results)
			}
		}
	}()
	return results
}

func (s *BackfillService) runAndPublish(ctx context.Context, results chan<- BackfillResult) {
	result, err := s.RunOnce(ctx)
	if err != nil {
		if result.Status == "" {
			result.Status = "error"
		}
		result.Output = joinErrorOutput(result.Output, err)
	}
	select {
	case results <- result:
	case <-ctx.Done():
	}
}

func (s *BackfillService) RunOnce(ctx context.Context) (BackfillResult, error) {
	if s.dbPath == "" {
		return BackfillResult{Status: "idle"}, nil
	}
	db, err := sql.Open("sqlite", s.dbPath+"?_time_format=sqlite")
	if err != nil {
		return BackfillResult{Status: "error"}, err
	}
	defer db.Close()

	target, ok, err := NextBackfillTarget(ctx, db)
	if err != nil {
		return BackfillResult{Status: "error"}, err
	}
	if !ok {
		return BackfillResult{Status: "idle"}, nil
	}

	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return BackfillResult{Status: "error", Target: target}, err
	}
	result := BackfillResult{Status: "fetched", Target: target}
	crawlResult, err := s.crawler.Crawl(ctx, CrawlerRequest{
		RequestID:   strings.TrimSpace(target.Kind + ":" + target.ID),
		Kind:        target.Kind,
		URL:         target.URL,
		MaxPages:    s.maxPages,
		Delay:       s.crawlerDelay,
		ArtifactDir: filepath.Dir(s.dbPath),
	})
	if err != nil {
		if errors.Is(err, ErrCrawlerUnavailable) {
			result.Status = "unavailable"
		}
		return result, err
	}
	result.JobID = crawlResult.JobID
	result.Output = strings.TrimSpace(crawlResult.Output)
	if strings.TrimSpace(crawlResult.ArtifactPath) == "" {
		return result, fmt.Errorf("%w: crawler returned no staged artifact", ErrCrawlerProtocol)
	}
	defer os.Remove(crawlResult.ArtifactPath)
	imported, err := ImportJSONLFile(ctx, db, crawlResult.ArtifactPath, target.URL)
	if err != nil {
		result.Status = "error"
		return result, err
	}
	result.ImportedMovies = imported.Movies
	result.ImportedPeople = imported.People
	result.ImportedEdges = imported.Edges
	result.Output = joinImportOutput(result.Output, imported)
	return result, nil
}

func NextBackfillTarget(ctx context.Context, db *sql.DB) (BackfillTarget, bool, error) {
	target, ok, err := nextMovieTarget(ctx, db)
	if err != nil || ok {
		return target, ok, err
	}
	return nextPersonTarget(ctx, db)
}

func nextMovieTarget(ctx context.Context, db *sql.DB) (BackfillTarget, bool, error) {
	row := db.QueryRowContext(ctx, `
SELECT movie_id, COALESCE(movie_title, ''), movie_url
FROM movie_people
WHERE COALESCE(movie_id, '') != ''
  AND COALESCE(movie_url, '') != ''
  AND NOT EXISTS (SELECT 1 FROM movies WHERE movies.movie_id = movie_people.movie_id)
GROUP BY movie_id, movie_title, movie_url
ORDER BY COALESCE(movie_title, ''), movie_id
LIMIT 1`)
	return scanBackfillTarget(row, "movie")
}

func nextPersonTarget(ctx context.Context, db *sql.DB) (BackfillTarget, bool, error) {
	row := db.QueryRowContext(ctx, `
SELECT person_id, COALESCE(person_name, ''), person_url
FROM movie_people
WHERE COALESCE(person_id, '') != ''
  AND COALESCE(person_url, '') != ''
  AND NOT EXISTS (SELECT 1 FROM people WHERE people.person_id = movie_people.person_id)
GROUP BY person_id, person_name, person_url
ORDER BY COALESCE(person_name, ''), person_id
LIMIT 1`)
	return scanBackfillTarget(row, "person")
}

func scanBackfillTarget(row *sql.Row, kind string) (BackfillTarget, bool, error) {
	var target BackfillTarget
	target.Kind = kind
	if err := row.Scan(&target.ID, &target.Title, &target.URL); err != nil {
		if err == sql.ErrNoRows {
			return BackfillTarget{}, false, nil
		}
		return BackfillTarget{}, false, err
	}
	return target, true, nil
}

func joinErrorOutput(output string, err error) string {
	if err == nil {
		return strings.TrimSpace(output)
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	return output + "\n" + err.Error()
}

func joinImportOutput(output string, imported CatalogImportResult) string {
	line := fmt.Sprintf("imported movies=%d people=%d edges=%d", imported.Movies, imported.People, imported.Edges)
	if output = strings.TrimSpace(output); output != "" {
		return output + "\n" + line
	}
	return line
}

func LogBackfillResult(prefix string, result BackfillResult) {
	if result.Status == "idle" {
		log.Printf("%s idle: no missing movie/person links", prefix)
		return
	}
	if result.Target.URL == "" {
		log.Printf("%s %s: %s", prefix, result.Status, result.Output)
		return
	}
	log.Printf("%s %s kind=%s id=%s title=%q url=%s output=%s",
		prefix, result.Status, result.Target.Kind, result.Target.ID, result.Target.Title, result.Target.URL, result.Output)
}
