package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"golang.org/x/text/unicode/norm"
)

var (
	errRoutineRestBookUnavailable   = errors.New("休憩読書の本文を利用できません")
	errRoutineRestBookChanged       = errors.New("休憩読書の本文が前回の取得から変更されました")
	errRoutineRestBookFinished      = errors.New("休憩読書の本を読み終えました")
	errRoutineRestBookCursorInvalid = errors.New("休憩読書の読み位置が不正です")
)

const (
	routineRestBookTitle             = "注文の多い料理店"
	routineRestBookAuthor            = "宮沢賢治"
	routineRestBookSourceURL         = "https://www.aozora.gr.jp/cards/000081/card43754.html"
	routineRestBookTextURL           = "https://www.aozora.gr.jp/cards/000081/files/43754_17659.html"
	routineRestBookRightsURL         = "https://www.aozora.gr.jp/guide/kijyunn.html"
	routineRestBookSourceID          = "aozora:000081:43754"
	routineRestBookNamespace         = "routine-rest-reading"
	routineRestBookMinimumChunkRunes = 1000
	routineRestBookMaximumChunkRunes = 1800
	routineRestBookMinimumBodyRunes  = 5000
	routineRestBookMaxBodyBytes      = 5 << 20
)

type routineRestBookSource struct {
	id          string
	title       string
	author      string
	sourceURL   string
	textURL     string
	rightsURL   string
	openingText []string
}

var routineRestBookCatalog = []routineRestBookSource{
	{
		id: routineRestBookSourceID, title: routineRestBookTitle, author: routineRestBookAuthor,
		sourceURL: routineRestBookSourceURL,
		textURL:   routineRestBookTextURL,
		rightsURL: routineRestBookRightsURL,
		// Ruby readings can appear inline in ExtractedText; verify stable base-word anchors.
		openingText: []string{"二人", "紳士", "イギリス"},
	},
	{
		id: "aozora:000148:789", title: "吾輩は猫である", author: "夏目漱石",
		sourceURL:   "https://www.aozora.gr.jp/cards/000148/card789.html",
		textURL:     "https://www.aozora.gr.jp/cards/000148/files/789_14547.html",
		rightsURL:   "https://www.aozora.gr.jp/guide/kijyunn.html",
		openingText: []string{"吾輩は猫である", "名前はまだ無い"},
	},
}

type routineRestReadCursor struct {
	SourceURL string `json:"source_url,omitempty"`
	BodyHash  string `json:"body_hash,omitempty"`
	NextRune  int    `json:"next_rune,omitempty"`
	BodyRunes int    `json:"body_runes,omitempty"`
}

type routineRestBookChunk struct {
	Title     string `json:"title"`
	Author    string `json:"author"`
	SourceURL string `json:"source_url"`
	RightsURL string `json:"rights_url"`
	BodyHash  string `json:"body_hash"`
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
	NextRune  int    `json:"next_rune"`
	BodyRunes int    `json:"body_runes"`
	Text      string `json:"-"`
}

func (c routineRestBookChunk) String() string {
	return fmt.Sprintf("routineRestBookChunk{title:%q author:%q source:%q range:%d..%d body_hash:%q text_runes:%d}",
		c.Title, c.Author, c.SourceURL, c.StartRune, c.EndRune, c.BodyHash, utf8.RuneCountInString(c.Text))
}

func (c routineRestBookChunk) GoString() string {
	return c.String()
}

type routineRestReader interface {
	FetchNext(ctx context.Context, cursor routineRestReadCursor) (routineRestBookChunk, error)
}

type routineRestBookReader struct {
	fetcher tools.WebGatherFetcher
}

func newRoutineRestBookReader(fetcher tools.WebGatherFetcher) routineRestReader {
	return &routineRestBookReader{fetcher: fetcher}
}

