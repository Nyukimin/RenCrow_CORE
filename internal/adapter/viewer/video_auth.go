package viewer

import (
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var videoBrowserActorIDPattern = regexp.MustCompile(`^user:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (c VideoBrowserAccessConfig) videoOwnerID(r *http.Request) (string, bool) {
	if !c.videoRequestAllowed(r) {
		return "", false
	}
	return c.ActorID, true
}

func (c VideoBrowserAccessConfig) videoRequestAllowed(r *http.Request) bool {
	if r == nil || !c.Enabled || !videoBrowserActorIDPattern.MatchString(c.ActorID) ||
		c.Host == "" || c.Origin == "" || len(c.AllowedClientIPv4) == 0 || r.Host != c.Host ||
		!videoRemoteAddrIsAllowed(r.RemoteAddr, c.AllowedClientIPv4) || hasVideoProxyIdentityHeader(r.Header) {
		return false
	}
	originValues := headerValuesCaseInsensitive(r.Header, "Origin")
	requiresOrigin := r.Method == http.MethodPost || r.Method == http.MethodDelete
	if requiresOrigin && (len(originValues) != 1 || originValues[0] != c.Origin) {
		return false
	}
	if len(originValues) > 0 && (len(originValues) != 1 || originValues[0] != c.Origin) {
		return false
	}
	return true
}

func videoRemoteAddrIsAllowed(raw string, allowedIPv4 []string) bool {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return false
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return false
	}
	for _, candidate := range allowedIPv4 {
		if ip.String() == candidate {
			return true
		}
	}
	return false
}

func hasVideoProxyIdentityHeader(header http.Header) bool {
	for name := range header {
		lower := strings.ToLower(name)
		if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "tailscale-") {
			return true
		}
	}
	return false
}
