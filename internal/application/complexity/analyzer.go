package complexity

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
)

type ScanRequest struct {
	ScanID            string
	WorkstreamID      string
	Repo              string
	RootPath          string
	ScanScope         []string
	MaxHotspots       int
	ExcludeDirs       []string
	CandidatePatterns []string
}

type Analyzer struct {
	now func() time.Time
}

func NewAnalyzer() *Analyzer {
	return &Analyzer{now: time.Now}
}

func (a *Analyzer) Scan(req ScanRequest) (domaincomplexity.ScanResult, error) {
	if strings.TrimSpace(req.ScanID) == "" {
		return domaincomplexity.ScanResult{}, fmt.Errorf("scan_id is required")
	}
	if strings.TrimSpace(req.Repo) == "" {
		return domaincomplexity.ScanResult{}, fmt.Errorf("repo is required")
	}
	root := strings.TrimSpace(req.RootPath)
	if root == "" {
		root = "."
	}
	maxHotspots := req.MaxHotspots
	if maxHotspots <= 0 {
		maxHotspots = 20
	}
	excludeDirs := defaultExcludeDirs(req.ExcludeDirs)
	files, err := collectFiles(root, req.ScanScope, excludeDirs)
	if err != nil {
		return domaincomplexity.ScanResult{}, err
	}
	if len(req.CandidatePatterns) > 0 {
		files, err = filterCandidateFiles(files, req.CandidatePatterns)
		if err != nil {
			return domaincomplexity.ScanResult{}, err
		}
	}
	now := a.now()
	var hotspots []domaincomplexity.Hotspot
	var evidence []domaincomplexity.HotspotEvidence
	for _, file := range files {
		if len(hotspots) >= maxHotspots {
			break
		}
		lines, err := readLines(file)
		if err != nil {
			continue
		}
		rel := relativePath(root, file)
		fileHotspots, fileEvidence := detectFileHotspots(req.ScanID, rel, lines, now)
		for i := range fileHotspots {
			if len(hotspots) >= maxHotspots {
				break
			}
			hotspots = append(hotspots, fileHotspots[i])
			evidence = append(evidence, fileEvidence[i])
		}
	}
	scan := domaincomplexity.ScanEvent{
		ScanID:        req.ScanID,
		WorkstreamID:  req.WorkstreamID,
		Repo:          req.Repo,
		ScanScope:     req.ScanScope,
		Mode:          "report_only",
		FilesScanned:  len(files),
		HotspotsFound: len(hotspots),
		Status:        "completed",
		CreatedAt:     now,
		CompletedAt:   now,
	}
	return domaincomplexity.ScanResult{Scan: scan, Hotspots: hotspots, Evidence: evidence}, nil
}

func filterCandidateFiles(files []string, patterns []string) ([]string, error) {
	var normalized []string
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			normalized = append(normalized, pattern)
		}
	}
	if len(normalized) == 0 {
		return files, nil
	}
	out := make([]string, 0, len(files))
	for _, file := range files {
		lines, err := readLines(file)
		if err != nil {
			continue
		}
		body := strings.Join(lines, "\n")
		for _, pattern := range normalized {
			if strings.Contains(body, pattern) {
				out = append(out, file)
				break
			}
		}
	}
	return out, nil
}

