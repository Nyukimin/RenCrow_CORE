// Package boardcommunication contains the authenticated wire client for
// RenCrow_BOARD's canonical communication endpoints.
package boardcommunication

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultRequestTimeout = 30 * time.Second
	responseHeaderTimeout = 30 * time.Second
	maxResponseBodyBytes  = 1 << 20
	maxRequestBodyBytes   = 64 << 10
	maxCanonicalJSONDepth = 64
	boardSocketHTTPHost   = "127.0.0.1:18792"
)

var (
	errResponseRead     = errors.New("response read failed")
	errResponseTooLarge = errors.New("response too large")
)

// TokenSource supplies the current BOARD bearer token. Implementations remain
// responsible for reading protected token storage; the client only uses the
// returned bytes for a request.
type TokenSource func(context.Context) ([]byte, error)

// Options selects exactly one connection mode. URL and UnixSocket are mutually
// exclusive; UnixSocket is an absolute socket path when supported.
// ExpectedConnection is required for a normal bound client.
type Options struct {
	URL                string
	UnixSocket         string
	TokenSource        TokenSource
	ExpectedConnection ConnectionIdentity
}

// Client is a BOARD wire client. It does not own token storage or application
// processing policy.
type Client struct {
	mu                 sync.RWMutex
	closed             bool
	lifetime           context.Context
	cancel             context.CancelFunc
	base               url.URL
	socketPath         string
	expectedConnection ConnectionIdentity
	tokenSource        TokenSource
	httpClient         *http.Client
	transport          *http.Transport
	eventClient        *http.Client
	eventTrans         *http.Transport
	streams            map[*EventStream]struct{}
}

// NewClient creates a client from an explicit root URL or a private Unix
// socket. It never reads or stores token material.
func NewClient(options Options) (*Client, error) {
	if !validConnectionIdentity(options.ExpectedConnection) {
		return nil, ErrInvalidConfiguration
	}
	return newClient(options)
}

func newClient(options Options) (*Client, error) {
	if options.TokenSource == nil || (options.URL == "") == (options.UnixSocket == "") {
		return nil, ErrInvalidConfiguration
	}

	var base url.URL
	var socketPath string
	var dialContext func(context.Context, string, string) (net.Conn, error)
	if options.URL != "" {
		parsed, err := parseRootURL(options.URL)
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		base = *parsed
	} else {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return nil, ErrUnsupported
		}
		if !isCleanAbsoluteSocketPath(options.UnixSocket) {
			return nil, ErrInvalidConfiguration
		}
		var err error
		dialContext, err = unixSocketDialContext(options.UnixSocket)
		if err != nil {
			return nil, err
		}
		socketPath = options.UnixSocket
		base = url.URL{Scheme: "http", Host: boardSocketHTTPHost}
	}

	lifetime, cancel := context.WithCancel(context.Background())
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		IdleConnTimeout:        30 * time.Second,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        2,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
		DisableKeepAlives:      true,
	}
	eventTransport := transport.Clone()
	client := &Client{
		lifetime:           lifetime,
		cancel:             cancel,
		base:               base,
		socketPath:         socketPath,
		expectedConnection: options.ExpectedConnection,
		tokenSource:        options.TokenSource,
		transport:          transport,
		eventTrans:         eventTransport,
		streams:            make(map[*EventStream]struct{}),
	}
	client.httpClient = &http.Client{
		Transport: transport,
		Timeout:   defaultRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	client.eventClient = &http.Client{
		Transport: eventTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, nil
}

func parseRootURL(raw string) (*url.URL, error) {
	if strings.Contains(raw, "#") {
		return nil, ErrInvalidConfiguration
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrInvalidConfiguration
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, ErrInvalidConfiguration
	}
	host := u.Hostname()
	if host == "" {
		return nil, ErrInvalidConfiguration
	}
	port := u.Port()
	if strings.Contains(u.Host, ":") && port == "" && !strings.HasSuffix(u.Host, "]") {
		return nil, ErrInvalidConfiguration
	}
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, ErrInvalidConfiguration
		}
	}
	if scheme == "http" && !isLoopbackHost(host) {
		return nil, ErrInvalidConfiguration
	}
	u.Scheme = scheme
	u.Path = ""
	u.RawPath = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isCleanAbsoluteSocketPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// Close cancels in-flight requests and releases idle transport resources.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	var streams []*EventStream
	if !c.closed {
		c.closed = true
		if c.cancel != nil {
			c.cancel()
		}
	}
	for stream := range c.streams {
		streams = append(streams, stream)
	}
	c.mu.Unlock()
	for _, stream := range streams {
		_ = stream.Close()
	}
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	if c.eventTrans != nil {
		c.eventTrans.CloseIdleConnections()
	}
}

