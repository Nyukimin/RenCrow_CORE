package viewer

import (
	"context"
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
	musicArtifactRoutePrefix  = "/viewer/music/artifacts/"
	maxMusicArtifactRangeSize = 64 << 20
	maxMusicRangeHeaderSize   = 256
)

var (
	musicArtifactIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	musicRangeRequest      = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)
	musicContentRange      = regexp.MustCompile(`^bytes [0-9]+-[0-9]+/(?:[0-9]+|\*)$`)
	musicUnsatRange        = regexp.MustCompile(`^bytes \*/[0-9]+$`)
)

// MusicArtifactGateway is the actor-bound authenticated Music Range client.
// Implementations must never expose the backend URL or filesystem location.
type MusicArtifactGateway interface {
	ActorID() string
	OpenMusicArtifact(ctx context.Context, artifactID string, rangeHeader string) (*http.Response, error)
}

// HandleMusicArtifactDownload proxies a single opaque Music artifact ID. The
// caller must wrap it in WithCMDMioOwnerAuthentication, and this handler still
// requires the resulting owner scope to match the fixed Music actor.
func HandleMusicArtifactDownload(gateway MusicArtifactGateway) http.HandlerFunc {
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
		const suffix = "/download"
		if !strings.HasPrefix(r.URL.Path, musicArtifactRoutePrefix) || !strings.HasSuffix(r.URL.Path, suffix) {
			http.NotFound(w, r)
			return
		}
		artifactID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, musicArtifactRoutePrefix), suffix)
		if !musicArtifactIDPattern.MatchString(artifactID) || strings.Contains(artifactID, "/") {
			http.NotFound(w, r)
			return
		}
		ranges := headerValuesCaseInsensitive(r.Header, "Range")
		if len(ranges) > 1 || (len(ranges) == 1 && (len(ranges[0]) > maxMusicRangeHeaderSize || !musicRangeRequest.MatchString(ranges[0]))) {
			http.Error(w, "invalid byte range", http.StatusBadRequest)
			return
		}
		rangeHeader := ""
		if len(ranges) == 1 {
			rangeHeader = ranges[0]
		}
		response, err := gateway.OpenMusicArtifact(r.Context(), artifactID, rangeHeader)
		if err != nil {
			writeMusicArtifactUpstreamError(w, err)
			return
		}
		if response == nil || response.Body == nil {
			http.Error(w, "Music artifact is unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if parseErr != nil || !musicArtifactMediaTypeAllowed(mediaType) {
				http.Error(w, "Music returned an unsupported audio type", http.StatusBadGateway)
				return
			}
			if response.ContentLength < 1 || response.ContentLength > maxMusicArtifactRangeSize {
				http.Error(w, "Music artifact exceeded the streaming limit", http.StatusBadGateway)
				return
			}
			if response.StatusCode == http.StatusPartialContent && !musicContentRange.MatchString(response.Header.Get("Content-Range")) {
				http.Error(w, "Music returned an invalid byte range", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", mediaType)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
			if response.StatusCode == http.StatusPartialContent {
				w.Header().Set("Content-Range", response.Header.Get("Content-Range"))
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.CopyN(w, response.Body, response.ContentLength)
		case http.StatusRequestedRangeNotSatisfiable:
			contentRange := response.Header.Get("Content-Range")
			if !musicUnsatRange.MatchString(contentRange) {
				http.Error(w, "Music returned an invalid byte range", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Range", contentRange)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			writeMusicArtifactUpstreamStatus(w, response.StatusCode)
		}
	}
}

func musicArtifactMediaTypeAllowed(mediaType string) bool {
	switch mediaType {
	case "audio/flac", "audio/wav", "audio/x-wav":
		return true
	default:
		return false
	}
}

func writeMusicArtifactUpstreamError(w http.ResponseWriter, err error) {
	if status, ok := err.(interface{ MusicGatewayStatusCode() int }); ok {
		writeMusicArtifactUpstreamStatus(w, status.MusicGatewayStatusCode())
		return
	}
	http.Error(w, "Music artifact is unavailable", http.StatusBadGateway)
}

func writeMusicArtifactUpstreamStatus(w http.ResponseWriter, statusCode int) {
	switch statusCode {
	case http.StatusNotFound:
		http.Error(w, "404 page not found", http.StatusNotFound)
	case http.StatusRequestedRangeNotSatisfiable:
		http.Error(w, "invalid byte range", http.StatusRequestedRangeNotSatisfiable)
	default:
		http.Error(w, fmt.Sprintf("Music artifact is unavailable (HTTP %d)", statusCode), http.StatusBadGateway)
	}
}
