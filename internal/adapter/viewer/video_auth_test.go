package viewer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVideoOwnerAllowsConfiguredLANClientsWithoutIdentityHeaders(t *testing.T) {
	for _, sourceIP := range []string{"192.168.1.204", "192.168.1.207"} {
		t.Run(sourceIP, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options", nil)
			req.RemoteAddr = sourceIP + ":43210"
			req.Host = videoTestHost
			actorID, ok := videoAccessConfig().videoOwnerID(req)
			if !ok || actorID != videoTestActor {
				t.Fatalf("LAN request actorID=%q allowed=%t, want shared Movie owner %q", actorID, ok, videoTestActor)
			}
		})
	}
}

func TestVideoActorIDRejectsUnconfiguredSourceHostOriginAndIdentityHeaders(t *testing.T) {
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong private source", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.205:43210" }},
		{name: "public source", edit: func(r *http.Request) { r.RemoteAddr = "8.8.8.8:43210" }},
		{name: "IPv6 mapped source", edit: func(r *http.Request) { r.RemoteAddr = "[::ffff:192.168.1.204]:43210" }},
		{name: "malformed source", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.204" }},
		{name: "wrong host", edit: func(r *http.Request) { r.Host = "192.168.1.205:18790" }},
		{name: "wrong origin", edit: func(r *http.Request) { r.Header.Set("Origin", "http://attacker.example") }},
		{name: "duplicate origin", edit: func(r *http.Request) {
			r.Header.Set("Origin", videoTestOrigin)
			r.Header.Add("Origin", videoTestOrigin)
		}},
		{name: "Tailscale login", edit: func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "yukimi@example.com") }},
		{name: "Tailscale Funnel", edit: func(r *http.Request) { r.Header.Set("Tailscale-Funnel-Request", "true") }},
		{name: "Forwarded", edit: func(r *http.Request) { r.Header.Set("Forwarded", "for=192.168.1.204") }},
		{name: "X-Forwarded-For", edit: func(r *http.Request) { r.Header.Set("X-Forwarded-For", "192.168.1.204") }},
		{name: "X-Forwarded-Proto", edit: func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options", nil)
			req.RemoteAddr = "192.168.1.204:43210"
			req.Host = videoTestHost
			tc.edit(req)
			if actorID, ok := videoAccessConfig().videoOwnerID(req); ok {
				t.Fatalf("request unexpectedly allowed with actor %q", actorID)
			}
		})
	}
}

func TestVideoActorIDRequiresOneExactHTTPOriginForPostAndDelete(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		cases := []struct {
			name    string
			origins []string
		}{
			{name: "missing"},
			{name: "wrong scheme", origins: []string{"https://" + videoTestHost}},
			{name: "wrong host", origins: []string{"http://192.168.1.205:18790"}},
			{name: "duplicate", origins: []string{videoTestOrigin, videoTestOrigin}},
		}
		for _, tc := range cases {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(method, "/viewer/image/video/generations", nil)
				req.RemoteAddr = "192.168.1.204:43210"
				req.Host = videoTestHost
				for _, origin := range tc.origins {
					req.Header.Add("Origin", origin)
				}
				if _, ok := videoAccessConfig().videoOwnerID(req); ok {
					t.Fatal("request unexpectedly allowed")
				}
			})
		}
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
				return HandleVideoOptions(g, videoAccessConfig())
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
				return HandleVideoGenerate(g, videoAccessConfig())
			},
		},
		{
			name: "status",
			request: func(_ *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/viewer/image/video/generations/vid_abc", nil)
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoStatus(g, videoAccessConfig())
			},
		},
		{
			name: "mp4",
			request: func(_ *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/viewer/image/video/vid_abc.mp4", nil)
			},
			handler: func(g *fakeVideoGateway) http.HandlerFunc {
				return HandleVideoResult(g, videoAccessConfig())
			},
		},
	}
	edits := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong source", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.205:43000" }},
		{name: "wrong host", edit: func(r *http.Request) { r.Host = "attacker.example" }},
		{name: "wrong origin", edit: func(r *http.Request) { r.Header.Set("Origin", "http://attacker.example") }},
		{name: "spoofed Tailscale identity", edit: func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "yukimi@example.com") }},
		{name: "proxy header", edit: func(r *http.Request) { r.Header.Set("X-Forwarded-For", "192.168.1.204") }},
		{name: "forwarded address cannot override socket", edit: func(r *http.Request) {
			r.RemoteAddr = "192.168.1.205:43210"
			r.Header.Set("X-Forwarded-For", "192.168.1.204")
		}},
	}
	for _, route := range routes {
		for _, edit := range edits {
			t.Run(route.name+"/"+edit.name, func(t *testing.T) {
				gateway := &fakeVideoGateway{}
				req := route.request(t)
				req.RemoteAddr = "192.168.1.204:43210"
				req.Host = videoTestHost
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
