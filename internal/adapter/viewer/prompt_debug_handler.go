package viewer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PromptDebugLogOptions configures the read-only Viewer projection of the
// RenCrow LLM prompt-boundary JSONL log.
type PromptDebugLogOptions struct {
	Path       string
	MaxRecords int
}

type promptDebugRecord struct {
	SchemaVersion int            `json:"schema_version"`
	CreatedAt     string         `json:"created_at"`
	Stage         string         `json:"stage"`
	Metadata      map[string]any `json:"metadata"`
	PayloadBytes  int            `json:"payload_bytes"`
	PayloadSHA256 string         `json:"payload_sha256"`
	PayloadText   string         `json:"payload_text"`
}

type promptDebugBlock struct {
	Label     string            `json:"label"`
	Type      string            `json:"type,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Text      string            `json:"text"`
}

type promptDebugItem struct {
	SchemaVersion     int                `json:"schema_version"`
	CreatedAt         string             `json:"created_at"`
	Stage             string             `json:"stage"`
	Level             string             `json:"level"`
	Metadata          map[string]any     `json:"metadata"`
	RequestID         string             `json:"request_id,omitempty"`
	PayloadBytes      int                `json:"payload_bytes"`
	PayloadSHA256     string             `json:"payload_sha256"`
	PayloadText       string             `json:"payload_text"`
	SystemPrompt      string             `json:"system_prompt,omitempty"`
	SystemPromptLines int                `json:"system_prompt_lines,omitempty"`
	SystemPromptBlock []promptDebugBlock `json:"system_prompt_blocks,omitempty"`
}

type promptDebugExchange struct {
	ExchangeID    string            `json:"exchange_id"`
	RequestID     string            `json:"request_id,omitempty"`
	AgentID       string            `json:"agent_id,omitempty"`
	Caller        string            `json:"caller,omitempty"`
	TargetID      string            `json:"target_id,omitempty"`
	ExecutionRole string            `json:"execution_role,omitempty"`
	CreatedAt     string            `json:"created_at"`
	Items         []promptDebugItem `json:"items"`
}

// HandlePromptDebugLogs serves a bounded, read-only view of the prompt
// boundary log. The file path is fixed by configuration/defaults and cannot
// be selected by a request parameter.
func HandlePromptDebugLogs(options PromptDebugLogOptions) http.HandlerFunc {
	maxRecords := options.MaxRecords
	if maxRecords <= 0 || maxRecords > 200 {
		maxRecords = 80
	}
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("RENCROW_LLM_PROMPT_DEBUG_LOG"))
	}
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".rencrow", "logs", "llm_prompt_debug.jsonl")
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, maxRecords)
		if !ok {
			return
		}
		if limit == 0 {
			limit = 40
		}
		items, characterLatest, internalExchanges, err := readPromptDebugProjection(path, limit)
		if err != nil {
			if os.IsNotExist(err) {
				writeMonitorJSON(w, map[string]any{
					"items":              []promptDebugItem{},
					"character_latest":   []promptDebugExchange{},
					"internal_exchanges": []promptDebugExchange{},
					"source":             path,
					"available":          false,
				})
				return
			}
			http.Error(w, "failed to load prompt debug logs", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{
			"items":              items,
			"character_latest":   characterLatest,
			"internal_exchanges": internalExchanges,
			"source":             path,
			"available":          true,
		})
	}
}

func readPromptDebugProjection(path string, limit int) ([]promptDebugItem, []promptDebugExchange, []promptDebugExchange, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer file.Close()

	rows := make([]promptDebugRecord, 0, limit*2)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var row promptDebugRecord
		if json.Unmarshal(scanner.Bytes(), &row) != nil || strings.TrimSpace(row.PayloadText) == "" {
			continue
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, err
	}
	exchanges := buildPromptDebugExchanges(rows)
	sort.SliceStable(exchanges, func(i, j int) bool { return exchanges[i].CreatedAt > exchanges[j].CreatedAt })

	characterLatest := make([]promptDebugExchange, 0, 4)
	internalExchanges := make([]promptDebugExchange, 0, limit)
	seenCharacters := map[string]bool{}
	for _, exchange := range exchanges {
		if isCharacterPromptExchange(exchange) {
			agentID := strings.ToLower(strings.TrimSpace(exchange.AgentID))
			if agentID != "" && !seenCharacters[agentID] {
				seenCharacters[agentID] = true
				characterLatest = append(characterLatest, exchange)
			}
			continue
		}
		if len(internalExchanges) < limit {
			internalExchanges = append(internalExchanges, exchange)
		}
	}

	items := make([]promptDebugItem, 0, limit)
	for _, exchange := range exchanges {
		for _, item := range exchange.Items {
			if len(items) >= limit {
				break
			}
			items = append(items, item)
		}
		if len(items) >= limit {
			break
		}
	}
	return items, characterLatest, internalExchanges, nil
}

func buildPromptDebugExchanges(rows []promptDebugRecord) []promptDebugExchange {
	exchanges := make([]promptDebugExchange, 0, (len(rows)+1)/2)
	openByRequest := map[string]int{}
	for rowIndex, row := range rows {
		item := promptDebugRecordToItem(row)
		requestID := item.RequestID
		if requestID == "" {
			requestID = "request-unknown"
		}
		stage := strings.ToLower(strings.TrimSpace(row.Stage))
		index, found := openByRequest[requestID]
		if stage == "gateway_received" || !found {
			exchanges = append(exchanges, promptDebugExchange{
				ExchangeID:    promptDebugExchangeID(requestID, row.CreatedAt, rowIndex),
				RequestID:     item.RequestID,
				AgentID:       promptDebugMetadataString(row.Metadata, "agent_id"),
				Caller:        promptDebugMetadataString(row.Metadata, "caller"),
				TargetID:      promptDebugMetadataString(row.Metadata, "target_id"),
				ExecutionRole: promptDebugMetadataString(row.Metadata, "execution_role"),
				CreatedAt:     row.CreatedAt,
				Items:         []promptDebugItem{item},
			})
			index = len(exchanges) - 1
			openByRequest[requestID] = index
		} else {
			item = inheritPromptContextBlocks(exchanges[index].Items, item)
			exchanges[index].Items = append(exchanges[index].Items, item)
			if row.CreatedAt > exchanges[index].CreatedAt {
				exchanges[index].CreatedAt = row.CreatedAt
			}
		}
		if stage == "target_sent" || stage == "target_error" || stage == "error" || stage == "failed" {
			delete(openByRequest, requestID)
		}
	}
	return exchanges
}

func inheritPromptContextBlocks(existing []promptDebugItem, item promptDebugItem) promptDebugItem {
	if hasTypedPromptContextBlocks(item.SystemPromptBlock) {
		return item
	}
	for _, candidate := range existing {
		if hasTypedPromptContextBlocks(candidate.SystemPromptBlock) {
			item.SystemPromptBlock = append([]promptDebugBlock(nil), candidate.SystemPromptBlock...)
			return item
		}
	}
	return item
}

func hasTypedPromptContextBlocks(blocks []promptDebugBlock) bool {
	for _, block := range blocks {
		if strings.TrimSpace(block.Type) != "" {
			return true
		}
	}
	return false
}

func promptDebugRecordToItem(row promptDebugRecord) promptDebugItem {
	item := promptDebugItem{
		SchemaVersion: row.SchemaVersion,
		CreatedAt:     row.CreatedAt,
		Stage:         row.Stage,
		Level:         promptDebugLevel(row.Stage, row.Metadata),
		Metadata:      row.Metadata,
		PayloadBytes:  row.PayloadBytes,
		PayloadSHA256: row.PayloadSHA256,
		PayloadText:   row.PayloadText,
	}
	item.RequestID = promptDebugMetadataString(row.Metadata, "request_id")
	item.SystemPrompt, item.SystemPromptBlock = extractSystemPromptBlocks(row.PayloadText)
	item.SystemPromptLines = lineCount(item.SystemPrompt)
	return item
}

func promptDebugMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func promptDebugExchangeID(requestID, createdAt string, rowIndex int) string {
	return fmt.Sprintf("%s:%s:%d", requestID, createdAt, rowIndex)
}

func isCharacterPromptExchange(exchange promptDebugExchange) bool {
	role := strings.ToLower(strings.TrimSpace(exchange.ExecutionRole))
	targetID := strings.ToLower(strings.TrimSpace(exchange.TargetID))
	return role == "chat" || strings.HasSuffix(targetID, "_chat")
}

func promptDebugLevel(stage string, metadata map[string]any) string {
	if metadata != nil {
		if level, ok := metadata["level"].(string); ok && strings.TrimSpace(level) != "" {
			return strings.ToLower(strings.TrimSpace(level))
		}
	}
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "error", "failed", "target_error":
		return "error"
	case "gateway_received", "target_sent":
		return "debug"
	default:
		return "info"
	}
}

func extractSystemPromptBlocks(payload string) (string, []promptDebugBlock) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &envelope) != nil {
		return "", nil
	}
	var messages []map[string]json.RawMessage
	if raw, ok := envelope["messages"]; !ok || json.Unmarshal(raw, &messages) != nil {
		return "", nil
	}
	var rencrow struct {
		PromptContextBlocks []map[string]any `json:"prompt_context_blocks"`
	}
	_ = json.Unmarshal(envelope["rencrow"], &rencrow)
	for _, block := range rencrow.PromptContextBlocks {
		indexValue, ok := block["message_index"].(float64)
		index := int(indexValue)
		if !ok || index < 0 || index >= len(messages) {
			continue
		}
		metadata := make(map[string]string, len(block))
		for key, value := range block {
			if text, ok := value.(string); ok {
				metadata[key] = text
			}
		}
		metadata["prompt_context_type"] = metadata["type"]
		encoded, _ := json.Marshal(metadata)
		messages[index]["metadata"] = encoded
	}
	parts := make([]string, 0, len(messages))
	typedBlocks := make([]promptDebugBlock, 0, len(messages))
	contextLineOffset := 0
	for _, message := range messages {
		var role string
		_ = json.Unmarshal(message["role"], &role)
		content := promptDebugMessageText(message["content"])
		if strings.EqualFold(strings.TrimSpace(role), "system") {
			parts = append(parts, content)
		}
		var metadata map[string]string
		_ = json.Unmarshal(message["metadata"], &metadata)
		if contextType := strings.TrimSpace(metadata["prompt_context_type"]); contextType != "" {
			metadata["message_role"] = strings.ToLower(strings.TrimSpace(role))
			label := promptContextBlockLabel(contextType, metadata)
			typedBlocks = append(typedBlocks, promptDebugBlock{
				Label:     label,
				Type:      contextType,
				Metadata:  metadata,
				StartLine: contextLineOffset + 1,
				EndLine:   contextLineOffset + lineCount(content),
				Text:      content,
			})
		}
		contextLineOffset += lineCount(content) + 2
	}
	text := strings.Join(parts, "\n\n")
	if text == "" {
		return "", nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len(typedBlocks) > 0 {
		return text, typedBlocks
	}
	blocks := splitSystemPromptSections(text)
	return text, blocks
}

func promptDebugMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var content string
	if json.Unmarshal(raw, &content) == nil {
		return content
	}
	return string(raw)
}

func promptContextBlockLabel(contextType string, metadata map[string]string) string {
	if label := strings.TrimSpace(metadata["character_prompt_block"]); label != "" {
		return label
	}
	if contextType == "stable_runtime_context" {
		switch metadata["runtime_context_kind"] {
		case "agent_contract":
			return "Agent Contract"
		case "interaction_contract":
			return "Interaction Contract"
		case "tool_boundary":
			return "Tool / Capability Boundary"
		}
	}
	if contextType == "recall_pack" {
		switch metadata["recall_section"] {
		case "l0", "l1", "l2", "l3":
			return strings.ToUpper(metadata["recall_section"])
		case "user_profile":
			return "UserProfile"
		case "knowledge":
			return "Knowledge"
		}
	}
	if contextType == "variable_runtime_context" {
		if kind := strings.TrimSpace(metadata["runtime_context_kind"]); kind != "" {
			return "Variable RuntimeContext / " + kind
		}
		return "Variable RuntimeContext"
	}
	if contextType == "user_message" {
		return "User Message"
	}
	return contextType
}

func splitSystemPromptSections(text string) []promptDebugBlock {
	const separator = "\n\n---\n\n"
	canonicalLabels := []string{"00_system.md", "10_policy.md", "20_scope.md", "30_knowledge.md"}
	parts := strings.Split(text, separator)
	if len(parts) == 1 {
		return []promptDebugBlock{{
			Label:     "system_prompt",
			StartLine: 1,
			EndLine:   lineCount(text),
			Text:      text,
		}}
	}
	blocks := make([]promptDebugBlock, 0, len(parts))
	offset := 0
	for index, part := range parts {
		label := ""
		if index < len(canonicalLabels) {
			label = canonicalLabels[index]
		} else {
			label = fmt.Sprintf("runtime_context_%02d", index-len(canonicalLabels)+1)
		}
		startLine := 1 + strings.Count(text[:offset], "\n")
		blocks = append(blocks, promptDebugBlock{
			Label:     label,
			StartLine: startLine,
			EndLine:   startLine + lineCount(part) - 1,
			Text:      part,
		})
		offset += len(part) + len(separator)
	}
	return blocks
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}
