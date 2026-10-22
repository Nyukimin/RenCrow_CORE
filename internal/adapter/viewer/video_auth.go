package viewer

import (
	"net"
	"net/http"
	"regexp"
	"strconv"
)

var videoBrowserActorIDPattern = regexp.MustCompile(`^user:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (c VideoBrowserAuthConfig) videoActorID(r *http.Request) (string, bool) {
	if !c.videoRequestAllowed(r) {
		return "", false
	}
	login, ok := singleHeaderValue(r.Header, "Tailscale-User-Login")
	if !ok || login != c.TailscaleUserLogin {
		return "", false
	}
	return c.ActorID, true
}

func (c VideoBrowserAuthConfig) videoRequestAllowed(r *http.Request) bool {
	if r == nil || !c.Enabled || !videoBrowserActorIDPattern.MatchString(c.ActorID) ||
		c.TailscaleUserLogin == "" || c.Host == "" || c.Origin == "" ||
		!videoLoopbackRemoteAddr(r.RemoteAddr) || r.Host != c.Host ||
		hasHeaderName(r.Header, "Tailscale-Funnel-Request") {
		return false
	}
	originValues := headerValuesCaseInsensitive(r.Header, "Origin")
	requiresOrigin := r.Method == http.MethodPost || r.Method == http.MethodDelete
	if requiresOrigin && len(originValues) != 1 {
		return false
	}
	if len(originValues) > 0 && (len(originValues) != 1 || originValues[0] == "null" || originValues[0] != c.Origin) {
		return false
	}
	return true
}

func videoLoopbackRemoteAddr(raw string) bool {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return false
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
