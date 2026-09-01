package security

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// IsDirectLoopbackRequest reports whether an HTTP request is a direct local
// request. A loopback RemoteAddr alone is not sufficient because a reverse
// proxy can make an external request look like loopback to the backend.
func IsDirectLoopbackRequest(r *http.Request) bool {
	if r == nil || !isLoopbackRemoteAddr(r.RemoteAddr) {
		return false
	}
	if hasProxyOriginHeaders(r.Header) {
		return false
	}
	return isLoopbackHostAuthority(r.Host)
}

func isLoopbackRemoteAddr(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil || !validPort(port) {
		return false
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHostAuthority(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return false
	}

	host := raw
	if strings.HasPrefix(raw, "[") {
		if strings.HasSuffix(raw, "]") {
			host = raw[1 : len(raw)-1]
		} else {
			parsedHost, port, err := net.SplitHostPort(raw)
			if err != nil || !validPort(port) {
				return false
			}
			host = parsedHost
		}
	} else if strings.Count(raw, ":") == 1 {
		parsedHost, port, err := net.SplitHostPort(raw)
		if err != nil || !validPort(port) {
			return false
		}
		host = parsedHost
	} else if strings.Contains(raw, ":") {
		// IPv6 literals in an HTTP authority must be bracketed, even without a
		// port. Reject unbracketed forms instead of guessing their meaning.
		return false
	}

	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validPort(raw string) bool {
	if raw == "" {
		return false
	}
	port, err := strconv.ParseUint(raw, 10, 16)
	return err == nil && port >= 1 && port <= 65535
}

func hasProxyOriginHeaders(headers http.Header) bool {
	for name := range headers {
		lowerName := strings.ToLower(name)
		switch {
		case lowerName == "forwarded",
			lowerName == "via",
			lowerName == "x-real-ip",
			lowerName == "x-forwarded",
			strings.HasPrefix(lowerName, "x-forwarded-"),
			strings.HasPrefix(lowerName, "tailscale-"):
			return true
		}
	}
	return false
}
