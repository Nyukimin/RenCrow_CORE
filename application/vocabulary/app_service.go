package vocabulary

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	vocabDomain "github.com/Nyukimin/picoclaw_multiLLM/domain/vocabulary"
)

// AppService implements the vocabulary service
type AppService struct {
	repo      vocabDomain.Repository
	sources   []RSSSource
	extractor *TermExtractor
}

// RSSSource configuration for fetching news
type RSSSource struct {
	URL      string
	Name     string
	Category string
}

// NewAppService creates a new application service
func NewAppService(repo vocabDomain.Repository, sources []RSSSource) *AppService {
	return &AppService{
		repo:      repo,
		sources:   sources,
		extractor: NewTermExtractor(),
	}
}

// UpdateFromSources fetches from RSS sources and stores extracted terms
func (s *AppService) UpdateFromSources() (int, error) {
	var totalAdded int

	for _, source := range s.sources {
		headlines, err := s.fetchHeadlines(source.URL)
		if err != nil {
			log.Printf("Failed to fetch from %s: %v", source.Name, err)
			continue
		}

		for _, headline := range headlines {
			terms := s.extractor.ExtractTerms(headline)
			for _, term := range terms {
				entry := &vocabDomain.Entry{
					Term:        term.Term,
					Description: term.Description,
					Source:      source.Name,
					Timestamp:   time.Now(),
					Categories:  []string{source.Category},
				}

				if err := s.repo.Store(entry); err != nil {
					log.Printf("Failed to store term %s: %v", term.Term, err)
				} else {
					totalAdded++
				}
			}
		}
	}

	return totalAdded, nil
}

// GetContext formats recent entries for LLM injection
func (s *AppService) GetContext(maxEntries int) (string, error) {
	entries, err := s.repo.FindRecent(7) // Last 7 days
	if err != nil {
		return "", err
	}

	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}

	var builder strings.Builder
	builder.WriteString("Recent topics glossary:\n")
	for _, entry := range entries {
		builder.WriteString("- ")
		builder.WriteString(entry.Term)
		builder.WriteString(": ")
		builder.WriteString(entry.Description)
		builder.WriteString(" (")
		builder.WriteString(entry.Source)
		builder.WriteString(")\n")
	}

	return builder.String(), nil
}

// SearchTerms finds entries matching search terms
func (s *AppService) SearchTerms(terms []string) ([]*vocabDomain.Entry, error) {
	var results []*vocabDomain.Entry
	for _, term := range terms {
		found, err := s.repo.FindByTerm(term)
		if err != nil {
			return nil, err
		}
		results = append(results, found...)
	}
	return results, nil
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title string `xml:"title"`
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title string `xml:"title"`
}

// Helper methods
func (s *AppService) fetchHeadlines(url string) ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch headlines: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rss rssFeed
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, err
	}
	if titles := rss.titles(); len(titles) > 0 {
		return titles, nil
	}

	var atom atomFeed
	if err := xml.Unmarshal(body, &atom); err != nil {
		return nil, err
	}
	if titles := atom.titles(); len(titles) > 0 {
		return titles, nil
	}

	return nil, nil
}

func (f rssFeed) titles() []string {
	titles := make([]string, 0, len(f.Items))
	for _, item := range f.Items {
		title := strings.TrimSpace(item.Title)
		if title != "" {
			titles = append(titles, title)
		}
	}
	return titles
}

func (f atomFeed) titles() []string {
	titles := make([]string, 0, len(f.Entries))
	for _, entry := range f.Entries {
		title := strings.TrimSpace(entry.Title)
		if title != "" {
			titles = append(titles, title)
		}
	}
	return titles
}
