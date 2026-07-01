package webgather

import "time"

const (
	DefaultNamespace         = "kb:web"
	DefaultFetchProvider     = "http"
	DefaultExtractor         = "html_basic"
	DefaultDiscoveryProvider = "direct_url"
	DefaultToolName          = "rencrow-web-gather"
	DefaultToolVersion       = "v0.1"
	DefaultLicenseNote       = "review source terms before promotion"
	DefaultSearchProvider    = "local_cache"
	DefaultSearchLimit       = 5
	DefaultMaxFetches        = 3
	DefaultSearchLanguage    = "ja"
	DefaultSearchFreshness   = "any"
)

type FetchPolicy struct {
	RequestTimeout time.Duration `json:"-"`
	MaxBodyBytes   int64         `json:"max_body_bytes"`
	MaxRedirects   int           `json:"max_redirects"`
	AllowLocalhost bool          `json:"allow_localhost"`
}

func DefaultFetchPolicy() FetchPolicy {
	return FetchPolicy{
		RequestTimeout: 15 * time.Second,
		MaxBodyBytes:   5 * 1024 * 1024,
		MaxRedirects:   5,
	}
}

func (p FetchPolicy) WithDefaults() FetchPolicy {
	def := DefaultFetchPolicy()
	if p.RequestTimeout <= 0 {
		p.RequestTimeout = def.RequestTimeout
	}
	if p.MaxBodyBytes <= 0 {
		p.MaxBodyBytes = def.MaxBodyBytes
	}
	if p.MaxRedirects < 0 {
		p.MaxRedirects = def.MaxRedirects
	}
	return p
}
