package imagegateway

import (
	"context"
	"encoding/binary"
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

func gatewayPCM16WAV() []byte {
	wav := make([]byte, 46)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], 2)
	return wav
}

func gatewayMP4Header() []byte {
	return []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0}
}

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
			case "mode", "prompt", "seed", "aspect_ratio":
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
			fields["aspect_ratio"] != "9:16 (Portrait Widescreen)" ||
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
		Mode: "i2v", Prompt: "a red kite over the sea", Seed: 42,
		AspectRatio: "9:16 (Portrait Widescreen)", Image: imageBytes, ImageType: "image/png",
	}, videoClientTestActor, videoClientTestKey)
	if err != nil {
		t.Fatalf("CreateVideoGeneration: %v", err)
	}
	if accepted.ID != "vid_abc" || accepted.Status != "queued" {
		t.Fatalf("accepted=%+v", accepted)
	}
}

func TestClientCreateLipSyncGenerationForwardsTaskAudioAndI2VImage(t *testing.T) {
	audioBytes := gatewayPCM16WAV()
	imageBytes := []byte("\x89PNG\r\n\x1a\nsmall-image")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		fields := map[string]string{}
		parts := map[string][]byte{}
		contentTypes := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad part", http.StatusBadRequest)
				return
			}
			value, err := io.ReadAll(part)
			if err != nil {
				http.Error(w, "bad part body", http.StatusBadRequest)
				return
			}
			if part.FileName() == "" {
				fields[part.FormName()] = string(value)
			} else {
				parts[part.FormName()] = value
				contentTypes[part.FormName()] = part.Header.Get("Content-Type")
			}
		}
		if fields["task"] != "lip_sync" || fields["mode"] != "i2v" || fields["prompt"] != "singing at dusk" ||
			fields["seed"] != "42" || !strings.Contains(fields["aspect_ratio"], "9:16") ||
			string(parts["image"]) != string(imageBytes) || contentTypes["image"] != "image/png" ||
			string(parts["audio"]) != string(audioBytes) || contentTypes["audio"] != "audio/wav" {
			http.Error(w, fmt.Sprintf("wrong lip-sync form: fields=%v content_types=%v", fields, contentTypes), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"vid_lipsync","status":"queued"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	accepted, err := client.CreateVideoGeneration(context.Background(), domainimage.VideoGenerateRequest{
		Task: domainimage.VideoTaskLipSync, Mode: "i2v", Prompt: "singing at dusk", Seed: 42,
		AspectRatio: "9:16 (Portrait Widescreen)", Image: imageBytes, ImageType: "image/png", Audio: audioBytes,
	}, videoClientTestActor, videoClientTestKey)
	if err != nil || accepted.ID != "vid_lipsync" || accepted.Status != "queued" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
}

func TestClientCreateV2VLipSyncForwardsOnlySourceVideoAndAudio(t *testing.T) {
	videoBytes := gatewayMP4Header()
	audioBytes := gatewayPCM16WAV()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		fields := map[string]string{}
		parts := map[string][]byte{}
		contentTypes := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad part", http.StatusBadRequest)
				return
			}
			value, err := io.ReadAll(part)
			if err != nil {
				http.Error(w, "bad part body", http.StatusBadRequest)
				return
			}
			if part.FileName() == "" {
				fields[part.FormName()] = string(value)
			} else {
				parts[part.FormName()] = value
				contentTypes[part.FormName()] = part.Header.Get("Content-Type")
			}
		}
		if len(fields) != 2 || fields["task"] != domainimage.VideoTaskLipSync || fields["mode"] != domainimage.VideoModeV2V ||
			len(parts) != 2 || string(parts["video"]) != string(videoBytes) || contentTypes["video"] != "video/mp4" ||
			string(parts["audio"]) != string(audioBytes) || contentTypes["audio"] != "audio/wav" {
			http.Error(w, fmt.Sprintf("wrong V2V form: fields=%v part_types=%v", fields, contentTypes), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"vid_v2v","status":"queued"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	accepted, err := client.CreateVideoGeneration(context.Background(), domainimage.VideoGenerateRequest{
		Task: domainimage.VideoTaskLipSync, Mode: domainimage.VideoModeV2V, Video: videoBytes, Audio: audioBytes,
	}, videoClientTestActor, videoClientTestKey)
	if err != nil || accepted.ID != "vid_v2v" || accepted.Status != "queued" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
}

func TestClientVideoOptionsUsesCanonicalRouteAndModeQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/options" || r.URL.Query().Get("mode") != "i2v" {
			http.Error(w, "wrong video options route", http.StatusNotFound)
			return
		}
		if r.Header.Get("X-RenCrow-Actor-ID") != "" {
			http.Error(w, "options must not require an actor", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"video-i2v-v1","mode":"i2v","aspect_ratios":["1:1 (Square)","9:16 (Portrait Widescreen)"],"default_aspect_ratio":"1:1 (Square)","megapixels":0.2,"duration_seconds":5,"steps":8}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.VideoOptions(context.Background(), "i2v", "")
	if err != nil {
		t.Fatalf("VideoOptions: %v", err)
	}
	if options.Model != "video-i2v-v1" || options.Mode != "i2v" || options.DefaultAspectRatio != "1:1 (Square)" ||
		options.Megapixels != 0.2 || options.DurationSeconds != 5 || options.Steps != 8 || len(options.AspectRatios) != 2 {
		t.Fatalf("options=%+v", options)
	}
}

