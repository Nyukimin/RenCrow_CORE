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

type lexicalMatch struct {
	module    domain.Module
	matchedBy string
	kind      int // canonical ID/name/path basename before aliases
	termLen   int
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

	// A concrete repository path is the strongest signal. Resolve it before
	// lexical IDs and aliases so that a path such as /workspace/RenCrow/
	// RenCrow_CORE cannot accidentally match the standalone "workspace"
	// snapshot alias.
	concrete := make([]domain.Module, 0, 1)
	for _, module := range r.modules {
		root := normalizePath(module.Root)
		if root == "" || !containsPathSpan(normalized, root) {
			continue
		}
		concrete = appendUniqueModule(concrete, module)
	}
	if len(concrete) > 0 {
		if len(concrete) > 1 {
			return domain.Resolution{Ambiguous: true, Candidates: concrete, Confidence: 0.1}
		}
		return domain.Resolution{Module: concrete[0], MatchedBy: concrete[0].Root, Confidence: 1.0}
	}

	workspaceRoots := registryWorkspaceRoots(r.modules)
	for _, workspaceRoot := range workspaceRoots {
		// The parent root is a location, not an editable module. Keep this
		// guard before lexical matching and report ambiguity rather than
		// guessing the snapshot module.
		if isPathLike(workspaceRoot) && containsPathSpan(normalized, workspaceRoot) {
			return domain.Resolution{MatchedBy: workspaceRoot, Ambiguous: true, Confidence: 0.1}
		}
	}

	// Remove every path span before matching IDs, display names, and aliases.
	// This prevents path components (especially "workspace") from leaking
	// into the lexical resolver while preserving standalone words outside a
	// path, such as "workspace のsnapshot".
	pathTerms := make([]string, 0, len(r.modules)*2)
	pathTerms = append(pathTerms, workspaceRoots...)
	for _, module := range r.modules {
		if root := normalizePath(module.Root); root != "" {
			pathTerms = append(pathTerms, root)
		}
	}
	lexicalInput := maskPathSpans(normalized, pathTerms)

	var best []lexicalMatch
	bestKind := 2
	bestLen := -1
	for _, module := range r.modules {
		canonicalTerms := []string{module.ID, module.DisplayName, moduleRootBase(module.Root)}
		for _, term := range canonicalTerms {
			normalizedTerm := normalize(term)
			if normalizedTerm == "" || !containsLexicalTerm(lexicalInput, normalizedTerm) {
				continue
			}
			match := lexicalMatch{module: module, matchedBy: term, kind: 0, termLen: len(normalizedTerm)}
			best, bestKind, bestLen = addBestLexicalMatch(best, match, bestKind, bestLen)
		}
		for _, term := range module.Aliases {
			normalizedTerm := normalize(term)
			if normalizedTerm == "" || !containsLexicalTerm(lexicalInput, normalizedTerm) {
				continue
			}
			match := lexicalMatch{module: module, matchedBy: term, kind: 1, termLen: len(normalizedTerm)}
			best, bestKind, bestLen = addBestLexicalMatch(best, match, bestKind, bestLen)
		}
	}
	if len(best) == 0 {
		return domain.Resolution{}
	}
	matches := make([]domain.Module, 0, len(best))
	matchedBy := best[0].matchedBy
	for _, match := range best {
		matches = appendUniqueModule(matches, match.module)
		if len(matches) == 1 {
			matchedBy = match.matchedBy
		}
	}
	if len(matches) > 1 {
		// An ambiguous lexical match must not silently select the first
		// module. Candidates remain available to the caller for a bounded
		// explicit resolution flow.
		return domain.Resolution{Ambiguous: true, Candidates: matches, Confidence: 0.1}
	}
	return domain.Resolution{Module: matches[0], MatchedBy: matchedBy, Confidence: 1.0}
}

func addBestLexicalMatch(best []lexicalMatch, match lexicalMatch, bestKind int, bestLen int) ([]lexicalMatch, int, int) {
	if match.kind < bestKind || (match.kind == bestKind && match.termLen > bestLen) {
		return []lexicalMatch{match}, match.kind, match.termLen
	}
	if match.kind == bestKind && match.termLen == bestLen {
		for _, existing := range best {
			if existing.module.ID == match.module.ID {
				return best, bestKind, bestLen
			}
		}
		best = append(best, match)
	}
	return best, bestKind, bestLen
}

func appendUniqueModule(modules []domain.Module, candidate domain.Module) []domain.Module {
	for _, module := range modules {
		if module.ID == candidate.ID {
			return modules
		}
	}
	return append(modules, candidate)
}

