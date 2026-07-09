package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"gopkg.in/yaml.v3"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type WikiIndexStore interface {
	SaveWikiPageIndex(ctx context.Context, item l1sqlite.WikiPageIndexItem) (*l1sqlite.WikiPageIndexItem, error)
}

type WikiIndexOptions struct {
	RootDir  string
	RepoRoot string
	Now      func() time.Time
}

type WikiIndexResult struct {
	Indexed int
	Skipped int
}

type wikiFrontmatter struct {
	PageID          string   `yaml:"page_id"`
	Type            string   `yaml:"type"`
	Status          string   `yaml:"status"`
	Owner           string   `yaml:"owner"`
	CanonicalSource string   `yaml:"canonical_source"`
	Source          []string `yaml:"source"`
	Related         []string `yaml:"related"`
	Summary         string   `yaml:"summary"`
	Updated         string   `yaml:"updated"`
}

func IndexKnowledgeWiki(ctx context.Context, store WikiIndexStore, opts WikiIndexOptions) (WikiIndexResult, error) {
	if store == nil {
		return WikiIndexResult{}, fmt.Errorf("wiki index store is required")
	}
	rootDir := strings.TrimSpace(opts.RootDir)
	if rootDir == "" {
		rootDir = filepath.Join("docs", "wiki")
	}
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	var result WikiIndexResult
	err := filepath.WalkDir(rootDir, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			result.Skipped++
			return nil
		}
		item, err := wikiPageIndexItemFromFile(filePath, repoRoot, now)
		if err != nil {
			return err
		}
		if _, err := store.SaveWikiPageIndex(ctx, item); err != nil {
			return err
		}
		result.Indexed++
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("failed to index knowledge wiki: %w", err)
	}
	return result, nil
}

func wikiPageIndexItemFromFile(filePath string, repoRoot string, now time.Time) (l1sqlite.WikiPageIndexItem, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return l1sqlite.WikiPageIndexItem{}, fmt.Errorf("failed to read wiki page %s: %w", filePath, err)
	}
	fm, body, err := parseWikiFrontmatter(data)
	if err != nil {
		return l1sqlite.WikiPageIndexItem{}, fmt.Errorf("invalid wiki page %s: %w", filePath, err)
	}
	relPath, err := filepath.Rel(repoRoot, filePath)
	if err != nil {
		return l1sqlite.WikiPageIndexItem{}, fmt.Errorf("failed to resolve wiki page path %s: %w", filePath, err)
	}
	relPath = filepath.ToSlash(relPath)
	title := firstMarkdownHeading(body)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	summary := strings.TrimSpace(fm.Summary)
	if summary == "" {
		summary = firstMarkdownParagraph(body)
	}
	updatedAt := now
	if strings.TrimSpace(fm.Updated) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(fm.Updated))
		if err != nil {
			return l1sqlite.WikiPageIndexItem{}, fmt.Errorf("invalid updated date: %w", err)
		}
		updatedAt = parsed.UTC()
	}
	pageID := strings.TrimSpace(fm.PageID)
	if pageID == "" {
		pageID = derivedWikiPageID(fm.Type, relPath)
	}
	sum := sha256.Sum256(data)
	return l1sqlite.WikiPageIndexItem{
		PageID:          pageID,
		Path:            relPath,
		Title:           title,
		Type:            strings.TrimSpace(fm.Type),
		Status:          strings.TrimSpace(fm.Status),
		Owner:           strings.TrimSpace(fm.Owner),
		CanonicalSource: strings.TrimSpace(fm.CanonicalSource),
		SourcePaths:     append([]string(nil), fm.Source...),
		Related:         append([]string(nil), fm.Related...),
		Summary:         summary,
		ContentHash:     hex.EncodeToString(sum[:]),
		CreatedAt:       now,
		UpdatedAt:       updatedAt,
	}, nil
}

func parseWikiFrontmatter(data []byte) (wikiFrontmatter, string, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return wikiFrontmatter{}, "", fmt.Errorf("frontmatter is required")
	}
	rest := strings.TrimPrefix(text, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return wikiFrontmatter{}, "", fmt.Errorf("frontmatter end marker is required")
	}
	rawFrontmatter := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---"):], "\r\n")
	var fm wikiFrontmatter
	if err := yaml.Unmarshal([]byte(rawFrontmatter), &fm); err != nil {
		return wikiFrontmatter{}, "", err
	}
	return fm, body, nil
}

func firstMarkdownHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func firstMarkdownParagraph(body string) string {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "```") {
			continue
		}
		runes := []rune(line)
		if len(runes) > 240 {
			return string(runes[:240])
		}
		return line
	}
	return ""
}

func derivedWikiPageID(pageType string, relPath string) string {
	pageType = strings.TrimSpace(pageType)
	if pageType == "" {
		pageType = "page"
	}
	base := filepath.Base(relPath)
	slug := strings.TrimSuffix(base, filepath.Ext(base))
	return pageType + ":" + slug
}
