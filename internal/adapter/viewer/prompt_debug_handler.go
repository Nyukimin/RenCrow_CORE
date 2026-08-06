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
	Label     string `json:"label"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
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
		items, err := readPromptDebugItems(path, limit)
		if err != nil {
			if os.IsNotExist(err) {
				writeMonitorJSON(w, map[string]any{"items": []promptDebugItem{}, "source": path, "available": false})
				return
			}
			http.Error(w, "failed to load prompt debug logs", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": items, "source": path, "available": true})
	}
}

func readPromptDebugItems(path string, limit int) ([]promptDebugItem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	rows := make([]promptDebugRecord, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var row promptDebugRecord
		if json.Unmarshal(scanner.Bytes(), &row) != nil || strings.TrimSpace(row.PayloadText) == "" {
			continue
		}
		rows = append(rows, row)
		if len(rows) > limit*4 {
			rows = rows[len(rows)-limit*2:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// The log is append-only, but sorting makes the projection deterministic
	// when records were copied or recovered out of order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt > rows[j].CreatedAt })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	items := make([]promptDebugItem, 0, len(rows))
	for _, row := range rows {
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
		if row.Metadata != nil {
			item.RequestID, _ = row.Metadata["request_id"].(string)
		}
		item.SystemPrompt, item.SystemPromptBlock = extractSystemPromptBlocks(row.PayloadText)
		item.SystemPromptLines = lineCount(item.SystemPrompt)
		items = append(items, item)
	}
	return items, nil
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
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		var role string
		_ = json.Unmarshal(message["role"], &role)
		if strings.ToLower(strings.TrimSpace(role)) != "system" {
			continue
		}
		var content string
		if json.Unmarshal(message["content"], &content) == nil {
			parts = append(parts, content)
			continue
		}
		if raw := message["content"]; len(raw) > 0 {
			parts = append(parts, string(raw))
		}
	}
	text := strings.Join(parts, "\n\n")
	if text == "" {
		return "", nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	blocks := make([]promptDebugBlock, 0, (len(lines)+9)/10)
	for start := 0; start < len(lines); start += 10 {
		end := start + 10
		if end > len(lines) {
			end = len(lines)
		}
		blocks = append(blocks, promptDebugBlock{
			Label:     formatPromptBlockLabel(start),
			StartLine: start + 1,
			EndLine:   end,
			Text:      strings.Join(lines[start:end], "\n"),
		})
	}
	return text, blocks
}

func formatPromptBlockLabel(start int) string {
	return fmt.Sprintf("%02d", start)
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}
