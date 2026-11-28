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

const movieRenderHandlerToken = "movie-render-handler-token-0123456789abcdef"

type movieRenderDownloadStub struct {
	actorID     string
	calls       int
	jobID       string
	rangeHeader string
	response    *http.Response
}

func (s *movieRenderDownloadStub) ActorID() string { return s.actorID }
func (s *movieRenderDownloadStub) OpenMovieRender(_ context.Context, jobID, rangeHeader string) (*http.Response, error) {
	s.calls++
	s.jobID = jobID
	s.rangeHeader = rangeHeader
	return s.response, nil
}

func TestHandleMovieRenderDownloadRequiresCMDOwnerAndPreservesRangeAndETag(t *testing.T) {
	data := []byte("mp4!")
	gateway := &movieRenderDownloadStub{
		actorID: "ren",
		response: &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":  []string{"video/mp4"},
				"Content-Range": []string{"bytes 5-8/20"},
				"Etag":          []string{testMovieRenderETag()},
			},
			ContentLength: int64(len(data)),
			Body:          io.NopCloser(bytes.NewReader(data)),
		},
	}
	handler := WithCMDMioOwnerAuthentication(HandleMovieRenderDownload(gateway), "ren", []byte(movieRenderHandlerToken))
	request := httptest.NewRequest(http.MethodGet, "/viewer/movie/jobs/job_opaque/download?to=mio", nil)
	request.Header.Set("Authorization", "Bearer "+movieRenderHandlerToken)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.Header.Set("Range", "bytes=5-8")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != string(data) {
		upstreamETag := gateway.response.Header.Get("ETag")
		t.Fatalf("Movie render download = status %d body=%q upstream type=%q length=%d ETag=%q validETag=%v", response.Code, response.Body.String(), gateway.response.Header.Get("Content-Type"), gateway.response.ContentLength, upstreamETag, movieRenderETagPattern.MatchString(upstreamETag))
	}
	if response.Header().Get("Content-Type") != "video/mp4" || response.Header().Get("ETag") == "" || response.Header().Get("Content-Range") != "bytes 5-8/20" || response.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Movie render response metadata = %#v", response.Header())
	}
	if gateway.calls != 1 || gateway.jobID != "job_opaque" || gateway.rangeHeader != "bytes=5-8" {
		t.Fatalf("Movie gateway input calls=%d id=%q range=%q", gateway.calls, gateway.jobID, gateway.rangeHeader)
	}

	request = httptest.NewRequest(http.MethodGet, "/viewer/movie/jobs/job_opaque/download?to=mio", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || gateway.calls != 1 {
		t.Fatalf("anonymous Movie read status=%d gatewayCalls=%d", response.Code, gateway.calls)
	}
}

func TestHandleMovieRenderDownloadRejectsInvalidQueryAndUpstreamMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		path     string
		rangeVal string
		response *http.Response
		want     int
	}{
		{name: "extra query", path: "/viewer/movie/jobs/job_opaque/download?to=mio&token=secret", response: movieRenderResponse(), want: http.StatusNotFound},
		{name: "invalid range", path: "/viewer/movie/jobs/job_opaque/download?to=mio", rangeVal: "bytes=1-2,4-5", response: movieRenderResponse(), want: http.StatusBadRequest},
		{name: "wrong content type", path: "/viewer/movie/jobs/job_opaque/download?to=mio", response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/octet-stream"}, "Etag": []string{testMovieRenderETag()}}, ContentLength: 4, Body: io.NopCloser(bytes.NewReader([]byte("data")))}, want: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := &movieRenderDownloadStub{actorID: "ren", response: test.response}
			handler := WithCMDMioOwnerAuthentication(HandleMovieRenderDownload(gateway), "ren", []byte(movieRenderHandlerToken))
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+movieRenderHandlerToken)
			request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
			request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
			if test.rangeVal != "" {
				request.Header.Set("Range", test.rangeVal)
			}
			request.RemoteAddr = "127.0.0.1:12345"
			request.Host = "127.0.0.1:18790"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("Movie render response status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
			if test.name == "extra query" || test.name == "invalid range" {
				if gateway.calls != 0 {
					t.Fatalf("invalid request reached Movie gateway %d times", gateway.calls)
				}
			}
		})
	}
}

func movieRenderResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
			"Etag":         []string{testMovieRenderETag()},
		},
		ContentLength: 4,
		Body:          io.NopCloser(bytes.NewReader([]byte("data"))),
	}
}

func testMovieRenderETag() string {
	return `"sha256:` + strings.Repeat("a", 64) + `"`
}
