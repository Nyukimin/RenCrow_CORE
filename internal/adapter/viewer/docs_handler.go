package viewer

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const docsSearchMaxFileBytes = 512 * 1024

type DocsSearchResult struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Score   int    `json:"score"`
}

type DocsDetail struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type docsRoot struct {
	Repo string
	Dir  string
}

func HandleDocsSearch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		limit, ok := parseOptionalLimit(w, r, 20)
		if !ok {
			return
		}
		results, err := searchRenCrowDocs(query, limit)
		if err != nil {
			http.Error(w, "failed to search docs", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": results})
	}
}

func HandleDocsDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		detail, err := readRenCrowDoc(id)
		if err != nil {
			http.Error(w, "doc not found", http.StatusNotFound)
			return
		}
		writeMonitorJSON(w, map[string]any{"doc": detail})
	}
}

func searchRenCrowDocs(query string, limit int) ([]DocsSearchResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	terms := docsSearchTerms(query)
	roots := discoverRenCrowDocsRoots()
	results := make([]DocsSearchResult, 0, limit)
	for _, root := range roots {
		err := filepath.WalkDir(root.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.ToLower(filepath.Ext(path)) != ".md" {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > docsSearchMaxFileBytes {
				return nil
			}
			contentBytes, err := os.ReadFile(path)
			if err != nil || !utf8.Valid(contentBytes) {
				return nil
			}
			content := string(contentBytes)
			rel, _ := filepath.Rel(root.Dir, path)
			docPath := filepath.ToSlash(filepath.Join("docs", rel))
			title := docsTitle(content, docPath)
			score := docsScore(docPath, title, content, terms)
			if len(terms) > 0 && score == 0 {
				return nil
			}
			results = append(results, DocsSearchResult{
				ID:      docsID(root.Repo, docPath),
				Repo:    root.Repo,
				Path:    docPath,
				Title:   title,
				Snippet: docsSnippet(content, terms),
				Score:   score,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Repo != results[j].Repo {
			return results[i].Repo < results[j].Repo
		}
		return results[i].Path < results[j].Path
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func readRenCrowDoc(id string) (DocsDetail, error) {
	repo, docPath, ok := strings.Cut(id, ":")
	if !ok || repo == "" || docPath == "" {
		return DocsDetail{}, fmt.Errorf("invalid doc id")
	}
	for _, root := range discoverRenCrowDocsRoots() {
		if root.Repo != repo {
			continue
		}
		rel := strings.TrimPrefix(filepath.FromSlash(docPath), "docs"+string(filepath.Separator))
		path := filepath.Join(root.Dir, rel)
		cleanRoot, cleanPath := filepath.Clean(root.Dir), filepath.Clean(path)
		if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
			return DocsDetail{}, fmt.Errorf("invalid doc path")
		}
		contentBytes, err := os.ReadFile(cleanPath)
		if err != nil || !utf8.Valid(contentBytes) {
			return DocsDetail{}, fmt.Errorf("read doc: %w", err)
		}
		content := string(contentBytes)
		return DocsDetail{
			ID:      id,
			Repo:    repo,
			Path:    docPath,
			Title:   docsTitle(content, docPath),
			Content: content,
		}, nil
	}
	return DocsDetail{}, fmt.Errorf("doc root not found")
}

func discoverRenCrowDocsRoots() []docsRoot {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	candidates := []struct {
		repo string
		dir  string
	}{
		{"RenCrow", filepath.Join(wd, "docs")},
	}
	umbrella := filepath.Dir(wd)
	for _, repo := range []string{
		"RenCrow_CORE",
		"RenCrow_CMD",
		"RenCrow_STT",
		"RenCrow_TTS",
		"RenCrow_LLM",
		"RenCrow_Vision",
		"RenCrow_Tools",
	} {
		candidates = append(candidates, struct {
			repo string
			dir  string
		}{repo: repo, dir: filepath.Join(umbrella, repo, "docs")})
	}
	seen := make(map[string]struct{})
	roots := make([]docsRoot, 0, len(candidates))
	for _, c := range candidates {
		dir, err := filepath.Abs(c.dir)
		if err != nil {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[dir] = struct{}{}
		roots = append(roots, docsRoot{Repo: c.repo, Dir: dir})
	}
	return roots
}

func docsSearchTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@"))))
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, " \t\r\n\"'`")
		if field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

func docsScore(path, title, content string, terms []string) int {
	if len(terms) == 0 {
		return 1
	}
	lowerPath := strings.ToLower(path)
	lowerTitle := strings.ToLower(title)
	lowerContent := strings.ToLower(content)
	score := 0
	for _, term := range terms {
		if strings.Contains(lowerTitle, term) {
			score += 20
		}
		if strings.Contains(lowerPath, term) {
			score += 12
		}
		score += strings.Count(lowerContent, term)
	}
	if strings.Contains(path, "01_正本仕様") {
		score += 5
	}
	if strings.Contains(path, "RenCrow") {
		score += 3
	}
	return score
}

func docsTitle(content, fallbackPath string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return strings.TrimSuffix(filepath.Base(fallbackPath), filepath.Ext(fallbackPath))
}

func docsSnippet(content string, terms []string) string {
	lines := strings.Split(content, "\n")
	best := 0
	if len(terms) > 0 {
		for i, line := range lines {
			lower := strings.ToLower(line)
			for _, term := range terms {
				if strings.Contains(lower, term) {
					best = i
					goto found
				}
			}
		}
	}
found:
	start := best - 1
	if start < 0 {
		start = 0
	}
	end := start + 3
	if end > len(lines) {
		end = len(lines)
	}
	snippet := strings.Join(lines[start:end], " ")
	snippet = strings.Join(strings.Fields(snippet), " ")
	runes := []rune(snippet)
	if len(runes) > 220 {
		snippet = string(runes[:220]) + "..."
	}
	return snippet
}

func docsID(repo, path string) string {
	return repo + ":" + filepath.ToSlash(path)
}

func writeDocsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
