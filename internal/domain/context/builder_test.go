package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildContext_ChatRoute(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Agent rules"), 0644)
	os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("Soul values"), 0644)
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("Identity info"), 0644)
	os.WriteFile(filepath.Join(dir, "USER.md"), []byte("User prefs"), 0644)
	os.MkdirAll(filepath.Join(dir, "persona"), 0755)
	os.WriteFile(filepath.Join(dir, "persona", "mio.md"), []byte("Mio persona"), 0644)

	b := NewBuilder(dir)
	got := b.BuildContext("CHAT")

	for _, want := range []string{"# AGENT\nAgent rules", "# SOUL\nSoul values", "# IDENTITY\nIdentity info", "# CHAT_PERSONA\nMio persona"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in context", want)
		}
	}
}

func TestBuildContext_NonChatRoute(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Agent rules"), 0644)
	os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("Soul values"), 0644)
	os.MkdirAll(filepath.Join(dir, "persona"), 0755)
	os.WriteFile(filepath.Join(dir, "persona", "mio.md"), []byte("Mio persona"), 0644)

	b := NewBuilder(dir)
	got := b.BuildContext("CODE")

	// Chat-only files should NOT be included
	if strings.Contains(got, "SOUL") {
		t.Error("SOUL should not be in non-CHAT context")
	}
	if strings.Contains(got, "CHAT_PERSONA") {
		t.Error("CHAT_PERSONA should not be in non-CHAT context")
	}
	// Shared files should be included
	if !strings.Contains(got, "# AGENT\nAgent rules") {
		t.Error("AGENT should be in all routes")
	}
}

func TestBuildContext_WithSkills(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "weather"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "weather", "SKILL.md"), []byte("# Weather lookup"), 0644)

	b := NewBuilder(dir)
	got := b.BuildContext("CHAT")

	if !strings.Contains(got, "weather: Weather lookup") {
		t.Error("expected skills summary")
	}
}

func TestBuildContext_WithMemoryStoreAndFewShot(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Agent rules"), 0644)
	os.WriteFile(filepath.Join(dir, "FewShot_01.md"), []byte("Example turn"), 0644)

	b := NewBuilder(dir).WithMemoryStore(fakeMemoryStore{context: "# MEMORY\nremember this"})
	got := b.BuildContext("CHAT")

	for _, want := range []string{"# AGENT\nAgent rules", "# MEMORY\nremember this", "# FewShot Example\nExample turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("context=%q, missing %q", got, want)
		}
	}
}

func TestBuildContext_Empty(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder(dir)
	got := b.BuildContext("CHAT")

	if got != "" {
		t.Errorf("expected empty context, got %q", got)
	}
}

func TestBuildMessageWithTask(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Be concise"), 0644)

	b := NewBuilder(dir)
	got := b.BuildMessageWithTask("CHAT", "HEARTBEAT TASKS", "Check system status")

	if !strings.Contains(got, "# AGENT\nBe concise") {
		t.Error("expected AGENT context")
	}
	if !strings.Contains(got, "===") {
		t.Error("expected separator")
	}
	if !strings.Contains(got, "# HEARTBEAT TASKS\nCheck system status") {
		t.Error("expected task section")
	}
}

func TestBuildMessageWithTask_NoContext(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder(dir)
	got := b.BuildMessageWithTask("CHAT", "TASK", "Do something")

	if got != "Do something" {
		t.Errorf("expected plain task content, got %q", got)
	}
}

func TestBuildSkillsSummary(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "web-search"), 0755)
	os.MkdirAll(filepath.Join(dir, "skills", "cron"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "web-search", "SKILL.md"), []byte("# Web search tool"), 0644)
	os.WriteFile(filepath.Join(dir, "skills", "cron", "SKILL.md"), []byte("# Scheduled tasks"), 0644)

	b := NewBuilder(dir)
	got := b.BuildSkillsSummary()

	if !strings.Contains(got, "web-search: Web search tool") {
		t.Error("expected web-search skill")
	}
	if !strings.Contains(got, "cron: Scheduled tasks") {
		t.Error("expected cron skill")
	}
}

func TestBuildSkillsSummary_Empty(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder(dir)
	got := b.BuildSkillsSummary()

	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBuildSkillsSummary_WithSkillDirsWorkspacePriority(t *testing.T) {
	dir := t.TempDir()
	workspaceSkills := filepath.Join(dir, "workspace", "skills")
	promptSkills := filepath.Join(dir, "prompts", "skills")
	os.MkdirAll(filepath.Join(workspaceSkills, "file_read"), 0755)
	os.MkdirAll(filepath.Join(promptSkills, "file_read"), 0755)
	os.MkdirAll(filepath.Join(promptSkills, "web_search"), 0755)
	os.WriteFile(filepath.Join(workspaceSkills, "file_read", "SKILL.md"), []byte("# Workspace file read"), 0644)
	os.WriteFile(filepath.Join(promptSkills, "file_read", "SKILL.md"), []byte("# Prompt file read"), 0644)
	os.WriteFile(filepath.Join(promptSkills, "web_search", "SKILL.md"), []byte("# Web search"), 0644)

	b := NewBuilder(filepath.Join(dir, "workspace")).WithSkillDirs(workspaceSkills, promptSkills)
	got := b.BuildSkillsSummary()

	if !strings.Contains(got, "file_read: Workspace file read") {
		t.Fatalf("expected workspace file_read skill: %q", got)
	}
	if strings.Contains(got, "Prompt file read") {
		t.Fatalf("prompt duplicate should not override workspace: %q", got)
	}
	if !strings.Contains(got, "web_search: Web search") {
		t.Fatalf("expected prompt-only skill: %q", got)
	}
}

type fakeMemoryStore struct {
	context string
}

func (s fakeMemoryStore) ReadLongTerm() string { return "" }

func (s fakeMemoryStore) WriteLongTerm(string) error { return nil }

func (s fakeMemoryStore) ReadToday() string { return "" }

func (s fakeMemoryStore) AppendToday(string) error { return nil }

func (s fakeMemoryStore) GetRecentDailyNotes(int) string { return "" }

func (s fakeMemoryStore) SaveDailyNoteForDate(time.Time, string) error { return nil }

func (s fakeMemoryStore) GetMemoryContext() string { return s.context }
