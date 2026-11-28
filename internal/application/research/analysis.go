package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	analysisEndpointPath          = "/v1/research/analyze"
	analysisProfileRevisionHeader = "X-RenCrow-Research-Profile-Revision"
	analysisMaxRequestBytes       = 128 * 1024
	analysisMaxResponseBytes      = 1 * 1024 * 1024
	analysisHTTPTimeout           = 45 * time.Second
	analysisEngineRevision        = "gateway_research_analysis.v1"
	analysisMaxClaims             = 8
	analysisMaxClaimTextRunes     = 600
	analysisMaxParagraphIDs       = 3
	analysisMaxUnresolved         = 8
)

var errAnalysisUnavailable = errors.New("research analysis: unavailable")

type HTTPAnalysisPort struct {
	endpoint string
	aliasRef string
	client   *http.Client
}

type analysisParagraph struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id"`
	Text       string `json:"text"`
}

type paragraphSource struct {
	documentID string
	hash       string
	start      int
	end        int
}

type documentUnderstandingInput struct {
	Question   string              `json:"question"`
	Language   string              `json:"language"`
	Paragraphs []analysisParagraph `json:"paragraphs"`
}

type modelClaim struct {
	ID           string   `json:"id"`
	Text         string   `json:"text"`
	ParagraphIDs []string `json:"paragraph_ids"`
}

type documentUnderstandingOutput struct {
	Claims     []modelClaim `json:"claims"`
	Unresolved []string     `json:"unresolved"`
}

type claimCheckInput struct {
	Question   string              `json:"question"`
	Language   string              `json:"language"`
	Claims     []modelClaim        `json:"claims"`
	Paragraphs []analysisParagraph `json:"paragraphs"`
}

type modelClaimCheck struct {
	ClaimID      string   `json:"claim_id"`
	Verdict      string   `json:"verdict"`
	ParagraphIDs []string `json:"paragraph_ids"`
}

type claimCheckOutput struct {
	Checks []modelClaimCheck `json:"checks"`
}

type analysisWireRequest struct {
	AliasRef  string               `json:"alias_ref"`
	Operation string               `json:"operation"`
	Input     any                  `json:"input"`
	RenCrow   analysisWireMetadata `json:"rencrow"`
}

type analysisWireMetadata struct {
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}

type analysisChatResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int64                `json:"created,omitempty"`
	Model             string               `json:"model"`
	Choices           []analysisChatChoice `json:"choices"`
	Usage             json.RawMessage      `json:"usage,omitempty"`
	SystemFingerprint string               `json:"system_fingerprint,omitempty"`
	ServiceTier       string               `json:"service_tier,omitempty"`
}

