package webgather

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"github.com/PuerkitoBio/goquery"
	htmlnode "golang.org/x/net/html"
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
	doc.Find("script,style,noscript,svg,canvas,iframe,header,footer,nav,aside,form,button").Each(func(_ int, s *goquery.Selection) {
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
	isNHKArticle := strings.HasPrefix(artifact.FinalURL, "https://news.web.nhk/newsweb/na/")
	isAozoraWorkHTML := isAozoraWorkHTMLURL(artifact.FinalURL)
	text := ""
	if isNHKArticle {
		text = extractNHKArticleText(doc)
	} else if isAozoraWorkHTML {
		text, err = extractAozoraMainText(doc)
		if err != nil {
			return modulewebgather.ExtractedDocument{}, err
		}
	} else {
		text = extractBestHTMLContent(doc)
		text = normalizeSpace(text)
	}
	if text == "" {
		return modulewebgather.ExtractedDocument{}, modulewebgather.NewError(modulewebgather.ErrEmptyContent, "HTML extracted text is empty")
	}
	extractor := "html_basic"
	if isNHKArticle {
		extractor = "nhk_news_article"
	} else if isAozoraWorkHTML {
		extractor = "aozora_main_text"
	}
	if requestedExtractor == "go_readability" && !isNHKArticle && !isAozoraWorkHTML {
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

func extractBestHTMLContent(doc *goquery.Document) string {
	semantic := longestSelectionText(doc, "[itemprop='articleBody'],.article-body,.post-content,.entry-content,.blog-content,article")
	if len([]rune(semantic)) >= 200 {
		return semantic
	}
	mainText := longestSelectionText(doc, "main,[role='main']")
	if len([]rune(mainText)) > 2*len([]rune(semantic)) {
		return mainText
	}
	if semantic != "" {
		return semantic
	}
	if mainText != "" {
		return mainText
	}
	return selectionText(doc.Find("body").First())
}

func isAozoraWorkHTMLURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User != nil || parsed.Port() != "" ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		!strings.EqualFold(parsed.Hostname(), "www.aozora.gr.jp") {
		return false
	}
	segments := strings.Split(parsed.Path, "/")
	if len(segments) != 5 || segments[0] != "" || segments[1] != "cards" ||
		segments[3] != "files" || segments[2] == "" || segments[4] == "" {
		return false
	}
	for _, digit := range segments[2] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return strings.EqualFold(filepath.Ext(segments[4]), ".html")
}

func extractAozoraMainText(doc *goquery.Document) (string, error) {
	if doc == nil {
		return "", modulewebgather.NewError(modulewebgather.ErrEmptyContent, "Aozora main_text is missing")
	}
	mainText := doc.Find(".main_text")
	if mainText.Length() != 1 {
		return "", modulewebgather.NewError(modulewebgather.ErrEmptyContent, "Aozora main_text must appear exactly once")
	}
	var extracted strings.Builder
	appendAozoraMainText(mainText.Nodes[0], &extracted)
	text := normalizeSpace(extracted.String())
	if text == "" {
		return "", modulewebgather.NewError(modulewebgather.ErrEmptyContent, "Aozora main_text is empty")
	}
	return text, nil
}

func appendAozoraMainText(node *htmlnode.Node, extracted *strings.Builder) {
	if node == nil {
		return
	}
	if node.Type == htmlnode.TextNode {
		extracted.WriteString(node.Data)
		return
	}
	if node.Type == htmlnode.ElementNode {
		switch strings.ToLower(node.Data) {
		case "rt", "rp":
			return
		case "br":
			extracted.WriteByte(' ')
			return
		case "img":
			for _, attr := range node.Attr {
				if strings.EqualFold(attr.Key, "alt") && strings.TrimSpace(attr.Val) != "" {
					extracted.WriteString(attr.Val)
					extracted.WriteByte(' ')
				}
			}
			return
		}
		if isAozoraBlockElement(node.Data) {
			extracted.WriteByte(' ')
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendAozoraMainText(child, extracted)
	}
	if node.Type == htmlnode.ElementNode && isAozoraBlockElement(node.Data) {
		extracted.WriteByte(' ')
	}
}

func isAozoraBlockElement(tag string) bool {
	switch strings.ToLower(tag) {
	case "blockquote", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "ol", "p", "pre", "ul":
		return true
	default:
		return false
	}
}

func longestSelectionText(doc *goquery.Document, selector string) string {
	best := ""
	doc.Find(selector).Each(func(_ int, candidate *goquery.Selection) {
		text := selectionText(candidate)
		if len([]rune(text)) > len([]rune(best)) {
			best = text
		}
	})
	return best
}

func extractNHKArticleText(doc *goquery.Document) string {
	articleParagraphClasses := map[string]struct{}{}
	doc.Find("main p").Each(func(_ int, paragraph *goquery.Selection) {
		if paragraph.Find(".c-part").Length() == 0 && paragraph.Closest(".c-part").Length() == 0 {
			return
		}
		if className := strings.TrimSpace(paragraph.AttrOr("class", "")); className != "" {
			articleParagraphClasses[className] = struct{}{}
		}
	})
	parts := []string{}
	doc.Find("main p").Each(func(_ int, paragraph *goquery.Selection) {
		isMarkedArticlePart := paragraph.Find(".c-part").Length() > 0 || paragraph.Closest(".c-part").Length() > 0
		_, sharesArticleClass := articleParagraphClasses[strings.TrimSpace(paragraph.AttrOr("class", ""))]
		if !isMarkedArticlePart && !sharesArticleClass {
			return
		}
		if text := normalizeSpace(paragraph.Text()); text != "" {
			parts = append(parts, text)
		}
	})
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
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
	if artifact.ProviderName == ArticleReaderProviderName {
		text = normalizeArticleReaderParagraphs(buf.String())
		extractor = "jina_reader_markdown"
	}
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

func normalizeArticleReaderParagraphs(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	paragraphs := strings.Split(text, "\n\n")
	cleaned := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph = normalizeSpace(paragraph); paragraph != "" {
			cleaned = append(cleaned, paragraph)
		}
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n\n"))
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
