package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	webgatherapp "github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type routineRestReaderTestFetcher struct {
	response modulewebgather.FetchResponse
	err      error
	request  modulewebgather.FetchRequest
	calls    int
}

func (f *routineRestReaderTestFetcher) FetchURL(_ context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.calls++
	f.request = req
	return f.response, f.err
}

func routineRestReaderTestBody() string {
	paragraph := "二人の若い紳士が、すっかりイギリスの兵隊のかたちをして、ぴかぴかする鉄砲をかついで、白熊のような犬を二疋つれて、 "
	return "注文の多い料理店 " + strings.Repeat(paragraph, 240)
}

func routineRestReaderTestResponse(text string) modulewebgather.FetchResponse {
	return modulewebgather.FetchResponse{
		URL: routineRestBookTextURL, FinalURL: routineRestBookTextURL,
		Status: "ok", HTTPStatus: 200, ContentType: "text/html",
		Title: routineRestBookTitle, ExtractedText: text, RawBytes: int64(len(text)),
		ExtractedChars: len([]rune(text)), TextPreview: "preview-only-decoy",
	}
}

func TestRoutineRestBookReaderFetchesBoundedExtractedTextAndReturnsResumeMetadata(t *testing.T) {
	body := routineRestReaderTestBody()
	fetcher := &routineRestReaderTestFetcher{response: routineRestReaderTestResponse(body)}
	reader := newRoutineRestBookReader(fetcher)

	chunk, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
	if err != nil {
		t.Fatalf("FetchNext() error = %v", err)
	}
	if chunk.Title != routineRestBookTitle || chunk.Author != routineRestBookAuthor {
		t.Fatalf("bibliographic metadata = %q by %q", chunk.Title, chunk.Author)
	}
	if chunk.SourceURL != routineRestBookSourceURL || chunk.RightsURL != routineRestBookRightsURL {
		t.Fatalf("source and rights metadata = %q / %q", chunk.SourceURL, chunk.RightsURL)
	}
	if chunk.BodyHash == "" || chunk.StartRune != 0 || chunk.EndRune != chunk.NextRune ||
		chunk.BodyRunes <= chunk.NextRune || chunk.NextRune <= 0 {
		t.Fatalf("invalid read cursor metadata: %+v", chunk)
	}
	if got := len([]rune(chunk.Text)); got < routineRestBookMinimumChunkRunes || got > routineRestBookMaximumChunkRunes {
		t.Fatalf("chunk rune length = %d, want %d..%d", got, routineRestBookMinimumChunkRunes, routineRestBookMaximumChunkRunes)
	}
	if !strings.Contains(chunk.Text, "二人の若い紳士") || strings.Contains(chunk.Text, "preview-only-decoy") {
		t.Fatal("chunk did not come from the extracted book body")
	}
	if fetcher.calls != 1 {
		t.Fatalf("FetchURL calls = %d, want 1", fetcher.calls)
	}
	req := fetcher.request
	if req.URL != routineRestBookTextURL || req.Namespace != routineRestBookNamespace || req.SourceID != routineRestBookSourceID {
		t.Fatalf("WebGather source request = %+v", req)
	}
	if req.FetchProvider != modulewebgather.DefaultFetchProvider || req.Extractor != modulewebgather.DefaultExtractor {
		t.Fatalf("WebGather provider/extractor = %q / %q", req.FetchProvider, req.Extractor)
	}
	if req.StoreStaging || !req.StoreStagingSet || !req.Refresh || !req.DryRun {
		t.Fatalf("WebGather must refresh without staging/fetch-result-cache writes: %+v", req)
	}

	encoded, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("json.Marshal(chunk): %v", err)
	}
	if strings.Contains(string(encoded), chunk.Text) || strings.Contains(string(encoded), "二人の若い紳士") {
		t.Fatal("book body leaked into the JSON representation")
	}
}

func TestRoutineRestBookChunkStringRedactsText(t *testing.T) {
	chunk := routineRestBookChunk{Title: routineRestBookTitle, Text: "private-chunk-body"}
	if strings.Contains(chunk.String(), chunk.Text) {
		t.Fatal("routineRestBookChunk.String() leaked the body")
	}
}

func TestRoutineRestBookReaderResumesSameBodyAtNextRune(t *testing.T) {
	body := routineRestReaderTestBody()
	fetcher := &routineRestReaderTestFetcher{response: routineRestReaderTestResponse(body)}
	reader := newRoutineRestBookReader(fetcher)

	first, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
	if err != nil {
		t.Fatalf("first FetchNext() error = %v", err)
	}
	second, err := reader.FetchNext(context.Background(), routineRestReadCursor{
		SourceURL: first.SourceURL, BodyHash: first.BodyHash, NextRune: first.NextRune, BodyRunes: first.BodyRunes,
	})
	if err != nil {
		t.Fatalf("resumed FetchNext() error = %v", err)
	}
	if second.StartRune != first.NextRune || second.BodyHash != first.BodyHash {
		t.Fatalf("resume metadata = %+v, want start %d and same hash", second, first.NextRune)
	}
	runes := []rune(body)
	if got, want := second.Text, string(runes[second.StartRune:second.EndRune]); got != want {
		t.Fatal("resumed chunk does not match the exact source rune range")
	}
	if strings.HasSuffix(first.Text, second.Text) {
		t.Fatal("consecutive chunks unexpectedly repeat content")
	}
}

