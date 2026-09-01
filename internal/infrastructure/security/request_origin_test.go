package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsDirectLoopbackRequest(t *testing.T) {
	tests := []struct {
		name       string
		requestURL string
		remoteAddr string
		header     string
		mutate     func(*http.Request)
		want       bool
	}{
		{name: "nil request", want: false},
		{name: "localhost", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", want: true},
		{name: "localhost with port", requestURL: "http://localhost:8090/v1/private", remoteAddr: "127.0.0.1:1234", want: true},
		{name: "ipv4 loopback", requestURL: "http://127.0.0.1/v1/private", remoteAddr: "127.0.0.1:1234", want: true},
		{name: "ipv4 loopback range", requestURL: "http://127.0.0.2/v1/private", remoteAddr: "127.0.0.2:1234", want: true},
		{name: "ipv6 loopback", requestURL: "http://[::1]/v1/private", remoteAddr: "[::1]:1234", want: true},
		{name: "ipv6 loopback with port", requestURL: "http://[::1]:8090/v1/private", remoteAddr: "[::1]:1234", want: true},
		{name: "remote address", requestURL: "http://localhost/v1/private", remoteAddr: "192.0.2.10:1234", want: false},
		{name: "remote host", requestURL: "http://192.0.2.10/v1/private", remoteAddr: "127.0.0.1:1234", want: false},
		{name: "non-ip remote address", requestURL: "http://localhost/v1/private", remoteAddr: "localhost:1234", want: false},
		{name: "remote address missing port", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1", want: false},
		{name: "remote address zero port", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:0", want: false},
		{name: "remote ipv6 missing port", requestURL: "http://[::1]/v1/private", remoteAddr: "[::1]", want: false},
		{name: "invalid host port", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", mutate: func(r *http.Request) { r.Host = "localhost:bad" }, want: false},
		{name: "out of range host port", requestURL: "http://127.0.0.1/v1/private", remoteAddr: "127.0.0.1:1234", mutate: func(r *http.Request) { r.Host = "127.0.0.1:65536" }, want: false},
		{name: "zero host port", requestURL: "http://127.0.0.1/v1/private", remoteAddr: "127.0.0.1:1234", mutate: func(r *http.Request) { r.Host = "127.0.0.1:0" }, want: false},
		{name: "missing host", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", mutate: func(r *http.Request) { r.Host = "" }, want: false},
		{name: "Forwarded", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "Forwarded", want: false},
		{name: "Via", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "Via", want: false},
		{name: "X-Forwarded-For", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "X-Forwarded-For", want: false},
		{name: "X-Forwarded-Host", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "X-Forwarded-Host", want: false},
		{name: "X-Real-IP", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "X-Real-IP", want: false},
		{name: "Tailscale-User-Login", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", header: "Tailscale-User-Login", want: false},
		{name: "lowercase forwarded", requestURL: "http://localhost/v1/private", remoteAddr: "127.0.0.1:1234", mutate: func(r *http.Request) { r.Header["x-forwarded-for"] = []string{"192.0.2.10"} }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.requestURL != "" {
				req = httptest.NewRequest(http.MethodPost, tt.requestURL, nil)
				req.RemoteAddr = tt.remoteAddr
				if tt.header != "" {
					req.Header.Set(tt.header, "test")
				}
				if tt.mutate != nil {
					tt.mutate(req)
				}
			}
			if got := IsDirectLoopbackRequest(req); got != tt.want {
				t.Fatalf("IsDirectLoopbackRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}
