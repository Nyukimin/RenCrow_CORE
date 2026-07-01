package webgather

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"strings"
)

func NormalizeURL(raw string, allowLocalhost bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", NewError(ErrInvalidURL, "url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", WrapError(ErrInvalidURL, "invalid url", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", NewError(ErrUnsupportedScheme, "only http and https URLs are supported")
	}
	if u.Host == "" {
		return "", NewError(ErrInvalidURL, "url host is required")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host == "" {
		return "", NewError(ErrInvalidURL, "url host is required")
	}
	if !allowLocalhost && IsPrivateHost(host) {
		return "", NewError(ErrBlockedByPolicy, "private or localhost URL is blocked by default")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String(), nil
}

func IsPrivateHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	return false
}

func SourceIDFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "web:unknown"
	}
	host := strings.TrimSpace(strings.ToLower(u.Hostname()))
	if host == "" {
		host = "unknown"
	}
	path := strings.TrimSpace(u.EscapedPath())
	if path == "" || path == "/" {
		path = "root"
	}
	sum := sha256.Sum256([]byte(rawURL))
	return "web:" + sanitizeIDPart(host) + ":" + hex.EncodeToString(sum[:])[:12]
}

func EventID(sourceID, finalURL, rawHash string) string {
	base := sourceID + "\x00" + finalURL + "\x00" + rawHash
	sum := sha256.Sum256([]byte(base))
	return "web_gather:" + sanitizeIDPart(sourceID) + ":" + hex.EncodeToString(sum[:])[:12]
}

func SHA256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TextPreview(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit])
}

func SafeURLForLog(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func sanitizeIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ':' || r == '_' || r == '.'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}