type analysisChatChoice struct {
	Index        int                 `json:"index"`
	Message      analysisChatMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type analysisChatMessage struct {
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
}

func NewHTTPAnalysisPort(baseURL, aliasRef string, client *http.Client) (*HTTPAnalysisPort, error) {
	baseURL = strings.TrimSpace(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || !localGatewayHost(parsed.Hostname()) || (parsed.Path != "" && parsed.Path != "/") ||
		strings.TrimSpace(aliasRef) != aliasRef || !validResearchAlias(aliasRef) || client == nil {
		return nil, errors.New("research analysis: invalid Gateway configuration")
	}
	copyClient := *client
	if copyClient.Timeout <= 0 || copyClient.Timeout > analysisHTTPTimeout {
		copyClient.Timeout = analysisHTTPTimeout
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpointURL := *parsed
	endpointURL.Path = analysisEndpointPath
	endpointURL.RawPath = ""
	return &HTTPAnalysisPort{endpoint: endpointURL.String(), aliasRef: aliasRef, client: &copyClient}, nil
}

func localGatewayHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func validResearchAlias(alias string) bool {
	if alias == "" || alias[0] < 'a' || alias[0] > 'z' {
		return false
	}
	for _, r := range alias[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (p *HTTPAnalysisPort) Analyze(ctx context.Context, request engine.AnalysisRequest) (contract.Analysis, error) {
	result := contract.Analysis{SchemaVersion: "rencrow.research.analysis.v1", CollectionHash: request.CollectionHash, EngineRevision: analysisEngineRevision}
	if p == nil || p.client == nil || p.endpoint == "" || p.aliasRef == "" || ctx == nil {
		return result, fmt.Errorf("%w: port and context are required", errAnalysisUnavailable)
	}
	if request.MaxCalls < 2 {
		return result, fmt.Errorf("%w: two analysis calls are required", errAnalysisUnavailable)
	}
	paragraphs, sources, err := buildAnalysisParagraphs(ctx, request.Collection)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errAnalysisUnavailable, err)
	}
	if len(paragraphs) == 0 {
		return result, fmt.Errorf("%w: collection has no complete text paragraphs", errAnalysisUnavailable)
	}
	language := strings.TrimSpace(request.Language)
	if language == "" {
		language = "ja"
	}
	question := strings.TrimSpace(request.Objective)
	if question == "" {
		return result, fmt.Errorf("%w: research objective is empty", errAnalysisUnavailable)
	}
	metadata := analysisWireMetadata{RequestID: request.Envelope.RequestID, TraceID: request.Envelope.TraceID, TaskID: request.Envelope.TaskID, RunID: request.Envelope.RunID}
	firstInput := documentUnderstandingInput{Question: question, Language: language, Paragraphs: paragraphs}
	firstRequest, err := p.encodeRequest("document_understanding", firstInput, metadata)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errAnalysisUnavailable, err)
	}
	firstResponse, revision, err := p.call(ctx, firstRequest)
	if err != nil {
		result.Calls = 1
		return result, fmt.Errorf("research analysis document_understanding: %w", err)
	}
	result.Calls = 1
	result.EngineRevision = analysisEngineRevision + "@" + revision
	var understood documentUnderstandingOutput
	if err := decodeStrictAnalysisJSON(firstResponse, &understood); err != nil {
		return result, fmt.Errorf("research analysis document_understanding response: %w", err)
	}
	if understood.Claims == nil || understood.Unresolved == nil {
		return result, errors.New("research analysis document_understanding response requires claims and unresolved arrays")
	}
	if len(understood.Unresolved) > analysisMaxUnresolved {
		return result, fmt.Errorf("research analysis document_understanding response: unresolved exceeds %d items", analysisMaxUnresolved)
	}
	if err := validateModelClaims(understood.Claims, sources); err != nil {
		return result, fmt.Errorf("research analysis document_understanding response: %w", err)
	}
	for _, unresolved := range understood.Unresolved {
		if utf8.RuneCountInString(unresolved) > analysisMaxClaimTextRunes {
			return result, fmt.Errorf("research analysis document_understanding response: unresolved item exceeds %d Unicode characters", analysisMaxClaimTextRunes)
		}
		unresolved = strings.TrimSpace(unresolved)
		if unresolved == "" {
			return result, errors.New("research analysis document_understanding response has an empty unresolved item")
		}
		result.Unresolved = append(result.Unresolved, unresolved)
	}
	if len(understood.Claims) == 0 {
		result.Unresolved = append(result.Unresolved, "document_understanding returned no claims")
		result.Coverage = "incomplete"
		return result, nil
	}
	secondInput := claimCheckInput{Question: question, Language: language, Claims: understood.Claims, Paragraphs: paragraphs}
	secondRequest, err := p.encodeRequest("claim_evidence_check", secondInput, metadata)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errAnalysisUnavailable, err)
	}
	secondResponse, secondRevision, err := p.call(ctx, secondRequest)
	if err != nil {
		result.Calls = 2
		return result, fmt.Errorf("research analysis claim_evidence_check: %w", err)
	}
	result.Calls = 2
	if revision != secondRevision {
		return result, errors.New("research analysis profile revision changed between analysis calls")
	}
	var checked claimCheckOutput
	if err := decodeStrictAnalysisJSON(secondResponse, &checked); err != nil {
		return result, fmt.Errorf("research analysis claim_evidence_check response: %w", err)
	}
	if checked.Checks == nil {
		return result, errors.New("research analysis claim_evidence_check response requires checks array")
	}
	claims, unresolved, err := verifyClaimChecks(understood.Claims, checked.Checks, sources)
	if err != nil {
		return result, fmt.Errorf("research analysis claim_evidence_check response: %w", err)
	}
	result.Claims = claims
	result.Unresolved = append(result.Unresolved, unresolved...)
	if len(result.Unresolved) > 0 || len(result.Claims) == 0 || len(result.Claims) != len(understood.Claims) {
		result.Coverage = "incomplete"
		return result, nil
	}
	result.Coverage = "complete"
	return result, nil
}

