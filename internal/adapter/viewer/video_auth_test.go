package viewer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVideoActorIDMapsOneExactTailscaleLoginToConfiguredUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = videoTestHost
	req.Header.Set("Tailscale-User-Login", videoTestLogin)

	actorID, ok := videoAuthConfig().videoActorID(req)
	if !ok || actorID != videoTestActor {
		t.Fatalf("actorID=%q authenticated=%t", actorID, ok)
	}
}

func TestVideoActorIDRejectsMissingOrNonExactLoginHeader(t *testing.T) {
	cases := []struct {
		name  string
		login []string
	}{
		{name: "missing"},
		{name: "empty", login: []string{""}},
		{name: "wrong", login: []string{"other@example.com"}},
		{name: "case changed", login: []string{"Yukimi@example.com"}},
		{name: "duplicate", login: []string{videoTestLogin, videoTestLogin}},
		{name: "comma joined", login: []string{videoTestLogin + ", " + videoTestLogin}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options", nil)
			req.RemoteAddr = "127.0.0.1:43210"
			req.Host = videoTestHost
			for _, value := range tc.login {
				req.Header.Add("Tailscale-User-Login", value)
			}
			if _, ok := videoAuthConfig().videoActorID(req); ok {
				t.Fatal("request unexpectedly authenticated")
			}
		})
	}
}

func TestVideoActorIDRejectsDuplicateHeaderNamesWithDifferentCase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = videoTestHost
	req.Header["Tailscale-User-Login"] = []string{videoTestLogin}
	req.Header["tailscale-user-login"] = []string{videoTestLogin}
	if _, ok := videoAuthConfig().videoActorID(req); ok {
		t.Fatal("request with duplicate case-variant login headers unexpectedly authenticated")
	}
}

func TestVideoRoutesRejectUntrustedRequestsBeforeGateway(t *testing.T) {
	routes := []struct {
		name    string
		request func(*testing.T) *http.Request
		handler func(*fakeVideoGateway) http.HandlerFunc
	}{
		{
			name: "options",
			request: func(_ *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/viewer/image/video/options?mode=t2v", nil)
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoOptions(g, videoAuthConfig())
			},
		},
		{
			name: "generate",
			request: func(t *testing.T) *http.Request {
				req, _ := videoRequest(t, "t2v", nil)
				req.Header.Set("Idempotency-Key", "9d08d01b-1a7d-4fce-9da3-aeb0a1528ea1")
				return req
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoGenerate(g, videoAuthConfig())
			},
		},
		{
			name: "status",
			request: func(_ *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/viewer/image/video/generations/vid_abc", nil)
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoStatus(g, videoAuthConfig())
			},
		},
		{
			name: "mp4",
			request: func(_ *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/viewer/image/video/vid_abc.mp4", nil)
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoResult(g, videoAuthConfig())
			},
		},
	}
	edits := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "missing login", edit: func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }},
		{name: "wrong login", edit: func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "other@example.com") }},
		{name: "duplicate login", edit: func(r *http.Request) { r.Header.Add("Tailscale-User-Login", videoTestLogin) }},
		{name: "LAN request", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.50:43000" }},
		{name: "wrong host", edit: func(r *http.Request) { r.Host = "attacker.example" }},
		{name: "wrong origin", edit: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") }},
		{name: "Funnel marker", edit: func(r *http.Request) { r.Header.Set("Tailscale-Funnel-Request", "true") }},
	}
	for _, route := range routes {
		for _, edit := range edits {
			t.Run(route.name+"/"+edit.name, func(t *testing.T) {
				gateway := &fakeVideoGateway{}
				req := route.request(t)
				req.RemoteAddr = "127.0.0.1:43210"
				req.Host = videoTestHost
				req.Header.Set("Tailscale-User-Login", videoTestLogin)
				if req.Method == http.MethodPost {
					req.Header.Set("Origin", videoTestOrigin)
				}
				edit.edit(req)
				rec := httptest.NewRecorder()
				route.handler(gateway).ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound || gateway.optionsCalls != 0 || gateway.createCalls != 0 ||
					gateway.statusCalls != 0 || gateway.mediaCalls != 0 {
					t.Fatalf("status=%d calls options=%d generate=%d status=%d mp4=%d body=%q",
						rec.Code, gateway.optionsCalls, gateway.createCalls, gateway.statusCalls, gateway.mediaCalls, rec.Body.String())
				}
			})
		}
	}
}

func TestVideoActorIDRequiresOneExactHTTPSOriginForPostAndDelete(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		cases := []struct {
			name    string
			origins []string
		}{
			{name: "missing"},
			{name: "wrong", origins: []string{"http://llm-rx6800.tailb07d8d.ts.net:8444"}},
			{name: "duplicate", origins: []string{videoTestOrigin, videoTestOrigin}},
		}
		for _, tc := range cases {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(method, "/viewer/image/video/generations", nil)
				req.RemoteAddr = "127.0.0.1:43210"
				req.Host = videoTestHost
				req.Header.Set("Tailscale-User-Login", videoTestLogin)
				for _, origin := range tc.origins {
					req.Header.Add("Origin", origin)
				}
				if _, ok := videoAuthConfig().videoActorID(req); ok {
					t.Fatal("request unexpectedly authenticated")
				}
			})
		}
	}
}
