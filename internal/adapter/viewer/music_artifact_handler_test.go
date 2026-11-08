package viewer

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const musicArtifactOwnerToken = "viewer-music-owner-token-0123456789abcdef"

type musicArtifactGatewayStub struct {
	actorID    string
	artifactID string
	rangeValue string
	calls      int
	response   *http.Response
	err        error
}

func (s *musicArtifactGatewayStub) ActorID() string { return s.actorID }
func (s *musicArtifactGatewayStub) OpenMusicArtifact(_ context.Context, artifactID, rangeValue string) (*http.Response, error) {
	s.calls++
	s.artifactID = artifactID
	s.rangeValue = rangeValue
	return s.response, s.err
}

func TestMusicArtifactDownloadUsesCMDOwnerProofAndForwardsSingleRange(t *testing.T) {
	gateway := &musicArtifactGatewayStub{
		actorID: "ren", response: &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":   []string{"audio/flac"},
				"Content-Range":  []string{"bytes 10-19/100"},
				"Content-Length": []string{"10"},
			},
			ContentLength: 10,
			Body:          io.NopCloser(bytes.NewReader([]byte("0123456789"))),
		},
	}
	handler := WithCMDMioOwnerAuthentication(HandleMusicArtifactDownload(gateway), "ren", []byte(musicArtifactOwnerToken))
	request := authenticatedMusicArtifactRequest("/viewer/music/artifacts/art_opaque/download?to=mio", "bytes=10-19")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent || response.Body.String() != "0123456789" {
		t.Fatalf("Range proxy response = status %d body=%q", response.Code, response.Body.String())
	}
	if gateway.calls != 1 || gateway.artifactID != "art_opaque" || gateway.rangeValue != "bytes=10-19" {
		t.Fatalf("gateway request = calls %d artifact %q Range %q", gateway.calls, gateway.artifactID, gateway.rangeValue)
	}
	for key, want := range map[string]string{
		"Content-Type": "audio/flac", "Content-Range": "bytes 10-19/100", "Accept-Ranges": "bytes",
		"Content-Length": "10", "Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff",
	} {
		if got := response.Header().Get(key); got != want {
			t.Errorf("response %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(response.Body.String(), musicArtifactOwnerToken) {
		t.Fatal("artifact response leaked the CMD bearer token")
	}
}

func TestMusicArtifactDownloadRejectsUntrustedOwnerIDsAndRanges(t *testing.T) {
	for _, tt := range []struct {
		name      string
		path      string
		rangeHead string
		ownerID   string
		setAuth   bool
		wantCode  int
	}{
		{name: "anonymous browser has no CMD owner proof", path: "/viewer/music/artifacts/art_opaque/download?to=mio", ownerID: "ren", wantCode: http.StatusNotFound},
		{name: "different configured owner", path: "/viewer/music/artifacts/art_opaque/download?to=mio", ownerID: "other", setAuth: true, wantCode: http.StatusNotFound},
		{name: "unsafe opaque ID", path: "/viewer/music/artifacts/%2e%2e%2fsecret/download?to=mio", ownerID: "ren", setAuth: true, wantCode: http.StatusNotFound},
		{name: "duplicate recipient", path: "/viewer/music/artifacts/art_opaque/download?to=mio&to=mio", ownerID: "ren", setAuth: true, wantCode: http.StatusBadRequest},
		{name: "multiple byte ranges", path: "/viewer/music/artifacts/art_opaque/download?to=mio", rangeHead: "bytes=0-1,5-6", ownerID: "ren", setAuth: true, wantCode: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &musicArtifactGatewayStub{actorID: "ren"}
			handler := WithCMDMioOwnerAuthentication(HandleMusicArtifactDownload(gateway), tt.ownerID, []byte(musicArtifactOwnerToken))
			request := authenticatedMusicArtifactRequest(tt.path, tt.rangeHead)
			if !tt.setAuth {
				request.Header.Del("Authorization")
				request.Header.Del("X-RenCrow-Client")
				request.Header.Del("X-RenCrow-Interaction-Profile")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.wantCode {
				t.Fatalf("response status = %d, want %d body=%q", response.Code, tt.wantCode, response.Body.String())
			}
			if gateway.calls != 0 {
				t.Fatalf("unauthorized or invalid request reached Music %d times", gateway.calls)
			}
		})
	}
}

func TestMusicArtifactDownloadValidatesUpstreamMediaAndUnsatisfiedRange(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response *http.Response
		wantCode int
	}{
		{
			name: "unsatisfied single range", response: &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable,
				Header: http.Header{"Content-Range": []string{"bytes */100"}}, Body: io.NopCloser(strings.NewReader(""))},
			wantCode: http.StatusRequestedRangeNotSatisfiable,
		},
		{
			name: "unsupported media type", response: &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"text/html"}}, ContentLength: 10, Body: io.NopCloser(strings.NewReader("<html>"))},
			wantCode: http.StatusBadGateway,
		},
		{
			name: "redirect is not exposed", response: &http.Response{StatusCode: http.StatusFound,
				Header: http.Header{"Location": []string{"http://127.0.0.1:9999/private"}}, Body: io.NopCloser(strings.NewReader(""))},
			wantCode: http.StatusBadGateway,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &musicArtifactGatewayStub{actorID: "ren", response: tt.response}
			handler := WithCMDMioOwnerAuthentication(HandleMusicArtifactDownload(gateway), "ren", []byte(musicArtifactOwnerToken))
			request := authenticatedMusicArtifactRequest("/viewer/music/artifacts/art_opaque/download?to=mio", "bytes=0-9")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.wantCode {
				t.Fatalf("response status = %d, want %d", response.Code, tt.wantCode)
			}
			if strings.Contains(response.Header().Get("Location"), "private") {
				t.Fatal("upstream redirect location was forwarded")
			}
		})
	}
}

func authenticatedMusicArtifactRequest(path, rangeHeader string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+musicArtifactOwnerToken)
	request.Header.Set("X-RenCrow-Client", viewerCMDClient)
	request.Header.Set("X-RenCrow-Interaction-Profile", viewerCMDChatProfile)
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	return request
}
