package browsertrace

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

type DiscoverRequest struct {
	TraceRunID    string
	WorkstreamID  string
	SiteID        string
	Goal          string
	TracePath     string
	RequestsPath  string
	ResponsesPath string
	CapturedAt    time.Time
}

type Discoverer struct {
	now           func() time.Time
	acceptedPaths []string
}

func NewDiscoverer() *Discoverer {
	return &Discoverer{now: func() time.Time { return time.Now().UTC() }}
}

func NewDiscovererWithAcceptedPaths(acceptedPaths []string) *Discoverer {
	d := NewDiscoverer()
	d.acceptedPaths = append([]string(nil), acceptedPaths...)
	return d
}

func (d *Discoverer) Discover(req DiscoverRequest) (domaintrace.DiscoveryResult, error) {
	now := d.now()
	run := domaintrace.TraceRun{
		TraceRunID:   strings.TrimSpace(req.TraceRunID),
		WorkstreamID: strings.TrimSpace(req.WorkstreamID),
		SiteID:       strings.TrimSpace(req.SiteID),
		Goal:         strings.TrimSpace(req.Goal),
		TracePath:    strings.TrimSpace(req.TracePath),
		CapturedAt:   req.CapturedAt,
		CreatedAt:    now,
	}
	if run.TracePath == "" {
		run.TracePath = req.RequestsPath
	}
	if run.CapturedAt.IsZero() {
		run.CapturedAt = now
	}
	if err := domaintrace.ValidateTraceRun(run); err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	if err := validateAcceptedTracePath(run.TracePath, d.acceptedPaths); err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	if err := validateAcceptedTracePath(req.RequestsPath, d.acceptedPaths); err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	if err := validateAcceptedTracePath(req.ResponsesPath, d.acceptedPaths); err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	requests, err := readRequests(req.RequestsPath)
	if err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	responses, err := readResponses(req.ResponsesPath)
	if err != nil {
		return domaintrace.DiscoveryResult{}, err
	}
	exchanges := PairRequestsResponses(requests, responses)
	candidates, schemas, coverage := BuildCandidates(run, exchanges, now)
	return domaintrace.DiscoveryResult{
		Run:        run,
		Candidates: candidates,
		Schemas:    schemas,
		Coverage:   coverage,
	}, nil
}

func validateAcceptedTracePath(value string, acceptedPaths []string) error {
	if len(acceptedPaths) == 0 || strings.TrimSpace(value) == "" {
		return nil
	}
	if tracePathAllowed(value, acceptedPaths) {
		return nil
	}
	return fmt.Errorf("trace path %q is outside accepted browser trace paths", value)
}

func tracePathAllowed(value string, acceptedPaths []string) bool {
	normalized := normalizeTracePath(value)
	for _, accepted := range acceptedPaths {
		accepted = normalizeTracePath(accepted)
		if accepted == "" {
			continue
		}
		if normalized == accepted ||
			strings.HasPrefix(normalized, accepted+"/") ||
			strings.Contains(normalized, "/"+accepted+"/") ||
			strings.HasSuffix(normalized, "/"+accepted) {
			return true
		}
	}
	return false
}

func normalizeTracePath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	return value
}

func PairRequestsResponses(requests []domaintrace.NetworkRequest, responses []domaintrace.NetworkResponse) []domaintrace.Exchange {
	byID := map[string]domaintrace.NetworkResponse{}
	for _, resp := range responses {
		if strings.TrimSpace(resp.RequestID) == "" {
			continue
		}
		byID[resp.RequestID] = resp
	}
	var out []domaintrace.Exchange
	for _, req := range requests {
		resp := byID[req.RequestID]
		out = append(out, domaintrace.Exchange{Request: req, Response: resp})
	}
	return out
}

