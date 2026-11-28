// Package researchembedding adapts the authenticated RenCrow LLM Gateway
// embedding-service contract to the Research application port.
package researchembedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/research"
)

var embeddingServiceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var embeddingAssetRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const (
	embeddingCapabilityFormat  = "rencrow.embedding-capability/v1"
	embeddingResultFormat      = "rencrow.embedding-result/v1"
	embeddingIdentityBasis     = "configured_asset_sha256+loaded_model_probe"
	embeddingCapabilityMaxBody = 64 << 10
	embeddingRequestMaxBody    = 4 << 20
	embeddingResponseMaxBody   = 16 << 20
	embeddingCapabilityTimeout = 3 * time.Second
	embeddingHTTPTimeout       = 45 * time.Second
)

// HTTPPort implements the application embedding port through RenCrow LLM Gateway.
type HTTPPort struct {
	capabilityURL string
	embeddingsURL string
	serviceID     string
	client        *http.Client
}

var _ research.SemanticEmbeddingPort = (*HTTPPort)(nil)

// NewHTTPPort creates a fixed-path adapter for one configured Gateway service.
// client must be the existing authenticated, direct Gateway client.
func NewHTTPPort(baseURL, serviceID string, client *http.Client) (*HTTPPort, error) {
	baseURL = strings.TrimSpace(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || !localGatewayHost(parsed.Hostname()) || (parsed.Path != "" && parsed.Path != "/") ||
		strings.TrimSpace(serviceID) != serviceID || !embeddingServiceIDPattern.MatchString(serviceID) || client == nil {
		return nil, errors.New("research embedding: invalid Gateway configuration")
	}
	copyClient := *client
	if copyClient.Timeout <= 0 || copyClient.Timeout > embeddingHTTPTimeout {
		copyClient.Timeout = embeddingHTTPTimeout
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	switch transport := copyClient.Transport.(type) {
	case nil:
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("research embedding: Gateway transport is unavailable")
		}
		localTransport := defaultTransport.Clone()
		localTransport.Proxy = nil
		copyClient.Transport = localTransport
	case *http.Transport:
		localTransport := transport.Clone()
		localTransport.Proxy = nil
		copyClient.Transport = localTransport
	}
	base := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	capabilityURL := *base
	capabilityURL.Path = "/v1/embedding-services/" + serviceID + "/capability"
	embeddingsURL := *base
	embeddingsURL.Path = "/v1/embedding-services/" + serviceID + "/embeddings"
	return &HTTPPort{capabilityURL: capabilityURL.String(), embeddingsURL: embeddingsURL.String(), serviceID: serviceID, client: &copyClient}, nil
}

func localGatewayHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

type embeddingCapabilityWire struct {
	FormatVersion string  `json:"format_version"`
	Available     *bool   `json:"available"`
	Reason        *string `json:"reason,omitempty"`
	ModelID       *string `json:"model_id,omitempty"`
	ModelRevision *string `json:"model_revision,omitempty"`
	AssetSHA256   *string `json:"asset_sha256,omitempty"`
	Dimension     *int    `json:"dimension,omitempty"`
	IdentityBasis *string `json:"identity_basis,omitempty"`
}

type embeddingResultWire struct {
	FormatVersion string                  `json:"format_version"`
	ServiceID     string                  `json:"service_id"`
	Capability    embeddingCapabilityWire `json:"capability"`
	Data          json.RawMessage         `json:"data"`
}

func (p *HTTPPort) Capability(ctx context.Context) (research.SemanticEmbeddingCapability, error) {
	if p == nil || p.client == nil || p.capabilityURL == "" || ctx == nil {
		return research.SemanticEmbeddingCapability{}, errors.New("research embedding: Gateway capability is unavailable")
	}
	body, err := p.request(ctx, http.MethodGet, p.capabilityURL, nil, embeddingCapabilityMaxBody, embeddingCapabilityTimeout)
	if err != nil {
		return research.SemanticEmbeddingCapability{}, err
	}
	var capability embeddingCapabilityWire
	if err := decodeStrictEmbeddingJSON(body, &capability); err != nil || capability.validate() != nil {
		return research.SemanticEmbeddingCapability{}, errors.New("research embedding: Gateway capability response is invalid")
	}
	if !capability.isAvailable() {
		return research.SemanticEmbeddingCapability{}, errors.New("research embedding: Gateway embedding service is unavailable")
	}
	return capability.semanticCapability(), nil
}

func (p *HTTPPort) Embed(ctx context.Context, expected research.SemanticEmbeddingCapability, inputs []string) (research.SemanticEmbeddingResult, error) {
	if p == nil || p.client == nil || p.embeddingsURL == "" || ctx == nil || !validSemanticEmbeddingCapability(expected) {
		return research.SemanticEmbeddingResult{}, errors.New("research embedding: Gateway embedding request is unavailable")
	}
	body, err := encodeEmbeddingRequest(inputs)
	if err != nil {
		return research.SemanticEmbeddingResult{}, err
	}
	responseBody, err := p.request(ctx, http.MethodPost, p.embeddingsURL, body, embeddingResponseMaxBody, embeddingHTTPTimeout)
	if err != nil {
		return research.SemanticEmbeddingResult{}, err
	}
	var response embeddingResultWire
	if err := decodeStrictEmbeddingJSON(responseBody, &response); err != nil {
		return research.SemanticEmbeddingResult{}, errors.New("research embedding: Gateway embedding response is invalid")
	}
	if response.FormatVersion != embeddingResultFormat || response.ServiceID != p.serviceID || response.Capability.validate() != nil || !response.Capability.isAvailable() {
		return research.SemanticEmbeddingResult{}, errors.New("research embedding: Gateway embedding response identity is invalid")
	}
	if response.Capability.semanticCapability() != expected {
		return research.SemanticEmbeddingResult{}, errors.New("research embedding: Gateway embedding capability changed")
	}
	vectors, err := parseEmbeddingData(response.Data, len(inputs), *response.Capability.Dimension)
	if err != nil {
		return research.SemanticEmbeddingResult{}, err
	}
	return research.SemanticEmbeddingResult{Capability: response.Capability.semanticCapability(), Vectors: vectors}, nil
}

func (p *HTTPPort) request(ctx context.Context, method, endpoint string, body []byte, maxBody int64, timeout time.Duration) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("research embedding: could not create Gateway request")
	}
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errors.New("research embedding: Gateway request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("research embedding: Gateway returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("research embedding: Gateway response is not application/json")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || int64(len(data)) > maxBody {
		return nil, errors.New("research embedding: Gateway response exceeds the bounded size")
	}
	return data, nil
}

