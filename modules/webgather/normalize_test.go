package webgather

import "testing"

func TestNormalizeURLBlocksLocalhostByDefault(t *testing.T) {
	if _, err := NormalizeURL("http://127.0.0.1:8080/page", false); err == nil {
		t.Fatal("expected localhost URL to be blocked")
	}
	got, err := NormalizeURL("http://127.0.0.1:8080/page#frag", true)
	if err != nil {
		t.Fatalf("NormalizeURL with allow localhost failed: %v", err)
	}
	if got != "http://127.0.0.1:8080/page" {
		t.Fatalf("unexpected normalized URL: %s", got)
	}
}

func TestNormalizeURLRejectsUnsupportedScheme(t *testing.T) {
	_, err := NormalizeURL("file:///tmp/a.txt", false)
	if err == nil {
		t.Fatal("expected unsupported scheme error")
	}
	wgErr, ok := err.(*Error)
	if !ok || wgErr.Code != ErrUnsupportedScheme {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}

func TestSourceIDAndEventIDAreStable(t *testing.T) {
	u := "https://example.com/articles/1"
	if SourceIDFromURL(u) != SourceIDFromURL(u) {
		t.Fatal("source id must be stable")
	}
	if EventID("web:example", u, "sha256:abc") != EventID("web:example", u, "sha256:abc") {
		t.Fatal("event id must be stable")
	}
}
