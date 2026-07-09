package dci

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	skillbootstrap "github.com/Nyukimin/RenCrow_CORE/internal/application/skillgovernance"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type TraceStore interface {
	SaveSearchTrace(ctx context.Context, trace domaindci.SearchTrace) error
}

type ResultStore interface {
	SaveSearchResult(ctx context.Context, result domaindci.SearchResult) error
}

type SourceCandidateStore interface {
	SaveDCISourceCandidates(ctx context.Context, result domaindci.SearchResult) error
}

type SourceMetadataRanker interface {
	RankDCICandidateFiles(ctx context.Context, paths []string, terms []string) ([]domaindci.SourceMetadataRank, error)
}

type SourceCandidateProvider interface {
	CandidateFiles(ctx context.Context, query string, terms []string, allowlist []string, limit int) ([]domaindci.SourceMetadataRank, error)
}

type Config struct {
	Enabled           bool
	Allowlist         []string
	DenylistPatterns  []string
	ExplicitKeywords  []string
	MaxSeconds        int
	MaxSteps          int
	MaxCandidateFiles int
	MaxFilesRead      int
	MaxEvidence       int
	MaxSnippetChars   int
	Now               func() time.Time
}

var errSearchLimitReached = errors.New("dci search limit reached")

type Explorer struct {
	cfg              Config
	store            TraceStore
	toolRunner       tool.RunnerV2
	skills           *skillbootstrap.BootstrapService
	sourceCandidates SourceCandidateStore
	sourceRanker     SourceMetadataRanker
	sourceProviders  []SourceCandidateProvider
}

type Option func(*Explorer)

func WithToolRunner(runner tool.RunnerV2) Option {
	return func(e *Explorer) {
		e.toolRunner = runner
	}
}

func WithSkillBootstrap(service *skillbootstrap.BootstrapService) Option {
	return func(e *Explorer) {
		e.skills = service
	}
}

func WithSourceCandidateStore(store SourceCandidateStore) Option {
	return func(e *Explorer) {
		e.sourceCandidates = store
	}
}

func WithSourceMetadataRanker(ranker SourceMetadataRanker) Option {
	return func(e *Explorer) {
		e.sourceRanker = ranker
	}
}

func WithSourceCandidateProvider(provider SourceCandidateProvider) Option {
	return func(e *Explorer) {
		if provider != nil {
			e.sourceProviders = append(e.sourceProviders, provider)
		}
	}
}

