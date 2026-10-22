package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

const (
	videoTestHost       = "192.168.1.206:18790"
	videoTestOrigin     = "http://192.168.1.206:18790"
	videoTestClientIPv4 = "192.168.1.206"
	videoTestActor      = "user:yukimi"
)

type fakeVideoGateway struct {
	accepted     domainimage.VideoGenerationAccepted
	status       domainimage.VideoGenerationStatus
	videoOptions domainimage.VideoOptionsResponse
	media        *http.Response
	optionsCalls int
	optionsMode  string
	createCalls  int
	statusCalls  int
	mediaCalls   int
	createReq    domainimage.VideoGenerateRequest
	createActor  string
	createKey    string
	statusActor  string
	mediaActor   string
	mediaRange   string
}

func (g *fakeVideoGateway) VideoOptions(_ context.Context, mode string) (domainimage.VideoOptionsResponse, error) {
	g.optionsCalls++
	g.optionsMode = mode
	return g.videoOptions, nil
}

func (g *fakeVideoGateway) CreateVideoGeneration(_ context.Context, request domainimage.VideoGenerateRequest, actorID, key string) (domainimage.VideoGenerationAccepted, error) {
	g.createCalls++
	g.createReq, g.createActor, g.createKey = request, actorID, key
	return g.accepted, nil
}

func (g *fakeVideoGateway) GetVideoGeneration(_ context.Context, _ string, actorID string) (domainimage.VideoGenerationStatus, error) {
	g.statusCalls++
	g.statusActor = actorID
	return g.status, nil
}

func (g *fakeVideoGateway) OpenVideo(_ context.Context, _ string, actorID, rangeHeader string) (*http.Response, error) {
	g.mediaCalls++
	g.mediaActor, g.mediaRange = actorID, rangeHeader
	return g.media, nil
}

func videoAccessConfig() VideoBrowserAccessConfig {
	return VideoBrowserAccessConfig{
		Enabled: true, AllowGeneration: true, Host: videoTestHost, Origin: videoTestOrigin,
		AllowedClientIPv4: []string{videoTestClientIPv4}, ActorID: videoTestActor,
	}
}

