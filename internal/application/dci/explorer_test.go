package dci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	skillbootstrap "github.com/Nyukimin/picoclaw_multiLLM/internal/application/skillgovernance"
	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

type memoryTraceStore struct {
	traces []domaindci.SearchTrace
}

func (s *memoryTraceStore) SaveSearchTrace(_ context.Context, trace domaindci.SearchTrace) error {
	s.traces = append(s.traces, trace)
	return nil
}

func TestExplorerSearchFindsEvidenceInsideAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "# DCI\nDirect Corpus Interaction is evidence lookup.\n")
	store := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:         true,
		Allowlist:       []string{dir},
		MaxEvidence:     3,
		MaxFilesRead:    5,
		MaxSnippetChars: 120,
		Now:             fixedNow,
	}, store)

	result, err := explorer.Search(context.Background(), "Corpus Interaction")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) == 0 {
		t.Fatal("expected evidence")
	}
	ev := result.Pack.Evidence[0]
	if ev.FilePath != filepath.Join(dir, "spec.md") {
		t.Fatalf("file path = %s", ev.FilePath)
	}
	if ev.LineStart != 2 {
		t.Fatalf("line start = %d", ev.LineStart)
	}
	if len(store.traces) != 1 || store.traces[0].FinalEvidenceCount == 0 {
		t.Fatalf("trace not saved with evidence: %#v", store.traces)
	}
}

func TestExplorerSearchSkipsDenylist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "public.md"), "public DCI note\n")
	writeFile(t, filepath.Join(dir, ".env"), "DCI_SECRET=do-not-read\n")
	explorer := NewExplorer(Config{
		Enabled:          true,
		Allowlist:        []string{dir},
		DenylistPatterns: []string{".env", "secret"},
		MaxEvidence:      10,
		MaxFilesRead:     10,
		Now:              fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "DCI_SECRET")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 0 {
		t.Fatalf("denylisted evidence leaked: %#v", result.Pack.Evidence)
	}
	if len(result.Pack.Limitations) == 0 {
		t.Fatal("expected limitation when no evidence found")
	}
}

func TestExplorerSearchStopsAtMaxSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	writeFile(t, filepath.Join(dir, "b.md"), "DCI late evidence\n")
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxSteps:     1,
		MaxEvidence:  10,
		MaxFilesRead: 10,
		Now:          fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Trace.Steps) != 2 || result.Trace.Steps[1].Tool != "limit" {
		t.Fatalf("expected read step followed by limit step, got %#v", result.Trace.Steps)
	}
	if len(result.Pack.Limitations) == 0 || result.Pack.Limitations[0] != "max search steps reached" {
		t.Fatalf("expected max steps limitation, got %#v", result.Pack.Limitations)
	}
	if len(result.Pack.Evidence) != 0 {
		t.Fatalf("expected no evidence after step limit, got %#v", result.Pack.Evidence)
	}
}

func TestExplorerSearchRanksPathMatchesBeforeWalkOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "zz_dci_target.md")
	writeFile(t, target, "DCI ranked evidence\n")
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one ranked evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected ranked target first, got %s", result.Pack.Evidence[0].FilePath)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only ranked target to be read, got %#v", result.Trace.Steps)
	}
}

func TestExplorerSearchRanksContentMatchesBeforeWalkOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "本文だけに Direct Corpus Interaction の根拠がある\n")
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "Direct Corpus Interaction")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one content-ranked evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected content-ranked target first, got %s", result.Pack.Evidence[0].FilePath)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only content-ranked target to be read, got %#v", result.Trace.Steps)
	}
}

type dciSourceMetadataRanker struct {
	ranks []domaindci.SourceMetadataRank
	err   error
	paths []string
	terms []string
}

func (r *dciSourceMetadataRanker) RankDCICandidateFiles(_ context.Context, paths []string, terms []string) ([]domaindci.SourceMetadataRank, error) {
	r.paths = append([]string(nil), paths...)
	r.terms = append([]string(nil), terms...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]domaindci.SourceMetadataRank(nil), r.ranks...), nil
}

type dciSourceCandidateProvider struct {
	ranks []domaindci.SourceMetadataRank
	err   error
	query string
	terms []string
	limit int
	calls int
}

