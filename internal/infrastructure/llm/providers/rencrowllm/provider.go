package rencrowllm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

const defaultBaseURL = "http://127.0.0.1:8090"

const gatewayRequestWriteDiagnosticCapacity = 64

type gatewayRequestWriteRecord struct {
	requestID         string
	doStartedAtUTC    time.Time
	wroteRequestAtUTC time.Time
	doToWriteNS       int64
	wroteRequest      bool
}

// GatewayRequestWriteRecorder keeps a bounded, process-local set of HTTP
// request-write observations. Multiple providers may retain separate
// recorders so diagnostics do not change their clients or connection pools.
type GatewayRequestWriteRecorder struct {
	mu      sync.Mutex
	samples [gatewayRequestWriteDiagnosticCapacity]gatewayRequestWriteRecord
	length  int
	dropped uint64
}

func NewGatewayRequestWriteRecorder() *GatewayRequestWriteRecorder {
	return &GatewayRequestWriteRecorder{}
}

// GatewayError preserves the machine-readable retry contract returned by
// RenCrow_LLM without exposing backend- or model-specific errors to CORE.
type GatewayError struct {
	Status   int
	Code     string
	Message  string
	CanRetry bool
}

func (e *GatewayError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("gateway stream error %s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("gateway API error: status=%d code=%s message=%s", e.Status, e.Code, e.Message)
}

func (e *GatewayError) Retryable() bool { return e != nil && e.CanRetry }

// GatewayProvider はRenCrow_LLM Gateway APIプロバイダーの実装
type GatewayProvider struct {
	apiKey           string
	model            string
	baseURL          string
	thinkingBridge   bool
	modelContext     int
	client           *http.Client
	agentID          string
	executionRole    string
	executionAlias   string
	writeDiagnostics *GatewayRequestWriteRecorder
}

// WithRenCrowExecution adds the explicit Agent/Role meaning of a Gateway
// execution alias. Physical RenCrow_LLM Gateway-compatible targets never receive this
// metadata because RenCrow_LLM removes the rencrow field before proxying.
func (p *GatewayProvider) WithRenCrowExecution(agentID, executionRole, executionAlias string) *GatewayProvider {
	p.agentID = strings.TrimSpace(agentID)
	p.executionRole = strings.ToLower(strings.TrimSpace(executionRole))
	p.executionAlias = strings.TrimSpace(executionAlias)
	return p
}

// WithGatewayRequestWriteRecorder assigns the bounded RAM-only recorder used
// for opt-in body-generation request-write observations.
func (p *GatewayProvider) WithGatewayRequestWriteRecorder(recorder *GatewayRequestWriteRecorder) *GatewayProvider {
	if p != nil && recorder != nil {
		p.writeDiagnostics = recorder
	}
	return p
}

// NewGatewayProvider は新しいGatewayProviderを作成
func NewGatewayProvider(apiKey, model string) *GatewayProvider {
	return NewGatewayProviderWithOptions(apiKey, model, defaultBaseURL, 120*time.Second)
}

// NewGatewayProviderWithOptions creates an RenCrow_LLM Gateway-compatible provider with custom endpoint and timeout.
func NewGatewayProviderWithOptions(apiKey, model, baseURL string, timeout time.Duration) *GatewayProvider {
	return NewGatewayProviderWithModelContext(apiKey, model, baseURL, timeout, 0)
}

// NewGatewayProviderWithModelContext creates an RenCrow_LLM Gateway-compatible provider with a default
// Gateway model context option.
func NewGatewayProviderWithModelContext(apiKey, model, baseURL string, timeout time.Duration, modelContext int) *GatewayProvider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	provider := &GatewayProvider{
		apiKey:         apiKey,
		model:          model,
		baseURL:        baseURL,
		thinkingBridge: true,
		modelContext:   modelContext,
		client: &http.Client{
			Timeout: timeout,
		},
	}
	if agentID, executionRole, ok := canonicalExecutionForAlias(model); ok {
		provider.WithRenCrowExecution(agentID, executionRole, strings.TrimSpace(model))
	}
	return provider
}

