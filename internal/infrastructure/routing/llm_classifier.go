package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

const classifierMaxTokens = 8

// LLMClassifier はLLMベースのタスク分類器
type LLMClassifier struct {
	llmProvider  llm.LLMProvider
	systemPrompt string
}

// NewLLMClassifier は新しいLLMClassifierを作成
func NewLLMClassifier(llmProvider llm.LLMProvider, systemPrompt string) *LLMClassifier {
	return &LLMClassifier{
		llmProvider:  llmProvider,
		systemPrompt: systemPrompt,
	}
}

// ClassifyWithChatReply is the optional one-generation route-and-reply path.
func (c *LLMClassifier) ClassifyWithChatReply(ctx context.Context, req llm.GenerateRequest) (domainagent.RouteReplyClassification, error) {
	if c.llmProvider == nil {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("LLM provider is unavailable")
	}
	req.SystemPrompt = ""
	policyMessage := llm.Message{
		Role:    "system",
		Content: strings.TrimSpace(c.systemPrompt + "\n\n" + routeReplyJSONContract),
		Type:    llm.PromptContextStable,
		Metadata: map[string]string{
			"runtime_context_kind": "classifier_route_contract",
		},
	}
	characterPrefix := 0
	for characterPrefix < len(req.Messages) && req.Messages[characterPrefix].Type == llm.PromptContextCharacter {
		characterPrefix++
	}
	req.Messages = append(req.Messages, llm.Message{})
	copy(req.Messages[characterPrefix+1:], req.Messages[characterPrefix:len(req.Messages)-1])
	req.Messages[characterPrefix] = policyMessage
	req.ResponseFormat = llm.ResponseFormatJSONObject
	// Combined JSON generation is non-streaming even when Mio's regular CHAT
	// configuration enables token callbacks. Never expose route JSON to hooks.
	req.OnToken = nil
	ensureNoThinking(&req)

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("LLM route and reply generation failed: %w", err)
	}
	decoded, err := decodeRouteReply(resp.Content)
	if err != nil {
		return domainagent.RouteReplyClassification{}, err
	}
	route, matched := c.parseRouteWithMatch(decoded.Route)
	if !matched || !strings.EqualFold(strings.TrimSpace(decoded.Route), string(route)) {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined generation returned invalid route field %q", decoded.Route)
	}
	if route == routing.RouteCHAT && strings.TrimSpace(decoded.Reply) == "" {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined CHAT generation returned an empty reply")
	}
	if route != routing.RouteCHAT && strings.TrimSpace(decoded.Reply) != "" {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined non-CHAT generation returned a reply")
	}
	confidence := c.calculateConfidence(route, string(route))
	reason := fmt.Sprintf("LLM classified as %s", route)
	decision := routing.NewDecisionWithEvidence(route, confidence,
		reason, routing.DecisionEvidence{
			Source:     routing.EvidenceSourceClassifier,
			Matched:    true,
			Route:      route,
			Confidence: confidence,
			Reason:     reason,
		})
	return domainagent.RouteReplyClassification{
		Decision:   decision,
		Reply:      decoded.Reply,
		ReplyValid: route == routing.RouteCHAT,
		Generation: resp,
	}, nil
}

