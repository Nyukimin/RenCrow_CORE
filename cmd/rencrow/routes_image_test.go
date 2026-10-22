package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestRegisterImageRoutesServesConfiguredOptionsOnCanonicalViewerPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/images/options" {
			http.Error(w, "wrong gateway route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"default_model":"forge_zimage_4060","models":[{"id":"forge_zimage_4060","engine":"comfyui","defaults":{"width":768,"height":768,"steps":28,"cfg_scale":4.5},"limits":{"width":{"min":256,"max":1536,"multiple_of":64},"height":{"min":256,"max":1536,"multiple_of":64},"steps":{"min":1,"max":60},"cfg_scale":{"min":1,"max":12,"step":0.5}},"features":{"face_detailer":false,"lora_strength":false}}]}`)
	}))
	defer server.Close()

	mux := http.NewServeMux()
	registerImageRoutes(mux, &config.Config{Image: config.ImageConfig{
		Enabled: true, BaseURL: server.URL, TimeoutMS: 1000,
	}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/image/options", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET /viewer/image/options status=%d body=%q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"default_model":"forge_zimage_4060"`) {
		t.Fatalf("unexpected options response: %s", response.Body.String())
	}
}

func TestRegisterImageRoutesProxiesVideoOptionsOnCanonicalPathWithLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/options" || r.URL.Query().Get("mode") != "i2v" {
			http.Error(w, "wrong Image video options route", http.StatusNotFound)
			return
		}
		if r.Header.Get("X-RenCrow-Actor-ID") != "" {
			http.Error(w, "options must not require an actor", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"video-i2v-v1","mode":"i2v","aspect_ratios":["1:1 (Square)"],"default_aspect_ratio":"1:1 (Square)","megapixels":0.2,"duration_seconds":5,"steps":8}`)
	}))
	defer server.Close()

	const host = "llm-rx6800.tailb07d8d.ts.net:8444"
	mux := http.NewServeMux()
	registerImageRoutes(mux, &config.Config{Image: config.ImageConfig{
		Enabled: true, BaseURL: server.URL, TimeoutMS: 1000,
		VideoBrowser: config.ImageVideoBrowserConfig{
			Enabled: true, AllowGeneration: true, Host: host, Origin: "https://" + host,
			ActorID: "user:yukimi", TailscaleUserLogin: "yukimi@example.com",
		},
	}})
	req := httptest.NewRequest(http.MethodGet, "/viewer/image/video/options?mode=i2v", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = host
	req.Header.Set("Tailscale-User-Login", "yukimi@example.com")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mode":"i2v"`) ||
		!strings.Contains(response.Body.String(), `"default_aspect_ratio":"1:1 (Square)"`) {
		t.Fatalf("GET video options status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRegisterImageRoutesRemovesVideoSessionEndpoint(t *testing.T) {
	const host = "llm-rx6800.tailb07d8d.ts.net:8444"
	mux := http.NewServeMux()
	registerImageRoutes(mux, &config.Config{Image: config.ImageConfig{
		VideoBrowser: config.ImageVideoBrowserConfig{
			Enabled: true, Host: host, Origin: "https://" + host,
			ActorID: "user:yukimi", TailscaleUserLogin: "yukimi@example.com",
		},
	}})
	req := httptest.NewRequest(http.MethodPost, "/viewer/image/video/session", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = host
	req.Header.Set("Origin", "https://"+host)
	req.Header.Set("Tailscale-User-Login", "yukimi@example.com")
	if _, pattern := mux.Handler(req); pattern != "/viewer/image/video/" {
		t.Fatalf("video session path matched %q, want only the generic video result route", pattern)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST removed video session endpoint status=%d body=%q", response.Code, response.Body.String())
	}
}