func (r *routineRestBookReader) FetchNext(ctx context.Context, cursor routineRestReadCursor) (routineRestBookChunk, error) {
	if r == nil || r.fetcher == nil || ctx == nil {
		return routineRestBookChunk{}, errRoutineRestBookUnavailable
	}
	source, err := routineRestBookSourceForCursor(cursor)
	if err != nil {
		return routineRestBookChunk{}, err
	}

	response, err := r.fetcher.FetchURL(ctx, routineRestBookFetchRequest(source))
	if err != nil {
		return routineRestBookChunk{}, fmt.Errorf("%w: WebGather fetch failed: %w", errRoutineRestBookUnavailable, err)
	}
	text, err := validateRoutineRestBookResponse(response, source)
	if err != nil {
		return routineRestBookChunk{}, err
	}
	bodyHash := routineRestBookBodyHash(text)
	if cursor.BodyHash != "" && cursor.BodyHash != bodyHash {
		return routineRestBookChunk{}, fmt.Errorf("%w: %s", errRoutineRestBookChanged, source.sourceURL)
	}

	runes := []rune(text)
	if cursor.BodyRunes > 0 && cursor.BodyRunes != len(runes) {
		return routineRestBookChunk{}, fmt.Errorf("%w: stored body rune count changed", errRoutineRestBookCursorInvalid)
	}
	if cursor.NextRune > len(runes) {
		return routineRestBookChunk{}, fmt.Errorf("%w: next rune exceeds body length", errRoutineRestBookCursorInvalid)
	}
	if cursor.NextRune == len(runes) {
		return routineRestBookChunk{}, errRoutineRestBookFinished
	}

	endRune := routineRestBookChunkEnd(runes, cursor.NextRune)
	if endRune <= cursor.NextRune {
		return routineRestBookChunk{}, errRoutineRestBookUnavailable
	}
	return routineRestBookChunk{
		Title: source.title, Author: source.author, SourceURL: source.sourceURL, RightsURL: source.rightsURL,
		BodyHash: bodyHash, StartRune: cursor.NextRune, EndRune: endRune, NextRune: endRune, BodyRunes: len(runes),
		Text: string(runes[cursor.NextRune:endRune]),
	}, nil
}

func routineRestBookSourceForCursor(cursor routineRestReadCursor) (routineRestBookSource, error) {
	if cursor.NextRune < 0 || cursor.BodyRunes < 0 ||
		(cursor.NextRune > 0 && cursor.BodyHash == "") ||
		(cursor.BodyHash != "" && cursor.SourceURL == "") ||
		(cursor.BodyRunes > 0 && (cursor.BodyHash == "" || cursor.SourceURL == "" || cursor.NextRune > cursor.BodyRunes)) {
		return routineRestBookSource{}, fmt.Errorf("%w: incomplete source/hash/offset tuple", errRoutineRestBookCursorInvalid)
	}
	if cursor.SourceURL == "" {
		if cursor.NextRune != 0 || cursor.BodyHash != "" {
			return routineRestBookSource{}, fmt.Errorf("%w: initial cursor must not carry a progress offset", errRoutineRestBookCursorInvalid)
		}
		return routineRestBookCatalog[0], nil
	}
	for _, source := range routineRestBookCatalog {
		if cursor.SourceURL == source.sourceURL {
			return source, nil
		}
	}
	return routineRestBookSource{}, fmt.Errorf("%w: source is not in the fixed reading catalog", errRoutineRestBookCursorInvalid)
}

func nextRoutineRestBookCursor(cursor routineRestReadCursor) (routineRestReadCursor, bool) {
	if cursor.BodyHash == "" || cursor.BodyRunes <= 0 || cursor.NextRune != cursor.BodyRunes {
		return routineRestReadCursor{}, false
	}
	for i, source := range routineRestBookCatalog {
		if cursor.SourceURL == source.sourceURL && i+1 < len(routineRestBookCatalog) {
			return routineRestReadCursor{SourceURL: routineRestBookCatalog[i+1].sourceURL}, true
		}
	}
	return routineRestReadCursor{}, false
}

