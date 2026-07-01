package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFile_WithFrontmatter(t *testing.T) {
	content := `---
name: weather
description: Get current weather and forecasts.
metadata: {"emoji":"🌤️"}
---

# Weather

Some body text here.`

	meta := parseSkillFile(content, "weather-dir")
	if meta.Name != "weather" {
		t.Errorf("Name = %q, want %q", meta.Name, "weather")
	}
	if meta.Description != "Get current weather and forecasts." {
		t.Errorf("Description = %q, want %q", meta.Description, "Get current weather and forecasts.")
	}
	if meta.DirName != "weather-dir" {
		t.Errorf("DirName = %q, want %q", meta.DirName, "weather-dir")
	}
	if meta.BodyText == "" {
		t.Error("BodyText should not be empty")
	}
}

func TestParseSkillFile_QuotedDescription(t *testing.T) {
	content := `---
name: github
description: "Interact with GitHub using the gh CLI."
---

# GitHub`

	meta := parseSkillFile(content, "github")
	if meta.Description != "Interact with GitHub using the gh CLI." {
		t.Errorf("Description = %q, want unquoted value", meta.Description)
	}
}

func TestParseSkillFile_NoFrontmatter(t *testing.T) {
	content := `# My Skill

This is the body.`

	meta := parseSkillFile(content, "my-skill")
	if meta.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", meta.Name, "my-skill")
	}
	if meta.Description != "My Skill" {
		t.Errorf("Description = %q, want %q", meta.Description, "My Skill")
	}
}

func TestParseSkillFile_EmptyContent(t *testing.T) {
	meta := parseSkillFile("", "empty")
	if meta.Name != "empty" {
		t.Errorf("Name = %q, want %q", meta.Name, "empty")
	}
}

func TestParseSkillFile_ToolContractFields(t *testing.T) {
	content := `---
name: file_write
tool_id: file_write
version: "1.0.0"
category: mutation
dry_run: true
invariants:
  - "path must be non-empty"
  - "path traversal is rejected"
  - "timeout: 10 seconds"
---

# file_write`

	meta := parseSkillFile(content, "file_write")
	if meta.ToolID != "file_write" {
		t.Errorf("ToolID = %q, want %q", meta.ToolID, "file_write")
	}
	if meta.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version, "1.0.0")
	}
	if meta.Category != "mutation" {
		t.Errorf("Category = %q, want %q", meta.Category, "mutation")
	}
	if !meta.DryRun {
		t.Error("DryRun should be true")
	}
	if len(meta.Invariants) != 3 {
		t.Errorf("Invariants len = %d, want 3", len(meta.Invariants))
	}
	if len(meta.Invariants) > 0 && meta.Invariants[0] != "path must be non-empty" {
		t.Errorf("Invariants[0] = %q", meta.Invariants[0])
	}
}

func TestParseSkillFile_QueryCategory(t *testing.T) {
	content := `---
name: file_read
tool_id: file_read
version: "1.0.0"
category: query
dry_run: false
---

# file_read`

	meta := parseSkillFile(content, "file_read")
	if meta.Category != "query" {
		t.Errorf("Category = %q, want %q", meta.Category, "query")
	}
	if meta.DryRun {
		t.Error("DryRun should be false")
	}
}

func TestFormatSummary(t *testing.T) {
	loader := NewSkillsLoader("/nonexistent")
	skills := []SkillMetadata{
		{Name: "weather", Description: "Get weather"},
		{Name: "github", Description: "GitHub CLI"},
		{Name: "bare"},
	}
	summary := loader.FormatSummary(skills)
	if summary == "" {
		t.Error("summary should not be empty")
	}
	// Check each line
	lines := 0
	for range skills {
		lines++
	}
	if lines != 3 {
		t.Errorf("expected 3 skills in summary")
	}
}

func TestSkillsLoader_LoadAllFromDirsWorkspaceOverridesPrompts(t *testing.T) {
	dir := t.TempDir()
	workspaceSkills := filepath.Join(dir, "workspace", "skills")
	promptSkills := filepath.Join(dir, "prompts", "skills")
	mustWriteSkill(t, filepath.Join(promptSkills, "file_read"), `---
name: file_read
description: Prompt file read.
---

# file_read`)
	mustWriteSkill(t, filepath.Join(workspaceSkills, "file_read"), `---
name: file_read
description: Workspace file read.
---

# file_read`)
	mustWriteSkill(t, filepath.Join(promptSkills, "shell"), `---
name: shell
description: Shell context only.
---

# shell`)

	loader := NewSkillsLoader(workspaceSkills)
	skills, err := loader.LoadAllFromDirs(workspaceSkills, promptSkills)
	if err != nil {
		t.Fatalf("LoadAllFromDirs failed: %v", err)
	}

	summary := loader.FormatSummary(skills)
	if !strings.Contains(summary, "file_read: Workspace file read.") {
		t.Fatalf("workspace skill should override prompt skill: %s", summary)
	}
	if strings.Contains(summary, "Prompt file read.") {
		t.Fatalf("prompt duplicate should be skipped: %s", summary)
	}
	if !strings.Contains(summary, "shell: Shell context only.") {
		t.Fatalf("non-duplicate prompt skill should be included: %s", summary)
	}
}

func TestSkillsLoader_LoadAllUsesConfiguredRoot(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	mustWriteSkill(t, filepath.Join(skillsDir, "weather"), `---
name: weather
description: Weather lookup.
---

# weather`)

	loader := NewSkillsLoader(skillsDir)
	skills, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "weather" {
		t.Fatalf("skills=%#v", skills)
	}
}

func TestSkillsLoader_LoadAllFromDirsSkipsBrokenSkillAndRemainsContextOnly(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	mustWriteSkill(t, filepath.Join(skillsDir, "broken"), `---
name: broken
description: missing closing frontmatter`)
	mustWriteSkill(t, filepath.Join(skillsDir, "write"), `---
name: write
description: Write files.
category: mutation
---

# write`)

	loader := NewSkillsLoader(skillsDir)
	skills, err := loader.LoadAllFromDirs(skillsDir)
	if err != nil {
		t.Fatalf("LoadAllFromDirs failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("skills count=%d, want 1: %#v", len(skills), skills)
	}
	if skills[0].Name != "write" {
		t.Fatalf("skill name=%q, want write", skills[0].Name)
	}
	if skills[0].CanExecute {
		t.Fatal("loaded skills are context only and must not grant execution permission")
	}
}

func mustWriteSkill(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