func collectFiles(root string, scopes []string, excludeDirs map[string]struct{}) ([]string, error) {
	if len(scopes) == 0 {
		scopes = []string{"."}
	}
	var files []string
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		start := filepath.Clean(filepath.Join(root, scope))
		if err := filepath.WalkDir(start, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if _, ok := excludeDirs[d.Name()]; ok {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldScanFile(path) {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func shouldScanFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".js", ".mjs", ".ts", ".tsx", ".jsx", ".py":
		return true
	default:
		return false
	}
}

func defaultExcludeDirs(extra []string) map[string]struct{} {
	names := []string{"node_modules", ".venv", "venv", "dist", "build", "coverage", ".git"}
	names = append(names, extra...)
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func detectFileHotspots(scanID, filePath string, lines []string, now time.Time) ([]domaincomplexity.Hotspot, []domaincomplexity.HotspotEvidence) {
	var hotspots []domaincomplexity.Hotspot
	var evidence []domaincomplexity.HotspotEvidence
	add := func(kind string, lineStart int, lineEnd int, complexity string, after string, risk string, score float64, confidence float64, summary string, improvement string, tests []string, reason string) {
		hotspotID := fmt.Sprintf("%s_%s_%d", scanID, kind, len(hotspots)+1)
		snippet := snippet(lines, lineStart, lineEnd)
		hotspots = append(hotspots, domaincomplexity.Hotspot{
			HotspotID:            hotspotID,
			ScanID:               scanID,
			FilePath:             filePath,
			LineStart:            lineStart,
			LineEnd:              lineEnd,
			HotspotType:          kind,
			EstimatedComplexity:  complexity,
			EstimatedAfter:       after,
			RiskLevel:            risk,
			PriorityScore:        score,
			Confidence:           confidence,
			Summary:              summary,
			SuggestedImprovement: improvement,
			RequiredTests:        tests,
			CreatedAt:            now,
		})
		evidence = append(evidence, domaincomplexity.HotspotEvidence{
			EvidenceID: fmt.Sprintf("%s_ev_%d", hotspotID, len(evidence)+1),
			HotspotID:  hotspotID,
			FilePath:   filePath,
			LineStart:  lineStart,
			LineEnd:    lineEnd,
			Snippet:    snippet,
			Reason:     reason,
			CreatedAt:  now,
		})
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		window := joinWindow(lines, i, 8)
		lineNo := i + 1
		if looksLikeLoop(trimmed) && strings.Count(window, "for ") >= 2 {
			add("nested_loop", lineNo, min(lineNo+8, len(lines)), "O(n^2)", "O(n)", "medium", 0.74, 0.68, "loop scope contains another loop", "事前index化または走査回数削減を検討する", commonTests(), "loop inside nearby loop")
			continue
		}
		if strings.Contains(trimmed, ".map(") && (strings.Contains(window, ".find(") || strings.Contains(window, ".filter(")) {
			add("nested_lookup", lineNo, min(lineNo+8, len(lines)), "O(n*m)", "O(n)", "medium", 0.78, 0.72, "map path contains find/filter lookup", "Map / Set によるlookup化を検討する", commonTests(), "map with repeated lookup")
			continue
		}
		if strings.Contains(window, ".find(") && strings.Count(window, ".find(") >= 2 {
			add("repeated_lookup", lineNo, min(lineNo+8, len(lines)), "O(k*n)", "O(n)", "low", 0.66, 0.65, "same window repeats find lookup", "事前index化または中間結果再利用を検討する", commonTests(), "repeated find in nearby lines")
			continue
		}
		if looksLikeLoop(trimmed) && (strings.Contains(window, ".Query(") || strings.Contains(window, ".QueryContext(") || strings.Contains(window, "fetch(") || strings.Contains(window, "http.Get(")) {
			add("n_plus_one_candidate", lineNo, min(lineNo+8, len(lines)), "O(n*io)", "O(io)", "high", 0.85, 0.7, "loop scope contains DB/API access", "batch query / prefetch / bulk API を検討する", nPlusOneTests(), "loop with DB/API call")
			continue
		}
		if renderHotspotFile(filePath) && (strings.Contains(trimmed, ".sort(") || strings.Contains(trimmed, ".filter(")) {
			add("render_hotspot", lineNo, lineNo, "O(n log n)", "O(n)", "medium", 0.6, 0.55, "UI file performs collection operation in render path candidate", "memoization / selector cache / component split を検討する", uiTests(), "UI collection operation")
			continue
		}
	}
	return hotspots, evidence
}

func looksLikeLoop(line string) bool {
	return strings.HasPrefix(line, "for ") || strings.Contains(line, ".map(") || strings.Contains(line, ".forEach(")
}

func renderHotspotFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx"
}

func joinWindow(lines []string, index int, size int) string {
	end := min(index+size, len(lines))
	return strings.Join(lines[index:end], "\n")
}

func snippet(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd < lineStart {
		lineEnd = lineStart
	}
	if lineStart > len(lines) {
		return ""
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func commonTests() []string {
	return []string{"empty collection", "single item", "duplicate data", "null or missing value", "order-sensitive data", "large data"}
}

func nPlusOneTests() []string {
	return []string{"same result count", "missing related data", "permission condition", "query count", "error response"}
}

func uiTests() []string {
	return []string{"same rendered result", "same filter result", "same sort order", "stable keys", "mobile display"}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