func routineRestBookFetchRequest(source routineRestBookSource) modulewebgather.FetchRequest {
	return modulewebgather.FetchRequest{
		URL: source.textURL, Namespace: routineRestBookNamespace, SourceID: source.id,
		FetchProvider: modulewebgather.DefaultFetchProvider, Extractor: modulewebgather.DefaultExtractor,
		StoreStaging: false, StoreStagingSet: true, Refresh: true, DryRun: true,
		LicenseNote: modulewebgather.DefaultLicenseNote, Policy: routineRestBookFetchPolicy(),
	}
}

func routineRestBookFetchPolicy() modulewebgather.FetchPolicy {
	return modulewebgather.FetchPolicy{
		RequestTimeout: 20 * time.Second, MaxBodyBytes: routineRestBookMaxBodyBytes,
		MaxRedirects: 3, AllowLocalhost: false,
	}
}

func validateRoutineRestBookResponse(response modulewebgather.FetchResponse, source routineRestBookSource) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(response.Status), "ok") || response.HTTPStatus != 200 ||
		strings.TrimSpace(response.URL) == "" || strings.TrimSpace(response.FinalURL) == "" ||
		response.ErrorCode != "" || strings.TrimSpace(response.ErrorMessage) != "" ||
		len(response.SecurityWarnings) != 0 || response.StagingID != "" {
		return "", errRoutineRestBookUnavailable
	}
	expectedURL, err := modulewebgather.NormalizeURL(source.textURL, false)
	if err != nil {
		return "", errRoutineRestBookUnavailable
	}
	for _, actualURL := range []string{response.URL, response.FinalURL} {
		normalizedURL, normalizeErr := modulewebgather.NormalizeURL(actualURL, false)
		if normalizeErr != nil || normalizedURL != expectedURL {
			return "", errRoutineRestBookUnavailable
		}
	}
	if response.ContentType != "" {
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.ContentType, ";")[0]))
		if contentType != "text/html" && contentType != "application/xhtml+xml" && contentType != "text/plain" {
			return "", errRoutineRestBookUnavailable
		}
	}
	if !utf8.ValidString(response.ExtractedText) {
		return "", errRoutineRestBookUnavailable
	}
	text := normalizeRoutineRestBookText(response.ExtractedText)
	runes := []rune(text)
	if len(runes) < routineRestBookMinimumBodyRunes || response.RawBytes <= 0 ||
		response.RawBytes > routineRestBookMaxBodyBytes ||
		response.ExtractedChars != len([]rune(response.ExtractedText)) {
		return "", errRoutineRestBookUnavailable
	}
	openingEnd := len(runes)
	if openingEnd > 512 {
		openingEnd = 512
	}
	opening := string(runes[:openingEnd])
	openingOffset := 0
	for _, marker := range source.openingText {
		markerOffset := strings.Index(opening[openingOffset:], marker)
		if markerOffset < 0 {
			return "", errRoutineRestBookUnavailable
		}
		openingOffset += markerOffset + len(marker)
	}
	return text, nil
}

func normalizeRoutineRestBookText(text string) string {
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	text = strings.NewReplacer(string([]byte{13, 10}), string([]byte{10}), string([]byte{13}), string([]byte{10})).Replace(text)
	return strings.TrimSpace(norm.NFC.String(text))
}

func routineRestBookBodyHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func routineRestBookChunkEnd(runes []rune, start int) int {
	end := start + routineRestBookMaximumChunkRunes
	if end > len(runes) {
		return len(runes)
	}
	minimumEnd := start + routineRestBookMinimumChunkRunes
	for i := end; i > minimumEnd; i-- {
		switch runes[i-1] {
		case 10, '。', '！', '？', '!', '?':
			return i
		}
	}
	return end
}

var _ routineRestReader = (*routineRestBookReader)(nil)
