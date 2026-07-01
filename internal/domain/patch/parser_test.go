package patch

import (
	"strings"
	"testing"
)

func TestParseJSONPatch(t *testing.T) {
	jsonPatch := `[
		{
			"type": "file_edit",
			"action": "create",
			"target": "/workspace/test.go",
			"content": "package main"
		},
		{
			"type": "shell_command",
			"action": "run",
			"target": "go test ./..."
		}
	]`

	commands, err := ParsePatch(jsonPatch)
	if err != nil {
		t.Fatalf("ParsePatch failed: %v", err)
	}

	if len(commands) != 2 {
		t.Fatalf("Expected 2 commands, got %d", len(commands))
	}

	// 1つ目のコマンド検証
	if commands[0].Type != TypeFileEdit {
		t.Errorf("Expected type file_edit, got %s", commands[0].Type)
	}

	if commands[0].Action != ActionCreate {
		t.Errorf("Expected action create, got %s", commands[0].Action)
	}

	if commands[0].Target != "/workspace/test.go" {
		t.Errorf("Expected target '/workspace/test.go', got '%s'", commands[0].Target)
	}

	// 2つ目のコマンド検証
	if commands[1].Type != TypeShellCommand {
		t.Errorf("Expected type shell_command, got %s", commands[1].Type)
	}

	if commands[1].Action != ActionRun {
		t.Errorf("Expected action run, got %s", commands[1].Action)
	}
}

func TestParseJSONPatchValidation(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "Missing type",
			json:    `[{"action":"create","target":"test.go","content":"content"}]`,
			wantErr: true,
		},
		{
			name:    "Missing action",
			json:    `[{"type":"file_edit","target":"test.go","content":"content"}]`,
			wantErr: true,
		},
		{
			name:    "Missing target",
			json:    `[{"type":"file_edit","action":"create","content":"content"}]`,
			wantErr: true,
		},
		{
			name:    "Invalid JSON",
			json:    `[{invalid json}]`,
			wantErr: true,
		},
		{
			name:    "Placeholder target",
			json:    `[{"type":"file_edit","action":"update","target":"path/to/chat_module.go","content":"package main"}]`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePatch(tt.json)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePatch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseMarkdownPatchRejectsPlaceholderTarget(t *testing.T) {
	markdownPatch := "```go:path/to/chat_module.go\npackage main\n```"

	_, err := ParsePatch(markdownPatch)
	if err == nil {
		t.Fatal("expected placeholder target to be rejected")
	}
	if !strings.Contains(err.Error(), "placeholder target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseMarkdownPatch(t *testing.T) {
	markdownPatch := "## Patch\n\n```go:src/main.go\npackage main\n\nfunc main() {}\n```\n\n```bash\ngo build\n```"

	commands, err := ParsePatch(markdownPatch)
	if err != nil {
		t.Fatalf("ParsePatch failed: %v", err)
	}

	if len(commands) != 2 {
		t.Fatalf("Expected 2 commands, got %d", len(commands))
	}

	// 1つ目: ファイル編集
	if commands[0].Type != TypeFileEdit {
		t.Errorf("Expected type file_edit, got %s", commands[0].Type)
	}

	if commands[0].Target != "src/main.go" {
		t.Errorf("Expected target 'src/main.go', got '%s'", commands[0].Target)
	}

	if commands[0].Action != ActionUpdate {
		t.Errorf("Expected action update, got %s", commands[0].Action)
	}

	lang, ok := commands[0].GetMetadata("language")
	if !ok || lang != "go" {
		t.Errorf("Expected language metadata 'go', got '%s'", lang)
	}

	// 2つ目: シェルコマンド
	if commands[1].Type != TypeShellCommand {
		t.Errorf("Expected type shell_command, got %s", commands[1].Type)
	}

	if commands[1].Target != "go build" {
		t.Errorf("Expected target 'go build', got '%s'", commands[1].Target)
	}
}

func TestParseMarkdownPatchOrderPreservation(t *testing.T) {
	// 順序が保存されることを確認
	markdownPatch := "```bash\nfirst\n```\n\n```go:file1.go\nsecond\n```\n\n```bash\nthird\n```"

	commands, err := ParsePatch(markdownPatch)
	if err != nil {
		t.Fatalf("ParsePatch failed: %v", err)
	}

	if len(commands) != 3 {
		t.Fatalf("Expected 3 commands, got %d", len(commands))
	}

	// 順序確認
	if commands[0].Target != "first" {
		t.Errorf("Expected first command target 'first', got '%s'", commands[0].Target)
	}

	if commands[1].Target != "file1.go" {
		t.Errorf("Expected second command target 'file1.go', got '%s'", commands[1].Target)
	}

	if commands[2].Target != "third" {
		t.Errorf("Expected third command target 'third', got '%s'", commands[2].Target)
	}
}

func TestParsePatchUnknownFormat(t *testing.T) {
	invalidPatch := "This is not a valid patch format"

	_, err := ParsePatch(invalidPatch)
	if err == nil {
		t.Error("Expected error for unknown patch format, got nil")
	}
}

func TestParseMarkdownPatchMultiline(t *testing.T) {
	// 複数行のコードブロックが正しく解析されることを確認
	markdownPatch := "```go:test.go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"test\")\n}\n```"

	commands, err := ParsePatch(markdownPatch)
	if err != nil {
		t.Fatalf("ParsePatch failed: %v", err)
	}

	if len(commands) != 1 {
		t.Fatalf("Expected 1 command, got %d", len(commands))
	}

	// Contentに複数行が含まれていることを確認（末尾改行は許容）
	content := strings.TrimSpace(commands[0].Content)
	expectedContent := "package main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"test\")\n}"
	if content != expectedContent {
		t.Errorf("Expected content:\n%s\n\nGot:\n%s", expectedContent, content)
	}
}