// ClassifyWithMioTurnOutcome is the optional one-generation route, answer, and
// retrieval-request path. Explicit streams select a strict schema version;
// nonstream requests ask for v3 and also accept the strict legacy v2 envelope.
// A fixed route is preserved exactly and invalid output is never repaired.
func (c *LLMClassifier) ClassifyWithMioTurnOutcome(ctx context.Context, req llm.GenerateRequest, fixedRoute *routing.Route) (domainagent.MioTurnClassification, error) {
	if c.llmProvider == nil {
		return domainagent.MioTurnClassification{}, fmt.Errorf("LLM provider is unavailable")
	}
	logicalOutcomeStreaming := req.LogicalOutcomeStream != ""
	if logicalOutcomeStreaming && req.LogicalOutcomeStream != llm.LogicalOutcomeStreamV1 && req.LogicalOutcomeStream != llm.LogicalOutcomeStreamV2 {
		return domainagent.MioTurnClassification{}, fmt.Errorf("%w: unsupported logical outcome stream version %q", domainagent.ErrInvalidMioTurnOutcome, req.LogicalOutcomeStream)
	}
	if logicalOutcomeStreaming && req.OnLogicalOutcomeEvent == nil {
		return domainagent.MioTurnClassification{}, fmt.Errorf("%w: logical outcome stream requires an event callback", domainagent.ErrInvalidMioTurnOutcome)
	}
	if !logicalOutcomeStreaming && req.OnLogicalOutcomeEvent != nil {
		return domainagent.MioTurnClassification{}, fmt.Errorf("%w: logical outcome event callback requires an explicit stream version", domainagent.ErrInvalidMioTurnOutcome)
	}
	schemaVersion := 3
	if req.LogicalOutcomeStream == llm.LogicalOutcomeStreamV1 {
		schemaVersion = 2
	}

	var streamState *mioTurnOutcomeStreamState
	if logicalOutcomeStreaming {
		if fixedRoute != nil {
			fixed := *fixedRoute
			fixedRoute = &fixed
			if _, err := c.validateMioTurnOutcomeRoute(string(fixed), fixedRoute); err != nil {
				return domainagent.MioTurnClassification{}, fmt.Errorf("%w: %v", domainagent.ErrInvalidMioTurnOutcome, err)
			}
		}
		streamState = &mioTurnOutcomeStreamState{
			classifier:     c,
			fixedRoute:     fixedRoute,
			streamVersion:  req.LogicalOutcomeStream,
			threadBoundary: conversation.ThreadBoundaryUnknown,
			onEvent:        req.OnLogicalOutcomeEvent,
			onToken:        req.OnToken,
		}
		req.OnLogicalOutcomeEvent = streamState.handleEvent
		if req.OnToken != nil {
			req.OnToken = streamState.forwardValidatedToken
		}
	} else {
		// The strict JSON contract remains nonstreaming unless a trusted caller
		// explicitly opts into the versioned logical event stream above.
		req.OnToken = nil
	}
	req.SystemPrompt = ""
	contractPrompt := mioTurnOutcomeJSONContract(schemaVersion)
	if fixedRoute != nil {
		contractPrompt += fmt.Sprintf("\n\nThe caller supplied fixed route %s. Set route exactly %s.", *fixedRoute, *fixedRoute)
	}
	policyMessage := llm.Message{
		Role:    "system",
		Content: strings.TrimSpace(c.systemPrompt + "\n\n" + contractPrompt),
		Type:    llm.PromptContextStable,
		Metadata: map[string]string{
			"runtime_context_kind": "classifier_mio_turn_outcome_contract",
		},
	}
	characterPrefix := 0
	for characterPrefix < len(req.Messages) && req.Messages[characterPrefix].Type == llm.PromptContextCharacter {
		characterPrefix++
	}
	req.Messages = append(req.Messages, llm.Message{})
	copy(req.Messages[characterPrefix+1:], req.Messages[characterPrefix:len(req.Messages)-1])
	req.Messages[characterPrefix] = policyMessage
	req.ResponseFormat = llm.ResponseFormatJSONObject
	ensureNoThinking(&req)

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return domainagent.MioTurnClassification{}, fmt.Errorf("LLM Mio turn outcome generation failed: %w", err)
	}
	var route routing.Route
	var outcome domainagent.MioTurnOutcome
	boundary := conversation.ThreadBoundaryUnknown
	if logicalOutcomeStreaming {
		route, outcome, boundary, err = streamState.classification(resp)
	} else {
		route, outcome, boundary, err = c.decodeMioTurnOutcome(resp.Content, fixedRoute, schemaVersion, true)
	}
	if err != nil {
		return domainagent.MioTurnClassification{}, fmt.Errorf("%w: %v", domainagent.ErrInvalidMioTurnOutcome, err)
	}
	confidence := c.calculateConfidence(route, string(route))
	reason := fmt.Sprintf("LLM generated MioTurnOutcome v%d: %s on %s", schemaVersion, outcome.Kind, route)
	decision := routing.NewDecisionWithEvidence(route, confidence, reason, routing.DecisionEvidence{
		Source:     routing.EvidenceSourceClassifier,
		Matched:    true,
		Route:      route,
		Confidence: confidence,
		Reason:     reason,
	})
	return domainagent.MioTurnClassification{
		Decision:   decision,
		Outcome:    outcome,
		Boundary:   boundary,
		Generation: resp,
	}, nil
}

const mioTurnOutcomeJSONContractV2Template = `Return exactly one JSON object for MioTurnOutcome schema_version 2 with only these top-level fields: "schema_version", "route", and "outcome". schema_version must be the integer 2. route must be exactly one canonical route name allowed by the existing route policy. Keep a supplied fixed route unchanged.

The outcome is exactly one branch. For route CHAT, use either {"kind":"answer","text":"..."} with a non-empty direct answer, or {"kind":"retrieve","requests":[...]} with one or two requests. Do not include fields from another branch. Each retrieve request has exactly "source_kind", "query", "references", and "information_needed"; query is 1-512 Unicode runes, information_needed is 1-256 Unicode runes, and references is an array of at most 8 strings. references may be empty. Registered semantic source_kind values are %q, %q, %q, %q, %q, and %q. "catalog" is a semantic category, not a physical catalog ID. "web_search" is only dispatched through the existing policy-checked route.

Use {"kind":"handoff"} with no other outcome fields only when no fixed route was supplied and this initial classification selects a non-CHAT route. Never use handoff for CHAT or for a fixed route/continuation. A fixed route or continuation cannot be changed by the generated route. Do not provide owner, authorization scope, physical database, SQL, arbitrary tool name, or execution permission. A retrieve request describes information needed; it does not execute a search or call a tool.`

