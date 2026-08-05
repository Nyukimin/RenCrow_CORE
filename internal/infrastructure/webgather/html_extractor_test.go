package webgather

import (
	"context"
	"strings"
	"testing"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestBasicExtractorExtractsHTML(t *testing.T) {
	doc, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		FinalURL:    "https://example.com/a",
		ContentType: "text/html",
		Body: []byte(`<html><head><title>Title</title><meta name="description" content="Desc"></head>
<body><nav>nav</nav><article><h1>Hello</h1><p>Article body</p></article><script>bad()</script></body></html>`),
	}, "html_basic")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if doc.Title != "Title" || doc.Excerpt != "Desc" || doc.Text != "Hello Article body" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
}

func TestBasicExtractorSelectsFullSemanticBodyInsteadOfFirstArticleCard(t *testing.T) {
	doc, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		FinalURL:    "https://huggingface.co/blog/example",
		ContentType: "text/html",
		Body: []byte(`<html><body><main><div class="blog-content"><h1>Full post</h1><p>` +
			strings.Repeat("Complete article paragraph. ", 30) +
			`</p></div><article class="overview-card"><h4>Related model card</h4></article></main></body></html>`),
	}, "html_basic")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if !strings.Contains(doc.Text, "Complete article paragraph") || strings.Contains(doc.Text, "Related model card") || len([]rune(doc.Text)) < 500 {
		t.Fatalf("full semantic article was not selected: %q", doc.Text)
	}
}

func TestBasicExtractorExtractsOnlyNHKArticleBody(t *testing.T) {
	doc, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		FinalURL:    "https://news.web.nhk/newsweb/na/na-k10015196681000",
		ContentType: "text/html",
		Body: []byte(`<html><head><title>NHK article</title></head><body><main>
<h1>ルーマニア 水位低下により原発一部停止 車生産一時休止も</h1>
<time datetime="2026-08-05T10:37:26+09:00">2026年8月5日 10:37</time><a>気象</a>
<p class="article-paragraph">ヨーロッパで記録的な熱波となるなか、影響が広がっています。</p>
<div class="c-part" data-nosnippet><p class="article-paragraph">ヨーロッパでは各地で長引く干ばつにより川の水位が低下しています。</p></div>
<div class="c-part" data-nosnippet><p class="article-paragraph">また隣国でも経済活動への影響が懸念されています。</p></div>
<h2>あわせて読みたい</h2><p class="related-card">関連記事と広告</p><h2>各地のニュース</h2><p class="related-card">地図から選ぶ</p>
</main></body></html>`),
	}, "html_basic")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	want := "ヨーロッパで記録的な熱波となるなか、影響が広がっています。\n\nヨーロッパでは各地で長引く干ばつにより川の水位が低下しています。\n\nまた隣国でも経済活動への影響が懸念されています。"
	if doc.Text != want {
		t.Fatalf("NHK article body included non-article content: %q", doc.Text)
	}
}

func TestBasicExtractorRejectsTruncatedNHKPageWithoutFullBody(t *testing.T) {
	_, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		FinalURL:    "https://news.web.nhk/newsweb/na/na-k10015196681000",
		ContentType: "text/html",
		Body: []byte(`<html><body><main><h1>見出し</h1><p>省略された導入文…</p>
<h2>あわせて読みたい</h2><p>関連記事と広告</p></main></body></html>`),
	}, "html_basic")
	if err == nil {
		t.Fatal("truncated NHK page must not be stored as a full article")
	}
	wgErr, ok := err.(*modulewebgather.Error)
	if !ok || wgErr.Code != modulewebgather.ErrEmptyContent {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}

func TestBasicExtractorRedactsJSONSecretKeys(t *testing.T) {
	doc, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		ContentType: "application/json",
		Body:        []byte(`{"title":"ok","token_value":"abc"}`),
	}, "json_text")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if doc.Text == "" || doc.Extractor != "json_text" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
	if doc.Text == `{"title":"ok","token_value":"abc"}` {
		t.Fatal("raw JSON must not be preserved unchanged")
	}
}

func TestBasicExtractorBlocksSecretLikeJSON(t *testing.T) {
	_, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		ContentType: "application/json",
		Body:        []byte(`{"authorization":"Bearer abc"}`),
	}, "json_text")
	if err == nil {
		t.Fatal("expected blocked_by_policy")
	}
	wgErr, ok := err.(*modulewebgather.Error)
	if !ok || wgErr.Code != modulewebgather.ErrBlockedByPolicy {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}