func (p *dciSourceCandidateProvider) CandidateFiles(_ context.Context, query string, terms []string, _ []string, limit int) ([]domaindci.SourceMetadataRank, error) {
	p.calls++
	p.query = query
	p.terms = append([]string(nil), terms...)
	p.limit = limit
	if p.err != nil {
		return nil, p.err
	}
	return append([]domaindci.SourceMetadataRank(nil), p.ranks...), nil
}

func TestExplorerSearchUsesFTSCandidateProviderBeforeWalkLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "DCI FTS narrowed evidence\n")
	provider := &dciSourceCandidateProvider{
		ranks: []domaindci.SourceMetadataRank{{
			FilePath: target,
			SourceID: "kb_fts_src",
			Score:    1.20,
			Reason:   "l1 knowledge FTS matched local corpus candidate",
		}},
	}
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 1,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceCandidateProvider(provider))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if provider.query != "DCI" || provider.limit != 1 || len(provider.terms) != 1 || provider.terms[0] != "dci" {
		t.Fatalf("provider input mismatch query=%q limit=%d terms=%#v", provider.query, provider.limit, provider.terms)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target || result.Pack.Evidence[0].SourceID != "kb_fts_src" {
		t.Fatalf("expected FTS narrowed target with source id, got %#v", result.Pack.Evidence[0])
	}
}

func TestExplorerSearchCombinesMultipleCandidateProviders(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	semanticTarget := filepath.Join(dir, "semantic.md")
	writeFile(t, semanticTarget, "DCI semantic narrowed evidence\n")
	ftsTarget := filepath.Join(dir, "fts.md")
	writeFile(t, ftsTarget, "DCI FTS narrowed evidence\n")
	fts := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{
		FilePath: ftsTarget,
		SourceID: "kb_fts_src",
		Score:    1.10,
		Reason:   "l1 knowledge FTS matched local corpus candidate",
	}}}
	semantic := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{
		FilePath: semanticTarget,
		SourceID: "kb_vector_src",
		Score:    2.10,
		Reason:   "vector kb semantic match narrowed local corpus candidate",
	}}}
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 3,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceCandidateProvider(fts), WithSourceCandidateProvider(semantic))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if fts.calls != 1 || semantic.calls != 1 {
		t.Fatalf("expected both providers to be called: fts=%d semantic=%d", fts.calls, semantic.calls)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != semanticTarget || result.Pack.Evidence[0].SourceID != "kb_vector_src" {
		t.Fatalf("expected semantic-ranked target first, got %#v", result.Pack.Evidence[0])
	}
}

func TestExplorerSearchUsesSourceRegistryMetadataRank(t *testing.T) {
	dir := t.TempDir()
	early := filepath.Join(dir, "a.md")
	writeFile(t, early, "DCI low priority evidence\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "DCI metadata ranked evidence\n")
	ranker := &dciSourceMetadataRanker{
		ranks: []domaindci.SourceMetadataRank{{
			FilePath: target,
			SourceID: "src_ranked_spec",
			Score:    0.95,
			Reason:   "validated source registry metadata match",
		}},
	}
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceMetadataRanker(ranker))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(ranker.paths) != 2 {
		t.Fatalf("expected ranker to receive candidates, got %#v", ranker.paths)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected metadata ranked target, got %s", result.Pack.Evidence[0].FilePath)
	}
	if result.Pack.Evidence[0].SourceID != "src_ranked_spec" {
		t.Fatalf("expected source id from metadata rank, got %#v", result.Pack.Evidence[0])
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only metadata ranked file to be read, got %#v", result.Trace.Steps)
	}
}

func TestExplorerSearchContinuesWhenSourceMetadataRankerFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "spec.md")
	writeFile(t, target, "DCI direct evidence\n")
	ranker := &dciSourceMetadataRanker{err: errors.New("source registry offline")}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxFilesRead: 1,
		MaxEvidence:  1,
		Now:          fixedNow,
	}, nil, WithSourceMetadataRanker(ranker))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected direct evidence after metadata rank failure, got %#v", result.Pack.Evidence)
	}
	if len(result.Pack.Limitations) == 0 {
		t.Fatalf("expected metadata ranking limitation")
	}
	if result.Pack.Limitations[0] != "source registry metadata ranking unavailable: source registry offline" {
		t.Fatalf("unexpected limitation: %#v", result.Pack.Limitations)
	}
}