func NewExplorer(cfg Config, store TraceStore, opts ...Option) *Explorer {
	if cfg.MaxSeconds <= 0 {
		cfg.MaxSeconds = 10
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.MaxCandidateFiles <= 0 {
		cfg.MaxCandidateFiles = 50
	}
	if cfg.MaxFilesRead <= 0 {
		cfg.MaxFilesRead = 10
	}
	if cfg.MaxEvidence <= 0 {
		cfg.MaxEvidence = 6
	}
	if cfg.MaxSnippetChars <= 0 {
		cfg.MaxSnippetChars = 800
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	e := &Explorer{cfg: cfg, store: store}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

func (e *Explorer) ShouldTrigger(query string) bool {
	if !e.cfg.Enabled {
		return false
	}
	normalized := strings.ToLower(query)
	for _, keyword := range e.cfg.ExplicitKeywords {
		if keyword != "" && strings.Contains(normalized, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func (e *Explorer) Search(ctx context.Context, query string) (domaindci.SearchResult, error) {
	if err := e.recordSkillBootstrap(ctx, query); err != nil {
		return domaindci.SearchResult{}, err
	}
	if e.cfg.MaxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(e.cfg.MaxSeconds)*time.Second)
		defer cancel()
	}
	started := e.cfg.Now().UTC()
	eventID := fmt.Sprintf("evt_dci_%d", started.UnixNano())
	trace := domaindci.SearchTrace{
		EventID:     eventID,
		StartedAt:   started,
		Actor:       "Worker",
		Mode:        "dci",
		UserQuery:   query,
		CorpusScope: append([]string(nil), e.cfg.Allowlist...),
		Status:      "completed",
	}
	pack := domaindci.EvidencePack{
		EventID:     eventID,
		Query:       query,
		Intent:      "direct corpus evidence lookup",
		CorpusScope: append([]string(nil), e.cfg.Allowlist...),
	}
	if len(e.cfg.Allowlist) == 0 {
		pack.Limitations = append(pack.Limitations, "no corpus allowlist configured")
		trace.Status = "completed"
		trace.FinalEvidenceCount = 0
		trace.EndedAt = e.cfg.Now().UTC()
		_ = e.saveResult(ctx, domaindci.SearchResult{Pack: pack, Trace: trace})
		return domaindci.SearchResult{Pack: pack, Trace: trace}, nil
	}

	terms := queryTerms(query)
	stepNo := 1
	limitReached := false
	reachLimit := func(reason string) error {
		if !limitReached {
			limitReached = true
			pack.Limitations = append(pack.Limitations, reason)
			trace.Steps = append(trace.Steps, e.step(stepNo, "limit", reason, 0, "stopped", reason))
			stepNo++
		}
		return errSearchLimitReached
	}
	candidates, seedRanks, collectErr := e.collectCandidateFiles(ctx, query, terms, &pack)
	if collectErr != nil {
		trace.Status = "failed"
		trace.ErrorMessage = collectErr.Error()
	}
	sourceRanks := e.rankCandidateFiles(ctx, candidates, terms, &pack)
	sourceRanks = mergeSourceMetadataRanks(sourceRanks, seedRanks)
	contentRanks := e.rankCandidateFilesByContent(ctx, candidates, terms, &pack)
	sortCandidateFilesWithRank(candidates, terms, sourceRanks, contentRanks)
	filesRead := 0
	for _, path := range candidates {
		if ctx.Err() != nil {
			trace.Status = "failed"
			trace.ErrorMessage = ctx.Err().Error()
			break
		}
		if limitReached || stepNo > e.cfg.MaxSteps {
			_ = reachLimit("max search steps reached")
			break
		}
		if filesRead >= e.cfg.MaxFilesRead || len(pack.Evidence) >= e.cfg.MaxEvidence {
			break
		}
		filesRead++
		matches, readErr := e.scanFile(ctx, path, terms, sourceRanks[path])
		status := "ok"
		errMsg := ""
		if readErr != nil {
			status = "error"
			errMsg = readErr.Error()
		}
		trace.Steps = append(trace.Steps, e.step(stepNo, "read_file", path, len(matches), status, errMsg))
		stepNo++
		for _, evidence := range matches {
			if len(pack.Evidence) >= e.cfg.MaxEvidence {
				break
			}
			evidence.EvidenceID = fmt.Sprintf("%s_ev_%03d", eventID, len(pack.Evidence)+1)
			pack.Evidence = append(pack.Evidence, evidence)
		}
	}
	if len(pack.Evidence) == 0 && trace.ErrorMessage == "" {
		pack.Limitations = append(pack.Limitations, "no evidence found in allowed corpus")
	}
	if len(pack.Evidence) > 0 {
		pack.Confidence = 0.70
	}
	trace.FinalEvidenceCount = len(pack.Evidence)
	trace.EndedAt = e.cfg.Now().UTC()
	result := domaindci.SearchResult{Pack: pack, Trace: trace}
	if err := e.saveResult(ctx, result); err != nil {
		return domaindci.SearchResult{Pack: pack, Trace: trace}, err
	}
	if err := e.saveSourceCandidates(ctx, result); err != nil {
		return domaindci.SearchResult{Pack: pack, Trace: trace}, err
	}
	return result, nil
}

func (e *Explorer) collectCandidateFiles(ctx context.Context, query string, terms []string, pack *domaindci.EvidencePack) ([]string, map[string]domaindci.SourceMetadataRank, error) {
	maxCandidates := e.cfg.MaxCandidateFiles
	if maxCandidates <= 0 {
		maxCandidates = 50
	}
	candidates := make([]string, 0, maxCandidates)
	seen := make(map[string]struct{}, maxCandidates)
	seedRanks := make(map[string]domaindci.SourceMetadataRank)
	addCandidate := func(path string, rank domaindci.SourceMetadataRank) {
		if len(candidates) >= maxCandidates || path == "" || e.pathDenied(path) || !e.pathAllowed(path) {
			return
		}
		if _, ok := seen[path]; ok {
			if rank.Score > 0 {
				if current, exists := seedRanks[path]; !exists || rank.Score > current.Score {
					rank.FilePath = path
					seedRanks[path] = rank
				}
			}
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
		if rank.Score > 0 {
			rank.FilePath = path
			seedRanks[path] = rank
		}
	}
	for _, provider := range e.sourceProviders {
		if provider == nil || len(candidates) >= maxCandidates {
			continue
		}
		remaining := maxCandidates - len(candidates)
		ranks, err := provider.CandidateFiles(ctx, query, terms, append([]string(nil), e.cfg.Allowlist...), remaining)
		if err != nil {
			if pack != nil {
				pack.Limitations = append(pack.Limitations, "dci candidate provider unavailable: "+err.Error())
			}
			continue
		}
		for _, rank := range ranks {
			addCandidate(rank.FilePath, rank)
		}
	}
	for _, root := range e.cfg.Allowlist {
		if ctx.Err() != nil {
			return candidates, seedRanks, ctx.Err()
		}
		if e.pathDenied(root) {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			if e.pathDenied(path) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			addCandidate(path, domaindci.SourceMetadataRank{})
			if len(candidates) >= maxCandidates {
				return errSearchLimitReached
			}
			return nil
		})
		if errors.Is(walkErr, errSearchLimitReached) {
			break
		}
		if walkErr != nil {
			return candidates, seedRanks, walkErr
		}
		if len(candidates) >= maxCandidates {
			break
		}
	}
	return candidates, seedRanks, nil
}

func (e *Explorer) recordSkillBootstrap(ctx context.Context, query string) error {
	if e.skills == nil {
		return nil
	}
	_, err := e.skills.Record(ctx, domainskill.TaskContext{
		Text:   query,
		Intent: "dci_search",
		Agent:  "Worker",
	}, []string{"core.dci-search", "core.dci"})
	if err != nil {
		return fmt.Errorf("dci skill bootstrap failed: %w", err)
	}
	return nil
}

func (e *Explorer) saveResult(ctx context.Context, result domaindci.SearchResult) error {
	if e.store == nil {
		return nil
	}
	if store, ok := e.store.(ResultStore); ok {
		return store.SaveSearchResult(ctx, result)
	}
	return e.store.SaveSearchTrace(ctx, result.Trace)
}

func (e *Explorer) saveSourceCandidates(ctx context.Context, result domaindci.SearchResult) error {
	if e.sourceCandidates == nil || len(result.Pack.Evidence) == 0 {
		return nil
	}
	if err := e.sourceCandidates.SaveDCISourceCandidates(ctx, result); err != nil {
		return fmt.Errorf("dci source candidate save failed: %w", err)
	}
	return nil
}

func (e *Explorer) rankCandidateFiles(ctx context.Context, candidates []string, terms []string, pack *domaindci.EvidencePack) map[string]domaindci.SourceMetadataRank {
	if e.sourceRanker == nil || len(candidates) == 0 {
		return nil
	}
	ranks, err := e.sourceRanker.RankDCICandidateFiles(ctx, append([]string(nil), candidates...), append([]string(nil), terms...))
	if err != nil {
		if pack != nil {
			pack.Limitations = append(pack.Limitations, "source registry metadata ranking unavailable: "+err.Error())
		}
		return nil
	}
	out := make(map[string]domaindci.SourceMetadataRank, len(ranks))
	for _, rank := range ranks {
		if rank.FilePath == "" || rank.Score <= 0 {
			continue
		}
		out[rank.FilePath] = rank
	}
	return out
}

func (e *Explorer) rankCandidateFilesByContent(ctx context.Context, candidates []string, terms []string, pack *domaindci.EvidencePack) map[string]int {
	if len(candidates) == 0 || len(terms) == 0 {
		return nil
	}
	out := make(map[string]int, len(candidates))
	for _, path := range candidates {
		if ctx.Err() != nil {
			if pack != nil {
				pack.Limitations = append(pack.Limitations, "content ranking stopped: "+ctx.Err().Error())
			}
			return out
		}
		if e.pathDenied(path) {
			continue
		}
		content, err := e.readCandidateRankContent(ctx, path)
		if err != nil {
			continue
		}
		score := contentCandidateScore(content, terms)
		if score > 0 {
			out[path] = score
		}
	}
	return out
}

func (e *Explorer) readCandidateRankContent(ctx context.Context, path string) (string, error) {
	if e.toolRunner != nil {
		return e.readFileViaTool(ctx, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (e *Explorer) scanFile(ctx context.Context, path string, terms []string, sourceRank domaindci.SourceMetadataRank) ([]domaindci.Evidence, error) {
	if e.toolRunner != nil {
		content, err := e.readFileViaTool(ctx, path)
		if err != nil {
			return nil, err
		}
		return e.scanText(path, content, terms, sourceRank), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := readScannerContent(f)
	if err != nil {
		return nil, err
	}
	return e.scanText(path, content, terms, sourceRank), nil
}

func (e *Explorer) readFileViaTool(ctx context.Context, path string) (string, error) {
	resp, err := e.toolRunner.ExecuteV2(ctx, "file_read", map[string]any{
		"path":   path,
		"limit":  10000,
		"offset": 0,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("file_read returned nil response")
	}
	if resp.IsError() {
		return "", resp.Error
	}
	return resp.String(), nil
}

func readScannerContent(f *os.File) (string, error) {
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func (e *Explorer) scanText(path string, content string, terms []string, sourceRank domaindci.SourceMetadataRank) []domaindci.Evidence {
	var out []domaindci.Evidence
	for index, line := range strings.Split(content, "\n") {
		if !lineMatches(line, terms) {
			continue
		}
		snippet := strings.TrimSpace(line)
		if len(snippet) > e.cfg.MaxSnippetChars {
			snippet = snippet[:e.cfg.MaxSnippetChars]
		}
		out = append(out, domaindci.Evidence{
			FilePath:   path,
			LineStart:  index + 1,
			LineEnd:    index + 1,
			Snippet:    snippet,
			Reason:     "query term matched allowed corpus line",
			Confidence: 0.70,
		})
		if sourceRank.SourceID != "" {
			out[len(out)-1].SourceID = sourceRank.SourceID
			out[len(out)-1].Reason = "query term matched allowed corpus line with source registry metadata"
			if sourceRank.Score > 0 {
				out[len(out)-1].Confidence = minFloat(0.95, 0.70+sourceRank.Score/10)
			}
		}
		if len(out) >= e.cfg.MaxEvidence {
			break
		}
	}
	return out
}

func (e *Explorer) step(no int, toolName, path string, count int, status string, errMsg string) domaindci.SearchStep {
	return domaindci.SearchStep{
		StepNo:       no,
		Tool:         toolName,
		CommandText:  toolName + " " + path,
		FilePath:     path,
		ResultCount:  count,
		Status:       status,
		ErrorMessage: errMsg,
		CreatedAt:    e.cfg.Now().UTC(),
	}
}

func (e *Explorer) pathDenied(path string) bool {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	for _, pattern := range e.cfg.DenylistPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
		if strings.Contains(clean, pattern) {
			return true
		}
	}
	return false
}

func (e *Explorer) pathAllowed(path string) bool {
	if len(e.cfg.Allowlist) == 0 {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range e.cfg.Allowlist {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if target == absRoot {
			return true
		}
		rel, err := filepath.Rel(absRoot, target)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func queryTerms(query string) []string {
	re := regexp.MustCompile(`[\p{L}\p{N}_\-.]+`)
	raw := re.FindAllString(query, -1)
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, term := range raw {
		term = strings.TrimSpace(strings.ToLower(term))
		if len([]rune(term)) < 2 || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

func sortCandidateFiles(paths []string, terms []string) {
	sortCandidateFilesWithMetadata(paths, terms, nil)
}

func sortCandidateFilesWithMetadata(paths []string, terms []string, metadata map[string]domaindci.SourceMetadataRank) {
	sortCandidateFilesWithRank(paths, terms, metadata, nil)
}

func sortCandidateFilesWithRank(paths []string, terms []string, metadata map[string]domaindci.SourceMetadataRank, contentRanks map[string]int) {
	sort.SliceStable(paths, func(i, j int) bool {
		left := candidateFileScore(paths[i], terms) + metadataCandidateScore(paths[i], metadata) + contentRanks[paths[i]]
		right := candidateFileScore(paths[j], terms) + metadataCandidateScore(paths[j], metadata) + contentRanks[paths[j]]
		if left != right {
			return left > right
		}
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		return paths[i] < paths[j]
	})
}

func mergeSourceMetadataRanks(base map[string]domaindci.SourceMetadataRank, seed map[string]domaindci.SourceMetadataRank) map[string]domaindci.SourceMetadataRank {
	if len(seed) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]domaindci.SourceMetadataRank, len(seed))
	}
	for path, rank := range seed {
		if rank.FilePath == "" {
			rank.FilePath = path
		}
		if current, ok := base[path]; !ok || rank.Score > current.Score {
			base[path] = rank
		}
	}
	return base
}

func metadataCandidateScore(path string, metadata map[string]domaindci.SourceMetadataRank) int {
	if metadata == nil {
		return 0
	}
	rank := metadata[path]
	if rank.Score <= 0 {
		return 0
	}
	return int(rank.Score * 100)
}

func candidateFileScore(path string, terms []string) int {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	score := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.Contains(base, term) {
			score += 20
		}
		if strings.Contains(lower, term) {
			score += 10
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".go", ".yaml", ".yml", ".json", ".js", ".ts", ".html", ".css":
		score += 3
	}
	return score
}

func contentCandidateScore(content string, terms []string) int {
	lower := strings.ToLower(content)
	if strings.TrimSpace(lower) == "" {
		return 0
	}
	score := 0
	matchedTerms := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		count := strings.Count(lower, term)
		if count <= 0 {
			continue
		}
		matchedTerms++
		score += 30
		if count > 1 {
			score += minInt(count-1, 5) * 4
		}
	}
	if matchedTerms == len(terms) && matchedTerms > 1 {
		score += 25
	}
	return score
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func lineMatches(line string, terms []string) bool {
	lower := strings.ToLower(line)
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}
