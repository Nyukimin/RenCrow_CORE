package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func videoSessionRequest(method string, body string) *http.Request {
	req := httptest.NewRequest(method, "/viewer/image/video/session", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = videoTestHost
	req.Header.Set("Origin", videoTestOrigin)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestHandleVideoSessionLoginAndLogoutUseSecureScopedCookie(t *testing.T) {
	auth := videoAuthConfig()
	rec := httptest.NewRecorder()
	HandleVideoSession(auth).ServeHTTP(rec, videoSessionRequest(http.MethodPost, `{"credential":"`+videoTestToken+`"}`))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), videoTestToken) {
		t.Fatalf("login status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login Cache-Control=%q", rec.Header().Get("Cache-Control"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want one", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != videoSessionCookieName || cookie.Value == "" || cookie.Path != videoSessionCookiePath ||
		cookie.Domain != "" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie attributes are incomplete: name=%q path=%q domain=%q secure=%t httpOnly=%t sameSite=%d maxAge=%d", cookie.Name, cookie.Path, cookie.Domain, cookie.Secure, cookie.HttpOnly, cookie.SameSite, cookie.MaxAge)
	}

	statusReq := videoSessionRequest(http.MethodGet, "")
	statusReq.Header.Del("Origin")
	statusReq.AddCookie(cookie)
	statusRec := httptest.NewRecorder()
	HandleVideoSession(auth).ServeHTTP(statusRec, statusReq)
	var session struct {
		Authenticated bool `json:"authenticated"`
	}
	if statusRec.Code != http.StatusOK || json.Unmarshal(statusRec.Body.Bytes(), &session) != nil || !session.Authenticated {
		t.Fatalf("session status=%d body=%q", statusRec.Code, statusRec.Body.String())
	}

	logoutReq := videoSessionRequest(http.MethodDelete, "")
	logoutReq.AddCookie(cookie)
	logoutRec := httptest.NewRecorder()
	HandleVideoSession(auth).ServeHTTP(logoutRec, logoutReq)
	cleared := logoutRec.Result().Cookies()
	if logoutRec.Code != http.StatusNoContent || len(cleared) != 1 || cleared[0].Name != videoSessionCookieName ||
		cleared[0].Path != videoSessionCookiePath || !cleared[0].Secure || !cleared[0].HttpOnly || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout status=%d cookies=%+v", logoutRec.Code, cleared)
	}
}

func TestHandleVideoSessionRejectsWrongCredential(t *testing.T) {
	req := videoSessionRequest(http.MethodPost, `{"credential":"`+strings.Repeat("x", 40)+`"}`)
	rec := httptest.NewRecorder()
	HandleVideoSession(videoAuthConfig()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Set-Cookie") != "" || strings.Contains(rec.Body.String(), "wrong-token") {
		t.Fatalf("wrong credential status=%d cookie=%q body=%q", rec.Code, rec.Header().Get("Set-Cookie"), rec.Body.String())
	}
}

func TestHandleVideoSessionRejectsUntrustedRoute(t *testing.T) {
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong host", edit: func(r *http.Request) { r.Host = "attacker.example" }},
		{name: "LAN request", edit: func(r *http.Request) { r.RemoteAddr = "192.168.1.207:43210" }},
		{name: "missing HTTPS origin", edit: func(r *http.Request) { r.Header.Del("Origin") }},
		{name: "wrong origin", edit: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") }},
		{name: "duplicate origin", edit: func(r *http.Request) { r.Header.Add("Origin", videoTestOrigin) }},
		{name: "Funnel marker", edit: func(r *http.Request) { r.Header.Set("Tailscale-Funnel-Request", "true") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := videoSessionRequest(http.MethodPost, `{"credential":"`+videoTestToken+`"}`)
			tc.edit(req)
			rec := httptest.NewRecorder()
			HandleVideoSession(videoAuthConfig()).ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound || rec.Header().Get("Set-Cookie") != "" {
				t.Fatalf("untrusted request status=%d Set-Cookie=%q", rec.Code, rec.Header().Get("Set-Cookie"))
			}
		})
	}
}

func TestVideoRoutesRejectInvalidSessionBeforeGateway(t *testing.T) {
	auth := videoAuthConfig()
	otherUser := videoAuthConfig()
	otherUser.ActorID = "user:other"
	expired, err := auth.signVideoSession(time.Now().Add(-time.Minute).Unix(), []byte(strings.Repeat("n", 32)))
	if err != nil {
		t.Fatalf("sign expired session: %v", err)
	}
	otherUserCookie, err := otherUser.newSessionCookie(time.Now())
	if err != nil {
		t.Fatalf("create other-user session: %v", err)
	}
	validCookie, err := auth.newSessionCookie(time.Now())
	if err != nil {
		t.Fatalf("create valid session: %v", err)
	}
	parts := strings.Split(validCookie.Value, ".")
	if len(parts) != 4 || parts[3] == "" {
		t.Fatal("test session cookie has unexpected format")
	}
	firstSignatureByte := byte('x')
	if parts[3][0] == firstSignatureByte {
		firstSignatureByte = 'y'
	}
	parts[3] = string(firstSignatureByte) + parts[3][1:]
	tampered := strings.Join(parts, ".")
	invalidCookies := []struct {
		name  string
		value string
	}{
		{name: "unauthenticated"},
		{name: "tampered", value: tampered},
		{name: "expired", value: expired},
		{name: "other actor", value: otherUserCookie.Value},
	}
	routes := []struct {
		name   string
		method string
		path   string
	}{
		{name: "generate", method: http.MethodPost, path: "/viewer/image/video/generations"},
		{name: "status", method: http.MethodGet, path: "/viewer/image/video/generations/vid_abc"},
		{name: "media", method: http.MethodGet, path: "/viewer/image/video/vid_abc.mp4"},
	}
	for _, route := range routes {
		for _, invalid := range invalidCookies {
			t.Run(route.name+"/"+invalid.name, func(t *testing.T) {
				gateway := &fakeVideoGateway{}
				var req *http.Request
				if route.name == "generate" {
					req, _ = videoRequest(t, "t2v", nil)
				} else {
					req = httptest.NewRequest(route.method, route.path, nil)
				}
				req.RemoteAddr = "127.0.0.1:43210"
				req.Host = videoTestHost
				req.Header.Set("Origin", videoTestOrigin)
				if route.name == "media" {
					req.Header.Set("Range", "bytes=0-4")
				}
				if invalid.value != "" {
					req.Header.Set("Cookie", videoSessionCookieName+"="+invalid.value)
				}
				rec := httptest.NewRecorder()
				switch route.name {
				case "generate":
					HandleVideoGenerate(gateway, auth).ServeHTTP(rec, req)
				case "status":
					HandleVideoStatus(gateway, auth).ServeHTTP(rec, req)
				case "media":
					HandleVideoResult(gateway, auth).ServeHTTP(rec, req)
				}
				if rec.Code != http.StatusNotFound || gateway.createCalls != 0 || gateway.statusCalls != 0 || gateway.mediaCalls != 0 {
					t.Fatalf("status=%d gateway calls create=%d status=%d media=%d", rec.Code, gateway.createCalls, gateway.statusCalls, gateway.mediaCalls)
				}
			})
		}
	}
}