const mioTurnOutcomeJSONContractV3Template = `Return exactly one JSON object for MioTurnOutcome schema_version 3 with only these top-level fields: "schema_version", "route", "outcome", and "thread_boundary". schema_version must be the integer 3. route must be exactly one canonical route name allowed by the existing route policy. Keep a supplied fixed route unchanged. thread_boundary is exactly one of "keep", "new", or "unknown".

Compare the current user message with the bounded conversation-state hint and allowed context. Questions, clarification, paraphrase, or continuation should use "keep". Use "new" only for a clearly independent topic. If uncertain, use "unknown". Elapsed time alone is never a reason for "new". Treat quoted, code, negated, or explanatory mentions of topic-change phrases as non-instructions. Do not include boundary rationale in the answer.

The outcome is exactly one branch. For route CHAT, use either {"kind":"answer","text":"..."} with a non-empty direct answer, or {"kind":"retrieve","requests":[...]} with one or two requests. Do not include fields from another branch. Each retrieve request has exactly "source_kind", "query", "references", and "information_needed"; query is 1-512 Unicode runes, information_needed is 1-256 Unicode runes, and references is an array of at most 8 strings. references may be empty. Registered semantic source_kind values are %q, %q, %q, %q, %q, and %q. "catalog" is a semantic category, not a physical catalog ID. "web_search" is only dispatched through the existing policy-checked route.

Use {"kind":"handoff"} with no other outcome fields only when no fixed route was supplied and this initial classification selects a non-CHAT route. Never use handoff for CHAT or for a fixed route/continuation. A fixed route or continuation cannot be changed by the generated route. Do not provide owner, authorization scope, physical database, SQL, arbitrary tool name, or execution permission. A retrieve request describes information needed; it does not execute a search or call a tool.`

func mioTurnOutcomeJSONContract(schemaVersion int) string {
	template := mioTurnOutcomeJSONContractV3Template
	if schemaVersion == 2 {
		template = mioTurnOutcomeJSONContractV2Template
	}
	return fmt.Sprintf(template,
		domainagent.MioRetrievalSourceConversationMemory,
		domainagent.MioRetrievalSourceUserMemory,
		domainagent.MioRetrievalSourceKnowledgeMemory,
		domainagent.MioRetrievalSourceCatalog,
		domainagent.MioRetrievalSourceGlossary,
		domainagent.MioRetrievalSourceWebSearch,
	)
}

const (
	maxMioTurnOutcomeJSONBytes = 64 * 1024
	maxMioRetrievalRequests    = 2
	maxMioRetrievalQueryRunes  = 512
	maxMioRetrievalNeedRunes   = 256
	maxMioRetrievalReferences  = 8
)

// allowLegacyV2 is enabled only for nonstreaming requests. Stream requests stay
// pinned to the exact version selected by their explicit stream opt-in.
func (c *LLMClassifier) decodeMioTurnOutcome(content string, fixedRoute *routing.Route, expectedVersion int, allowLegacyV2 bool) (routing.Route, domainagent.MioTurnOutcome, conversation.ThreadBoundaryProposal, error) {
	if len(content) > maxMioTurnOutcomeJSONBytes {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("combined MioTurnOutcome response exceeds %d bytes", maxMioTurnOutcomeJSONBytes)
	}
	if err := rejectDuplicateMioOutcomeJSONKeys([]byte(content)); err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("combined MioTurnOutcome has invalid or duplicate JSON keys: %w", err)
	}
	allowedFields := []string{"schema_version", "route", "outcome"}
	if expectedVersion == 3 {
		allowedFields = append(allowedFields, "thread_boundary")
	}
	fields, err := decodeStrictJSONObject([]byte(content), "combined MioTurnOutcome", allowedFields...)
	if err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, err
	}
	if err := requireJSONFields(fields, "schema_version", "route", "outcome"); err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("combined MioTurnOutcome %w", err)
	}
	version, err := decodeJSONInt(fields["schema_version"], "schema_version")
	if err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, err
	}
	legacyV2 := allowLegacyV2 && expectedVersion == 3 && version == 2
	if version != expectedVersion && !legacyV2 {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("combined MioTurnOutcome schema_version = %d, want %d", version, expectedVersion)
	}
	if version == 2 && fields["thread_boundary"] != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("combined MioTurnOutcome schema_version 2 does not allow thread_boundary")
	}
	routeText, err := decodeJSONStringValue(fields["route"], "route")
	if err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, err
	}
	route, err := c.validateMioTurnOutcomeRoute(routeText, fixedRoute)
	if err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, err
	}
	outcome, err := decodeMioTurnOutcomeBranch(fields["outcome"], route)
	if err != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, err
	}
	if fixedRoute != nil && outcome.Kind == domainagent.MioTurnOutcomeHandoff {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("handoff outcome is only valid for an initial classification without a fixed route")
	}
	boundary := conversation.ThreadBoundaryUnknown
	if version == 3 {
		boundary = decodeThreadBoundaryProposal(fields["thread_boundary"])
	}
	return route, outcome, boundary, nil
}

func rejectDuplicateMioOutcomeJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeMioOutcomeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func consumeMioOutcomeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON object repeats field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeMioOutcomeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("JSON object has invalid end")
		}
	case '[':
		for decoder.More() {
			if err := consumeMioOutcomeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("JSON array has invalid end")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func decodeThreadBoundaryProposal(raw json.RawMessage) conversation.ThreadBoundaryProposal {
	proposal, err := decodeJSONStringValue(raw, "thread_boundary")
	if err != nil {
		return conversation.ThreadBoundaryUnknown
	}
	return threadBoundaryProposalFromString(proposal)
}

func threadBoundaryProposalFromString(value string) conversation.ThreadBoundaryProposal {
	switch conversation.ThreadBoundaryProposal(value) {
	case conversation.ThreadBoundaryKeep, conversation.ThreadBoundaryNew:
		return conversation.ThreadBoundaryProposal(value)
	default:
		return conversation.ThreadBoundaryUnknown
	}
}

func (c *LLMClassifier) validateMioTurnOutcomeRoute(routeText string, fixedRoute *routing.Route) (routing.Route, error) {
	route, matched := c.parseRouteWithMatch(routeText)
	if !matched || string(route) != routeText {
		return "", fmt.Errorf("MioTurnOutcome route must be canonical, got %q", routeText)
	}
	if !isMioTurnOutcomeClassifierRoute(route) {
		return "", fmt.Errorf("MioTurnOutcome route %s is not allowed by classifier policy", route)
	}
	if fixedRoute != nil {
		fixed, known := c.parseRouteWithMatch(string(*fixedRoute))
		if !known || fixed != *fixedRoute || !isMioTurnOutcomeClassifierRoute(*fixedRoute) {
			return "", fmt.Errorf("MioTurnOutcome fixed route %q is invalid", *fixedRoute)
		}
		if route != *fixedRoute {
			return "", fmt.Errorf("MioTurnOutcome route %s does not match fixed route %s", route, *fixedRoute)
		}
	}
	return route, nil
}

type mioTurnOutcomeStreamBranch uint8

const (
	mioTurnOutcomeStreamNoBranch mioTurnOutcomeStreamBranch = iota
	mioTurnOutcomeStreamAnswer
	mioTurnOutcomeStreamRetrieve
	mioTurnOutcomeStreamHandoff
)

type mioTurnOutcomeStreamState struct {
	classifier    *LLMClassifier
	fixedRoute    *routing.Route
	streamVersion string
	onEvent       llm.LogicalOutcomeEventCallback
	onToken       llm.StreamCallback

	branch         mioTurnOutcomeStreamBranch
	route          routing.Route
	outcome        domainagent.MioTurnOutcome
	threadBoundary conversation.ThreadBoundaryProposal
	answer         strings.Builder
	answerDone     bool
	tokenPending   bool
	pendingToken   string
	callbackErr    error
}

func (s *mioTurnOutcomeStreamState) handleEvent(event llm.LogicalOutcomeEvent) error {
	if s.callbackErr != nil {
		return s.callbackErr
	}
	if s.tokenPending {
		return s.fail("answer_delta was not followed by its text callback")
	}

	switch event.Kind {
	case llm.LogicalOutcomeAnswerStart:
		if s.branch != mioTurnOutcomeStreamNoBranch {
			return s.fail("answer_start is contradictory or duplicated")
		}
		if event.SchemaVersion != s.expectedSchemaVersion() || event.PayloadKind != "text" || event.Text != "" || event.FinishReason != "" || event.ThreadBoundary != "" || len(event.RetrievalRequests) != 0 {
			return s.fail("answer_start has an invalid schema or branch shape")
		}
		route, err := s.classifier.validateMioTurnOutcomeRoute(event.Route, s.fixedRoute)
		if err != nil {
			return s.fail("answer_start route: %v", err)
		}
		if route != routing.RouteCHAT {
			return s.fail("answer_start requires route CHAT, got %s", route)
		}
		s.branch = mioTurnOutcomeStreamAnswer
		s.route = route
		s.outcome.Kind = domainagent.MioTurnOutcomeAnswer
	case llm.LogicalOutcomeAnswerDelta:
		if s.branch != mioTurnOutcomeStreamAnswer || s.answerDone {
			return s.fail("answer_delta is outside the active answer branch")
		}
		if event.SchemaVersion != 0 || event.Route != "" || event.PayloadKind != "" || event.FinishReason != "" || event.ThreadBoundary != "" || len(event.RetrievalRequests) != 0 {
			return s.fail("answer_delta contains fields outside its branch")
		}
		if !utf8.ValidString(event.Text) {
			return s.fail("answer_delta is not valid UTF-8")
		}
		s.answer.WriteString(event.Text)
	case llm.LogicalOutcomeAnswerDone:
		if s.branch != mioTurnOutcomeStreamAnswer || s.answerDone {
			return s.fail("answer_done is outside the active answer branch")
		}
		if event.SchemaVersion != 0 || event.Route != "" || event.PayloadKind != "" || event.Text != "" || len(event.RetrievalRequests) != 0 {
			return s.fail("answer_done contains fields outside its branch")
		}
		if s.streamVersion == llm.LogicalOutcomeStreamV1 && event.ThreadBoundary != "" {
			return s.fail("v1 answer_done contains v2 boundary metadata")
		}
		if event.FinishReason != "stop" || strings.TrimSpace(s.answer.String()) == "" {
			return s.fail("answer_done has an invalid finish reason or empty answer")
		}
		s.threadBoundary = threadBoundaryProposalFromString(event.ThreadBoundary)
		s.answerDone = true
	case llm.LogicalOutcomeRetrievalRequest:
		if s.branch != mioTurnOutcomeStreamNoBranch {
			return s.fail("retrieval_request is contradictory or duplicated")
		}
		if event.PayloadKind != "" || event.Text != "" || event.FinishReason != "" {
			return s.fail("retrieval_request contains fields outside its branch")
		}
		if event.SchemaVersion != s.expectedSchemaVersion() {
			return s.fail("retrieval_request schema_version = %d, want %d", event.SchemaVersion, s.expectedSchemaVersion())
		}
		route, err := s.classifier.validateMioTurnOutcomeRoute(event.Route, s.fixedRoute)
		if err != nil {
			return s.fail("retrieval_request route: %v", err)
		}
		if len(event.RetrievalRequests) > maxMioTurnOutcomeJSONBytes {
			return s.fail("retrieval_request exceeds %d bytes", maxMioTurnOutcomeJSONBytes)
		}
		branchJSON := append([]byte(`{"kind":"retrieve","requests":`), event.RetrievalRequests...)
		branchJSON = append(branchJSON, '}')
		outcome, err := decodeMioTurnOutcomeBranch(branchJSON, route)
		if err != nil {
			return s.fail("retrieval_request payload: %v", err)
		}
		s.branch = mioTurnOutcomeStreamRetrieve
		s.route = route
		s.outcome = outcome
		if s.streamVersion == llm.LogicalOutcomeStreamV1 && event.ThreadBoundary != "" {
			return s.fail("v1 retrieval_request contains v2 boundary metadata")
		}
		s.threadBoundary = threadBoundaryProposalFromString(event.ThreadBoundary)
	case llm.LogicalOutcomeHandoff:
		if s.branch != mioTurnOutcomeStreamNoBranch {
			return s.fail("handoff is contradictory or duplicated")
		}
		if event.PayloadKind != "" || event.Text != "" || event.FinishReason != "" || len(event.RetrievalRequests) != 0 {
			return s.fail("handoff contains fields outside its branch")
		}
		if event.SchemaVersion != s.expectedSchemaVersion() {
			return s.fail("handoff schema_version = %d, want %d", event.SchemaVersion, s.expectedSchemaVersion())
		}
		if s.fixedRoute != nil {
			return s.fail("handoff is invalid for a fixed route")
		}
		route, err := s.classifier.validateMioTurnOutcomeRoute(event.Route, nil)
		if err != nil {
			return s.fail("handoff route: %v", err)
		}
		if route == routing.RouteCHAT {
			return s.fail("handoff requires a non-CHAT route")
		}
		s.branch = mioTurnOutcomeStreamHandoff
		s.route = route
		s.outcome = domainagent.MioTurnOutcome{Kind: domainagent.MioTurnOutcomeHandoff}
		if s.streamVersion == llm.LogicalOutcomeStreamV1 && event.ThreadBoundary != "" {
			return s.fail("v1 handoff contains v2 boundary metadata")
		}
		s.threadBoundary = threadBoundaryProposalFromString(event.ThreadBoundary)
	default:
		return s.fail("unknown event kind %q", event.Kind)
	}

	if s.onEvent != nil {
		publishedEvent := event
		publishedEvent.ThreadBoundary = ""
		if err := s.onEvent(publishedEvent); err != nil {
			s.callbackErr = err
			return err
		}
	}
	if event.Kind == llm.LogicalOutcomeAnswerDelta && s.onToken != nil && event.Text != "" {
		s.pendingToken = event.Text
		s.tokenPending = true
	}
	return nil
}

func (s *mioTurnOutcomeStreamState) forwardValidatedToken(token string) {
	if s.callbackErr != nil {
		return
	}
	if s.branch != mioTurnOutcomeStreamAnswer || s.answerDone || !s.tokenPending || token != s.pendingToken {
		s.callbackErr = fmt.Errorf("%w: provider text callback did not match a validated answer_delta", domainagent.ErrInvalidMioTurnOutcome)
		return
	}
	s.pendingToken = ""
	s.tokenPending = false
	s.onToken(token)
}

func (s *mioTurnOutcomeStreamState) classification(response llm.GenerateResponse) (routing.Route, domainagent.MioTurnOutcome, conversation.ThreadBoundaryProposal, error) {
	if s.callbackErr != nil {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, s.callbackErr
	}
	if s.tokenPending {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("answer_delta was not followed by its text callback")
	}
	if s.branch == mioTurnOutcomeStreamNoBranch {
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("logical outcome stream ended without an outcome")
	}
	switch s.branch {
	case mioTurnOutcomeStreamAnswer:
		answer := s.answer.String()
		if !s.answerDone || response.FinishReason != "stop" || strings.TrimSpace(answer) == "" {
			return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("logical answer stream ended before a completed answer")
		}
		if response.Content != answer {
			return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("logical answer content does not match streamed answer deltas")
		}
		s.outcome.Text = answer
	case mioTurnOutcomeStreamRetrieve, mioTurnOutcomeStreamHandoff:
		if response.Content != "" || response.FinishReason != "" {
			return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("non-answer logical outcome returned answer content or finish reason")
		}
	default:
		return "", domainagent.MioTurnOutcome{}, conversation.ThreadBoundaryUnknown, fmt.Errorf("logical outcome stream has an unknown branch")
	}
	return s.route, s.outcome, s.threadBoundary, nil
}

func (s *mioTurnOutcomeStreamState) expectedSchemaVersion() int {
	if s.streamVersion == llm.LogicalOutcomeStreamV1 {
		return 2
	}
	return 3
}

func (s *mioTurnOutcomeStreamState) fail(format string, args ...any) error {
	s.callbackErr = fmt.Errorf("%w: logical outcome stream "+format, append([]any{domainagent.ErrInvalidMioTurnOutcome}, args...)...)
	return s.callbackErr
}

// isMioTurnOutcomeClassifierRoute mirrors the route categories defined in
// prompts/classifier.md. Keep parseRouteWithMatch unchanged for the legacy v1
// contract, which historically accepts additional route names.
func isMioTurnOutcomeClassifierRoute(route routing.Route) bool {
	switch route {
	case routing.RouteCHAT,
		routing.RoutePLAN,
		routing.RouteANALYZE,
		routing.RouteOPS,
		routing.RouteRESEARCH,
		routing.RouteCODE,
		routing.RouteCODE1,
		routing.RouteCODE2,
		routing.RouteCODE3:
		return true
	default:
		return false
	}
}

func decodeMioTurnOutcomeBranch(raw json.RawMessage, route routing.Route) (domainagent.MioTurnOutcome, error) {
	fields, err := decodeStrictJSONObject(raw, "MioTurnOutcome outcome", "kind", "text", "requests")
	if err != nil {
		return domainagent.MioTurnOutcome{}, err
	}
	kindText, err := decodeRequiredJSONString(fields, "kind", "outcome")
	if err != nil {
		return domainagent.MioTurnOutcome{}, err
	}
	kind := domainagent.MioTurnOutcomeKind(kindText)
	outcome := domainagent.MioTurnOutcome{Kind: kind}
	switch kind {
	case domainagent.MioTurnOutcomeAnswer:
		if err := allowJSONFields(fields, "kind", "text"); err != nil {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("answer outcome %w", err)
		}
		if route != routing.RouteCHAT {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("answer outcome requires route CHAT, got %s", route)
		}
		outcome.Text, err = decodeRequiredJSONString(fields, "text", "answer outcome")
		if err != nil {
			return domainagent.MioTurnOutcome{}, err
		}
		if strings.TrimSpace(outcome.Text) == "" {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("answer outcome text must not be empty")
		}
	case domainagent.MioTurnOutcomeRetrieve:
		if err := allowJSONFields(fields, "kind", "requests"); err != nil {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("retrieve outcome %w", err)
		}
		if route != routing.RouteCHAT {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("retrieve outcome requires route CHAT, got %s", route)
		}
		requestsRaw, ok := fields["requests"]
		if !ok {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("retrieve outcome is missing requests")
		}
		requestValues, err := decodeJSONRawArray(requestsRaw, "retrieve requests", maxMioRetrievalRequests)
		if err != nil {
			return domainagent.MioTurnOutcome{}, err
		}
		if len(requestValues) == 0 {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("retrieve outcome must include at least one request")
		}
		outcome.Requests = make([]domainagent.MioRetrievalRequest, 0, len(requestValues))
		for index, requestValue := range requestValues {
			request, err := decodeMioRetrievalRequest(requestValue, index)
			if err != nil {
				return domainagent.MioTurnOutcome{}, err
			}
			outcome.Requests = append(outcome.Requests, request)
		}
	case domainagent.MioTurnOutcomeHandoff:
		if err := allowJSONFields(fields, "kind"); err != nil {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("handoff outcome %w", err)
		}
		if route == routing.RouteCHAT {
			return domainagent.MioTurnOutcome{}, fmt.Errorf("handoff outcome requires a non-CHAT route")
		}
	default:
		return domainagent.MioTurnOutcome{}, fmt.Errorf("unknown MioTurnOutcome kind %q", kindText)
	}
	return outcome, nil
}

func decodeMioRetrievalRequest(raw json.RawMessage, index int) (domainagent.MioRetrievalRequest, error) {
	label := fmt.Sprintf("retrieve request %d", index+1)
	fields, err := decodeStrictJSONObject(raw, label, "source_kind", "query", "references", "information_needed")
	if err != nil {
		return domainagent.MioRetrievalRequest{}, err
	}
	if err := requireJSONFields(fields, "source_kind", "query", "references", "information_needed"); err != nil {
		return domainagent.MioRetrievalRequest{}, fmt.Errorf("%s %w", label, err)
	}
	sourceText, err := decodeJSONStringValue(fields["source_kind"], label+" source_kind")
	if err != nil {
		return domainagent.MioRetrievalRequest{}, err
	}
	sourceKind := domainagent.MioRetrievalSourceKind(sourceText)
	if !sourceKind.IsRegistered() {
		return domainagent.MioRetrievalRequest{}, fmt.Errorf("%s source_kind %q is not registered", label, sourceText)
	}
	query, err := decodeJSONStringValue(fields["query"], label+" query")
	if err != nil {
		return domainagent.MioRetrievalRequest{}, err
	}
	queryRunes := utf8.RuneCountInString(query)
	if strings.TrimSpace(query) == "" || queryRunes < 1 || queryRunes > maxMioRetrievalQueryRunes {
		return domainagent.MioRetrievalRequest{}, fmt.Errorf("%s query must contain 1-%d Unicode runes", label, maxMioRetrievalQueryRunes)
	}
	informationNeeded, err := decodeJSONStringValue(fields["information_needed"], label+" information_needed")
	if err != nil {
		return domainagent.MioRetrievalRequest{}, err
	}
	informationNeededRunes := utf8.RuneCountInString(informationNeeded)
	if strings.TrimSpace(informationNeeded) == "" || informationNeededRunes < 1 || informationNeededRunes > maxMioRetrievalNeedRunes {
		return domainagent.MioRetrievalRequest{}, fmt.Errorf("%s information_needed must contain 1-%d Unicode runes", label, maxMioRetrievalNeedRunes)
	}
	referenceValues, err := decodeJSONRawArray(fields["references"], label+" references", maxMioRetrievalReferences)
	if err != nil {
		return domainagent.MioRetrievalRequest{}, err
	}
	references := make([]string, 0, len(referenceValues))
	for referenceIndex, referenceValue := range referenceValues {
		reference, err := decodeJSONStringValue(referenceValue, fmt.Sprintf("%s references[%d]", label, referenceIndex))
		if err != nil {
			return domainagent.MioRetrievalRequest{}, err
		}
		if strings.TrimSpace(reference) == "" {
			return domainagent.MioRetrievalRequest{}, fmt.Errorf("%s references[%d] must not be empty", label, referenceIndex)
		}
		references = append(references, reference)
	}
	return domainagent.MioRetrievalRequest{
		SourceKind:        sourceKind,
		Query:             query,
		References:        references,
		InformationNeeded: informationNeeded,
	}, nil
}

func decodeStrictJSONObject(raw []byte, label string, allowedFields ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s object: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	fields := make(map[string]json.RawMessage, len(allowedFields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode %s field: %w", label, err)
		}
		field, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s field name must be a string", label)
		}
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("%s contains unknown field %q", label, field)
		}
		if _, duplicate := fields[field]; duplicate {
			return nil, fmt.Errorf("%s repeats field %q", label, field)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s field %q: %w", label, field, err)
		}
		fields[field] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s object end: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("%s has invalid object end", label)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s contains trailing JSON", label)
		}
		return nil, fmt.Errorf("%s has trailing content: %w", label, err)
	}
	return fields, nil
}