func videoRequest(t *testing.T, mode string, image []byte, aspectRatio ...string) (*http.Request, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range map[string]string{"mode": mode, "prompt": "blue bird flying over a quiet lake", "seed": "42"} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if len(aspectRatio) > 0 {
		if err := writer.WriteField("aspect_ratio", aspectRatio[0]); err != nil {
			t.Fatal(err)
		}
	}
	if image != nil {
		part, err := writer.CreateFormFile("image", "input.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(image); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/image/video/generations", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, body.String()
}

func TestHandleVideoOptionsUsesModeOptionsForConfiguredLANClient(t *testing.T) {
	options := domainimage.VideoOptionsResponse{
		Model: "video-i2v-v1", Mode: "i2v", AspectRatios: []string{"1:1 (Square)", "9:16 (Portrait Widescreen)"},
		DefaultAspectRatio: "1:1 (Square)", Megapixels: 0.2, DurationSeconds: 5, Steps: 8,
	}
	gateway := &fakeVideoGateway{videoOptions: options}
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options?mode=i2v", nil)
	req.RemoteAddr = videoTestClientIPv4 + ":43210"
	req.Host = videoTestHost
	rec := httptest.NewRecorder()
	HandleVideoOptions(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gateway.optionsCalls != 1 || gateway.optionsMode != "i2v" {
		t.Fatalf("status=%d calls=%d mode=%q body=%s", rec.Code, gateway.optionsCalls, gateway.optionsMode, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "authenticated") {
		t.Fatalf("unexpected headers or auth requirement: headers=%v body=%s", rec.Header(), rec.Body.String())
	}
	var got domainimage.VideoOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Model != options.Model || got.Mode != options.Mode || got.DefaultAspectRatio != options.DefaultAspectRatio {
		t.Fatalf("options=%+v err=%v", got, err)
	}
}

func TestHandleVideoOptionsReturns404WhenDisabled(t *testing.T) {
	gateway := &fakeVideoGateway{}
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options?mode=t2v", nil)
	req.RemoteAddr = videoTestClientIPv4 + ":43210"
	req.Host = videoTestHost
	access := videoAccessConfig()
	access.Enabled = false
	rec := httptest.NewRecorder()
	HandleVideoOptions(gateway, access).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || gateway.optionsCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, gateway.optionsCalls, rec.Body.String())
	}
}

func TestHandleVideoGenerateUsesSharedMovieOwnerWithoutBrowserSession(t *testing.T) {
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_abc", Status: "queued"}}
	req, _ := videoRequest(t, "t2v", nil)
	req.RemoteAddr = videoTestClientIPv4 + ":43210"
	req.Host = videoTestHost
	req.Header.Set("Origin", videoTestOrigin)
	req.Header.Set("Idempotency-Key", "9d08d01b-1a7d-4fce-9da3-aeb0a1528ea1")
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || gateway.createCalls != 1 || gateway.createActor != videoTestActor {
		t.Fatalf("status=%d calls=%d actor=%q body=%s", rec.Code, gateway.createCalls, gateway.createActor, rec.Body.String())
	}
}

func TestHandleVideoOptionsRejectsInvalidModeAndUntrustedRequest(t *testing.T) {
	cases := []struct {
		name string
		url  string
		edit func(*http.Request)
		want int
	}{
		{name: "unsupported mode", url: "/viewer/image/video/options?mode=other", want: http.StatusBadRequest},
		{name: "duplicate mode", url: "/viewer/image/video/options?mode=t2v&mode=i2v", want: http.StatusBadRequest},
		{name: "wrong LAN source", url: "/viewer/image/video/options?mode=t2v", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.50:43000" }, want: http.StatusNotFound},
		{name: "wrong host", url: "/viewer/image/video/options?mode=t2v", edit: func(r *http.Request) { r.Host = "attacker.example" }, want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			req.RemoteAddr = videoTestClientIPv4 + ":43210"
			req.Host = videoTestHost
			if tc.edit != nil {
				tc.edit(req)
			}
			gateway := &fakeVideoGateway{videoOptions: domainimage.VideoOptionsResponse{
				Model: "video-t2v-v1", Mode: "t2v", AspectRatios: []string{"16:9 (Widescreen)"},
				DefaultAspectRatio: "16:9 (Widescreen)", Megapixels: 0.2, DurationSeconds: 5, Steps: 8,
			}}
			rec := httptest.NewRecorder()
			HandleVideoOptions(gateway, videoAccessConfig()).ServeHTTP(rec, req)
			if rec.Code != tc.want || gateway.optionsCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, gateway.optionsCalls, rec.Body.String())
			}
		})
	}
}

func allowedLANVideoRequest(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	req.RemoteAddr = videoTestClientIPv4 + ":43210"
	req.Host = videoTestHost
	req.Header.Set("Origin", videoTestOrigin)
	req.Header.Set("Idempotency-Key", "9d08d01b-1a7d-4fce-9da3-aeb0a1528ea1")
	return req
}

func TestHandleVideoGenerateAcceptsExistingIdempotentJobStatus(t *testing.T) {
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_existing", Status: "running"}}
	req, _ := videoRequest(t, "t2v", nil)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	var accepted domainimage.VideoGenerationAccepted
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.Status != "running" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleVideoGenerateAcceptsBoundedPNGForI2V(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_i2v", Status: "queued"}}
	req, _ := videoRequest(t, "i2v", png)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || gateway.createCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, gateway.createCalls, rec.Body.String())
	}
	if gateway.createReq.ImageType != "image/png" || !bytes.Equal(gateway.createReq.Image, png) {
		t.Fatalf("image type=%q bytes=%x", gateway.createReq.ImageType, gateway.createReq.Image)
	}
}