func (capability embeddingCapabilityWire) validate() error {
	if capability.FormatVersion != embeddingCapabilityFormat || capability.Available == nil {
		return errors.New("invalid capability format")
	}
	if !*capability.Available {
		if capability.Reason == nil || strings.TrimSpace(*capability.Reason) == "" || capability.ModelID != nil || capability.ModelRevision != nil || capability.AssetSHA256 != nil || capability.Dimension != nil || capability.IdentityBasis != nil {
			return errors.New("invalid unavailable capability")
		}
		return nil
	}
	if capability.Reason != nil || capability.ModelID == nil || capability.ModelRevision == nil || capability.AssetSHA256 == nil || capability.Dimension == nil || capability.IdentityBasis == nil {
		return errors.New("invalid available capability")
	}
	if strings.TrimSpace(*capability.ModelID) != *capability.ModelID || *capability.ModelID == "" || len(*capability.ModelID) > 128 || !utf8.ValidString(*capability.ModelID) ||
		!embeddingAssetRevisionPattern.MatchString(*capability.ModelRevision) || *capability.ModelRevision != "sha256:"+*capability.AssetSHA256 ||
		!isLowerSHA256(*capability.AssetSHA256) || *capability.Dimension < 1 || *capability.IdentityBasis != embeddingIdentityBasis {
		return errors.New("invalid available capability")
	}
	return nil
}

func (capability embeddingCapabilityWire) isAvailable() bool {
	return capability.Available != nil && *capability.Available
}

func (capability embeddingCapabilityWire) semanticCapability() research.SemanticEmbeddingCapability {
	return research.SemanticEmbeddingCapability{ModelID: *capability.ModelID, Revision: *capability.ModelRevision, Dimension: *capability.Dimension}
}

func validSemanticEmbeddingCapability(capability research.SemanticEmbeddingCapability) bool {
	return capability.ModelID != "" && strings.TrimSpace(capability.ModelID) == capability.ModelID && len(capability.ModelID) <= 128 && utf8.ValidString(capability.ModelID) &&
		embeddingAssetRevisionPattern.MatchString(capability.Revision) && capability.Dimension > 0
}

func isLowerSHA256(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func encodeEmbeddingRequest(inputs []string) ([]byte, error) {
	if len(inputs) == 0 {
		return nil, errors.New("research embedding: at least one input is required")
	}
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" || !utf8.ValidString(input) {
			return nil, errors.New("research embedding: input text is empty or invalid")
		}
	}
	body, err := json.Marshal(struct {
		Input          []string `json:"input"`
		EncodingFormat string   `json:"encoding_format"`
	}{Input: inputs, EncodingFormat: "float"})
	if err != nil || len(body) > embeddingRequestMaxBody {
		return nil, errors.New("research embedding: input exceeds the Gateway request limit")
	}
	return body, nil
}