func requireJSONFields(fields map[string]json.RawMessage, required ...string) error {
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("is missing required field %q", field)
		}
	}
	return nil
}

func allowJSONFields(fields map[string]json.RawMessage, allowedFields ...string) error {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("contains field %q from another branch", field)
		}
	}
	return nil
}

func decodeRequiredJSONString(fields map[string]json.RawMessage, field, label string) (string, error) {
	raw, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("%s is missing required field %q", label, field)
	}
	return decodeJSONStringValue(raw, label+" "+field)
}

func decodeJSONStringValue(raw json.RawMessage, label string) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("%s must be a JSON string", label)
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", fmt.Errorf("decode %s: %w", label, err)
	}
	return value, nil
}

func decodeJSONInt(raw json.RawMessage, label string) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s must be a JSON integer: %w", label, err)
	}
	return value, nil
}

func decodeJSONRawArray(raw json.RawMessage, label string, maxItems int) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s array: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, fmt.Errorf("%s must be a JSON array", label)
	}
	values := make([]json.RawMessage, 0)
	for decoder.More() {
		if len(values) >= maxItems {
			return nil, fmt.Errorf("%s must contain at most %d items", label, maxItems)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s item: %w", label, err)
		}
		values = append(values, value)
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s array end: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return nil, fmt.Errorf("%s has invalid array end", label)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s contains trailing JSON", label)
		}
		return nil, fmt.Errorf("%s has trailing content: %w", label, err)
	}
	return values, nil
}