func TestRoutineRestBookReaderCanSelectSecondCatalogWork(t *testing.T) {
	source := routineRestBookCatalog[1]
	body := "吾輩は猫である。名前はまだ無い。" + strings.Repeat("本文。", 2000)
	response := routineRestReaderTestResponse(body)
	response.URL = source.textURL
	response.FinalURL = source.textURL
	response.Title = source.title
	fetcher := &routineRestReaderTestFetcher{response: response}

	chunk, err := newRoutineRestBookReader(fetcher).FetchNext(context.Background(), routineRestReadCursor{
		SourceURL: source.sourceURL,
	})
	if err != nil {
		t.Fatalf("FetchNext(second catalog source) error = %v", err)
	}
	if chunk.Title != source.title || chunk.Author != source.author || chunk.SourceURL != source.sourceURL ||
		chunk.RightsURL != source.rightsURL || fetcher.request.SourceID != source.id ||
		fetcher.request.URL != source.textURL {
		t.Fatalf("second-catalog metadata or request mismatch: chunk=%+v request=%+v", chunk, fetcher.request)
	}
	if !strings.Contains(chunk.Text, "吾輩は猫である") || !strings.Contains(chunk.Text, "名前はまだ無い") {
		t.Fatal("second-catalog chunk does not contain the selected work's opening text")
	}
}

func TestRoutineRestBookReaderRejectsChangedSourceAndCursor(t *testing.T) {
	t.Run("body changed", func(t *testing.T) {
		firstFetcher := &routineRestReaderTestFetcher{response: routineRestReaderTestResponse(routineRestReaderTestBody())}
		first, err := newRoutineRestBookReader(firstFetcher).FetchNext(context.Background(), routineRestReadCursor{})
		if err != nil {
			t.Fatalf("initial FetchNext() error = %v", err)
		}
		changed := routineRestReaderTestBody() + "追記"
		reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{response: routineRestReaderTestResponse(changed)})
		_, err = reader.FetchNext(context.Background(), routineRestReadCursor{
			SourceURL: first.SourceURL, BodyHash: first.BodyHash, NextRune: first.NextRune,
		})
		if !errors.Is(err, errRoutineRestBookChanged) {
			t.Fatalf("changed-body error = %v, want ErrRoutineRestBookChanged", err)
		}
	})
	t.Run("wrong source", func(t *testing.T) {
		reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{response: routineRestReaderTestResponse(routineRestReaderTestBody())})
		_, err := reader.FetchNext(context.Background(), routineRestReadCursor{
			SourceURL: "https://example.invalid/other-book", BodyHash: "sha256:unchanged", NextRune: 100,
		})
		if !errors.Is(err, errRoutineRestBookCursorInvalid) {
			t.Fatalf("wrong-source error = %v, want ErrRoutineRestBookCursorInvalid", err)
		}
	})
	t.Run("negative offset", func(t *testing.T) {
		reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{response: routineRestReaderTestResponse(routineRestReaderTestBody())})
		_, err := reader.FetchNext(context.Background(), routineRestReadCursor{
			SourceURL: routineRestBookSourceURL, BodyHash: "sha256:unchanged", NextRune: -1,
		})
		if !errors.Is(err, errRoutineRestBookCursorInvalid) {
			t.Fatalf("negative-offset error = %v, want ErrRoutineRestBookCursorInvalid", err)
		}
	})
}

func TestRoutineRestBookReaderRejectsPreviewOnlyAndUnavailableFetches(t *testing.T) {
	t.Run("preview only", func(t *testing.T) {
		response := routineRestReaderTestResponse("")
		response.TextPreview = "preview-only-decoy"
		reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{response: response})
		_, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
		if !errors.Is(err, errRoutineRestBookUnavailable) {
			t.Fatalf("preview-only error = %v, want ErrRoutineRestBookUnavailable", err)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		fetchErr := modulewebgather.NewError(modulewebgather.ErrFetchTimeout, "offline")
		fetcher := &routineRestReaderTestFetcher{err: fetchErr}
		reader := newRoutineRestBookReader(fetcher)
		_, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
		var typedErr *modulewebgather.Error
		if !errors.Is(err, errRoutineRestBookUnavailable) || !errors.Is(err, fetchErr) ||
			!errors.As(err, &typedErr) || typedErr.Code != modulewebgather.ErrFetchTimeout {
			t.Fatalf("fetch error did not preserve typed cause: %v", err)
		}
	})
	t.Run("unsafe response", func(t *testing.T) {
		response := routineRestReaderTestResponse(routineRestReaderTestBody())
		response.SecurityWarnings = []string{"untrusted input warning"}
		reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{response: response})
		_, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
		if !errors.Is(err, errRoutineRestBookUnavailable) {
			t.Fatalf("unsafe-response error = %v, want ErrRoutineRestBookUnavailable", err)
		}
	})
}

