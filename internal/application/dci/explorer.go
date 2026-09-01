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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errCandidateCollectionLimit = errors.New("dci candidate collection limit reached")

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
	ActorKind         string
	ActorID           string
	Now               func() time.Time
}

const (
	dciComponentID              = "dci"
	dciSearchRequestedEventType = "dci.search.requested"
	dciSearchStartedEventType   = "dci.search.started"
	dciSourceSelectedEventType  = "dci.source.selected"
	dciFileReadEventType        = "dci.file.read"
	dciEvidenceCreatedEventType = "dci.evidence.created"
	dciSearchCompletedEventType = "dci.search.completed"
	dciSearchFailedEventType    = "dci.search.failed"
	dciRecoveryTimeout          = 5 * time.Second
)

type Explorer struct {
	cfg              Config
	store            TraceStore
	events           modulecore.EventAppender
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

func WithEventAppender(appender modulecore.EventAppender) Option {
	return func(e *Explorer) {
		e.events = appender
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

func newDCIRecoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), dciRecoveryTimeout)
}

func (e *Explorer) Search(ctx context.Context, query string) (domaindci.SearchResult, error) {
	return e.SearchWithIdentity(ctx, query, modulecore.NewTraceID(), modulecore.NewActionID(), e.cfg.ActorKind, e.cfg.ActorID, "")
}

