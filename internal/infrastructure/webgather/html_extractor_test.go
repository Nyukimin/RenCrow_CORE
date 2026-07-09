package webgather

import (
	"context"
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