func BuildCandidates(run domaintrace.TraceRun, exchanges []domaintrace.Exchange, now time.Time) ([]domaintrace.APICandidate, []domaintrace.APICandidateSchema, domaintrace.APICoverageReport) {
	type grouped struct {
		first    domaintrace.Exchange
		count    int
		samples  []string
		statuses []int
	}
	groups := map[string]*grouped{}
	for _, ex := range exchanges {
		method := strings.ToUpper(strings.TrimSpace(ex.Request.Method))
		if method == "" || method == "PUT" || method == "PATCH" || method == "DELETE" {
			continue
		}
		templated, pathTemplate, _ := TemplatizeURL(ex.Request.URL)
		key := method + " " + templated
		g, ok := groups[key]
		if !ok {
			g = &grouped{first: ex}
			groups[key] = g
		}
		g.count++
		if strings.TrimSpace(ex.Response.Body) != "" {
			g.samples = append(g.samples, ex.Response.Body)
		}
		if ex.Response.Status > 0 {
			g.statuses = append(g.statuses, ex.Response.Status)
		}
		_ = pathTemplate
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidates := make([]domaintrace.APICandidate, 0, len(keys))
	schemas := []domaintrace.APICandidateSchema{}
	endpoints := []string{}
	for _, key := range keys {
		g := groups[key]
		ex := g.first
		templated, pathTemplate, params := TemplatizeURL(ex.Request.URL)
		candidateID := "api_cand_" + shortHash(run.TraceRunID+"|"+key)
		risk := "low"
		if strings.ToUpper(ex.Request.Method) == "POST" {
			risk = "medium"
		}
		if hasAuthHeader(ex.Request.Headers) {
			risk = "medium"
		}
		candidate := domaintrace.APICandidate{
			CandidateID:          candidateID,
			TraceRunID:           run.TraceRunID,
			SiteID:               run.SiteID,
			Method:               strings.ToUpper(ex.Request.Method),
			ObservedURL:          ex.Request.URL,
			TemplatedURL:         templated,
			PathTemplate:         pathTemplate,
			QueryParams:          params,
			AuthRequired:         hasAuthHeader(ex.Request.Headers),
			ContainsPersonalData: "unknown",
			RiskLevel:            risk,
			Status:               "candidate",
			Confidence:           0.7,
			CreatedAt:            now,
		}
		if err := domaintrace.ValidateAPICandidate(candidate); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
		endpoints = append(endpoints, candidate.Method+" "+candidate.PathTemplate)
		if schema := InferJSONSchema(g.samples); schema != "" {
			schemas = append(schemas, domaintrace.APICandidateSchema{
				SchemaID:    "schema_" + shortHash(candidateID+"|response"),
				CandidateID: candidateID,
				SchemaType:  "response",
				SchemaJSON:  schema,
				SampleCount: len(g.samples),
				Confidence:  0.6,
				CreatedAt:   now,
			})
		}
	}
	coverage := domaintrace.APICoverageReport{
		ReportID:              "api_cov_" + shortHash(run.TraceRunID),
		TraceRunID:            run.TraceRunID,
		ObservedFlows:         []string{"network_trace"},
		ObservedEndpoints:     endpoints,
		MissingFlows:          []string{"error cases", "pagination edge cases", "terms review"},
		RecommendedNextTraces: []string{"empty result", "last page", "invalid id"},
		CreatedAt:             now,
	}
	return candidates, schemas, coverage
}

func TemplatizeURL(raw string) (string, string, []domaintrace.APIQueryParam) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, "", nil
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, segment := range segments {
		if isLikelyID(segment) {
			segments[i] = "{id}"
		}
	}
	pathTemplate := "/" + strings.Join(segments, "/")
	if len(segments) == 1 && segments[0] == "" {
		pathTemplate = "/"
	}
	values := u.Query()
	params := make([]domaintrace.APIQueryParam, 0, len(values))
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		observed := values[name]
		params = append(params, domaintrace.APIQueryParam{
			Name:           name,
			Type:           inferStringType(firstString(observed)),
			ObservedValues: observed,
		})
		values.Set(name, "{"+name+"}")
	}
	u.Path = path.Clean(pathTemplate)
	if pathTemplate == "/" {
		u.Path = "/"
	}
	u.RawQuery = values.Encode()
	return u.String(), pathTemplate, params
}

func InferJSONSchema(samples []string) string {
	for _, sample := range samples {
		var value any
		if err := json.Unmarshal([]byte(sample), &value); err != nil {
			continue
		}
		schema := inferSchema(value)
		if schema == nil {
			continue
		}
		data, err := json.Marshal(schema)
		if err != nil {
			continue
		}
		return string(data)
	}
	return ""
}