// SearchWithIdentity performs one DCI search with the trusted owner identity
// supplied by the caller. Canonical IDs and actor identity are never inferred
// from model payloads inside the search itself.
func (e *Explorer) SearchWithIdentity(ctx context.Context, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) (domaindci.SearchResult, error) {
	query = strings.TrimSpace(query)
	traceID = modulecore.TraceID(strings.TrimSpace(string(traceID)))
	actionID = modulecore.ActionID(strings.TrimSpace(string(actionID)))
	actorKind = strings.ToLower(strings.TrimSpace(actorKind))
	actorID = strings.TrimSpace(actorID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if query == "" {
		return domaindci.SearchResult{}, fmt.Errorf("dci search query is required")
	}
	if err := traceID.Validate(); err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci search trace_id: %w", err)
	}
	if err := actionID.Validate(); err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci search action_id: %w", err)
	}
	if err := domaindci.ValidateActor(actorKind, actorID); err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci search actor: %w", err)
	}
	if e.events == nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci event appender is required")
	}
	if ctx == nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci search context is required")
	}

	searchCtx := ctx
	var cancel context.CancelFunc
	if e.cfg.MaxSeconds > 0 {
		searchCtx, cancel = context.WithTimeout(ctx, time.Duration(e.cfg.MaxSeconds)*time.Second)
		defer cancel()
	}
	terms := queryTerms(query)
	started := e.cfg.Now().UTC()
	trace := domaindci.SearchTrace{
		TraceID:          traceID,
		ActionID:         actionID,
		StartedAt:        started,
		ActorAttribution: domaindci.ActorAttributionAuthenticated,
		ActorKind:        actorKind,
		ActorID:          actorID,
		IdempotencyKey:   idempotencyKey,
		Mode:             "dci",
		UserQuery:        query,
		CorpusScope:      append([]string(nil), e.cfg.Allowlist...),
		Status:           "completed",
	}
	pack := domaindci.EvidencePack{
		ActionID:     actionID,
		Query:        query,
		Intent:       "direct corpus evidence lookup",
		CorpusScope:  append([]string(nil), e.cfg.Allowlist...),
		DerivedTerms: append([]string(nil), terms...),
	}
	lastEventID := modulecore.EventID("")
	evidenceEventIDs := make([]modulecore.EventID, 0)
	appendEvent := func(appendCtx context.Context, eventType string, cause modulecore.EventID, dependencies []modulecore.EventID, evidenceID modulecore.EvidenceID, payload map[string]any) (modulecore.EventEnvelope, error) {
		event := modulecore.NewEventEnvelope(traceID, cause, dependencies, dciComponentID, eventType, e.cfg.Now().UTC(), payload)
		event.ActionID = actionID
		event.ActorKind = actorKind
		event.ActorID = actorID
		event.EvidenceID = evidenceID
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return modulecore.EventEnvelope{}, fmt.Errorf("dci event %s invalid: %w", eventType, err)
		}
		if err := e.events.Append(appendCtx, event); err != nil {
			return modulecore.EventEnvelope{}, fmt.Errorf("dci event %s append failed: %w", eventType, err)
		}
		return event, nil
	}
	appendTerminal := func(status string, searchErr error) (domaindci.SearchResult, error) {
		if status == "completed" {
			// Build a complete terminal projection before the completed event so
			// ancillary projection failures can be reflected in its payload.
			trace.Status = "completed"
			trace.ErrorMessage = ""
			trace.FinalEvidenceCount = len(pack.Evidence)
			trace.EndedAt = e.cfg.Now().UTC()
			if searchCtx.Err() == nil {
				projection := domaindci.SearchResult{
					Pack:  pack,
					Trace: trace,
				}
				if err := e.saveSourceCandidates(searchCtx, projection); err != nil {
					pack.Limitations = append(pack.Limitations, "dci source candidate save failed")
					if searchCtx.Err() != nil {
						status = "failed"
						searchErr = searchCtx.Err()
					}
				}
			}
			if searchCtx.Err() != nil {
				status = "failed"
				searchErr = searchCtx.Err()
			}
		}
		trace.Status = status
		trace.ErrorMessage = ""
		if searchErr != nil {
			trace.ErrorMessage = searchErr.Error()
		}
		trace.FinalEvidenceCount = len(pack.Evidence)
		trace.EndedAt = e.cfg.Now().UTC()
		payload := map[string]any{
			"status":         status,
			"evidence_count": len(pack.Evidence),
			"limitations":    append([]string(nil), pack.Limitations...),
		}
		terminalType := dciSearchCompletedEventType
		if status == "failed" {
			terminalType = dciSearchFailedEventType
			if searchErr != nil {
				payload["error"] = searchErr.Error()
			}
		}
		terminalCause, terminalDependencies, joinErr := terminalEventJoin(lastEventID, evidenceEventIDs)
		if joinErr != nil {
			return domaindci.SearchResult{Pack: pack, Trace: trace}, joinErr
		}
		persistCtx := searchCtx
		var recoveryCancel context.CancelFunc
		if searchCtx.Err() != nil || errors.Is(searchErr, context.Canceled) || errors.Is(searchErr, context.DeadlineExceeded) {
			persistCtx, recoveryCancel = newDCIRecoveryContext(ctx)
			defer recoveryCancel()
		}
		terminal, err := appendEvent(persistCtx, terminalType, terminalCause, terminalDependencies, "", payload)
		if err != nil {
			return domaindci.SearchResult{Pack: pack, Trace: trace}, err
		}
		lastEventID = terminal.EventID
		result := domaindci.SearchResult{Pack: pack, Trace: trace}
		if err := domaindci.ValidateSearchResult(result); err != nil {
			return result, err
		}
		if err := e.saveResult(persistCtx, result); err != nil {
			if searchErr != nil {
				return result, errors.Join(searchErr, err)
			}
			return result, err
		}
		if searchErr != nil {
			return result, searchErr
		}
		return result, nil
	}
	requested, err := appendEvent(searchCtx, dciSearchRequestedEventType, "", nil, "", map[string]any{
		"query": query,
	})
	if err != nil {
		return domaindci.SearchResult{}, err
	}
	lastEventID = requested.EventID
	startedEvent, err := appendEvent(searchCtx, dciSearchStartedEventType, lastEventID, nil, "", map[string]any{
		"query": query,
	})
	if err != nil {
		return domaindci.SearchResult{}, err
	}
	lastEventID = startedEvent.EventID
	if err := e.recordSkillBootstrap(searchCtx, query, actorID); err != nil {
		return appendTerminal("failed", err)
	}
	if len(e.cfg.Allowlist) == 0 {
		pack.Limitations = append(pack.Limitations, "no corpus allowlist configured")
		return appendTerminal("completed", nil)
	}

	stepNo := 1
	candidates, seedRanks, collectErr := e.collectCandidateFiles(searchCtx, query, terms, &pack)
	if collectErr != nil {
		trace.Status = "failed"
		trace.ErrorMessage = collectErr.Error()
	}
	if searchCtx.Err() != nil {
		return appendTerminal("failed", searchCtx.Err())
	}
	sourceRanks := e.rankCandidateFiles(searchCtx, candidates, terms, &pack)
	sourceRanks = mergeSourceMetadataRanks(sourceRanks, seedRanks)
	contentRanks := e.rankCandidateFilesByContent(searchCtx, candidates, terms, &pack)
	sortCandidateFilesWithRank(candidates, terms, sourceRanks, contentRanks)
	filesRead := 0
	for _, path := range candidates {
		if searchCtx.Err() != nil {
			return appendTerminal("failed", searchCtx.Err())
		}
		if stepNo > e.cfg.MaxSteps {
			pack.Limitations = append(pack.Limitations, "max search steps reached")
			break
		}
		if filesRead >= e.cfg.MaxFilesRead {
			pack.Limitations = append(pack.Limitations, "max files read reached")
			break
		}
		if len(pack.Evidence) >= e.cfg.MaxEvidence {
			pack.Limitations = append(pack.Limitations, "max evidence reached")
			break
		}
		filesRead++
		sourceSelected, err := appendEvent(searchCtx, dciSourceSelectedEventType, lastEventID, nil, "", map[string]any{
			"file_path": path,
		})
		if err != nil {
			return domaindci.SearchResult{}, err
		}
		lastEventID = sourceSelected.EventID
		matches, readErr := e.scanFile(searchCtx, path, terms, sourceRanks[path])
		status := "ok"
		errMsg := ""
		if readErr != nil {
			status = "error"
			errMsg = readErr.Error()
		}
		readAppendCtx := searchCtx
		var readRecoveryCancel context.CancelFunc
		if searchCtx.Err() != nil {
			readAppendCtx, readRecoveryCancel = newDCIRecoveryContext(ctx)
			defer readRecoveryCancel()
		}
		readEvent, err := appendEvent(readAppendCtx, dciFileReadEventType, lastEventID, nil, "", map[string]any{
			"file_path":    path,
			"status":       status,
			"result_count": len(matches),
			"error":        errMsg,
		})
		if err != nil {
			return domaindci.SearchResult{}, err
		}
		lastEventID = readEvent.EventID
		trace.Steps = append(trace.Steps, e.step(readEvent.EventID, stepNo, "read_file", path, len(matches), status, errMsg))
		stepNo++
		if readErr != nil {
			if searchCtx.Err() != nil {
				return appendTerminal("failed", searchCtx.Err())
			}
			continue
		}
		readEventID := readEvent.EventID
		for _, evidence := range matches {
			if len(pack.Evidence) >= e.cfg.MaxEvidence {
				pack.Limitations = append(pack.Limitations, "max evidence reached")
				break
			}
			evidenceID := modulecore.NewEvidenceID()
			evidenceEvent, err := appendEvent(searchCtx, dciEvidenceCreatedEventType, readEventID, nil, evidenceID, map[string]any{
				"file_path":  evidence.FilePath,
				"line_start": evidence.LineStart,
				"line_end":   evidence.LineEnd,
				"snippet":    evidence.Snippet,
				"source_id":  evidence.SourceID,
				"reason":     evidence.Reason,
				"confidence": evidence.Confidence,
			})
			if err != nil {
				return domaindci.SearchResult{}, err
			}
			evidence.EvidenceID = evidenceID
			evidence.CreatedByEventID = evidenceEvent.EventID
			pack.Evidence = append(pack.Evidence, evidence)
			evidenceEventIDs = append(evidenceEventIDs, evidenceEvent.EventID)
			lastEventID = evidenceEvent.EventID
		}
	}
	if searchCtx.Err() != nil {
		return appendTerminal("failed", searchCtx.Err())
	}
	if len(pack.Evidence) == 0 && trace.ErrorMessage == "" {
		pack.Limitations = append(pack.Limitations, "no evidence found in allowed corpus")
	}
	if len(pack.Evidence) > 0 {
		pack.Confidence = 0.70
	}
	if collectErr != nil {
		return appendTerminal("failed", collectErr)
	}
	return appendTerminal("completed", nil)
}