const routeReplyJSONContract = `Use the route policy above without changing its criteria. Return exactly one JSON object with the string fields "route" and "reply". The route field must contain exactly one canonical route name allowed by the route policy above; do not infer the route from the reply text. If the selected route is CHAT, write Mio's direct response in reply using the supplied persona and conversation context. For every other route, reply must be empty. Do not call tools, perform searches, or claim tool-dependent work in this response.`

type routeReplyJSON struct {
	Route string
	Reply string
}

const maxRouteReplyJSONBytes = 64 * 1024

func decodeRouteReply(content string) (routeReplyJSON, error) {
	if len(content) > maxRouteReplyJSONBytes {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response exceeds %d bytes", maxRouteReplyJSONBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return routeReplyJSON{}, fmt.Errorf("decode combined route/reply JSON: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response must be one JSON object")
	}
	var result routeReplyJSON
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return routeReplyJSON{}, fmt.Errorf("decode combined route/reply field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply field name must be a string")
		}
		if seen[key] {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response repeats field %q", key)
		}
		seen[key] = true
		switch key {
		case "route":
			result.Route, err = decodeJSONString(decoder)
		case "reply":
			result.Reply, err = decodeJSONString(decoder)
		default:
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response contains unknown field %q", key)
		}
		if err != nil {
			return routeReplyJSON{}, fmt.Errorf("decode combined route/reply field %q: %w", key, err)
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return routeReplyJSON{}, fmt.Errorf("decode combined route/reply object end: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response has invalid object end")
	}
	if !seen["route"] || !seen["reply"] {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response must include route and reply fields")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response contains trailing JSON")
		}
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response has trailing content: %w", err)
	}
	return result, nil
}