func TestRoutineRestBookReaderNormalizesBeforeHashAndRangeCalculation(t *testing.T) {
	body := "注文の多い料理店\r\n二人、紳士、イギリス。か\u3099。"
	precomposed := "注文の多い料理店\n二人、紳士、イギリス。が。"
	first, err := newRoutineRestBookReader(&routineRestReaderTestFetcher{
		response: routineRestReaderTestResponse(body + strings.Repeat("本文。", 2000)),
	}).FetchNext(context.Background(), routineRestReadCursor{})
	if err != nil {
		t.Fatalf("first FetchNext() error = %v", err)
	}
	second, err := newRoutineRestBookReader(&routineRestReaderTestFetcher{
		response: routineRestReaderTestResponse(precomposed + strings.Repeat("本文。", 2000)),
	}).FetchNext(context.Background(), routineRestReadCursor{})
	if err != nil {
		t.Fatalf("second FetchNext() error = %v", err)
	}
	if first.BodyHash != second.BodyHash || first.Text != second.Text {
		t.Fatal("equivalent line-ending and Unicode forms did not produce stable hash/text")
	}
	if !utf8.ValidString(first.Text) {
		t.Fatal("chunk is not valid UTF-8")
	}
}

func TestRoutineRestBookCursorAdvancesOnlyAfterVerifiedEndOfBook(t *testing.T) {
	source := routineRestBookCatalog[0]
	body := routineRestReaderTestBody()
	bodyRunes := len([]rune(normalizeRoutineRestBookText(body)))
	if next, ok := nextRoutineRestBookCursor(routineRestReadCursor{
		SourceURL: source.sourceURL, BodyHash: routineRestBookBodyHash(normalizeRoutineRestBookText(body)),
		NextRune: 1000, BodyRunes: bodyRunes,
	}); ok || next != (routineRestReadCursor{}) {
		t.Fatalf("arbitrary-offset cursor advanced: %+v, available=%t", next, ok)
	}
	next, ok := nextRoutineRestBookCursor(routineRestReadCursor{
		SourceURL: source.sourceURL, BodyHash: routineRestBookBodyHash(normalizeRoutineRestBookText(body)),
		NextRune: bodyRunes, BodyRunes: bodyRunes,
	})
	if !ok || next.SourceURL != routineRestBookCatalog[1].sourceURL || next.BodyHash != "" || next.NextRune != 0 {
		t.Fatalf("cursor after first book = %+v, available=%t", next, ok)
	}
	if next, ok := nextRoutineRestBookCursor(routineRestReadCursor{
		SourceURL: routineRestBookCatalog[1].sourceURL, BodyHash: "sha256:finished", NextRune: 321975, BodyRunes: 321975,
	}); ok || next != (routineRestReadCursor{}) {
		t.Fatalf("catalog end cursor = %+v, available=%t, want unavailable", next, ok)
	}
}

func TestRoutineRestBookReaderRejectsFetchResponseURLMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*modulewebgather.FetchResponse){
		"response url": func(response *modulewebgather.FetchResponse) {
			response.URL = "https://example.invalid/book.html"
		},
		"final url": func(response *modulewebgather.FetchResponse) {
			response.FinalURL = "https://example.invalid/book.html"
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := routineRestReaderTestResponse(routineRestReaderTestBody())
			mutate(&response)
			fetcher := &routineRestReaderTestFetcher{response: response}
			_, err := newRoutineRestBookReader(fetcher).FetchNext(context.Background(), routineRestReadCursor{})
			if !errors.Is(err, errRoutineRestBookUnavailable) {
				t.Fatalf("mismatched-source error = %v, want ErrRoutineRestBookUnavailable", err)
			}
			if fetcher.calls != 1 || fetcher.request.URL != routineRestBookTextURL {
				t.Fatalf("expected one pinned-source fetch, calls=%d request=%+v", fetcher.calls, fetcher.request)
			}
		})
	}
}

