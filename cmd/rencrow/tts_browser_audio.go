package main

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func buildLocalTTSAudioURL(outputDir, audioPath string) string {
	return moduletts.BuildLocalAudioURL(outputDir, audioPath)
}

func localTTSAudioRelPath(outputDir, audioPath string) (string, bool) {
	return moduletts.LocalAudioRelPath(outputDir, audioPath)
}

func handleLocalTTSAudio(outputDir string, accessOwners ...*ttsAudioAccessOwner) http.HandlerFunc {
	return handleTTSAudio(outputDir, "", accessOwners...)
}

func handleTTSAudio(outputDir, ttsBaseURL string, accessOwners ...*ttsAudioAccessOwner) http.HandlerFunc {
	baseDir, ok := normalizeLocalTTSAudioBase(outputDir)
	allowedRemoteHost := allowedTTSAudioHost(ttsBaseURL)
	var accessOwner *ttsAudioAccessOwner
	if len(accessOwners) == 1 {
		accessOwner = accessOwners[0]
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		resourceKind, rawResource, validQuery := ttsAudioQueryResource(r)
		if !validQuery || accessOwner == nil {
			http.NotFound(w, r)
			return
		}
		if resourceKind == ttsAudioLocalResource && isSpeechEvalArtifactPathAttempt(rawResource) {
			target, validPath := accessOwner.resolveSpeechEvalArtifact(rawResource)
			if !validPath {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, target)
			return
		}
		var registration ttsAudioAccessRegistration
		switch resourceKind {
		case ttsAudioLocalResource:
			relPath, validPath := localTTSAudioRelPath(outputDir, rawResource)
			if !validPath || !ok {
				http.NotFound(w, r)
				return
			}
			registration, validPath = accessOwner.lookupPathRegistration(relPath)
			if !validPath {
				http.NotFound(w, r)
				return
			}
		case ttsAudioRemoteResource:
			canonicalURL, validURL := normalizeRegisteredTTSAudioURL(rawResource, allowedRemoteHost)
			if !validURL {
				http.NotFound(w, r)
				return
			}
			registration, validURL = accessOwner.lookupURLRegistration(canonicalURL)
			if !validURL {
				http.NotFound(w, r)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		private, accessStatus := accessOwner.authorize(r, registration)
		if accessStatus != 0 {
			if private {
				w.Header().Set("Cache-Control", "private, no-store")
			}
			if accessStatus == http.StatusUnauthorized {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if private {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		guardedWriter := &ttsAudioGuardedResponseWriter{
			ResponseWriter: w, request: r, owner: accessOwner, registration: registration,
		}
		if resourceKind == ttsAudioRemoteResource {
			proxyRemoteTTSAudio(guardedWriter, r, client, rawResource, allowedRemoteHost)
			return
		}
		target, validPath := resolveLocalTTSAudioPath(baseDir, rawResource)
		if !validPath {
			http.NotFound(w, r)
			return
		}
		target, validPath = resolveContainedTTSAudioPath(baseDir, target)
		if !validPath {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(guardedWriter, r, target)
	}
}

var errTTSAudioRegistrationRevoked = errors.New("TTS audio registration is no longer current")

// ttsAudioGuardedResponseWriter rechecks the immutable audio authority after
// the source has been opened/read and before each response write. It never
// holds the Session owner guard while reading files, fetching upstream data,
// or writing an entire response.
type ttsAudioGuardedResponseWriter struct {
	http.ResponseWriter
	request      *http.Request
	owner        *ttsAudioAccessOwner
	registration ttsAudioAccessRegistration
	wroteHeader  bool
	denied       bool
}

func (w *ttsAudioGuardedResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader || w.denied {
		return
	}
	if w.request == nil || w.request.Context().Err() != nil {
		w.denied = true
		return
	}
	if !w.owner.isRegistrationCurrent(w.request.Context(), w.registration) {
		w.denyBeforeResponse()
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *ttsAudioGuardedResponseWriter) Write(data []byte) (int, error) {
	if w.denied {
		return 0, errTTSAudioRegistrationRevoked
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
		if w.denied {
			return 0, errTTSAudioRegistrationRevoked
		}
	}
	if w.request == nil || w.request.Context().Err() != nil {
		w.denied = true
		return 0, errTTSAudioRegistrationRevoked
	}
	if !w.owner.isRegistrationCurrent(w.request.Context(), w.registration) {
		w.denied = true
		return 0, errTTSAudioRegistrationRevoked
	}
	return w.ResponseWriter.Write(data)
}

func (w *ttsAudioGuardedResponseWriter) denyBeforeResponse() {
	w.denied = true
	w.wroteHeader = true
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "Content-Type"} {
		w.ResponseWriter.Header().Del(header)
	}
	http.NotFound(w.ResponseWriter, w.request)
}

type ttsAudioResourceKind string

const (
	ttsAudioLocalResource  ttsAudioResourceKind = "path"
	ttsAudioRemoteResource ttsAudioResourceKind = "url"
)

func ttsAudioQueryResource(r *http.Request) (ttsAudioResourceKind, string, bool) {
	if r == nil || r.URL == nil {
		return "", "", false
	}
	query := r.URL.Query()
	if len(query) != 1 {
		return "", "", false
	}
	for key, values := range query {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", "", false
		}
		switch key {
		case string(ttsAudioLocalResource):
			return ttsAudioLocalResource, values[0], true
		case string(ttsAudioRemoteResource):
			return ttsAudioRemoteResource, values[0], true
		default:
			return "", "", false
		}
	}
	return "", "", false
}

func allowedTTSAudioHost(ttsBaseURL string) string {
	u, err := url.Parse(strings.TrimSpace(ttsBaseURL))
	if err != nil || u == nil {
		return ""
	}
	return strings.TrimSpace(u.Host)
}

func proxyRemoteTTSAudio(w http.ResponseWriter, r *http.Request, client *http.Client, rawURL, allowedHost string) {
	canonicalURL, ok := normalizeRegisteredTTSAudioURL(rawURL, allowedHost)
	if !ok {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, canonicalURL, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.NotFound(w, r)
		return
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "audio/wav"
	}
	w.Header().Set("Content-Type", contentType)
	if contentLength := strings.TrimSpace(resp.Header.Get("Content-Length")); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func isAllowedRemoteTTSAudioHost(rawHost, allowedHost string) bool {
	host := strings.TrimSpace(rawHost)
	configuredHost := strings.TrimSpace(allowedHost)
	if host == "" || configuredHost == "" {
		return false
	}
	return strings.EqualFold(host, configuredHost)
}

func normalizeLocalTTSAudioBase(outputDir string) (string, bool) {
	return moduletts.NormalizeLocalAudioBase(outputDir)
}

func resolveLocalTTSAudioPath(baseDir, rawRelPath string) (string, bool) {
	return moduletts.ResolveLocalAudioPath(baseDir, rawRelPath)
}

func resolveContainedTTSAudioPath(baseDir, target string) (string, bool) {
	resolvedBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", false
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(resolvedBase, resolvedTarget)
	if err != nil {
		return "", false
	}
	rel = filepath.Clean(rel)
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolvedTarget, true
}