func TestHandleVideoGenerateRejectsOversizedImageBeforeGateway(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range map[string]string{"mode": "i2v", "prompt": "a small red boat", "seed": "9"} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("image", "large.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{0x89}, maxVideoImageBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/image/video/generations", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	allowedLANVideoRequest(t, req)
	gateway := &fakeVideoGateway{}
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || gateway.createCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, gateway.createCalls, rec.Body.String())
	}
}

func TestHandleVideoGenerateUsesCOREAuthenticatedActorAndIdempotencyKey(t *testing.T) {
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_abc", Status: "queued"}}
	req, _ := videoRequest(t, "t2v", nil)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gateway.createCalls != 1 || gateway.createActor != videoTestActor || gateway.createKey != req.Header.Get("Idempotency-Key") {
		t.Fatalf("create call=%d actor=%q key=%q", gateway.createCalls, gateway.createActor, gateway.createKey)
	}
	if gateway.createReq.Mode != "t2v" || gateway.createReq.Prompt != "blue bird flying over a quiet lake" || gateway.createReq.Seed != 42 || len(gateway.createReq.Image) != 0 {
		t.Fatalf("request=%+v", gateway.createReq)
	}
}

func TestHandleVideoGenerateForwardsExactAspectRatio(t *testing.T) {
	const aspectRatio = "9:16 (Portrait Widescreen)"
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_portrait", Status: "queued"}}
	req, _ := videoRequest(t, "t2v", nil, aspectRatio)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || gateway.createCalls != 1 || gateway.createReq.AspectRatio != aspectRatio {
		t.Fatalf("status=%d calls=%d aspect=%q body=%s", rec.Code, gateway.createCalls, gateway.createReq.AspectRatio, rec.Body.String())
	}
}

func TestHandleVideoGenerateRejectsOversizedAspectRatioBeforeGateway(t *testing.T) {
	req, _ := videoRequest(t, "t2v", nil, strings.Repeat("x", maxVideoAspectRatioBytes+1))
	allowedLANVideoRequest(t, req)
	gateway := &fakeVideoGateway{}
	rec := httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || gateway.createCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, gateway.createCalls, rec.Body.String())
	}
}

func TestHandleVideoGenerateRejectsUntrustedRequestsBeforeGateway(t *testing.T) {
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "unapproved browser source 192.168.1.204", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.204:43000" }},
		{name: "ComfyUI backend is not a browser source", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.207:43000" }},
		{name: "wrong LAN source", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.50:43000" }},
		{name: "wrong host", edit: func(r *http.Request) { r.Host = "attacker.example" }},
		{name: "missing origin", edit: func(r *http.Request) { r.Header.Del("Origin") }},
		{name: "null origin", edit: func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{name: "Tailscale identity", edit: func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "yukimi@example.com") }},
		{name: "Tailscale Funnel", edit: func(r *http.Request) { r.Header.Set("Tailscale-Funnel-Request", "true") }},
		{name: "Forwarded proxy header", edit: func(r *http.Request) { r.Header.Set("Forwarded", "for=192.168.1.204") }},
		{name: "X-Forwarded proxy header", edit: func(r *http.Request) { r.Header.Set("X-Forwarded-For", "192.168.1.204") }},
		{name: "duplicate origin", edit: func(r *http.Request) { r.Header.Add("Origin", videoTestOrigin) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_abc", Status: "queued"}}
			req, _ := videoRequest(t, "t2v", nil)
			tt.edit(allowedLANVideoRequest(t, req))
			rec := httptest.NewRecorder()
			HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound || gateway.createCalls != 0 {
				t.Fatalf("status=%d createCalls=%d body=%q", rec.Code, gateway.createCalls, rec.Body.String())
			}
		})
	}
}

func TestHandleVideoGenerateValidatesModeSpecificInputs(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		image []byte
	}{
		{name: "I2V requires image", mode: "i2v"},
		{name: "T2V rejects image", mode: "t2v", image: []byte("not an image")},
		{name: "unknown mode", mode: "other"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_abc", Status: "queued"}}
			req, _ := videoRequest(t, tt.mode, tt.image)
			allowedLANVideoRequest(t, req)
			rec := httptest.NewRecorder()
			HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || gateway.createCalls != 0 {
				t.Fatalf("status=%d createCalls=%d body=%q", rec.Code, gateway.createCalls, rec.Body.String())
			}
		})
	}
}

