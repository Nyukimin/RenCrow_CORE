package imagegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

func TestClientGenerateUsesRenCrowImageContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var request domainimage.GenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Prompt != "青い鳥" || request.Seed == nil || *request.Seed != 42 {
			http.Error(w, "wrong payload", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"id":"img_1","profile":"forge_zimage_4060","prompt":"青い鳥","image":{"id":"img_1","path":"/v1/images/img_1.png","content_type":"image/png","width":768,"height":768}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	seed := int64(42)
	result, err := client.Generate(context.Background(), domainimage.GenerateRequest{
		Prompt: "青い鳥",
		Seed:   &seed,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !result.OK || result.ID != "img_1" || result.Image.Width != 768 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientImageFetchDoesNotAcceptPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/img_1.png" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	image, contentType, err := client.Image(context.Background(), "img_1")
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	if string(image) != "png" || contentType != "image/png" {
		t.Fatalf("unexpected image response: %q %q", image, contentType)
	}
	if _, _, err := client.Image(context.Background(), "../secret"); err == nil {
		t.Fatal("expected opaque image ID validation error")
	}
}