func decodeJSONString(decoder *json.Decoder) (string, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("value must be a JSON string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func ensureNoThinking(req *llm.GenerateRequest) {
	options := make(map[string]any, len(req.ProviderOptions)+2)
	for key, value := range req.ProviderOptions {
		options[key] = value
	}
	options["think"] = false
	kwargs := make(map[string]any, 1)
	if existing, ok := options["chat_template_kwargs"].(map[string]any); ok {
		for key, value := range existing {
			kwargs[key] = value
		}
	}
	kwargs["enable_thinking"] = false
	options["chat_template_kwargs"] = kwargs
	req.ProviderOptions = options
}

// Classify はタスクを分類
func (c *LLMClassifier) Classify(ctx context.Context, t conversation.TurnInput) (routing.Decision, error) {
	userMessage := fmt.Sprintf("ユーザーからのメッセージ: %s", t.MessageText())

	req := llm.GenerateRequest{
		SystemPrompt: c.systemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   classifierMaxTokens,
		Temperature: 0.3, // 低温度で安定した分類
		ProviderOptions: map[string]any{
			"think": false,
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		},
	}
	if admission := domainagent.MioTurnGenerateRequestAdmissionFromContext(ctx); admission != nil {
		req.RequestAdmission = admission
	}

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return routing.Decision{}, fmt.Errorf("LLM classification failed: %w", err)
	}

	// LLM応答からルートを抽出
	route, matched := c.parseRouteWithMatch(resp.Content)
	confidence := c.calculateConfidence(route, resp.Content)
	reason := fmt.Sprintf("LLM classified as %s", route)
	source := routing.EvidenceSourceClassifier
	if !matched {
		source = routing.EvidenceSourceSafeFallback
		reason = "LLM classification fallback to CHAT"
	}

	return routing.NewDecisionWithEvidence(route, confidence, reason, routing.DecisionEvidence{
		Source:     source,
		Matched:    true,
		Route:      route,
		Confidence: confidence,
		Reason:     reason,
	}), nil
}

