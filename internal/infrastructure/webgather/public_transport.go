package webgather

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func publicHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A proxy would resolve the original hostname independently of our policy.
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "source hostname resolution failed")
		}
		if err := validatePublicAddresses(ips); err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "source connection failed")
	}
	return transport
}

var nonPublicRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b:1::/48"),
}

func validatePublicAddresses(ips []netip.Addr) error {
	if len(ips) == 0 {
		return modulewebgather.NewError(modulewebgather.ErrFetchFailed, "source hostname has no addresses")
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		blocked := !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
		for _, prefix := range nonPublicRanges {
			blocked = blocked || prefix.Contains(ip)
		}
		if blocked {
			return modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "source resolves to a non-public address")
		}
	}
	return nil
}