func TestClientVideoOptionsForLipSyncUsesTaskQueryAndValidatesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/options" ||
			r.URL.Query().Get("mode") != "t2v" || r.URL.Query().Get("task") != "lip_sync" {
			http.Error(w, "wrong lip-sync options route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task":"lip_sync","model":"video-lipsync-v1","mode":"t2v","aspect_ratios":["16:9 (Widescreen)"],"default_aspect_ratio":"16:9 (Widescreen)","megapixels":0.2,"duration_seconds":10,"steps":8}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.VideoOptions(context.Background(), "t2v", domainimage.VideoTaskLipSync)
	if err != nil {
		t.Fatalf("VideoOptions with task: %v", err)
	}
	if options.Task != domainimage.VideoTaskLipSync || options.DurationSeconds != 10 || options.Mode != "t2v" {
		t.Fatalf("options=%+v", options)
	}
}

func TestClientVideoOptionsForLipSyncRejectsUnmarkedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"video-t2v-v1","mode":"t2v","aspect_ratios":["16:9"],"default_aspect_ratio":"16:9","megapixels":0.2,"duration_seconds":10,"steps":8}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.VideoOptions(context.Background(), "t2v", domainimage.VideoTaskLipSync); err == nil {
		t.Fatal("expected unmarked lip-sync options response to be rejected")
	}
}

func TestClientVideoOptionsForV2VLipSyncUsesBoundedOptionsWithoutDummyFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/options" ||
			r.URL.Query().Get("mode") != domainimage.VideoModeV2V || r.URL.Query().Get("task") != domainimage.VideoTaskLipSync {
			http.Error(w, "wrong V2V options route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"task":"lip_sync","model":"MuseTalk 1.5","mode":"v2v","duration_seconds":10,"max_video_bytes":%d,"max_audio_bytes":%d}`,
			domainimage.MaxVideoV2VBytes, domainimage.MaxVideoAudioBytes)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.VideoOptions(context.Background(), domainimage.VideoModeV2V, domainimage.VideoTaskLipSync)
	if err != nil {
		t.Fatalf("VideoOptions: %v", err)
	}
	if options.Task != domainimage.VideoTaskLipSync || options.Model != "MuseTalk 1.5" || options.Mode != domainimage.VideoModeV2V ||
		options.DurationSeconds != 10 || options.MaxVideoBytes != domainimage.MaxVideoV2VBytes || options.MaxAudioBytes != domainimage.MaxVideoAudioBytes ||
		options.AspectRatios != nil || options.Megapixels != 0 || options.Steps != 0 {
		t.Fatalf("options=%+v", options)
	}
}

func TestClientVideoOptionsForV2VRejectsAspectAndStepFields(t *testing.T) {
	for _, payload := range []string{
		`{"task":"lip_sync","model":"MuseTalk 1.5","mode":"v2v","duration_seconds":10,"max_video_bytes":104857600,"max_audio_bytes":20971520,"aspect_ratios":["16:9"]}`,
		`{"task":"lip_sync","model":"MuseTalk 1.5","mode":"v2v","duration_seconds":10,"max_video_bytes":104857600,"max_audio_bytes":20971520,"steps":8}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, payload)
		}))
		client, err := NewClient(server.URL, 2*time.Second)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.VideoOptions(context.Background(), domainimage.VideoModeV2V, domainimage.VideoTaskLipSync); err == nil {
			t.Fatalf("expected invalid V2V options to be rejected: %s", payload)
		}
		server.Close()
	}
}

func TestClientVideoOptionsRejectsInvalidModeAndMalformedResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"video-i2v-v1","mode":"t2v","aspect_ratios":["1:1 (Square)"],"default_aspect_ratio":"16:9 (Widescreen)","megapixels":0.2,"duration_seconds":5,"steps":8}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.VideoOptions(context.Background(), "unknown", ""); err == nil || requests != 0 {
		t.Fatalf("unsupported mode made request=%d err=%v", requests, err)
	}
	if _, err := client.VideoOptions(context.Background(), "i2v", ""); err == nil || requests != 1 {
		t.Fatalf("malformed options response requests=%d err=%v", requests, err)
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

func TestValidateVideoRequestRejectsSourceVideoOutsideV2V(t *testing.T) {
	tests := []struct {
		name    string
		request domainimage.VideoGenerateRequest
	}{
		{
			name: "t2v",
			request: domainimage.VideoGenerateRequest{
				Mode: "t2v", Prompt: "a small boat", Seed: 1, Video: gatewayMP4Header(),
			},
		},
		{
			name: "i2v",
			request: domainimage.VideoGenerateRequest{
				Mode: "i2v", Prompt: "a small boat", Seed: 1,
				Image: []byte("image"), ImageType: "image/png", Video: gatewayMP4Header(),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateVideoRequest(test.request); err == nil {
				t.Fatal("expected source video to be rejected outside V2V")
			}
		})
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