func registryWorkspaceRoots(modules []domain.Module) []string {
	roots := make([]string, 0, len(modules))
	for _, module := range modules {
		root := normalizePath(module.Root)
		separator := strings.LastIndex(root, "/")
		if separator <= 0 {
			continue
		}
		parent := strings.TrimRight(root[:separator], "/")
		if parent == "" || !isPathLike(parent) {
			continue
		}
		alreadySeen := false
		for _, existing := range roots {
			if existing == parent {
				alreadySeen = true
				break
			}
		}
		if !alreadySeen {
			roots = append(roots, parent)
		}
	}
	sort.Strings(roots)
	return roots
}

func moduleRootBase(root string) string {
	root = normalizePath(root)
	if root == "" {
		return ""
	}
	if separator := strings.LastIndex(root, "/"); separator >= 0 {
		return root[separator+1:]
	}
	return root
}

func normalizePath(value string) string {
	normalized := normalize(value)
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	return strings.TrimRight(normalized, "/")
}

func isPathLike(value string) bool {
	return strings.Contains(value, "/") || (len(value) >= 2 && value[1] == ':')
}

func containsPathSpan(haystack string, needle string) bool {
	if needle == "" {
		return false
	}
	for offset := 0; offset <= len(haystack)-len(needle); {
		relative := strings.Index(haystack[offset:], needle)
		if relative < 0 {
			return false
		}
		start := offset + relative
		end := start + len(needle)
		if pathBoundaryBefore(haystack, start) && pathBoundaryAfter(haystack, end) {
			return true
		}
		offset = start + 1
	}
	return false
}

func pathBoundaryBefore(value string, offset int) bool {
	if offset == 0 {
		return true
	}
	return !isPathWordByte(value[offset-1])
}

func pathBoundaryAfter(value string, offset int) bool {
	if offset >= len(value) {
		return true
	}
	return !isPathWordByte(value[offset])
}

func isPathWordByte(value byte) bool {
	return value == '_' || value == '-' || value == '.' ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

func maskPathSpans(value string, terms []string) string {
	masked := []byte(value)
	for _, term := range terms {
		if term == "" {
			continue
		}
		for offset := 0; offset <= len(value)-len(term); {
			relative := strings.Index(value[offset:], term)
			if relative < 0 {
				break
			}
			start := offset + relative
			end := start + len(term)
			if pathBoundaryBefore(value, start) && pathBoundaryAfter(value, end) {
				for index := start; index < end; index++ {
					masked[index] = 0
				}
			}
			offset = start + 1
		}
	}
	return string(maskUnknownPathSpans(value, masked))
}

func maskUnknownPathSpans(value string, masked []byte) []byte {
	// A path that is not one of the registry roots is still a path, not a
	// lexical module alias. Mask its ASCII path components as well so an
	// arbitrary /some/workspace/... string cannot select the snapshot module.
	for offset := 0; offset < len(value); {
		if value[offset] != '/' {
			offset++
			continue
		}
		end := offset
		for end < len(value) {
			current := value[end]
			if current != '/' && current != ':' && !isPathWordByte(current) {
				break
			}
			end++
		}
		if end > offset+1 {
			for index := offset; index < end; index++ {
				masked[index] = 0
			}
			offset = end
			continue
		}
		offset++
	}
	return masked
}

func containsLexicalTerm(haystack string, term string) bool {
	if term == "" {
		return false
	}
	for offset := 0; offset <= len(haystack)-len(term); {
		relative := strings.Index(haystack[offset:], term)
		if relative < 0 {
			return false
		}
		start := offset + relative
		end := start + len(term)
		if lexicalBoundaryBefore(haystack, start) && (lexicalBoundaryAfter(haystack, end) || lexicalCompoundTerm(term, haystack, end)) {
			return true
		}
		offset = start + 1
	}
	return false
}

func lexicalCompoundTerm(term string, haystack string, end int) bool {
	// Repository names such as RenCrow_LLM are commonly followed directly by
	// an English word after whitespace normalization ("RenCrow_LLM Gateway").
	// The path spans were already masked, so allowing this repository-name
	// compound does not reintroduce the workspace-path collision.
	return strings.ContainsAny(term, "_.") && end < len(haystack) && isPathWordByte(haystack[end])
}

func lexicalBoundaryBefore(value string, offset int) bool {
	return offset == 0 || !isPathWordByte(value[offset-1])
}

func lexicalBoundaryAfter(value string, offset int) bool {
	return offset >= len(value) || !isPathWordByte(value[offset])
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "＿", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, `\`, "/")
	return s
}