func TestHandleVideoStatusRewritesUpstreamMediaURL(t *testing.T) {
	progress := 37
	gateway := &fakeVideoGateway{status: domainimage.VideoGenerationStatus{
		ID: "vid_abc", Status: "succeeded", Progress: &progress,
		Video: &domainimage.VideoArtifact{URL: "/v1/videos/vid_abc.mp4", SHA256: strings.Repeat("a", 64), SizeBytes: 12, ContentType: "video/mp4"},
	}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/generations/vid_abc", nil)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	HandleVideoStatus(gateway, videoAccessConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || gateway.statusCalls != 1 || gateway.statusActor != videoTestActor {
		t.Fatalf("status=%d calls=%d actor=%q body=%s", rec.Code, gateway.statusCalls, gateway.statusActor, rec.Body.String())
	}
	var payload domainimage.VideoGenerationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Video == nil || payload.Video.URL != "/viewer/image/video/vid_abc.mp4" || payload.Progress == nil || *payload.Progress != 37 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestHandleVideoResultForwardsActorAndRange(t *testing.T) {
	gateway := &fakeVideoGateway{media: &http.Response{
		StatusCode: http.StatusPartialContent,
		Header:     http.Header{"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-4/10"}, "Content-Length": {"5"}},
		Body:       io.NopCloser(strings.NewReader("video")),
	}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/vid_abc.mp4", nil)
	allowedLANVideoRequest(t, req)
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	HandleVideoResult(gateway, videoAccessConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent || gateway.mediaCalls != 1 || gateway.mediaActor != videoTestActor || gateway.mediaRange != "bytes=0-4" {
		t.Fatalf("status=%d calls=%d actor=%q range=%q", rec.Code, gateway.mediaCalls, gateway.mediaActor, gateway.mediaRange)
	}
	if rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("Content-Range") != "bytes 0-4/10" || rec.Body.String() != "video" {
		t.Fatalf("headers=%v body=%q", rec.Header(), rec.Body.String())
	}
}

func TestHandleVideoGenerateRequiresExplicitCOREPermission(t *testing.T) {
	gateway := &fakeVideoGateway{accepted: domainimage.VideoGenerationAccepted{ID: "vid_abc", Status: "queued"}}
	req, _ := videoRequest(t, "t2v", nil)
	allowedLANVideoRequest(t, req)
	rec := httptest.NewRecorder()
	access := videoAccessConfig()
	access.AllowGeneration = false
	HandleVideoGenerate(gateway, access).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || gateway.createCalls != 0 {
		t.Fatalf("policy-disabled status=%d createCalls=%d", rec.Code, gateway.createCalls)
	}

	req, _ = videoRequest(t, "t2v", nil)
	allowedLANVideoRequest(t, req)
	req.Header.Add("Idempotency-Key", "second")
	rec = httptest.NewRecorder()
	HandleVideoGenerate(gateway, videoAccessConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || gateway.createCalls != 0 {
		t.Fatalf("duplicate key status=%d createCalls=%d", rec.Code, gateway.createCalls)
	}
}

func TestHandleVideoStatusRejectsCrossOriginAndInvalidID(t *testing.T) {
	for _, path := range []string{"/viewer/image/video/generations/../secret", "/viewer/image/video/generations/vid_abc/extra"} {
		gateway := &fakeVideoGateway{}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		allowedLANVideoRequest(t, req)
		req.Header.Set("Origin", "https://attacker.example")
		rec := httptest.NewRecorder()
		HandleVideoStatus(gateway, videoAccessConfig()).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || gateway.statusCalls != 0 {
			t.Fatalf("path=%s status=%d calls=%d", path, rec.Code, gateway.statusCalls)
		}
	}
}
