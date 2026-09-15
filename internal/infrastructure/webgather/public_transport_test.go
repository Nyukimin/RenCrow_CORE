package webgather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestPublicFetchCannotReachLoopbackThroughDNS(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	transport := publicHTTPTransport()
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	address := strings.Replace(strings.TrimPrefix(server.URL, "http://"), "127.0.0.1", "localhost", 1)
	conn, err := transport.DialContext(ctx, "tcp", address)
	if conn != nil {
		conn.Close()
		t.Fatal("private resolved destination was dialed")
	}
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v requests=%d", err, calls.Load())
	}
	_, err = NewHTTPFetcher().Fetch(ctx, server.URL, modulewebgather.FetchPolicy{})
	if err == nil || calls.Load() != 0 {
		t.Fatal("literal private destination accepted")
	}
}

func TestPublicAddressesRejectMixedAndMappedPrivateAnswers(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1", "224.0.0.1"} {
		if validatePublicAddresses([]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(raw)}) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := validatePublicAddresses([]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")}); err != nil {
		t.Fatal(err)
	}
}
