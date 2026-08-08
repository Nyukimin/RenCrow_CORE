package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestHandlePromptDebugLogsExtractsSystemPromptBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.jsonl")
	sections := []string{
		"# System\nsystem-line-2",
		"# Policy\npolicy-line-2\npolicy-line-3",
		"# Scope\nscope-line-2",
		"# Knowledge\nknowledge-line-2",
		"# Runtime\nruntime-line-2",
	}
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": strings.Join(sections, "\n\n---\n\n")},
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
		"metadata":       map[string]any{"request_id": "req-1", "agent_id": "mio", "execution_role": "chat", "target_id": "mio_chat", "caller": "core.unattributed"},
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
		CharacterLatest []promptDebugExchange `json:"character_latest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].RequestID != "req-1" || body.Items[0].Level != "debug" {
		t.Fatalf("unexpected item: %+v", body.Items)
	}
	if body.Items[0].SystemPromptLines != 23 || len(body.Items[0].Blocks) != 5 {
		t.Fatalf("unexpected blocks: %+v", body.Items[0])
	}
	if body.Items[0].Blocks[0].Label != "00_system.md" || body.Items[0].Blocks[0].StartLine != 1 || body.Items[0].Blocks[0].EndLine != 2 {
		t.Fatalf("unexpected first block: %+v", body.Items[0].Blocks[0])
	}
	if body.Items[0].Blocks[1].Label != "10_policy.md" || body.Items[0].Blocks[1].StartLine != 6 || body.Items[0].Blocks[1].EndLine != 8 {
		t.Fatalf("unexpected second block: %+v", body.Items[0].Blocks[1])
	}
	if body.Items[0].Blocks[2].Label != "20_scope.md" || body.Items[0].Blocks[3].Label != "30_knowledge.md" {
		t.Fatalf("unexpected canonical labels: %+v", body.Items[0].Blocks)
	}
	if body.Items[0].Blocks[4].Label != "runtime_context_01" {
		t.Fatalf("unexpected runtime context label: %+v", body.Items[0].Blocks[4])
	}
	if len(body.CharacterLatest) != 1 || body.CharacterLatest[0].AgentID != "mio" {
		t.Fatalf("unexpected character latest: %+v", body.CharacterLatest)
	}
}

func TestExtractSystemPromptBlocksKeepsUnsectionedPromptWhole(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"role": "system", "content": "line-1\nline-2\nline-3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, blocks := extractSystemPromptBlocks(string(payload))
	if len(blocks) != 1 || blocks[0].Label != "system_prompt" || blocks[0].StartLine != 1 || blocks[0].EndLine != 3 {
		t.Fatalf("unexpected unsectioned prompt blocks: %+v", blocks)
	}
}

func TestExtractSystemPromptBlocksUsesTypeMetadataInsteadOfPosition(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{
				"role":    "system",
				"content": "runtime first in fixture",
			},
			{
				"role":    "system",
				"content": "identity",
			},
		},
		"rencrow": map[string]any{"prompt_context_blocks": []map[string]any{
			{"message_index": 0, "type": "variable_runtime_context", "runtime_context_kind": "time"},
			{"message_index": 1, "type": "character_system_prompt", "character_prompt_block": "00_system.md"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, blocks := extractSystemPromptBlocks(string(payload))
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "variable_runtime_context" || blocks[0].Label != "Variable RuntimeContext / time" {
		t.Fatalf("first block must use explicit type metadata: %+v", blocks[0])
	}
	if blocks[1].Type != "character_system_prompt" || blocks[1].Label != "00_system.md" {
		t.Fatalf("character block must use explicit metadata: %+v", blocks[1])
	}
}

func TestExtractSystemPromptBlocksIncludesRecallRolesAndCurrentUser(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "system", "content": "identity"},
			{"role": "assistant", "content": "[Mio] previous reply"},
			{"role": "user", "content": "current question"},
		},
		"rencrow": map[string]any{"prompt_context_blocks": []map[string]any{
			{"message_index": 0, "type": "character_system_prompt", "character_prompt_block": "00_system.md"},
			{"message_index": 1, "type": "recall_pack", "recall_section": "l0"},
			{"message_index": 2, "type": "user_message"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	systemPrompt, blocks := extractSystemPromptBlocks(string(payload))
	if systemPrompt != "identity" {
		t.Fatalf("system prompt = %q", systemPrompt)
	}
	if len(blocks) != 3 || blocks[0].Label != "00_system.md" || blocks[1].Label != "L0" || blocks[2].Label != "User Message" {
		t.Fatalf("typed prompt context blocks = %+v", blocks)
	}
	if blocks[1].Metadata["message_role"] != "assistant" || blocks[2].Metadata["message_role"] != "user" {
		t.Fatalf("message roles missing from projection: %+v", blocks)
	}
}

func TestInheritPromptContextBlocksForTargetSent(t *testing.T) {
	typed := promptDebugItem{SystemPromptBlock: []promptDebugBlock{{Label: "L0", Type: "recall_pack", Text: "memory"}}}
	target := promptDebugItem{Stage: "target_sent", SystemPromptBlock: []promptDebugBlock{{Label: "system_prompt", Text: "merged fallback"}}}
	got := inheritPromptContextBlocks([]promptDebugItem{typed}, target)
	if len(got.SystemPromptBlock) != 1 || got.SystemPromptBlock[0].Type != "recall_pack" || got.SystemPromptBlock[0].Label != "L0" {
		t.Fatalf("target prompt context blocks were not inherited: %+v", got.SystemPromptBlock)
	}
}

func TestPromptDebugProjectionSeparatesCharacterLatestFromInternalWorker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.jsonl")
	rows := []map[string]any{
		promptDebugTestRow("2026-08-06T10:00:00Z", "chat-mio", "gateway_received", "mio", "chat", "mio_chat", "core.unattributed"),
		promptDebugTestRow("2026-08-06T10:00:01Z", "chat-mio", "target_sent", "mio", "chat", "mio_chat", "core.unattributed"),
		promptDebugTestRow("2026-08-06T10:01:00Z", "shared-id", "gateway_received", "shiro", "worker", "shiro_worker", "idlechat.daily_source_brief"),
		promptDebugTestRow("2026-08-06T10:01:01Z", "shared-id", "target_sent", "shiro", "worker", "shiro_worker", "idlechat.daily_source_brief"),
		promptDebugTestRow("2026-08-06T10:02:00Z", "shared-id", "gateway_received", "shiro", "worker", "shiro_worker", "memory.profile_promotion"),
		promptDebugTestRow("2026-08-06T10:02:01Z", "shared-id", "target_sent", "shiro", "worker", "shiro_worker", "memory.profile_promotion"),
	}
	var encoded strings.Builder
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(encoded.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	_, characters, internal, err := readPromptDebugProjection(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(characters) != 1 || characters[0].AgentID != "mio" || len(characters[0].Items) != 2 {
		t.Fatalf("unexpected character projection: %+v", characters)
	}
	if len(internal) != 2 {
		t.Fatalf("internal exchanges=%d want=2: %+v", len(internal), internal)
	}
	if internal[0].Caller != "memory.profile_promotion" || internal[1].Caller != "idlechat.daily_source_brief" {
		t.Fatalf("unexpected internal order: %+v", internal)
	}
	if internal[0].ExchangeID == internal[1].ExchangeID {
		t.Fatalf("reused request IDs must remain separate exchanges: %+v", internal)
	}
}

func promptDebugTestRow(createdAt, requestID, stage, agentID, role, targetID, caller string) map[string]any {
	payload := `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"hello"}],"rencrow":{"prompt_context_blocks":[{"message_index":0,"type":"character_system_prompt","character_prompt_block":"00_system.md"}]}}`
	return map[string]any{
		"schema_version": 1,
		"created_at":     createdAt,
		"stage":          stage,
		"metadata": map[string]any{
			"request_id":     requestID,
			"agent_id":       agentID,
			"execution_role": role,
			"target_id":      targetID,
			"caller":         caller,
		},
		"payload_bytes":  len(payload),
		"payload_sha256": "abc",
		"payload_text":   payload,
	}
}

func TestPromptDebugClassifierRequestDoesNotReplaceLatestCharacterPrompt(t *testing.T) {
	character := promptDebugTestRow("2026-08-08T05:00:00Z", "chat", "gateway_received", "mio", "chat", "mio_chat", "core.unattributed")
	classifier := promptDebugTestRow("2026-08-08T05:01:00Z", "classifier", "gateway_received", "mio", "chat", "mio_chat", "core.unattributed")
	classifierPayload := `{"messages":[{"role":"system","content":"classifier"},{"role":"user","content":"classify"}],"rencrow":{"prompt_context_blocks":[{"message_index":0,"type":"variable_runtime_context","runtime_context_kind":"time"}]}}`
	classifier["payload_text"] = classifierPayload
	classifier["payload_bytes"] = len(classifierPayload)

	rows := make([]promptDebugRecord, 0, 2)
	for _, source := range []map[string]any{character, classifier} {
		encoded, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var row promptDebugRecord
		if err := json.Unmarshal(encoded, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	exchanges := buildPromptDebugExchanges(rows)
	sort.SliceStable(exchanges, func(i, j int) bool { return exchanges[i].CreatedAt > exchanges[j].CreatedAt })
	var latest promptDebugExchange
	for _, exchange := range exchanges {
		if isCharacterPromptExchange(exchange) {
			latest = exchange
			break
		}
	}
	if latest.RequestID != "chat" {
		t.Fatalf("latest character request = %q, want chat", latest.RequestID)
	}
}

func TestHandlePromptDebugLogsMissingFileIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePromptDebugLogs(PromptDebugLogOptions{Path: filepath.Join(t.TempDir(), "missing.jsonl")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/prompt-debug", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
