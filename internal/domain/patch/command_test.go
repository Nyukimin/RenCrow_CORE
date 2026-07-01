package patch

import "testing"

func TestNewPatchCommand(t *testing.T) {
	cmd := NewPatchCommand(TypeFileEdit, ActionCreate, "/workspace/test.go", "package main")

	if cmd.Type != TypeFileEdit {
		t.Errorf("Expected type %s, got %s", TypeFileEdit, cmd.Type)
	}

	if cmd.Action != ActionCreate {
		t.Errorf("Expected action %s, got %s", ActionCreate, cmd.Action)
	}

	if cmd.Target != "/workspace/test.go" {
		t.Errorf("Expected target '/workspace/test.go', got '%s'", cmd.Target)
	}

	if cmd.Content != "package main" {
		t.Errorf("Expected content 'package main', got '%s'", cmd.Content)
	}
}

func TestPatchCommandWithMetadata(t *testing.T) {
	cmd := NewPatchCommand(TypeFileEdit, ActionCreate, "test.go", "content")
	cmdWithMeta := cmd.WithMetadata("language", "go")

	value, ok := cmdWithMeta.GetMetadata("language")
	if !ok {
		t.Error("Metadata should exist")
	}

	if value != "go" {
		t.Errorf("Expected metadata value 'go', got '%s'", value)
	}

	// 元のcmdは変更されない（イミュータブル）
	_, exists := cmd.GetMetadata("language")
	if exists {
		t.Error("Original command should not be modified")
	}
}

func TestPatchCommandTypeChecks(t *testing.T) {
	tests := []struct {
		name           string
		cmdType        Type
		isFileEdit     bool
		isShellCommand bool
		isGitOperation bool
	}{
		{"FileEdit", TypeFileEdit, true, false, false},
		{"ShellCommand", TypeShellCommand, false, true, false},
		{"GitOperation", TypeGitOperation, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewPatchCommand(tt.cmdType, ActionRun, "target", "content")

			if cmd.IsFileEdit() != tt.isFileEdit {
				t.Errorf("Expected IsFileEdit()=%v, got %v", tt.isFileEdit, cmd.IsFileEdit())
			}

			if cmd.IsShellCommand() != tt.isShellCommand {
				t.Errorf("Expected IsShellCommand()=%v, got %v", tt.isShellCommand, cmd.IsShellCommand())
			}

			if cmd.IsGitOperation() != tt.isGitOperation {
				t.Errorf("Expected IsGitOperation()=%v, got %v", tt.isGitOperation, cmd.IsGitOperation())
			}
		})
	}
}

func TestPatchCommandGetMetadataNotExist(t *testing.T) {
	cmd := NewPatchCommand(TypeFileEdit, ActionCreate, "test.go", "content")

	_, ok := cmd.GetMetadata("nonexistent")
	if ok {
		t.Error("GetMetadata should return false for nonexistent key")
	}

	cmd.Metadata = nil
	if cmdWithMeta := cmd.WithMetadata("owner", "agent"); cmdWithMeta.Metadata["owner"] != "agent" {
		t.Fatalf("WithMetadata should handle nil metadata: %#v", cmdWithMeta.Metadata)
	}
	if _, ok := cmd.GetMetadata("owner"); ok {
		t.Fatal("original nil metadata command should remain unchanged")
	}
}