// parseRoute はLLM応答からルートを抽出
func (c *LLMClassifier) parseRoute(response string) routing.Route {
	route, _ := c.parseRouteWithMatch(response)
	return route
}

func (c *LLMClassifier) parseRouteWithMatch(response string) (routing.Route, bool) {
	// レスポンスをトリムして大文字化
	trimmed := strings.TrimSpace(response)
	upper := strings.ToUpper(trimmed)

	// 長いルート名から順にチェック（CODE3 を CODE より先に判定）
	validRoutes := []struct {
		key   string
		route routing.Route
	}{
		{"CODE4", routing.RouteCODE4},
		{"CODE3", routing.RouteCODE3},
		{"CODE2", routing.RouteCODE2},
		{"CODE1", routing.RouteCODE1},
		{"CODE", routing.RouteCODE},
		{"ANALYZE", routing.RouteANALYZE},
		{"RESEARCH", routing.RouteRESEARCH},
		{"WILD", routing.RouteWILD},
		{"PLAN", routing.RoutePLAN},
		{"CHAT", routing.RouteCHAT},
		{"OPS", routing.RouteOPS},
	}

	for _, vr := range validRoutes {
		if strings.Contains(upper, vr.key) {
			return vr.route, true
		}
	}

	// 無効なルート名の場合はCHATにフォールバック
	return routing.RouteCHAT, false
}

// calculateConfidence は信頼度を計算
func (c *LLMClassifier) calculateConfidence(route routing.Route, response string) float64 {
	// CHATへのフォールバックは低信頼度
	if route == routing.RouteCHAT && !strings.Contains(strings.ToUpper(response), "CHAT") {
		return 0.4
	}

	// 明確なマッチは高信頼度
	return 0.7
}