func canonicalExecutionForAlias(alias string) (agentID, executionRole string, ok bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(alias)); normalized {
	case "mio":
		return "mio", "chat", true
	case "shiro":
		return "shiro", "chatworker", true
	case "worker":
		return "shiro", "worker", true
	case "midori":
		return "midori", "wild", true
	case "kuro":
		return "kuro", "heavy", true
	case "coder1", "coder2", "coder3", "coder4":
		return normalized, "coder", true
	default:
		return "", "", false
	}
}

// SetBaseURL はベースURLを設定（テスト用）
func (p *GatewayProvider) SetBaseURL(url string) {
	p.baseURL = url
}

// Generate はLLM生成を実行
func (p *GatewayProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	logicalOutcomeStreaming := req.LogicalOutcomeStream != ""
	if logicalOutcomeStreaming && req.LogicalOutcomeStream != llm.LogicalOutcomeStreamV1 {
		return llm.GenerateResponse{}, fmt.Errorf("unsupported logical outcome stream version %q", req.LogicalOutcomeStream)
	}
	if logicalOutcomeStreaming && req.OnLogicalOutcomeEvent == nil {
		return llm.GenerateResponse{}, fmt.Errorf("logical outcome stream requires an event callback")
	}
	if !logicalOutcomeStreaming && req.OnLogicalOutcomeEvent != nil {
		return llm.GenerateResponse{}, fmt.Errorf("logical outcome event callback requires an explicit stream version")
	}
	streaming := req.OnToken != nil || logicalOutcomeStreaming

	// RenCrow_LLM Gateway APIリクエスト構築
	gatewayReq := map[string]interface{}{
		"model":    p.model,
		"messages": p.convertMessages(req),
	}
	p.addThinkingBridgeFields(gatewayReq, streaming)
	p.addProviderOptions(gatewayReq, req.ProviderOptions)
	p.addModelContextOption(gatewayReq)
	p.addRenCrowExecutionMetadata(ctx, gatewayReq, promptContextBlockMetadata(req))
	if logicalOutcomeStreaming {
		metadata, _ := gatewayReq["rencrow"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
			gatewayReq["rencrow"] = metadata
		}
		metadata["logical_outcome_stream"] = req.LogicalOutcomeStream
		gatewayReq["stream"] = true
	}
	if err := addResponseFormat(gatewayReq, req.ResponseFormat); err != nil {
		return llm.GenerateResponse{}, err
	}
	if err := addReasoningEffort(gatewayReq, req.ReasoningEffort); err != nil {
		return llm.GenerateResponse{}, err
	}
	if streaming && !logicalOutcomeStreaming {
		gatewayReq["stream_options"] = map[string]any{"include_usage": true}
	}

	// MaxTokens（RenCrow_LLM Gatewayではmax_tokens）
	if req.MaxTokens > 0 {
		gatewayReq["max_tokens"] = req.MaxTokens
	}

	// Temperature
	if req.Temperature > 0 {
		gatewayReq["temperature"] = req.Temperature
	}

	requestID, receiptOwner, err := beginLLMTransport(ctx, gatewayReq, "llm.generate")
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	reqBody, err := json.Marshal(gatewayReq)
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.GenerateResponse{}, receiptErr
		}
		return llm.GenerateResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// HTTPリクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.GenerateResponse{}, receiptErr
		}
		return llm.GenerateResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-RenCrow-Request-ID", string(requestID))
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	if req.RequestAdmission != nil {
		if err := req.RequestAdmission.Admit(); err != nil {
			if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
				return llm.GenerateResponse{}, receiptErr
			}
			return llm.GenerateResponse{}, fmt.Errorf("generation request admission rejected: %w", err)
		}
	}

	// リクエスト実行
	turntiming.MarkGatewayRequestStarted(ctx)
	resp, err := p.doGenerateRequest(httpReq)
	if resp != nil {
		turntiming.MarkGatewayResponseHeaders(ctx)
	}
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.GenerateResponse{}, receiptErr
		}
		return llm.GenerateResponse{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()
	responseID, err := completeLLMTransport(ctx, receiptOwner, requestID, providerExternalRef(resp))
	if err != nil {
		return llm.GenerateResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.GenerateResponse{}, decodeGatewayError(resp.StatusCode, body)
	}

	if streaming {
		var result llm.GenerateResponse
		if logicalOutcomeStreaming {
			result, err = p.readLogicalOutcomeStream(resp.Body, req.OnToken, req.OnLogicalOutcomeEvent)
		} else {
			result, err = p.readChatCompletionsStream(resp.Body, req.OnToken)
		}
		result.RequestID = requestID
		result.ResponseID = responseID
		return result, err
	}

	// レスポンスパース
	var gatewayResp struct {
		Choices []struct {
			Message struct {
				Role        string `json:"role"`
				Content     string `json:"content"`
				ParseStatus string `json:"parse_status"`
				ParserName  string `json:"parser_name"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&gatewayResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// コンテンツ抽出
	var content string
	var finishReason string
	if len(gatewayResp.Choices) > 0 {
		msg := gatewayResp.Choices[0].Message
		content = p.sanitizeThinkingBridgeContent(msg.Content, msg.ParseStatus, msg.ParserName)
		finishReason = gatewayResp.Choices[0].FinishReason
	}

	return llm.GenerateResponse{
		Content:      content,
		TokensUsed:   gatewayResp.Usage.TotalTokens,
		FinishReason: finishReason,
		RequestID:    requestID,
		ResponseID:   responseID,
	}, nil
}

func (p *GatewayProvider) readLogicalOutcomeStream(body io.Reader, onToken llm.StreamCallback, onEvent llm.LogicalOutcomeEventCallback) (llm.GenerateResponse, error) {
	type outcomeBranch uint8
	const (
		noOutcomeBranch outcomeBranch = iota
		answerOutcomeBranch
		retrievalOutcomeBranch
		handoffOutcomeBranch
	)

	branch := noOutcomeBranch
	answerDone := false
	sawDone := false
	var content strings.Builder
	finishReason := ""
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var eventName string
	eventNameSet := false
	var data strings.Builder
	dataSet := false
	processFrame := func() error {
		if !eventNameSet && !dataSet {
			return nil
		}
		name := eventName
		hasData := dataSet
		payload := []byte(data.String())
		eventName = ""
		eventNameSet = false
		data.Reset()
		dataSet = false

		if sawDone {
			return fmt.Errorf("logical outcome stream contains data after [DONE]")
		}
		if !hasData {
			return fmt.Errorf("logical outcome SSE frame has no data field")
		}
		if !utf8.Valid(payload) {
			return fmt.Errorf("logical outcome SSE payload is not valid UTF-8")
		}

		if strings.TrimSpace(string(payload)) == "[DONE]" {
			if name != "" {
				return fmt.Errorf("logical outcome [DONE] must not have an event name")
			}
			switch branch {
			case answerOutcomeBranch:
				if !answerDone {
					return fmt.Errorf("logical outcome answer ended before answer_done")
				}
			case retrievalOutcomeBranch:
			case handoffOutcomeBranch:
			case noOutcomeBranch:
				return fmt.Errorf("logical outcome stream ended without an outcome")
			}
			sawDone = true
			return nil
		}

		if gatewayErr, isError, err := decodeLogicalOutcomeGatewayError(name, payload); err != nil {
			return err
		} else if isError {
			return gatewayErr
		}

		if name == "" {
			return fmt.Errorf("logical outcome SSE data has no event name")
		}
		switch llm.LogicalOutcomeEventKind(name) {
		case llm.LogicalOutcomeAnswerStart:
			if branch != noOutcomeBranch {
				return fmt.Errorf("logical outcome stream contains a contradictory or duplicate answer_start")
			}
			var wire struct {
				SchemaVersion *int    `json:"schema_version"`
				Route         *string `json:"route"`
				PayloadKind   *string `json:"payload_kind"`
			}
			if err := decodeLogicalOutcomePayload(payload, &wire); err != nil {
				return fmt.Errorf("decode answer_start: %w", err)
			}
			if wire.SchemaVersion == nil || *wire.SchemaVersion != 2 || wire.Route == nil || strings.TrimSpace(*wire.Route) == "" || *wire.Route != strings.TrimSpace(*wire.Route) || wire.PayloadKind == nil || *wire.PayloadKind != "text" {
				return fmt.Errorf("answer_start has an invalid schema_version, route, or payload_kind")
			}
			branch = answerOutcomeBranch
			event := llm.LogicalOutcomeEvent{
				Kind:          llm.LogicalOutcomeAnswerStart,
				SchemaVersion: *wire.SchemaVersion,
				Route:         *wire.Route,
				PayloadKind:   *wire.PayloadKind,
			}
			if err := onEvent(event); err != nil {
				return fmt.Errorf("logical outcome callback rejected %s: %w", event.Kind, err)
			}
		case llm.LogicalOutcomeAnswerDelta:
			if branch != answerOutcomeBranch || answerDone {
				return fmt.Errorf("answer_delta is outside the answer branch")
			}
			var wire struct {
				Text *string `json:"text"`
			}
			if err := decodeLogicalOutcomePayload(payload, &wire); err != nil {
				return fmt.Errorf("decode answer_delta: %w", err)
			}
			if wire.Text == nil {
				return fmt.Errorf("answer_delta is missing text")
			}
			event := llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDelta, Text: *wire.Text}
			if err := onEvent(event); err != nil {
				return fmt.Errorf("logical outcome callback rejected %s: %w", event.Kind, err)
			}
			content.WriteString(event.Text)
			if event.Text != "" && onToken != nil {
				onToken(event.Text)
			}
		case llm.LogicalOutcomeAnswerDone:
			if branch != answerOutcomeBranch || answerDone {
				return fmt.Errorf("answer_done is outside the active answer branch")
			}
			var wire struct {
				FinishReason *string `json:"finish_reason"`
			}
			if err := decodeLogicalOutcomePayload(payload, &wire); err != nil {
				return fmt.Errorf("decode answer_done: %w", err)
			}
			if wire.FinishReason == nil || *wire.FinishReason != "stop" || strings.TrimSpace(content.String()) == "" {
				return fmt.Errorf("answer_done has an invalid finish_reason or empty answer")
			}
			answerDone = true
			finishReason = *wire.FinishReason
			event := llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: *wire.FinishReason}
			if err := onEvent(event); err != nil {
				return fmt.Errorf("logical outcome callback rejected %s: %w", event.Kind, err)
			}
		case llm.LogicalOutcomeRetrievalRequest:
			if branch != noOutcomeBranch {
				return fmt.Errorf("logical outcome stream contains a contradictory or duplicate retrieval_request")
			}
			var wire struct {
				SchemaVersion *int            `json:"schema_version"`
				Route         *string         `json:"route"`
				Requests      json.RawMessage `json:"requests"`
			}
			if err := decodeLogicalOutcomePayload(payload, &wire); err != nil {
				return fmt.Errorf("decode retrieval_request: %w", err)
			}
			if wire.SchemaVersion == nil || *wire.SchemaVersion != 2 || wire.Route == nil || strings.TrimSpace(*wire.Route) == "" || *wire.Route != strings.TrimSpace(*wire.Route) {
				return fmt.Errorf("retrieval_request has an invalid schema_version or route")
			}
			trimmedRequests := bytes.TrimSpace(wire.Requests)
			if len(trimmedRequests) == 0 || trimmedRequests[0] != '[' || !json.Valid(trimmedRequests) {
				return fmt.Errorf("retrieval_request requests must be a JSON array")
			}
			branch = retrievalOutcomeBranch
			event := llm.LogicalOutcomeEvent{
				Kind:              llm.LogicalOutcomeRetrievalRequest,
				SchemaVersion:     *wire.SchemaVersion,
				Route:             *wire.Route,
				RetrievalRequests: append(json.RawMessage(nil), trimmedRequests...),
			}
			if err := onEvent(event); err != nil {
				return fmt.Errorf("logical outcome callback rejected %s: %w", event.Kind, err)
			}
		case llm.LogicalOutcomeHandoff:
			if branch != noOutcomeBranch {
				return fmt.Errorf("logical outcome stream contains a contradictory or duplicate handoff")
			}
			var wire struct {
				SchemaVersion *int    `json:"schema_version"`
				Route         *string `json:"route"`
			}
			if err := decodeLogicalOutcomePayload(payload, &wire); err != nil {
				return fmt.Errorf("decode handoff: %w", err)
			}
			if wire.SchemaVersion == nil || *wire.SchemaVersion != 2 || wire.Route == nil || strings.TrimSpace(*wire.Route) == "" || *wire.Route != strings.TrimSpace(*wire.Route) {
				return fmt.Errorf("handoff has an invalid schema_version or route")
			}
			branch = handoffOutcomeBranch
			event := llm.LogicalOutcomeEvent{
				Kind:          llm.LogicalOutcomeHandoff,
				SchemaVersion: *wire.SchemaVersion,
				Route:         *wire.Route,
			}
			if err := onEvent(event); err != nil {
				return fmt.Errorf("logical outcome callback rejected %s: %w", event.Kind, err)
			}
		default:
			return fmt.Errorf("unsupported logical outcome SSE event %q", name)
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := processFrame(); err != nil {
				return llm.GenerateResponse{}, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			value = ""
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			if eventNameSet {
				return llm.GenerateResponse{}, fmt.Errorf("logical outcome SSE frame has duplicate event fields")
			}
			eventName, eventNameSet = value, true
		case "data":
			if dataSet {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			dataSet = true
		default:
			return llm.GenerateResponse{}, fmt.Errorf("logical outcome SSE frame has unsupported field %q", field)
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("read logical outcome stream: %w", err)
	}
	if eventNameSet || dataSet {
		return llm.GenerateResponse{}, fmt.Errorf("logical outcome SSE stream ended with an unterminated event")
	}
	if !sawDone {
		return llm.GenerateResponse{}, fmt.Errorf("logical outcome stream ended without [DONE]")
	}
	return llm.GenerateResponse{Content: content.String(), FinishReason: finishReason}, nil
}

func decodeLogicalOutcomePayload(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeLogicalOutcomeGatewayError(eventName string, data []byte) (*GatewayError, bool, error) {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		if eventName == "error" {
			return nil, false, fmt.Errorf("decode typed logical outcome error: %w", err)
		}
		return nil, false, nil
	}
	if len(envelope.Error) == 0 || string(envelope.Error) == "null" {
		if eventName == "error" {
			return nil, false, fmt.Errorf("typed logical outcome error is missing error details")
		}
		return nil, false, nil
	}
	var details struct {
		Code      *string `json:"code"`
		Message   *string `json:"message"`
		Retryable *bool   `json:"retryable"`
	}
	if err := json.Unmarshal(envelope.Error, &details); err != nil {
		return nil, false, fmt.Errorf("decode typed logical outcome error: %w", err)
	}
	if details.Code == nil || strings.TrimSpace(*details.Code) == "" || details.Message == nil || strings.TrimSpace(*details.Message) == "" || details.Retryable == nil {
		return nil, false, fmt.Errorf("typed logical outcome error is missing code, message, or retryable")
	}
	return &GatewayError{Code: strings.TrimSpace(*details.Code), Message: strings.TrimSpace(*details.Message), CanRetry: *details.Retryable}, true, nil
}

func (p *GatewayProvider) doGenerateRequest(req *http.Request) (*http.Response, error) {
	if !turntiming.IsBodyGenerationContext(req.Context()) {
		return p.client.Do(req)
	}

	doStartedAt := time.Now()
	requestID := strings.TrimSpace(req.Header.Get("X-RenCrow-Request-ID"))
	sampleIndex := p.beginGatewayRequestWriteDiagnostic(requestID, doStartedAt)
	if sampleIndex < 0 {
		return p.client.Do(req)
	}
	var wroteOnce sync.Once
	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil || requestID == "" {
				return
			}
			wroteOnce.Do(func() {
				wroteAt := time.Now()
				p.completeGatewayRequestWriteDiagnostic(sampleIndex, wroteAt, wroteAt.Sub(doStartedAt))
			})
		},
	}
	tracedReq := req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	return p.client.Do(tracedReq)
}

func (p *GatewayProvider) beginGatewayRequestWriteDiagnostic(requestID string, doStartedAt time.Time) int {
	if p == nil || p.writeDiagnostics == nil || requestID == "" {
		return -1
	}
	return p.writeDiagnostics.begin(requestID, doStartedAt)
}

func (r *GatewayRequestWriteRecorder) begin(requestID string, doStartedAt time.Time) int {
	if r == nil || requestID == "" {
		return -1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.length == len(r.samples) {
		r.dropped++
		return -1
	}
	index := r.length
	r.samples[index] = gatewayRequestWriteRecord{
		requestID:      requestID,
		doStartedAtUTC: doStartedAt.UTC(),
	}
	r.length++
	return index
}

func (p *GatewayProvider) completeGatewayRequestWriteDiagnostic(index int, wroteRequestAt time.Time, doToWrite time.Duration) {
	if p == nil || p.writeDiagnostics == nil {
		return
	}
	p.writeDiagnostics.complete(index, wroteRequestAt, doToWrite)
}

func (r *GatewayRequestWriteRecorder) complete(index int, wroteRequestAt time.Time, doToWrite time.Duration) {
	if r == nil || index < 0 || index >= gatewayRequestWriteDiagnosticCapacity {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if index >= r.length {
		return
	}
	record := &r.samples[index]
	record.wroteRequestAtUTC = wroteRequestAt.UTC()
	record.doToWriteNS = doToWrite.Nanoseconds()
	record.wroteRequest = true
}

// GatewayRequestWriteDiagnostics returns the current process-local bounded
// sample set. It exposes no request body, response body, or authorization data.
func (p *GatewayProvider) GatewayRequestWriteDiagnostics() *modulellm.GatewayRequestWriteDiagnosticsSnapshot {
	if p == nil || p.writeDiagnostics == nil {
		return nil
	}
	return p.writeDiagnostics.GatewayRequestWriteDiagnostics()
}

// GatewayRequestWriteDiagnostics returns a copy of this recorder's sample set.
func (r *GatewayRequestWriteRecorder) GatewayRequestWriteDiagnostics() *modulellm.GatewayRequestWriteDiagnosticsSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.length == 0 && r.dropped == 0 {
		return nil
	}

	requests := make([]modulellm.GatewayRequestWriteSample, 0, r.length)
	for i := 0; i < r.length; i++ {
		record := r.samples[i]
		sample := modulellm.GatewayRequestWriteSample{
			RequestID:      record.requestID,
			DoStartedAtUTC: record.doStartedAtUTC.Format(time.RFC3339Nano),
		}
		if record.wroteRequest {
			durationNS := record.doToWriteNS
			sample.WroteRequestAtUTC = record.wroteRequestAtUTC.Format(time.RFC3339Nano)
			sample.DoToWriteDurationNS = &durationNS
		}
		requests = append(requests, sample)
	}
	return &modulellm.GatewayRequestWriteDiagnosticsSnapshot{
		Capacity: len(r.samples),
		Dropped:  r.dropped,
		Requests: requests,
	}
}

func decodeGatewayError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		code, message, retryable := envelope.Code, envelope.Message, envelope.Retryable
		if envelope.Error.Code != "" || envelope.Error.Message != "" {
			code, message, retryable = envelope.Error.Code, envelope.Error.Message, envelope.Error.Retryable
		}
		return &GatewayError{Status: status, Code: strings.TrimSpace(code), Message: strings.TrimSpace(message), CanRetry: retryable}
	}
	return &GatewayError{Status: status, Message: http.StatusText(status), CanRetry: status >= 500}
}

func addResponseFormat(payload map[string]interface{}, format llm.ResponseFormat) error {
	switch format {
	case llm.ResponseFormatText:
		return nil
	case llm.ResponseFormatJSONObject:
		payload["response_format"] = map[string]any{"type": string(format)}
		return nil
	default:
		return fmt.Errorf("unsupported response format %q", format)
	}
}

// Name はプロバイダー名を返す
func (p *GatewayProvider) Name() string {
	return fmt.Sprintf("rencrow_llm-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（RenCrow_LLM Gateway /v1/chat/completions + tools）
func (p *GatewayProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := p.convertChatMessages(req.Messages)

	gatewayReq := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	p.addThinkingBridgeFields(gatewayReq, false)
	if err := addReasoningEffort(gatewayReq, req.ReasoningEffort); err != nil {
		return llm.ChatResponse{}, err
	}
	p.addModelContextOption(gatewayReq)
	p.addRenCrowExecutionMetadata(ctx, gatewayReq, promptContextBlockMetadataFromChat(req))
	if req.MaxTokens > 0 {
		gatewayReq["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, td := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        td.Function.Name,
					"description": td.Function.Description,
					"parameters":  td.Function.Parameters,
				},
			})
		}
		gatewayReq["tools"] = tools
	}
	if req.Temperature > 0 {
		gatewayReq["temperature"] = req.Temperature
	}

	requestID, receiptOwner, err := beginLLMTransport(ctx, gatewayReq, "llm.chat")
	if err != nil {
		return llm.ChatResponse{}, err
	}
	reqBody, err := json.Marshal(gatewayReq)
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.ChatResponse{}, receiptErr
		}
		return llm.ChatResponse{}, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.ChatResponse{}, receiptErr
		}
		return llm.ChatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-RenCrow-Request-ID", string(requestID))
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if receiptErr := failLLMTransport(ctx, receiptOwner, requestID, err); receiptErr != nil {
			return llm.ChatResponse{}, receiptErr
		}
		return llm.ChatResponse{}, fmt.Errorf("failed to execute chat request: %w", err)
	}
	defer resp.Body.Close()
	responseID, err := completeLLMTransport(ctx, receiptOwner, requestID, providerExternalRef(resp))
	if err != nil {
		return llm.ChatResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.ChatResponse{}, fmt.Errorf("gateway chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	result, err := readToolChatCompletionsStream(resp.Body)
	result.RequestID = requestID
	result.ResponseID = responseID
	return result, err
}

func beginLLMTransport(ctx context.Context, payload map[string]interface{}, operation string) (modulecore.RequestID, domaintransport.ReceiptOwner, error) {
	owner, owned := domaintransport.ReceiptOwnerFromContext(ctx)
	requestID := modulecore.NewRequestID()
	if owned {
		request, err := owner.BeginRequest(ctx, operation)
		if err != nil {
			return "", nil, fmt.Errorf("begin %s transport request: %w", operation, err)
		}
		requestID = request.RequestID
	}
	metadata, _ := payload["rencrow"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		payload["rencrow"] = metadata
	}
	metadata["request_id"] = string(requestID)
	return requestID, owner, nil
}

func completeLLMTransport(ctx context.Context, owner domaintransport.ReceiptOwner, requestID modulecore.RequestID, externalRef string) (modulecore.ResponseID, error) {
	if owner == nil {
		return modulecore.NewResponseID(), nil
	}
	response, err := owner.CompleteResponse(context.WithoutCancel(ctx), requestID, externalRef)
	if err != nil {
		return "", fmt.Errorf("persist LLM transport response: %w", err)
	}
	return response.ResponseID, nil
}

func failLLMTransport(ctx context.Context, owner domaintransport.ReceiptOwner, requestID modulecore.RequestID, cause error) error {
	if owner == nil {
		return nil
	}
	if err := owner.FailWithoutResponse(context.WithoutCancel(ctx), requestID, cause.Error()); err != nil {
		return fmt.Errorf("persist LLM transport failure: %w", err)
	}
	return nil
}

func providerExternalRef(response *http.Response) string {
	if response == nil {
		return ""
	}
	for _, key := range []string{"X-Provider-Request-ID", "X-Request-ID", "Request-ID"} {
		if value := strings.TrimSpace(response.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func addReasoningEffort(payload map[string]interface{}, effort llm.ReasoningEffort) error {
	switch effort {
	case llm.ReasoningEffortUnspecified:
		return nil
	case llm.ReasoningEffortLow:
		payload["think"] = "low"
		payload["reasoning_effort"] = "low"
		kwargs, _ := payload["chat_template_kwargs"].(map[string]any)
		if kwargs == nil {
			kwargs = map[string]any{}
			payload["chat_template_kwargs"] = kwargs
		}
		kwargs["enable_thinking"] = true
		kwargs["reasoning_effort"] = "low"
		return nil
	default:
		return fmt.Errorf("unsupported reasoning effort %q", effort)
	}
}

func (p *GatewayProvider) addRenCrowExecutionMetadata(ctx context.Context, payload map[string]interface{}, blocks []map[string]any) {
	metadata := map[string]any{}
	addNonEmptyMetadata(metadata, "agent_id", p.agentID)
	addNonEmptyMetadata(metadata, "execution_role", p.executionRole)
	addNonEmptyMetadata(metadata, "execution_alias", p.executionAlias)
	observationCtx := llm.WithExecutionObservationDefaults(ctx, llm.ExecutionObservation{
		Initiator: p.agentID,
		Caller:    "core.unattributed",
		Purpose:   "unattributed",
	})
	observation, _ := llm.ExecutionObservationFromContext(observationCtx)
	addNonEmptyMetadata(metadata, "trace_id", observation.TraceID)
	addNonEmptyMetadata(metadata, "task_id", string(observation.TaskID))
	addNonEmptyMetadata(metadata, "session_id", observation.SessionID)
	addNonEmptyMetadata(metadata, "initiator", observation.Initiator)
	addNonEmptyMetadata(metadata, "caller", observation.Caller)
	addNonEmptyMetadata(metadata, "purpose", observation.Purpose)
	if len(blocks) > 0 {
		metadata["prompt_context_blocks"] = blocks
	}
	if len(metadata) == 0 {
		return
	}
	payload["rencrow"] = metadata
}

func promptContextBlockMetadataFromChat(request llm.ChatRequest) []map[string]any {
	blocks := make([]map[string]any, 0, len(request.Messages))
	for messageIndex, message := range request.Messages {
		if message.Type == "" && len(message.Metadata) == 0 {
			continue
		}
		block := map[string]any{"message_index": messageIndex, "type": string(message.Type)}
		for key, value := range message.Metadata {
			block[key] = value
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func promptContextBlockMetadata(request llm.GenerateRequest) []map[string]any {
	blocks := make([]map[string]any, 0, len(request.Messages)+1)
	messageIndex := 0
	if request.SystemPrompt != "" {
		// convertMessages は SystemPrompt を index 0 の system へ変換する。固定の指示なので
		// Stable として申告する。申告が無いと Gateway は instruction_without_block で
		// fallback し、時刻などの Variable を先頭 system へ畳み込んで先頭固定が崩れる。
		blocks = append(blocks, map[string]any{"message_index": messageIndex, "type": string(llm.PromptContextStable)})
		messageIndex++
	}
	for _, message := range request.Messages {
		if message.Type != "" || len(message.Metadata) > 0 {
			block := map[string]any{"message_index": messageIndex, "type": string(message.Type)}
			for key, value := range message.Metadata {
				block[key] = value
			}
			blocks = append(blocks, block)
		}
		messageIndex++
	}
	return blocks
}

func addNonEmptyMetadata(metadata map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = value
	}
}
