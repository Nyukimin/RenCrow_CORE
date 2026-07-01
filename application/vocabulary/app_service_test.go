package vocabulary

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchHeadlinesFromRSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<rss><channel>
			<item><title> Test Title 1 </title></item>
			<item><title>Test Title 2</title></item>
		</channel></rss>`))
	}))
	defer server.Close()

	service := NewAppService(nil, nil)
	titles, err := service.fetchHeadlines(server.URL)
	if err != nil {
		t.Fatalf("fetchHeadlines returned error: %v", err)
	}

	want := []string{"Test Title 1", "Test Title 2"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles mismatch: got %#v want %#v", titles, want)
	}
}

func TestFetchHeadlinesFromAtom(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<feed>
			<entry><title>Atom Title 1</title></entry>
			<entry><title> Atom Title 2 </title></entry>
		</feed>`))
	}))
	defer server.Close()

	service := NewAppService(nil, nil)
	titles, err := service.fetchHeadlines(server.URL)
	if err != nil {
		t.Fatalf("fetchHeadlines returned error: %v", err)
	}

	want := []string{"Atom Title 1", "Atom Title 2"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles mismatch: got %#v want %#v", titles, want)
	}
}

func TestFetchHeadlinesReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	service := NewAppService(nil, nil)
	if _, err := service.fetchHeadlines(server.URL); err == nil {
		t.Fatal("expected status error")
	}
}
