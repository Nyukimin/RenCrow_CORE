package tts

import (
	"net"
	"net/url"
	"path"
	"strings"
)

// resolveAudioURL builds a browser-fetchable URL from explicit audio_url or audio_path.
func resolveAudioURL(httpBaseURL, audioPath, explicitAudioURL string) string {
	base := strings.TrimSpace(httpBaseURL)
	raw := strings.TrimSpace(explicitAudioURL)
	if raw == "" {
		raw = strings.TrimSpace(audioPath)
	}
	if raw == "" {
		return ""
	}

	// If already absolute URL, use as-is.
	if u, err := url.Parse(raw); err == nil && u.IsAbs() {
		// Some SBV2 bridges return internal cache-a/cache-b paths that are not publicly served.
		// Normalize those paths to /audio/<filename> for browser playback.
		if rewritten := rewriteInternalCacheURL(u); rewritten != "" {
			return rewritten
		}
		if rewritten := rewriteLocalAudioURL(base, u); rewritten != "" {
			return rewritten
		}
		return raw
	}

	if base == "" {
		return raw
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return raw
	}

	// Best-effort normalize server-returned path (Windows separators, relative path).
	rel := strings.ReplaceAll(raw, "\\", "/")
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return raw
	}
	if isInternalCachePath(rel) {
		rel = path.Join("audio", path.Base(rel))
	}
	baseURL.Path = path.Join(strings.TrimRight(baseURL.Path, "/"), rel)
	baseURL.RawPath = ""
	return baseURL.String()
}

func rewriteLocalAudioURL(base string, src *url.URL) string {
	if src == nil || !isLocalAudioHost(src.Host) {
		return ""
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL == nil {
		return ""
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return ""
	}
	rewritten := *baseURL
	rewritten.Path = path.Join(strings.TrimRight(baseURL.Path, "/"), strings.TrimPrefix(src.Path, "/"))
	rewritten.RawPath = ""
	rewritten.RawQuery = src.RawQuery
	rewritten.Fragment = src.Fragment
	return rewritten.String()
}

func isLocalAudioHost(rawHost string) bool {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

func rewriteInternalCacheURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	rel := strings.TrimPrefix(strings.TrimSpace(u.Path), "/")
	if !isInternalCachePath(rel) {
		return ""
	}
	u2 := *u
	u2.Path = "/" + path.Join("audio", path.Base(rel))
	u2.RawPath = ""
	return u2.String()
}

func isInternalCachePath(rel string) bool {
	rel = strings.TrimSpace(strings.TrimPrefix(rel, "/"))
	return strings.HasPrefix(rel, "cache-a/") || strings.HasPrefix(rel, "cache-b/")
}