func parseEmbeddingData(data json.RawMessage, inputCount, dimension int) ([]research.SemanticEmbeddingVector, error) {
	if len(data) == 0 || inputCount < 1 || dimension < 1 {
		return nil, errors.New("research embedding: Gateway data array is missing or invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return nil, errors.New("research embedding: Gateway data is not an array")
	}
	vectors := make([]research.SemanticEmbeddingVector, 0)
	seen := make([]bool, inputCount)
	for decoder.More() {
		if len(vectors) >= inputCount {
			return nil, errors.New("research embedding: Gateway embedding count does not match input count")
		}
		vector, err := parseEmbeddingDataItem(decoder, inputCount, dimension)
		if err != nil {
			return nil, err
		}
		if seen[vector.Index] {
			return nil, errors.New("research embedding: Gateway embedding index is duplicated")
		}
		seen[vector.Index] = true
		vectors = append(vectors, vector)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return nil, errors.New("research embedding: Gateway data array is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("research embedding: Gateway data contains trailing content")
	}
	if len(vectors) != inputCount {
		return nil, errors.New("research embedding: Gateway embedding count does not match input count")
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("research embedding: Gateway embedding index is missing")
		}
	}
	return vectors, nil
}

func parseEmbeddingDataItem(decoder *json.Decoder, inputCount, dimension int) (research.SemanticEmbeddingVector, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway data item is invalid")
	}
	var indexRaw, embeddingRaw, objectRaw json.RawMessage
	hasIndex, hasEmbedding, hasObject := false, false, false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		field, ok := fieldToken.(string)
		if err != nil || !ok {
			return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway data item field is invalid")
		}
		var target *json.RawMessage
		switch field {
		case "index":
			if hasIndex {
				return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway embedding index is duplicated")
			}
			hasIndex = true
			target = &indexRaw
		case "embedding":
			if hasEmbedding {
				return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway vector is duplicated")
			}
			hasEmbedding = true
			target = &embeddingRaw
		case "object":
			if hasObject {
				return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway object field is duplicated")
			}
			hasObject = true
			target = &objectRaw
		default:
			return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway data item contains an unknown field")
		}
		if err := decoder.Decode(target); err != nil {
			return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway data item field is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway data item is invalid")
	}
	if !hasIndex {
		return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway embedding index is missing")
	}
	index64, err := strconv.ParseInt(string(bytes.TrimSpace(indexRaw)), 10, 64)
	if err != nil || index64 < 0 || index64 >= int64(inputCount) {
		return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway embedding index is out of range")
	}
	if hasObject && !validEmbeddingObject(objectRaw) {
		return research.SemanticEmbeddingVector{}, errors.New("research embedding: Gateway embedding object type is invalid")
	}
	values, err := parseEmbeddingVector(embeddingRaw, dimension)
	if err != nil {
		return research.SemanticEmbeddingVector{}, err
	}
	return research.SemanticEmbeddingVector{Index: int(index64), Values: values}, nil
}

func parseEmbeddingVector(raw json.RawMessage, dimension int) ([]float32, error) {
	if len(raw) == 0 || dimension < 1 {
		return nil, errors.New("research embedding: Gateway vector is missing or invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return nil, errors.New("research embedding: Gateway vector is not an array")
	}
	values := make([]float32, 0)
	nonzero := false
	for decoder.More() {
		if len(values) >= dimension {
			return nil, errors.New("research embedding: Gateway vector dimension does not match capability")
		}
		var number json.RawMessage
		if err := decoder.Decode(&number); err != nil {
			return nil, errors.New("research embedding: Gateway vector contains an invalid value")
		}
		value, err := strconv.ParseFloat(string(bytes.TrimSpace(number)), 32)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("research embedding: Gateway vector contains a non-finite or invalid value")
		}
		floatValue := float32(value)
		if math.IsNaN(float64(floatValue)) || math.IsInf(float64(floatValue), 0) {
			return nil, errors.New("research embedding: Gateway vector contains a non-finite or invalid value")
		}
		if floatValue != 0 {
			nonzero = true
		}
		values = append(values, floatValue)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("research embedding: Gateway vector array is invalid")
	}
	if len(values) != dimension {
		return nil, errors.New("research embedding: Gateway vector dimension does not match capability")
	}
	if !nonzero {
		return nil, errors.New("research embedding: Gateway vector is all zero")
	}
	return values, nil
}

func validEmbeddingObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var object string
	return json.Unmarshal(raw, &object) == nil && object == "embedding"
}

func decodeStrictEmbeddingJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("JSON contains trailing data")
	}
	return nil
}