type captureToolRunner struct {
	calls []toolCall
}

type toolCall struct {
	name string
	args map[string]any
}

func (r *captureToolRunner) ExecuteV2(_ context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	r.calls = append(r.calls, toolCall{name: toolName, args: args})
	return tool.NewSuccess("tool mediated DCI evidence\n"), nil
}

func (r *captureToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "file_read"}}, nil
}

func TestExplorerSearchUsesToolRunnerForFileReadWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "spec.md")
	writeFile(t, target, "fallback content should not be used\n")
	runner := &captureToolRunner{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  3,
		MaxFilesRead: 5,
		Now:          fixedNow,
	}, nil, WithToolRunner(runner))

	result, err := explorer.Search(context.Background(), "mediated")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected content ranking and final file_read calls, got %d", len(runner.calls))
	}
	for _, call := range runner.calls {
		if call.name != "file_read" {
			t.Fatalf("tool = %s", call.name)
		}
		if call.args["path"] != target {
			t.Fatalf("path arg = %#v", call.args["path"])
		}
		if _, ok := call.args["limit"]; !ok {
			t.Fatalf("expected bounded file_read limit, got %#v", call.args)
		}
	}
	if len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].Snippet != "tool mediated DCI evidence" {
		t.Fatalf("expected tool response evidence, got %#v", result.Pack.Evidence)
	}
}

func TestExplorerShouldTriggerOnlyExplicitKeywords(t *testing.T) {
	explorer := NewExplorer(Config{
		Enabled:          true,
		ExplicitKeywords: []string{"探して", "grep", "原文"},
	}, nil)

	if !explorer.ShouldTrigger("仕様書から探して") {
		t.Fatal("expected explicit DCI trigger")
	}
	if explorer.ShouldTrigger("普通に雑談しよう") {
		t.Fatal("did not expect DCI trigger")
	}
}

type dciBootstrapStore struct {
	manifests []domainskill.SkillManifest
	logs      []domainskill.SkillTriggerLog
}

func (s *dciBootstrapStore) ListSkillManifests(_ context.Context, _ int) ([]domainskill.SkillManifest, error) {
	return append([]domainskill.SkillManifest(nil), s.manifests...), nil
}

func (s *dciBootstrapStore) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	s.logs = append(s.logs, log)
	return nil
}

func TestExplorerSearchRecordsSkillBootstrap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI evidence\n")
	store := &dciBootstrapStore{
		manifests: []domainskill.SkillManifest{{
			SkillID:         "core.dci-search",
			Enabled:         true,
			IntentTriggers:  []string{"dci_search"},
			KeywordTriggers: []string{"原文"},
		}},
	}
	skills := skillbootstrap.NewBootstrapService(store).WithNow(fixedNow)
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, nil, WithSkillBootstrap(skills))

	if _, err := explorer.Search(context.Background(), "原文を探して"); err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(store.logs) != 1 {
		t.Fatalf("expected one skill log, got %#v", store.logs)
	}
	if store.logs[0].SkillID != "core.dci-search" || store.logs[0].Status != domainskill.TriggerStatusTriggered {
		t.Fatalf("unexpected skill log: %#v", store.logs[0])
	}
}

type dciSourceCandidateStore struct {
	results []domaindci.SearchResult
}

func (s *dciSourceCandidateStore) SaveDCISourceCandidates(_ context.Context, result domaindci.SearchResult) error {
	s.results = append(s.results, result)
	return nil
}

func TestExplorerSearchSavesSourceCandidatesForEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI candidate evidence\n")
	candidates := &dciSourceCandidateStore{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, &memoryTraceStore{}, WithSourceCandidateStore(candidates))

	result, err := explorer.Search(context.Background(), "candidate")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if len(candidates.results) != 1 || len(candidates.results[0].Pack.Evidence) != 1 {
		t.Fatalf("source candidates were not saved: %#v", candidates.results)
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
}