func (p *HTTPAnalysisPort) encodeRequest(operation string, input any, metadata analysisWireMetadata) ([]byte, error) {
	body, err := json.Marshal(analysisWireRequest{AliasRef: p.aliasRef, Operation: operation, Input: input, RenCrow: metadata})
	if err != nil {
		return nil, err
	}
	if len(body) > analysisMaxRequestBytes {
		return nil, errors.New("complete research input exceeds Gateway 128 KiB request limit")
	}
	return body, nil
}

func (p *HTTPAnalysisPort) call(ctx context.Context, body []byte) (json.RawMessage, string, error) {
	requestContext, cancel := context.WithTimeout(ctx, analysisHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Gateway returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, "", errors.New("Gateway response is not application/json")
	}
	revision := response.Header.Get(analysisProfileRevisionHeader)
	if !validResearchRevision(revision) {
		return nil, "", errors.New("Gateway response omitted a valid research profile revision")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, analysisMaxResponseBytes+1))
	if err != nil || len(data) > analysisMaxResponseBytes {
		return nil, "", errors.New("Gateway response exceeds the bounded response size")
	}
	var chat analysisChatResponse
	if err := decodeStrictAnalysisJSON(data, &chat); err != nil {
		return nil, "", fmt.Errorf("decode Gateway response: %w", err)
	}
	if chat.ID == "" || chat.Object != "chat.completion" || chat.Model == "" || len(chat.Choices) != 1 || chat.Choices[0].Index != 0 || chat.Choices[0].Message.Role != "assistant" || chat.Choices[0].FinishReason != "stop" {
		return nil, "", errors.New("Gateway response did not contain one completed assistant choice")
	}
	message := chat.Choices[0].Message
	if strings.TrimSpace(message.Content) == "" || len(message.ToolCalls) != 0 {
		return nil, "", errors.New("Gateway response has empty content or tool calls")
	}
	return json.RawMessage(message.Content), revision, nil
}

func validResearchRevision(revision string) bool {
	return validResearchAlias(revision)
}

func decodeStrictAnalysisJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("JSON value has trailing data")
	}
	return nil
}

func buildAnalysisParagraphs(ctx context.Context, collection contract.Collection) ([]analysisParagraph, map[string]paragraphSource, error) {
	if len(collection.Documents) == 0 {
		return nil, nil, errors.New("collection has no documents")
	}
	paragraphs := make([]analysisParagraph, 0)
	sources := make(map[string]paragraphSource)
	documentIDs := make(map[string]struct{}, len(collection.Documents))
	inputBytes := 0
	for _, document := range collection.Documents {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(document.ID) == "" || strings.TrimSpace(document.Text) == "" {
			return nil, nil, errors.New("collection contains a document without ID or complete text")
		}
		if len(document.Text) > analysisMaxRequestBytes-inputBytes {
			return nil, nil, errors.New("complete collection text exceeds Gateway 128 KiB request limit")
		}
		inputBytes += len(document.Text)
		if _, duplicate := documentIDs[document.ID]; duplicate {
			return nil, nil, fmt.Errorf("collection repeats document %q", document.ID)
		}
		documentIDs[document.ID] = struct{}{}
		digest := sha256.Sum256([]byte(document.Text))
		if document.ExtractedHash != hex.EncodeToString(digest[:]) {
			return nil, nil, fmt.Errorf("document %q text does not match its extracted hash", document.ID)
		}
		segments := splitAnalysisParagraphs(document.Text)
		if len(segments) == 0 {
			return nil, nil, fmt.Errorf("document %q contains no nonempty paragraph", document.ID)
		}
		for _, segment := range segments {
			id := fmt.Sprintf("p%06d", len(paragraphs)+1)
			paragraphs = append(paragraphs, analysisParagraph{ID: id, DocumentID: document.ID, Text: segment.text})
			sources[id] = paragraphSource{documentID: document.ID, hash: document.ExtractedHash, start: segment.start, end: segment.end}
		}
	}
	return paragraphs, sources, nil
}

type paragraphSegment struct {
	text       string
	start, end int
}

