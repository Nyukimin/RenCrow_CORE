package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlePromptDebugLogsExtractsSystemPromptBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.jsonl")
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "line-" + string(rune('a'+i))
	}
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": strings.Join(lines, "\n")},
			{"role": "user", "content": "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"created_at":     "2026-08-06T10:00:00Z",
		"stage":          "gateway_received",
		"metadata":       map[string]any{"request_id": "req-1", "agent_id": "mio"},
		"payload_bytes":  len(payload),
		"payload_sha256": "abc",
		"payload_text":   string(payload),
	})
	if err := os.WriteFile(path, append(row, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	HandlePromptDebugLogs(PromptDebugLogOptions{Path: path}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/prompt-debug?limit=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			RequestID         string `json:"request_id"`
			Level             string `json:"level"`
			SystemPromptLines int    `json:"system_prompt_lines"`
			Blocks            []struct {
				Label     string `json:"label"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			} `json:"system_prompt_blocks"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].RequestID != "req-1" || body.Items[0].Level != "debug" {
		t.Fatalf("unexpected item: %+v", body.Items)
	}
	if body.Items[0].SystemPromptLines != 12 || len(body.Items[0].Blocks) != 2 {
		t.Fatalf("unexpected blocks: %+v", body.Items[0])
	}
	if body.Items[0].Blocks[0].Label != "00" || body.Items[0].Blocks[0].StartLine != 1 || body.Items[0].Blocks[0].EndLine != 10 {
		t.Fatalf("unexpected first block: %+v", body.Items[0].Blocks[0])
	}
	if body.Items[0].Blocks[1].Label != "10" || body.Items[0].Blocks[1].StartLine != 11 || body.Items[0].Blocks[1].EndLine != 12 {
		t.Fatalf("unexpected second block: %+v", body.Items[0].Blocks[1])
	}
}

func TestHandlePromptDebugLogsMissingFileIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePromptDebugLogs(PromptDebugLogOptions{Path: filepath.Join(t.TempDir(), "missing.jsonl")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/prompt-debug", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
