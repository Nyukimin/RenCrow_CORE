package webgather

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"
)

type BasicExtractor struct{}

func NewBasicExtractor() *BasicExtractor {
	return &BasicExtractor{}
}

func (e *BasicExtractor) Extract(ctx context.Context, artifact modulewebgather.FetchArtifact, requestedExtractor string) (modulewebgather.ExtractedDocument, error) {
	select {
	case <-ctx.Done():
		return modulewebgather.ExtractedDocument{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "extract canceled", ctx.Err())
	default:
	}
	ct := strings.ToLower(strings.TrimSpace(artifact.ContentType))
	switch ct {
	case "text/html", "application/xhtml+xml", "":
		return extractHTML(artifact, requestedExtractor)
	case "text/plain", "text/markdown":
		return extractPlain(artifact, "plain_text")
	case "application/json", "application/ld+json":
		return extractJSON(artifact)
	default:
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrUnsupportedContentType, "unsupported content type: "+artifact.ContentType)
	}
}

func extractHTML(artifact modulewebgather.FetchArtifact, requestedExtractor string) (modulewebgather.ExtractedDocument, error) {
	reader, err := charset.NewReader(bytes.NewReader(artifact.Body), artifact.ContentType)
	if err != nil {
		reader = bytes.NewReader(artifact.Body)
	}
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return modulewebgather.ExtractedDocument{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "failed to parse HTML", err)
	}
	doc.Find("script,style,noscript,svg,canvas,iframe").Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if title == "" {
		title = attrContent(doc, `meta[property="og:title"]`)
	}
	excerpt := firstNonEmpty(attrContent(doc, `meta[name="description"]`), attrContent(doc, `meta[property="og:description"]`))
	canonical := firstNonEmpty(attrHref(doc, `link[rel="canonical"]`), artifact.FinalURL)
	published := firstNonEmpty(attrContent(doc, `meta[property="article:published_time"]`), attrContent(doc, `meta[name="date"]`))
	siteName := attrContent(doc, `meta[property="og:site_name"]`)
	text := selectionText(doc.Find("article").First())
	if text == "" {
		text = selectionText(doc.Find("main").First())
	}
	if text == "" {
		text = selectionText(doc.Find("body").First())
	}
	text = normalizeSpace(text)
	if text == "" {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrEmptyContent, "HTML extracted text is empty")
	}
	extractor := "html_basic"
	if requestedExtractor == "go_readability" {
		extractor = "html_basic"
	}
	return modulewebgather.ExtractedDocument{
		Text:         text,
		Title:        title,
		Excerpt:      excerpt,
		SiteName:     siteName,
		CanonicalURL: canonical,
		Keywords:     []string{"web_gather", "html"},
		Extractor:    extractor,
		Meta: map[string]any{
			"html_extractor":           extractor,
			"requested_html_extractor": requestedExtractor,
			"published_at_text":        published,
		},
	}, nil
}

func extractPlain(artifact modulewebgather.FetchArtifact, extractor string) (modulewebgather.ExtractedDocument, error) {
	reader, err := charset.NewReader(bytes.NewReader(artifact.Body), artifact.ContentType)
	if err != nil {
		reader = bytes.NewReader(artifact.Body)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(reader); err != nil {
		return modulewebgather.ExtractedDocument{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "failed to read text", err)
	}
	text := normalizeSpace(buf.String())
	if text == "" {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrEmptyContent, "plain text is empty")
	}
	return modulewebgather.ExtractedDocument{
		Text:      text,
		Excerpt:   modulewebgather.TextPreview(text, 240),
		Keywords:  []string{"web_gather", "text"},
		Extractor: extractor,
		Meta:      map[string]any{},
	}, nil
}

func extractJSON(artifact modulewebgather.FetchArtifact) (modulewebgather.ExtractedDocument, error) {
	if len(artifact.Body) > 1024*1024 {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrBodyTooLarge, "JSON body exceeded extractor limit")
	}
	var value any
	if err := json.Unmarshal(artifact.Body, &value); err != nil {
		return modulewebgather.ExtractedDocument{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "failed to parse JSON", err)
	}
	if containsDangerousJSONSecret(value) {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "JSON appears to contain secret material")
	}
	cleaned := redactJSON(value)
	pretty, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return modulewebgather.ExtractedDocument{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "failed to format JSON", err)
	}
	text := strings.TrimSpace(string(pretty))
	if text == "" || text == "null" {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrEmptyContent, "JSON extracted text is empty")
	}
	return modulewebgather.ExtractedDocument{
		Text:      text,
		Excerpt:   modulewebgather.TextPreview(text, 240),
		Keywords:  []string{"web_gather", "json"},
		Extractor: "json_text",
		Meta:      map[string]any{},
	}, nil
}

func attrContent(doc *goquery.Document, selector string) string {
	v, _ := doc.Find(selector).First().Attr("content")
	return strings.TrimSpace(v)
}

func attrHref(doc *goquery.Document, selector string) string {
	v, _ := doc.Find(selector).First().Attr("href")
	return strings.TrimSpace(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var whitespaceRE = regexp.MustCompile(`\s+`)

func selectionText(sel *goquery.Selection) string {
	if sel == nil || sel.Length() == 0 {
		return ""
	}
	parts := []string{}
	sel.Find("h1,h2,h3,h4,h5,h6,p,li,blockquote,pre").Each(func(_ int, child *goquery.Selection) {
		if text := normalizeSpace(child.Text()); text != "" {
			parts = append(parts, text)
		}
	})
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return normalizeSpace(sel.Text())
}

func normalizeSpace(text string) string {
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(text, " "))
}

func containsSecretLikeText(text string) bool {
	normalized := strings.ToLower(text)
	markers := []string{"api_key", "apikey", "authorization", "bearer ", "cookie", "set-cookie", "password", "secret", "token"}
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func containsDangerousJSONSecret(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "authorization") || strings.Contains(lk, "cookie") || strings.Contains(lk, "password") || strings.Contains(lk, "secret") || strings.Contains(lk, "api_key") || strings.Contains(lk, "apikey") {
				return true
			}
			if containsDangerousJSONSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsDangerousJSONSecret(child) {
				return true
			}
		}
	case string:
		normalized := strings.ToLower(v)
		return strings.Contains(normalized, "authorization") || strings.Contains(normalized, "bearer ") || strings.Contains(normalized, "set-cookie")
	}
	return false
}

func redactJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			lk := strings.ToLower(k)
			if lk == "html" || lk == "raw_html" || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "cookie") || strings.Contains(lk, "authorization") {
				out[k] = "[redacted]"
				continue
			}
			out[k] = redactJSON(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			out = append(out, redactJSON(child))
		}
		return out
	case string:
		if containsSecretLikeText(v) {
			return "[redacted]"
		}
		return v
	default:
		return fmt.Sprint(v)
	}
}