func splitAnalysisParagraphs(text string) []paragraphSegment {
	runes := []rune(text)
	segments := make([]paragraphSegment, 0)
	start, end := -1, -1
	flush := func() {
		if start < 0 || end <= start {
			start, end = -1, -1
			return
		}
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		if end > start {
			segments = append(segments, paragraphSegment{text: string(runes[start:end]), start: start, end: end})
		}
		start, end = -1, -1
	}
	lineStart := 0
	for lineStart <= len(runes) {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && runes[contentEnd-1] == '\r' {
			contentEnd--
		}
		blank := strings.TrimSpace(string(runes[lineStart:contentEnd])) == ""
		if blank {
			flush()
		} else {
			if start < 0 {
				start = lineStart
			}
			end = contentEnd
		}
		if lineEnd == len(runes) {
			break
		}
		lineStart = lineEnd + 1
	}
	flush()
	return segments
}

func validateModelClaims(claims []modelClaim, sources map[string]paragraphSource) error {
	if len(claims) > analysisMaxClaims {
		return fmt.Errorf("claims exceeds %d items", analysisMaxClaims)
	}
	seen := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.Text) == "" {
			return errors.New("claim requires ID and text")
		}
		if utf8.RuneCountInString(claim.Text) > analysisMaxClaimTextRunes {
			return fmt.Errorf("claim text exceeds %d Unicode characters", analysisMaxClaimTextRunes)
		}
		if _, duplicate := seen[claim.ID]; duplicate {
			return fmt.Errorf("duplicate claim ID %q", claim.ID)
		}
		seen[claim.ID] = struct{}{}
		if claim.ParagraphIDs == nil {
			return fmt.Errorf("claim %q has no paragraph ID array", claim.ID)
		}
		if err := validateParagraphIDList(claim.ParagraphIDs, sources); err != nil {
			return fmt.Errorf("claim %q: %w", claim.ID, err)
		}
	}
	return nil
}

func validateParagraphIDList(ids []string, sources map[string]paragraphSource) error {
	if len(ids) > analysisMaxParagraphIDs {
		return fmt.Errorf("paragraph IDs exceeds %d items", analysisMaxParagraphIDs)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := sources[id]; !ok {
			return fmt.Errorf("paragraph ID %q is not in the supplied collection", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("paragraph ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func verifyClaimChecks(claims []modelClaim, checks []modelClaimCheck, sources map[string]paragraphSource) ([]contract.Claim, []string, error) {
	if len(checks) > analysisMaxClaims {
		return nil, nil, fmt.Errorf("checks exceeds %d items", analysisMaxClaims)
	}
	claimsByID := make(map[string]modelClaim, len(claims))
	for _, claim := range claims {
		claimsByID[claim.ID] = claim
	}
	checksByID := make(map[string]modelClaimCheck, len(checks))
	for _, check := range checks {
		if _, ok := claimsByID[check.ClaimID]; !ok {
			return nil, nil, fmt.Errorf("check references unknown claim %q", check.ClaimID)
		}
		if _, duplicate := checksByID[check.ClaimID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate check for claim %q", check.ClaimID)
		}
		if check.ParagraphIDs == nil {
			return nil, nil, fmt.Errorf("check for claim %q has no paragraph ID array", check.ClaimID)
		}
		if err := validateParagraphIDList(check.ParagraphIDs, sources); err != nil {
			return nil, nil, fmt.Errorf("check for claim %q: %w", check.ClaimID, err)
		}
		switch check.Verdict {
		case "supported", "contradicted", "insufficient":
		default:
			return nil, nil, fmt.Errorf("check for claim %q has invalid verdict %q", check.ClaimID, check.Verdict)
		}
		checksByID[check.ClaimID] = check
	}
	verified := make([]contract.Claim, 0, len(claims))
	unresolved := make([]string, 0)
	for _, claim := range claims {
		check, ok := checksByID[claim.ID]
		if !ok {
			unresolved = append(unresolved, "No evidence check returned for claim: "+claim.Text)
			continue
		}
		if check.Verdict != "supported" || len(check.ParagraphIDs) == 0 {
			unresolved = append(unresolved, "Claim "+claim.ID+" was not supported ("+check.Verdict+"): "+claim.Text)
			continue
		}
		evidence := make([]contract.EvidenceRef, 0, len(check.ParagraphIDs))
		for _, id := range check.ParagraphIDs {
			source := sources[id]
			evidence = append(evidence, contract.EvidenceRef{
				DocumentID: source.documentID, ArtifactHash: source.hash,
				Start: source.start, End: source.end, Locator: fmt.Sprintf("characters:%d-%d", source.start, source.end),
			})
		}
		verified = append(verified, contract.Claim{Text: strings.TrimSpace(claim.Text), Evidence: evidence, Verified: true})
	}
	return verified, unresolved, nil
}