// BoardContext is an external BOARD context. Its ID is a BOARD identifier,
// not a CORE canonical ID.
type BoardContext struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
}

// ListContexts reads the authenticated principal's canonical BOARD contexts.
func (c *Client) ListContexts(ctx context.Context) ([]BoardContext, error) {
	response, release, err := c.do(ctx, http.MethodGet, "/api/v1/contexts", nil, "")
	if err != nil {
		return nil, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, connectionResponseStatusError(response)
	}
	body, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		if errors.Is(err, errResponseRead) {
			return nil, ErrUnavailable
		}
		return nil, ErrProtocol
	}
	var envelope struct {
		Contexts      []BoardContext `json:"contexts"`
		CorrelationID string         `json:"correlation_id"`
	}
	if err := decodeStrictJSON(body, &envelope); err != nil || envelope.Contexts == nil {
		return nil, ErrProtocol
	}
	for _, item := range envelope.Contexts {
		if !validBoardID(item.ID) || item.Label == "" {
			return nil, ErrProtocol
		}
	}
	return envelope.Contexts, nil
}

func contextResponseError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthentication
	}
	if status >= 500 && status <= 599 {
		return ErrUnavailable
	}
	return ErrProtocol
}

func (c *Client) do(ctx context.Context, method, endpoint string, body []byte, idempotencyKey string) (*http.Response, func(), error) {
	if c == nil || ctx == nil {
		return nil, nil, ErrInvalidRequest
	}
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, nil, ErrClosed
	}
	base := c.base
	lifetime := c.lifetime
	tokenSource := c.tokenSource
	httpClient := c.httpClient
	socketPath := c.socketPath
	expectedConnection := c.expectedConnection
	c.mu.RUnlock()
	if lifetime == nil || tokenSource == nil || httpClient == nil {
		return nil, nil, ErrUnavailable
	}
	if ctx.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	if socketPath != "" {
		if err := validateUnixSocketPath(socketPath); err != nil {
			return nil, nil, err
		}
	}

	operationCtx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	stopLifetime := context.AfterFunc(lifetime, cancel)
	release := func() {
		stopLifetime()
		cancel()
	}
	token, err := tokenSource(operationCtx)
	if err != nil || !validBearerToken(token) {
		release()
		return nil, nil, ErrAuthentication
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.IsAbs() || parsedEndpoint.Opaque != "" || parsedEndpoint.Host != "" ||
		parsedEndpoint.User != nil || !strings.HasPrefix(parsedEndpoint.Path, "/") ||
		parsedEndpoint.Fragment != "" || parsedEndpoint.RawFragment != "" {
		release()
		return nil, nil, ErrInvalidRequest
	}
	requestURL := base
	requestURL.Path = parsedEndpoint.Path
	requestURL.RawPath = parsedEndpoint.RawPath
	requestURL.RawQuery = parsedEndpoint.RawQuery
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(operationCtx, method, requestURL.String(), requestBody)
	if err != nil {
		release()
		return nil, nil, ErrInvalidRequest
	}
	request.GetBody = nil // prevents Transport from replaying Idempotency-Key requests
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Accept", "application/json")
	setConnectionIdentityHeaders(request.Header, expectedConnection)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		release()
		if errors.Is(err, ErrInvalidConfiguration) {
			return nil, nil, ErrInvalidConfiguration
		}
		if errors.Is(err, ErrUnavailable) {
			return nil, nil, ErrUnavailable
		}
		if errors.Is(err, errUnixSocketDialFailure) {
			return nil, nil, ErrUnavailable
		}
		if method == http.MethodPost {
			return nil, nil, ErrUnknownOutcome
		}
		return nil, nil, ErrUnavailable
	}
	return response, release, nil
}

