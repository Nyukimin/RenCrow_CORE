package moduleregistry

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/moduleregistry"
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
		m.Root = cleanModuleRoot(m.Root)
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
	root := defaultWorkspaceRoot()
	return NewRegistry([]domain.Module{
		{
			ID:           "chat",
			DisplayName:  "RenCrow_CORE",
			Root:         filepath.Join(root, "RenCrow_CORE"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"chat", "chat本体", "orchestrator", "viewer", "worker", "rencrow_core", "rencrow.service", "本体"},
		},
		{
			ID:           "cli",
			DisplayName:  "RenCrow_CMD",
			Root:         filepath.Join(root, "RenCrow_CMD"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_cmd", "rencrowctl", "rencrowctl chat", "cli", "command facade", "入口"},
		},
		{
			ID:           "portal",
			DisplayName:  "RenCrow_PORTAL",
			Root:         filepath.Join(root, "RenCrow_PORTAL"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_portal", "portal", "chat portal", "idlechat portal", "web portal", "外部ポータル"},
		},
		{
			ID:           "games",
			DisplayName:  "RenCrow_GAMES",
			Root:         filepath.Join(root, "RenCrow_GAMES"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_games", "games", "game", "ゲーム", "observer", "game world"},
		},
		{
			ID:           "stt",
			DisplayName:  "RenCrow_STT",
			Root:         filepath.Join(root, "RenCrow_STT"),
			Kind:         "go",
			BuildCommand: "go -C gateway build ./...",
			TestCommand:  "go -C gateway vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_stt", "stt", "音声認識", "音声入力", "streaming transcript", "字幕"},
		},
		{
			ID:           "tts",
			DisplayName:  "RenCrow_TTS",
			Root:         filepath.Join(root, "RenCrow_TTS"),
			Kind:         "go",
			BuildCommand: "go -C gateway build ./...",
			TestCommand:  "go -C gateway vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_tts", "tts", "音声合成", "読み上げ", "口パク", "lipsync"},
		},
		{
			ID:           "llm",
			DisplayName:  "RenCrow_LLM",
			Root:         filepath.Join(root, "RenCrow_LLM"),
			Kind:         "go",
			BuildCommand: "go -C gateway build ./...",
			TestCommand:  "go -C gateway vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_llm", "llm", "モデル", "provider", "gateway", "model gateway"},
		},
		{
			ID:           "vision",
			DisplayName:  "RenCrow_Vision",
			Root:         filepath.Join(root, "RenCrow_Vision"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_vision", "vision", "画像認識", "動画認識", "vision analysis"},
		},
		{
			ID:           "image",
			DisplayName:  "RenCrow_Image",
			Root:         filepath.Join(root, "RenCrow_Image"),
			Kind:         "go",
			BuildCommand: "go build ./...",
			TestCommand:  "go vet ./...",
			OwnerRoute:   "CODE",
			Aliases:      []string{"rencrow_image", "image", "画像生成", "image workflow"},
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
			Kind:        "snapshot",
			OwnerRoute:  "OPS",
			Aliases:     []string{"rencrow_workspace", "workspace snapshot", "workspace repository", "workspace backup repository"},
		},
	})
}

func defaultWorkspaceRoot() string {
	if configured := strings.TrimSpace(os.Getenv("RENCROW_WORKSPACE_ROOT")); configured != "" {
		return filepath.Clean(configured)
	}
	if current, err := os.Getwd(); err == nil {
		for candidate := filepath.Clean(current); ; candidate = filepath.Dir(candidate) {
			if strings.EqualFold(filepath.Base(candidate), "RenCrow_CORE") {
				return filepath.Dir(candidate)
			}
			if directoryExists(filepath.Join(candidate, "RenCrow_CORE")) {
				return candidate
			}
			parent := filepath.Dir(candidate)
			if parent == candidate {
				break
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, candidate := range []string{
			filepath.Join(home, "RenCrow"),
			filepath.Join(home, "Documents", "GenerativeAI", "RenCrow"),
		} {
			if directoryExists(filepath.Join(candidate, "RenCrow_CORE")) {
				return candidate
			}
		}
		return filepath.Join(home, "RenCrow")
	}
	return filepath.Clean("RenCrow")
}

func directoryExists(target string) bool {
	info, err := os.Stat(target)
	return err == nil && info.IsDir()
}

func cleanModuleRoot(root string) string {
	root = strings.TrimSpace(root)
	if strings.HasPrefix(root, "/") {
		return path.Clean(root)
	}
	return filepath.Clean(root)
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
		for _, module := range r.modules {
			workspaceRoot := filepath.Dir(module.Root)
			if workspaceRoot != "." && strings.Contains(normalized, normalize(workspaceRoot)) {
				return domain.Resolution{MatchedBy: workspaceRoot, Ambiguous: true, Confidence: 0.1}
			}
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
	s = strings.ReplaceAll(s, `\`, "/")
	return s
}
