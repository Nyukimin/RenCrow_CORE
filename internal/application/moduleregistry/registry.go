package moduleregistry

import (
	"path/filepath"
	"sort"
	"strings"

	domain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/moduleregistry"
)

type Registry struct {
	modules []domain.Module
}

func NewRegistry(modules []domain.Module) *Registry {
	cleaned := make([]domain.Module, 0, len(modules))
	seen := make(map[string]bool)
	for _, m := range modules {
		m.ID = strings.ToLower(strings.TrimSpace(m.ID))
		m.DisplayName = strings.TrimSpace(m.DisplayName)
		m.Root = filepath.Clean(strings.TrimSpace(m.Root))
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		cleaned = append(cleaned, m)
	}
	sort.Slice(cleaned, func(i, j int) bool { return cleaned[i].ID < cleaned[j].ID })
	return &Registry{modules: cleaned}
}

func DefaultRegistry() *Registry {
	const root = "/home/nyukimi/RenCrow"
	return NewRegistry([]domain.Module{
		{
			ID:             "chat",
			DisplayName:    "picoclaw_multiLLM",
			Root:           filepath.Join(root, "picoclaw_multiLLM"),
			Kind:           "go",
			BuildCommand:   "make build",
			TestCommand:    "go test ./...",
			InstallCommand: "cp build/picoclaw-linux-amd64 ~/.local/bin/picoclaw",
			RestartTarget:  "picoclaw.service",
			HealthCheck:    "systemctl --user status picoclaw.service --no-pager",
			OwnerRoute:     "CODE",
			Aliases:        []string{"chat", "chat本体", "orchestrator", "viewer", "worker", "picoclaw", "picoclaw.service", "本体"},
		},
		{
			ID:             "cli",
			DisplayName:    "RenCrow_CMD",
			Root:           filepath.Join(root, "RenCrow_CMD"),
			Kind:           "go",
			BuildCommand:   "make build",
			TestCommand:    "go test ./...",
			InstallCommand: "cp build/rencrow-linux-amd64 ~/.local/bin/rencrow",
			OwnerRoute:     "CODE",
			Aliases:        []string{"rencrow_cmd", "rencrow-cli", "rencrow_cli", "rencrow chat", "cli", "command wrapper", "入口"},
		},
		{
			ID:          "stt",
			DisplayName: "RenCrow_STT",
			Root:        filepath.Join(root, "RenCrow_STT"),
			Kind:        "mixed",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_stt", "stt", "音声認識", "音声入力", "streaming transcript", "字幕"},
		},
		{
			ID:          "tts",
			DisplayName: "RenCrow_TTS",
			Root:        filepath.Join(root, "RenCrow_TTS"),
			Kind:        "mixed",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_tts", "tts", "音声合成", "読み上げ", "口パク", "lipsync"},
		},
		{
			ID:          "llm",
			DisplayName: "RenCrow_LLM",
			Root:        filepath.Join(root, "RenCrow_LLM"),
			Kind:        "mixed",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_llm", "llm", "モデル", "provider", "mlx", "ollama", "openai互換", "model gateway"},
		},
		{
			ID:          "vision",
			DisplayName: "RenCrow_Vision",
			Root:        filepath.Join(root, "RenCrow_Vision"),
			Kind:        "mixed",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_vision", "vision", "画像認識", "動画認識", "vision analysis"},
		},
		{
			ID:          "image",
			DisplayName: "RenCrow_Image",
			Root:        filepath.Join(root, "RenCrow_Image"),
			Kind:        "mixed",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_image", "image", "画像生成", "comfyui", "image workflow"},
		},
		{
			ID:          "tools",
			DisplayName: "RenCrow_Tools",
			Root:        filepath.Join(root, "RenCrow_Tools"),
			Kind:        "mixed",
			TestCommand: "make test",
			OwnerRoute:  "CODE",
			Aliases:     []string{"rencrow_tools", "tools", "tool", "ツール", "補助ツール", "横断ツール", "helper tools"},
		},
		{
			ID:          "workspace",
			DisplayName: "RenCrow_Workspace",
			Root:        filepath.Join(root, "RenCrow_Workspace"),
			Kind:        "config",
			OwnerRoute:  "OPS",
			Aliases:     []string{"rencrow_workspace", "workspace", "config", "成果物", "共有作業領域"},
		},
	})
}

func (r *Registry) Modules() []domain.Module {
	if r == nil {
		return nil
	}
	out := make([]domain.Module, len(r.modules))
	copy(out, r.modules)
	return out
}

func (r *Registry) Resolve(message string) domain.Resolution {
	if r == nil {
		return domain.Resolution{}
	}
	normalized := normalize(message)
	if normalized == "" {
		return domain.Resolution{}
	}
	var matches []domain.Module
	var matchedBy string
	bestLen := 0
	for _, m := range r.modules {
		terms := append([]string{m.ID, m.DisplayName, filepath.Base(m.Root)}, m.Aliases...)
		for _, term := range terms {
			n := normalize(term)
			if n == "" {
				continue
			}
			if normalized == n || strings.Contains(normalized, n) {
				if len(n) > bestLen {
					bestLen = len(n)
					matches = matches[:0]
					matchedBy = ""
				}
				if len(n) < bestLen {
					continue
				}
				matches = append(matches, m)
				if matchedBy == "" {
					matchedBy = term
				}
				break
			}
		}
	}
	if len(matches) == 0 {
		if strings.Contains(normalized, normalize("/home/nyukimi/RenCrow")) {
			return domain.Resolution{MatchedBy: "/home/nyukimi/RenCrow", Ambiguous: true, Confidence: 0.1}
		}
		return domain.Resolution{}
	}
	if len(matches) > 1 {
		return domain.Resolution{Module: matches[0], MatchedBy: matchedBy, Confidence: 0.65, Ambiguous: true, Candidates: matches}
	}
	return domain.Resolution{Module: matches[0], MatchedBy: matchedBy, Confidence: 1.0}
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "＿", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