func validBearerToken(token []byte) bool {
	if len(token) == 0 {
		return false
	}
	padding := false
	content := false
	for _, value := range token {
		if value == '=' {
			if !content {
				return false
			}
			padding = true
			continue
		}
		if padding || !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || strings.ContainsRune("-._~+/", rune(value))) {
			return false
		}
		content = true
	}
	return content
}

func readBounded(reader io.Reader, max int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, errResponseRead
	}
	if int64(len(body)) > max {
		return nil, errResponseTooLarge
	}
	return body, nil
}

func decodeStrictJSON(body []byte, out any) error {
	if err := rejectDuplicateObjectKeys(body); err != nil {
		return err
	}
	if err := validateCanonicalJSONFieldNames(body, out); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing json")
	}
	return nil
}

// validateCanonicalJSONFieldNames rejects case-folded aliases accepted by
// encoding/json while preserving custom UnmarshalJSON types as opaque leaves.
func validateCanonicalJSONFieldNames(body []byte, out any) error {
	if out == nil {
		return errors.New("json output type required")
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	return validateCanonicalJSONValue(document, reflect.TypeOf(out), 0)
}

func validateCanonicalJSONValue(value any, target reflect.Type, depth int) error {
	if value == nil || target == nil {
		return nil
	}
	if depth > maxCanonicalJSONDepth {
		return errors.New("json nesting depth exceeded")
	}
	unmarshalerType := reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	if target.Implements(unmarshalerType) ||
		(target.Kind() != reflect.Pointer && reflect.PointerTo(target).Implements(unmarshalerType)) {
		return nil
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			if field.PkgPath != "" {
				continue
			}
			tag := field.Tag.Get("json")
			name := strings.SplitN(tag, ",", 2)[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for name, nested := range object {
			fieldType, exists := fields[name]
			if !exists {
				return errors.New("noncanonical json field name")
			}
			if err := validateCanonicalJSONValue(nested, fieldType, depth+1); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := validateCanonicalJSONValue(item, target.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for _, nested := range object {
			if err := validateCanonicalJSONValue(nested, target.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectDuplicateObjectKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	delimiter, ok := first.(json.Delim)
	if err != nil || !ok || delimiter != '{' {
		return errors.New("object required")
	}
	if err := rejectDuplicateObjectContents(decoder); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("trailing json")
	}
	return nil
}

func rejectDuplicateObjectContents(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("object key required")
		}
		foldedKey := foldJSONFieldName(key)
		if _, exists := seen[foldedKey]; exists {
			return errors.New("duplicate object key")
		}
		seen[foldedKey] = struct{}{}
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		if err := rejectDuplicateJSONValue(decoder, value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return errors.New("object closing delimiter required")
	}
	return nil
}

func rejectDuplicateJSONValue(decoder *json.Decoder, value json.Token) error {
	delimiter, ok := value.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return rejectDuplicateObjectContents(decoder)
	case '[':
		for decoder.More() {
			item, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := rejectDuplicateJSONValue(decoder, item); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing, ok := closing.(json.Delim); !ok || closing != ']' {
			return errors.New("array closing delimiter required")
		}
		return nil
	default:
		return errors.New("unexpected json delimiter")
	}
}

func foldJSONFieldName(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, character := range value {
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		} else if character >= 0x80 {
			character = foldJSONRune(character)
		}
		folded.WriteRune(character)
	}
	return folded.String()
}

func foldJSONRune(character rune) rune {
	for {
		next := unicode.SimpleFold(character)
		if next <= character {
			return next
		}
		character = next
	}
}

func validBoardID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