// terminalEventJoin closes every evidence branch from one search.  The last
// evidence event remains the deterministic causation edge; all earlier
// evidence events are sorted dependencies.  Empty and single-evidence
// searches retain the historical single-cause shape with no dependencies.
func terminalEventJoin(lastEventID modulecore.EventID, evidenceEventIDs []modulecore.EventID) (modulecore.EventID, []modulecore.EventID, error) {
	if err := lastEventID.Validate(); err != nil {
		return "", nil, errors.New("dci terminal event join cause is invalid")
	}
	seen := make(map[modulecore.EventID]struct{}, len(evidenceEventIDs))
	for _, eventID := range evidenceEventIDs {
		if err := eventID.Validate(); err != nil {
			return "", nil, errors.New("dci terminal event join evidence is invalid")
		}
		if _, exists := seen[eventID]; exists {
			return "", nil, errors.New("dci terminal event join evidence is duplicated")
		}
		seen[eventID] = struct{}{}
	}
	if len(evidenceEventIDs) == 0 {
		return lastEventID, nil, nil
	}
	cause := evidenceEventIDs[len(evidenceEventIDs)-1]
	if len(evidenceEventIDs) == 1 {
		return cause, nil, nil
	}
	dependencies := make([]modulecore.EventID, 0, len(evidenceEventIDs)-1)
	for _, eventID := range evidenceEventIDs[:len(evidenceEventIDs)-1] {
		dependencies = append(dependencies, eventID)
	}
	sort.Slice(dependencies, func(left, right int) bool {
		return dependencies[left] < dependencies[right]
	})
	return cause, dependencies, nil
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
				return errCandidateCollectionLimit
			}
			return nil
		})
		if errors.Is(walkErr, errCandidateCollectionLimit) {
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

func (e *Explorer) recordSkillBootstrap(ctx context.Context, query, actor string) error {
	if e.skills == nil {
		return nil
	}
	_, err := e.skills.Record(ctx, domainskill.TaskContext{
		Text:   query,
		Intent: "dci_search",
		Agent:  actor,
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

func (e *Explorer) step(eventID modulecore.EventID, no int, toolName, path string, count int, status string, errMsg string) domaindci.SearchStep {
	return domaindci.SearchStep{
		StepNo:       no,
		EventID:      eventID,
		EventType:    dciFileReadEventType,
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