func inferSchema(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		props := map[string]any{}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if child := inferSchema(v[key]); child != nil {
				props[key] = child
			} else {
				props[key] = map[string]any{"type": "unknown"}
			}
		}
		return map[string]any{"type": "object", "properties": props}
	case []any:
		if len(v) == 0 {
			return map[string]any{"type": "array", "items": map[string]any{"type": "unknown"}}
		}
		child := inferSchema(v[0])
		if child == nil {
			child = map[string]any{"type": "unknown"}
		}
		return map[string]any{"type": "array", "items": child}
	case string:
		return map[string]any{"type": "string"}
	case bool:
		return map[string]any{"type": "boolean"}
	case float64:
		if v == float64(int64(v)) {
			return map[string]any{"type": "integer"}
		}
		return map[string]any{"type": "number"}
	case nil:
		return map[string]any{"type": "null"}
	default:
		return nil
	}
}

func readRequests(filePath string) ([]domaintrace.NetworkRequest, error) {
	var out []domaintrace.NetworkRequest
	err := readJSONLMaps(filePath, func(m map[string]any) error {
		req := domaintrace.NetworkRequest{
			RequestID: firstNestedString(m, "request_id", "requestId", "id", "params.requestId"),
			Method:    strings.ToUpper(firstNestedString(m, "method", "params.request.method")),
			URL:       firstNestedString(m, "url", "params.request.url"),
			Headers:   extractStringMap(firstNestedMap(m, "headers", "params.request.headers")),
		}
		if req.RequestID == "" {
			req.RequestID = shortHash(req.Method + "|" + req.URL)
		}
		if req.Method != "" && req.URL != "" {
			out = append(out, req)
		}
		return nil
	})
	return out, err
}

func readResponses(filePath string) ([]domaintrace.NetworkResponse, error) {
	var out []domaintrace.NetworkResponse
	err := readJSONLMaps(filePath, func(m map[string]any) error {
		resp := domaintrace.NetworkResponse{
			RequestID: firstNestedString(m, "request_id", "requestId", "id", "params.requestId"),
			URL:       firstNestedString(m, "url", "params.response.url"),
			Status:    firstNestedInt(m, "status", "params.response.status"),
			Body:      firstNestedString(m, "body", "response.body", "params.response.body"),
		}
		if resp.RequestID != "" {
			out = append(out, resp)
		}
		return nil
	})
	return out, err
}

func readJSONLMaps(filePath string, fn func(map[string]any) error) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return err
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func firstNestedString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := nestedValue(m, key); ok {
			switch typed := v.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case fmt.Stringer:
				return typed.String()
			}
		}
	}
	return ""
}

func firstNestedInt(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := nestedValue(m, key); ok {
			switch typed := v.(type) {
			case float64:
				return int(typed)
			case int:
				return typed
			case string:
				n, _ := strconv.Atoi(typed)
				return n
			}
		}
	}
	return 0
}

func firstNestedMap(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if v, ok := nestedValue(m, key); ok {
			if out, ok := v.(map[string]any); ok {
				return out
			}
		}
	}
	return nil
}

func nestedValue(m map[string]any, dotted string) (any, bool) {
	current := any(m)
	for _, part := range strings.Split(dotted, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func extractStringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range m {
		if s, ok := value.(string); ok {
			out[strings.ToLower(key)] = s
		}
	}
	return out
}

func hasAuthHeader(headers map[string]string) bool {
	for key := range headers {
		k := strings.ToLower(key)
		if k == "authorization" || k == "cookie" || k == "x-csrf-token" {
			return true
		}
	}
	return false
}

var hexish = regexp.MustCompile(`^[0-9a-fA-F-]{8,}$`)

func isLikelyID(segment string) bool {
	if _, err := strconv.Atoi(segment); err == nil {
		return true
	}
	return hexish.MatchString(segment)
}

func inferStringType(value string) string {
	if value == "" {
		return "string"
	}
	if _, err := strconv.Atoi(value); err == nil {
		return "integer"
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return "number"
	}
	if value == "true" || value == "false" {
		return "boolean"
	}
	return "string"
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func shortHash(v string) string {
	sum := sha1.Sum([]byte(v))
	return hex.EncodeToString(sum[:])[:12]
}
