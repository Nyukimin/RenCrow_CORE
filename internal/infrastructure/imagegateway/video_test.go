package imagegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

const (
	videoClientTestActor = "user:yukimi"
	videoClientTestKey   = "9d08d01b-1a7d-4fce-9da3-aeb0a1528ea1"
)

func TestClientCreateVideoGenerationUsesActorScopedMultipartContract(t *testing.T) {
	imageBytes := []byte("\x89PNG\r\n\x1a\nsmall-input")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/videos/generations" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if r.Header.Get("X-RenCrow-Actor-ID") != videoClientTestActor || r.Header.Get("Idempotency-Key") != videoClientTestKey {
			http.Error(w, "missing identity", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			http.Error(w, "not multipart", http.StatusBadRequest)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		fields := map[string]string{}
		var gotImage []byte
		var gotImageType string
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad part", http.StatusBadRequest)
				return
			}
			switch part.FormName() {
			case "mode", "prompt", "seed":
				value, readErr := io.ReadAll(part)
				if readErr != nil {
					http.Error(w, "bad field", http.StatusBadRequest)
					return
				}
				fields[part.FormName()] = string(value)
			case "image":
				gotImageType = part.Header.Get("Content-Type")
				gotImage, err = io.ReadAll(part)
				if err != nil {
					http.Error(w, "bad image", http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "unknown field", http.StatusBadRequest)
				return
			}
		}
		if fields["mode"] != "i2v" || fields["prompt"] != "a red kite over the sea" || fields["seed"] != "42" ||
			gotImageType != "image/png" || string(gotImage) != string(imageBytes) {
			http.Error(w, fmt.Sprintf("wrong form: %v %q %q", fields, gotImageType, gotImage), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "{\"id\":\"vid_abc\",\"status\":\"queued\"}")
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	accepted, err := client.CreateVideoGeneration(context.Background(), domainimage.VideoGenerateRequest{
		Mode: "i2v", Prompt: "a red kite over the sea", Seed: 42, Image: imageBytes, ImageType: "image/png",
	}, videoClientTestActor, videoClientTestKey)
	if err != nil {
		t.Fatalf("CreateVideoGeneration: %v", err)
	}
	if accepted.ID != "vid_abc" || accepted.Status != "queued" {
		t.Fatalf("accepted=%+v", accepted)
	}
}

func TestClientVideoStatusAndRangeForwardActorAndRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-RenCrow-Actor-ID") != videoClientTestActor {
			http.Error(w, "missing actor", http.StatusBadRequest)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/generations/vid_abc":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{\"id\":\"vid_abc\",\"status\":\"running\",\"created_at\":\"2026-10-21T00:00:00Z\",\"updated_at\":\"2026-10-21T00:00:01Z\"}")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/vid_abc.mp4":
			if r.Header.Get("Range") != "bytes=5-9" {
				http.Error(w, "missing range", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 5-9/10")
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "video")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	status, err := client.GetVideoGeneration(context.Background(), "vid_abc", videoClientTestActor)
	if err != nil {
		t.Fatalf("GetVideoGeneration: %v", err)
	}
	if status.ID != "vid_abc" || status.Status != "running" {
		t.Fatalf("status=%+v", status)
	}
	response, err := client.OpenVideo(context.Background(), "vid_abc", videoClientTestActor, "bytes=5-9")
	if err != nil {
		t.Fatalf("OpenVideo: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read video body: %v", err)
	}
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != "bytes 5-9/10" || string(body) != "video" {
		t.Fatalf("response=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
}

func TestClientVideoRejectsRedirectAndInvalidActor(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:1/steal")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewClient(redirect.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.GetVideoGeneration(context.Background(), "vid_abc", ""); err == nil {
		t.Fatal("expected invalid actor error")
	}
	response, err := client.OpenVideo(context.Background(), "vid_abc", videoClientTestActor, "")
	if err != nil {
		t.Fatalf("OpenVideo should return the redirect response without following it: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status=%d want=%d", response.StatusCode, http.StatusFound)
	}
}

func TestClientCreateVideoAcceptsExistingIdempotentJobStatuses(t *testing.T) {
	for _, state := range []string{"running", "succeeded", "failed"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "vid_retry", "status": state})
			}))
			defer server.Close()
			client, err := NewClient(server.URL, 2*time.Second)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			accepted, err := client.CreateVideoGeneration(context.Background(), domainimage.VideoGenerateRequest{
				Mode: "t2v", Prompt: "a small boat", Seed: 1,
			}, videoClientTestActor, videoClientTestKey)
			if err != nil || accepted.ID != "vid_retry" || accepted.Status != state {
				t.Fatalf("accepted=%+v err=%v", accepted, err)
			}
		})
	}
}

func TestClientCreateVideoRejectsMalformedAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "{\"id\":\"../other\",\"status\":\"queued\"}")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.CreateVideoGeneration(context.Background(), domainimage.VideoGenerateRequest{
		Mode: "t2v", Prompt: "a small boat", Seed: 1,
	}, videoClientTestActor, videoClientTestKey)
	if err == nil {
		t.Fatal("expected malformed acceptance error")
	}
}
