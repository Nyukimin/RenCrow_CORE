package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	movieRenderRoutePrefix = "/viewer/movie/jobs/"
	maxMovieRenderByteSize = 1 << 30
)

var (
	movieRenderJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	movieRenderRangePattern = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)
	movieRenderContentRange = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)
	movieRenderUnsatRange   = regexp.MustCompile(`^bytes \*/([0-9]+)$`)
	movieRenderETagPattern  = regexp.MustCompile(`^"sha256:[0-9a-f]{64}"$`)
)

type MovieRenderDownloadGateway interface {
	ActorID() string
	OpenMovieRender(ctx context.Context, jobID, rangeHeader string) (*http.Response, error)
}

func HandleMovieRenderDownload(gateway MovieRenderDownloadGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ownerID, authenticated, scopeErr := AuthenticatedViewerOwnerID(r.Context())
		if scopeErr != nil || !authenticated || gateway == nil || ownerID == "" || ownerID != gateway.ActorID() {
			http.NotFound(w, r)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["to"]) != 1 || query.Get("to") != "mio" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, movieRenderRoutePrefix) || !strings.HasSuffix(r.URL.Path, "/download") {
			http.NotFound(w, r)
			return
		}
		jobID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, movieRenderRoutePrefix), "/download")
		if !movieRenderJobIDPattern.MatchString(jobID) || strings.Contains(jobID, "/") {
			http.NotFound(w, r)
			return
		}
		ranges := headerValuesCaseInsensitive(r.Header, "Range")
		if len(ranges) > 1 || len(ranges) == 1 && (len(ranges[0]) > 256 || !movieRenderRangePattern.MatchString(ranges[0])) {
			http.Error(w, "invalid byte range", http.StatusBadRequest)
			return
		}
		rangeHeader := ""
		if len(ranges) == 1 {
			rangeHeader = ranges[0]
		}
		response, err := gateway.OpenMovieRender(r.Context(), jobID, rangeHeader)
		if err != nil {
			writeMovieRenderUpstreamError(w, err)
			return
		}
		if response == nil || response.Body == nil {
			http.Error(w, "Movie render is unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if parseErr != nil || mediaType != "video/mp4" || response.ContentLength < 1 || response.ContentLength > maxMovieRenderByteSize || !movieRenderETagPattern.MatchString(response.Header.Get("ETag")) {
				http.Error(w, "Movie returned invalid MP4 metadata", http.StatusBadGateway)
				return
			}
			contentRange := ""
			if response.StatusCode == http.StatusPartialContent {
				contentRange = response.Header.Get("Content-Range")
				match := movieRenderContentRange.FindStringSubmatch(contentRange)
				if match == nil {
					http.Error(w, "Movie returned an invalid byte range", http.StatusBadGateway)
					return
				}
				start, startErr := strconv.ParseInt(match[1], 10, 64)
				end, endErr := strconv.ParseInt(match[2], 10, 64)
				total, totalErr := strconv.ParseInt(match[3], 10, 64)
				if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total < end+1 || total > maxMovieRenderByteSize || end-start+1 != response.ContentLength {
					http.Error(w, "Movie returned an invalid byte range", http.StatusBadGateway)
					return
				}
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
			w.Header().Set("ETag", response.Header.Get("ETag"))
			if contentRange != "" {
				w.Header().Set("Content-Range", contentRange)
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.CopyN(w, response.Body, response.ContentLength)
		case http.StatusRequestedRangeNotSatisfiable:
			match := movieRenderUnsatRange.FindStringSubmatch(response.Header.Get("Content-Range"))
			if match == nil {
				http.Error(w, "Movie returned an invalid byte range", http.StatusBadGateway)
				return
			}
			total, parseErr := strconv.ParseInt(match[1], 10, 64)
			if parseErr != nil || total < 1 || total > maxMovieRenderByteSize {
				http.Error(w, "Movie returned an invalid byte range", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Range", response.Header.Get("Content-Range"))
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			writeMovieRenderUpstreamStatus(w, response.StatusCode)
		}
	}
}

func writeMovieRenderUpstreamError(w http.ResponseWriter, err error) {
	var status interface{ MovieGatewayStatusCode() int }
	if errors.As(err, &status) {
		writeMovieRenderUpstreamStatus(w, status.MovieGatewayStatusCode())
		return
	}
	http.Error(w, "Movie render is unavailable", http.StatusBadGateway)
}

func writeMovieRenderUpstreamStatus(w http.ResponseWriter, statusCode int) {
	switch statusCode {
	case http.StatusNotFound:
		http.Error(w, "404 page not found", http.StatusNotFound)
	case http.StatusRequestedRangeNotSatisfiable:
		http.Error(w, "invalid byte range", http.StatusRequestedRangeNotSatisfiable)
	default:
		http.Error(w, fmt.Sprintf("Movie render is unavailable (HTTP %d)", statusCode), http.StatusBadGateway)
	}
}