func TestRoutineRestBookReaderRejectsOpeningMarkersOutsideAuthenticPrefix(t *testing.T) {
	body := strings.Repeat("別作品の本文です。", 300) +
		"注文の多い料理店 二人の紳士とイギリスの話です。" +
		strings.Repeat("別作品の本文です。", 400)
	reader := newRoutineRestBookReader(&routineRestReaderTestFetcher{
		response: routineRestReaderTestResponse(body),
	})
	_, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
	if !errors.Is(err, errRoutineRestBookUnavailable) {
		t.Fatalf("unrelated-body error = %v, want ErrRoutineRestBookUnavailable", err)
	}
}

func TestRoutineRestBookReaderNeverWrapsToBeginningAfterEnd(t *testing.T) {
	body := routineRestReaderTestBody()
	fetcher := &routineRestReaderTestFetcher{response: routineRestReaderTestResponse(body)}
	reader := newRoutineRestBookReader(fetcher)
	_, err := reader.FetchNext(context.Background(), routineRestReadCursor{
		SourceURL: routineRestBookSourceURL, BodyHash: routineRestBookBodyHash(normalizeRoutineRestBookText(body)),
		NextRune: len([]rune(normalizeRoutineRestBookText(body))), BodyRunes: len([]rune(normalizeRoutineRestBookText(body))),
	})
	if !errors.Is(err, errRoutineRestBookFinished) {
		t.Fatalf("end-of-book error = %v, want ErrRoutineRestBookFinished", err)
	}
}

func TestRoutineRestBookReaderAozoraLiveSource(t *testing.T) {
	if os.Getenv("RENCROW_REST_READER_LIVE_TEST") != "1" {
		t.Skip("set RENCROW_REST_READER_LIVE_TEST=1 to fetch the public Aozora Bunko source")
	}
	fetcher := webgatherapp.NewUseCase(webgather.NewHTTPFetcher(), webgather.NewBasicExtractor(), nil)
	reader := newRoutineRestBookReader(fetcher)
	chunk, err := reader.FetchNext(context.Background(), routineRestReadCursor{})
	if err != nil {
		t.Fatalf("live Aozora FetchNext() error = %v", err)
	}
	if chunk.Title != routineRestBookTitle || chunk.Author != routineRestBookAuthor ||
		chunk.SourceURL != routineRestBookSourceURL || chunk.RightsURL != routineRestBookRightsURL {
		t.Fatalf("live Aozora metadata is not the selected work metadata: title=%q author=%q source=%q rights=%q",
			chunk.Title, chunk.Author, chunk.SourceURL, chunk.RightsURL)
	}
	if !strings.Contains(chunk.Text, "二人の若い紳士") {
		t.Fatal("live extracted body did not contain the verified opening of the selected work")
	}
	if chunk.BodyHash == "" || len([]rune(chunk.Text)) < routineRestBookMinimumChunkRunes {
		t.Fatal("live extracted text did not produce a full reading chunk")
	}

	for _, check := range []struct {
		url, mustContain string
	}{
		{routineRestBookRightsURL, "著作権"},
	} {
		response, err := fetcher.FetchURL(context.Background(), modulewebgather.FetchRequest{
			URL: check.url, Namespace: routineRestBookNamespace, SourceID: routineRestBookSourceID,
			FetchProvider: modulewebgather.DefaultFetchProvider, Extractor: modulewebgather.DefaultExtractor,
			StoreStaging: false, StoreStagingSet: true, Refresh: true, DryRun: true,
			LicenseNote: modulewebgather.DefaultLicenseNote, Policy: routineRestBookFetchPolicy(),
		})
		if err != nil || response.Status != "ok" || !strings.Contains(response.ExtractedText, check.mustContain) {
			t.Fatalf("live metadata/rights source validation failed for %s (status=%q, http=%d, error=%v)",
				check.url, response.Status, response.HTTPStatus, err)
		}
	}
}

func TestRoutineRestBookReaderAozoraLiveSecondCatalogSource(t *testing.T) {
	if os.Getenv("RENCROW_REST_READER_LIVE_TEST") != "1" {
		t.Skip("set RENCROW_REST_READER_LIVE_TEST=1 to fetch the public Aozora Bunko source")
	}
	source := routineRestBookCatalog[1]
	fetcher := webgatherapp.NewUseCase(webgather.NewHTTPFetcher(), webgather.NewBasicExtractor(), nil)
	chunk, err := newRoutineRestBookReader(fetcher).FetchNext(context.Background(), routineRestReadCursor{
		SourceURL: source.sourceURL,
	})
	if err != nil {
		t.Fatalf("live Aozora second-source FetchNext() error = %v", err)
	}
	if chunk.Title != source.title || chunk.Author != source.author || chunk.SourceURL != source.sourceURL ||
		chunk.RightsURL != source.rightsURL || !strings.Contains(chunk.Text, "吾輩は猫である") ||
		!strings.Contains(chunk.Text, "名前はまだ無い") {
		t.Fatalf("live Aozora second-source metadata/opening validation failed")
	}
}
