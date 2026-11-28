package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	localRecallDefaultLimit  = 8
	localRecallMaxQueryRunes = 2000
	localRecallGitTimeout    = 10 * time.Second
	localRecallGitMaxOutput  = 2 << 20
	localRecallMaxExcerpt    = 500
)

// ResearchDocumentStore searches only the owner's pending research staging
// namespace. Search rows are candidates; every row is re-read by exact key.
type ResearchDocumentStore interface {
	SearchResearchStagingDocuments(context.Context, string, string, string, []string, time.Time, time.Time, int) (l1sqlite.L1ResearchStagingSearchResult, error)
	ResearchDocumentExactReader
}

// ResearchStructuredRecallPort resolves only owner-registered, current local
// indexes. Implementations must exact-read source evidence before returning a
// hit and report unavailable when the current receipt or source is missing.
type ResearchStructuredRecallPort interface {
	RecallPageIndexDocuments(context.Context, c.Envelope, c.RecallRequest, string, int) ([]c.Hit, bool, error)
	RecallCodeGraph(context.Context, c.Envelope, c.RecallRequest, string, int) ([]c.Hit, bool, error)
}

// ResearchStructuredRecallPorts enables a configured structured backend for
// each target independently. Disabled code recall keeps the literal Git path.
type ResearchStructuredRecallPorts struct {
	Owner            ResearchStructuredRecallPort
	PageIndexEnabled bool
	CodeGraphEnabled bool
}

// RegisteredRepository is an owner-configured local Git snapshot. Request
// values select only Ref and an allowlisted immutable commit, never Path.
type RegisteredRepository struct {
	Ref              string
	Path             string
	AllowedRevisions []string
	Scope            string
	OwnerID          string
}

// LocalRecall combines the existing knowledge search with exact owner reads
// from pending research staging and registered Git snapshots. It performs no
// external network requests and does not execute source code.
type LocalRecall struct {
	knowledge    engine.RecallPort
	documents    ResearchDocumentStore
	repositories []RegisteredRepository
	semantic     *ResearchSemanticRecallPorts
	structured   *ResearchStructuredRecallPorts
}

func NewLocalRecall(knowledge engine.RecallPort, documents ResearchDocumentStore, repos []RegisteredRepository) *LocalRecall {
	return &LocalRecall{knowledge: knowledge, documents: documents, repositories: append([]RegisteredRepository(nil), repos...)}
}

// NewLocalRecallWithSemantic combines the lexical baseline with the current
// local semantic index. The legacy constructor remains lexical-only by default.
func NewLocalRecallWithSemantic(knowledge engine.RecallPort, documents ResearchDocumentStore, repos []RegisteredRepository, semantic ResearchSemanticRecallPorts) *LocalRecall {
	recall := NewLocalRecall(knowledge, documents, repos)
	recall.semantic = &semantic
	return recall
}

// NewLocalRecallWithStructured preserves the lexical owner paths and adds
// explicitly enabled local PageIndex/CodeGraph recall.
func NewLocalRecallWithStructured(knowledge engine.RecallPort, documents ResearchDocumentStore, repos []RegisteredRepository, structured ResearchStructuredRecallPorts) *LocalRecall {
	recall := NewLocalRecall(knowledge, documents, repos)
	recall.structured = &structured
	return recall
}

// NewLocalRecallWithSemanticAndStructured composes the existing read-only
// semantic path with independently configured local structure backends.
func NewLocalRecallWithSemanticAndStructured(knowledge engine.RecallPort, documents ResearchDocumentStore, repos []RegisteredRepository, semantic ResearchSemanticRecallPorts, structured ResearchStructuredRecallPorts) *LocalRecall {
	recall := NewLocalRecallWithSemantic(knowledge, documents, repos, semantic)
	recall.structured = &structured
	return recall
}

var _ engine.RecallPort = (*LocalRecall)(nil)

func (r *LocalRecall) Recall(ctx context.Context, envelope c.Envelope, request c.RecallRequest) (c.RecallResult, error) {
	if ctx == nil {
		return c.RecallResult{}, errors.New("local research recall context is required")
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || len([]rune(query)) > localRecallMaxQueryRunes || request.Limit < 0 || request.Limit > l1sqlite.L1ResearchRecallMaxLimit {
		return c.RecallResult{}, errors.New("invalid local research recall query or limit")
	}
	if len(request.DocumentRefs) > l1sqlite.L1ResearchRecallMaxLimit {
		return c.RecallResult{}, errors.New("too many local research document refs")
	}
	for _, ref := range request.DocumentRefs {
		if ref == "" || ref != strings.TrimSpace(ref) || len(ref) > 512 {
			return c.RecallResult{}, errors.New("invalid local research document ref")
		}
	}
	if request.TimeWindow != nil && (request.TimeWindow.Since.IsZero() || request.TimeWindow.Until.IsZero() || !request.TimeWindow.Until.After(request.TimeWindow.Since)) {
		return c.RecallResult{}, errors.New("invalid local research recall time window")
	}
	if _, err := recallScope(envelope); err != nil {
		return c.RecallResult{}, err
	}
	targets := request.Targets
	if len(targets) == 0 {
		targets = []string{"knowledge"}
	}
	limit := request.Limit
	if limit == 0 {
		limit = localRecallDefaultLimit
	}
	result := c.RecallResult{Hits: []c.Hit{}, Missing: []string{}}
	seen := make(map[string]struct{}, len(targets))
	var errs []error
	for _, target := range targets {
		if _, duplicate := seen[target]; duplicate {
			continue
		}
		seen[target] = struct{}{}
		switch target {
		case "knowledge":
			if r == nil || r.knowledge == nil {
				result.Missing = append(result.Missing, target)
				continue
			}
			child := request
			child.Query, child.Limit = query, limit
			child.Targets = []string{"knowledge"}
			knowledgeResult, err := r.knowledge.Recall(ctx, envelope, child)
			if err != nil {
				result.Missing = append(result.Missing, target)
				errs = append(errs, fmt.Errorf("knowledge recall unavailable: %w", err))
				continue
			}
			result.Hits = append(result.Hits, knowledgeResult.Hits...)
			result.Missing = append(result.Missing, knowledgeResult.Missing...)
		case "documents":
			var hits []c.Hit
			var unavailable bool
			var err error
			if r != nil && r.semantic != nil {
				semanticHits, semanticUnavailable, semanticErr := r.recallSemanticDocuments(ctx, envelope, request, query, limit, *r.semantic)
				lexicalHits, lexicalUnavailable, lexicalErr := r.recallDocuments(ctx, envelope, request, query, limit)
				hits = mergeResearchDocumentHits(lexicalHits, semanticHits, limit)
				unavailable = semanticUnavailable || lexicalUnavailable
				err = errors.Join(semanticErr, lexicalErr)
			} else {
				hits, unavailable, err = r.recallDocuments(ctx, envelope, request, query, limit)
			}
			if r != nil && r.structured != nil && r.structured.PageIndexEnabled {
				if r.structured.Owner == nil {
					unavailable = true
				} else {
					pageHits, pageUnavailable, pageErr := r.structured.Owner.RecallPageIndexDocuments(ctx, envelope, request, query, limit)
					hits = append(pageHits, hits...)
					unavailable = unavailable || pageUnavailable
					err = errors.Join(err, pageErr)
				}
			}
			result.Hits = append(result.Hits, hits...)
			if unavailable {
				result.Missing = append(result.Missing, target)
			}
			if err != nil {
				errs = append(errs, err)
			}
		case "code":
			var hits []c.Hit
			var unavailable bool
			var err error
			if r != nil && r.structured != nil && r.structured.CodeGraphEnabled {
				if r.structured.Owner == nil {
					unavailable = true
				} else {
					hits, unavailable, err = r.structured.Owner.RecallCodeGraph(ctx, envelope, request, query, limit)
				}
			} else {
				hits, unavailable, err = r.recallCode(ctx, envelope, request, query, limit)
			}
			result.Hits = append(result.Hits, hits...)
			if unavailable {
				result.Missing = append(result.Missing, target)
			}
			if err != nil {
				errs = append(errs, err)
			}
		default:
			return c.RecallResult{}, fmt.Errorf("unsupported research recall target %q", target)
		}
	}
	if len(result.Hits) > limit {
		result.Hits = result.Hits[:limit]
	}
	return result, errors.Join(errs...)
}

func (r *LocalRecall) recallDocuments(ctx context.Context, envelope c.Envelope, request c.RecallRequest, query string, limit int) ([]c.Hit, bool, error) {
	if r == nil || r.documents == nil {
		return []c.Hit{}, true, nil
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return []c.Hit{}, true, err
	}
	since, until := time.Time{}, time.Time{}
	if request.TimeWindow != nil {
		since, until = request.TimeWindow.Since, request.TimeWindow.Until
	}
	searchResult, err := r.documents.SearchResearchStagingDocuments(ctx, envelope.DataScope, envelope.OwnerID, query, request.DocumentRefs, since, until, limit)
	if err != nil {
		return []c.Hit{}, true, fmt.Errorf("research documents unavailable: %w", err)
	}
	hits := make([]c.Hit, 0, len(searchResult.Items))
	for _, candidate := range searchResult.Items {
		if candidate.Namespace != namespace || candidate.EventID == "" {
			continue
		}
		item, found, readErr := r.documents.FindStagingItemByNamespaceEventID(ctx, namespace, candidate.EventID)
		if readErr != nil {
			return hits, true, fmt.Errorf("research document exact readback unavailable: %w", readErr)
		}
		if !found || !validResearchStagingEvidence(item, envelope, namespace, candidate.EventID) {
			continue
		}
		basis, timestamp := researchStagingTime(item)
		if request.TimeWindow != nil && (timestamp.Before(request.TimeWindow.Since) || !timestamp.Before(request.TimeWindow.Until)) {
			continue
		}
		start, end, ok := findRuneRange(item.RawText, query)
		if !ok {
			continue
		}
		locator := "research-document://" + item.Namespace + "/" + item.EventID
		document := researchDocumentFromStagingMeta(item)
		revision := document.Revision
		if revision == "" {
			revision = item.RawHash
		}
		hits = append(hits, c.Hit{
			Target: "documents", ProviderID: "core.l1_staging", CanonicalRef: locator,
			Revision: revision, Locator: locator, MatchKind: "literal_substring", EvidenceState: "acquired",
			Title: item.SourceURL, Excerpt: researchExcerpt(item.RawText, start, end),
			Evidence:  &c.EvidenceRef{DocumentID: item.ID, ArtifactHash: item.RawHash, Start: start, End: end, Locator: locator},
			TimeBasis: basis,
		})
		if len(hits) >= limit {
			break
		}
	}
	return hits, searchResult.ScanLimited || searchResult.CandidatesTruncated, nil
}

func validResearchStagingEvidence(item l1sqlite.L1StagingItem, envelope c.Envelope, namespace, eventID string) bool {
	if item.Kind != l1sqlite.L1StagingKindExternalFetch || item.Namespace != namespace || item.EventID != eventID || item.ValidationStatus != l1sqlite.L1StagingStatusPending || strings.TrimSpace(item.RawText) == "" || item.LicenseNote != "retention explicitly authorized by research request" {
		return false
	}
	if !isSHA256(item.RawHash) || digestBytes([]byte(item.RawText)) != item.RawHash {
		return false
	}
	doc := researchDocumentFromStagingMeta(item)
	metaScope, _ := item.Meta["research_scope"].(string)
	if policyBoundResearchSource(doc.SourceKind) && metaString(item.Meta, "research_policy_revision") != envelope.PolicyRevision {
		return false
	}
	metaNamespace, _ := item.Meta["research_namespace"].(string)
	metaDigest, _ := item.Meta["research_digest"].(string)
	complete, _ := item.Meta["content_complete"].(bool)
	retention, _ := item.Meta["retention_allowed"].(bool)
	originalURL, originalErr := canonicalHTTPURL(metaString(item.Meta, "original_url"))
	finalURL, finalErr := canonicalHTTPURL(metaString(item.Meta, "final_url"))
	sourceID := ""
	if finalErr == nil {
		sourceID = modulewebgather.SourceIDFromURL(finalURL)
	}
	return metaScope == envelope.DataScope && metaNamespace == namespace && metaDigest == item.RawHash && complete && retention &&
		originalErr == nil && finalErr == nil && originalURL == metaString(item.Meta, "original_url") && finalURL == metaString(item.Meta, "final_url") && item.SourceURL == finalURL && item.SourceID == sourceID && validResearchDocumentIdentity(doc) &&
		metaString(item.Meta, "extracted_hash") == item.RawHash &&
		researchEventID(sourceID, finalURL, item.RawHash, doc) == item.EventID &&
		item.PublishedAt.Equal(researchPublishedAt(doc))
}

func researchStagingTime(item l1sqlite.L1StagingItem) (string, time.Time) {
	if !item.PublishedAt.IsZero() {
		return "published_at", item.PublishedAt
	}
	return "fetched_at", item.FetchedAt
}

func findRuneRange(text, query string) (int, int, bool) {
	byteStart := strings.Index(text, query)
	if byteStart < 0 || !utf8.ValidString(text) {
		return 0, 0, false
	}
	start := len([]rune(text[:byteStart]))
	return start, start + len([]rune(query)), true
}

func researchExcerpt(text string, start, end int) string {
	runes := []rune(text)
	left, right := start-80, end+80
	if left < 0 {
		left = 0
	}
	if right > len(runes) {
		right = len(runes)
	}
	if right-left > localRecallMaxExcerpt {
		right = left + localRecallMaxExcerpt
	}
	return string(runes[left:right])
}

func (r *LocalRecall) recallCode(ctx context.Context, envelope c.Envelope, request c.RecallRequest, query string, limit int) ([]c.Hit, bool, error) {
	repository, ok := r.registeredRepository(request.RepoRef)
	if !ok || request.Revision == "" || !isGitRevision(request.Revision) || !allowedGitRevision(repository, request.Revision) || repository.Scope != envelope.DataScope || repository.OwnerID != envelope.OwnerID {
		return []c.Hit{}, true, nil
	}
	if ctx == nil {
		return []c.Hit{}, true, errors.New("research code recall context is required")
	}
	if !filepath.IsAbs(repository.Path) {
		return []c.Hit{}, true, nil
	}
	args := []string{"-C", repository.Path, "grep", "-n", "-I", "--full-name", "-F", "-e", query, request.Revision, "--"}
	output, err := boundedGitCommand(ctx, args...)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return []c.Hit{}, false, nil
		}
		return []c.Hit{}, true, fmt.Errorf("registered code snapshot search failed")
	}
	lines, err := parseGitGrepOutput(output, request.Revision, limit)
	if err != nil {
		return []c.Hit{}, true, errors.New("registered code snapshot returned an invalid search result")
	}
	hits := make([]c.Hit, 0, len(lines))
	for _, match := range lines {
		showArg := request.Revision + ":" + match.path
		fileBytes, showErr := boundedGitCommand(ctx, "-C", repository.Path, "show", showArg)
		if showErr != nil || len(fileBytes) >= localRecallGitMaxOutput {
			return hits, true, fmt.Errorf("registered code snapshot exact readback failed: %v", showErr)
		}
		fileText := string(fileBytes)
		if !utf8.ValidString(fileText) {
			continue
		}
		fileLines := strings.Split(fileText, "\n")
		if match.line < 1 || match.line > len(fileLines) {
			continue
		}
		lineText := strings.TrimSuffix(fileLines[match.line-1], "\r")
		if lineText != match.text || !strings.Contains(lineText, query) {
			continue
		}
		lineHash := sha256.Sum256(fileBytes)
		artifactHash := hex.EncodeToString(lineHash[:])
		locator := fmt.Sprintf("%s@%s:%s:%d", repository.Ref, request.Revision, match.path, match.line)
		start := runeIndex(lineText, query)
		if start < 0 {
			continue
		}
		end := start + len([]rune(query))
		hits = append(hits, c.Hit{
			Target: "code", ProviderID: "core.git_snapshot", CanonicalRef: repository.Ref,
			Revision: request.Revision, Locator: locator, MatchKind: "git_grep_literal", EvidenceState: "acquired",
			Title: match.path, Excerpt: boundedText(lineText, localRecallMaxExcerpt),
			Evidence: &c.EvidenceRef{DocumentID: repository.Ref + "@" + request.Revision + ":" + match.path, ArtifactHash: artifactHash, Start: start, End: end, Locator: locator},
		})
		if len(hits) >= limit {
			break
		}
	}
	return hits, false, nil
}

func (r *LocalRecall) registeredRepository(ref string) (RegisteredRepository, bool) {
	if r == nil || strings.TrimSpace(ref) == "" {
		return RegisteredRepository{}, false
	}
	var match RegisteredRepository
	found := false
	for _, repository := range r.repositories {
		if repository.Ref == ref && repository.Path != "" {
			if found {
				return RegisteredRepository{}, false
			}
			match, found = repository, true
		}
	}
	return match, found
}

func isGitRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func allowedGitRevision(repository RegisteredRepository, revision string) bool {
	for _, allowed := range repository.AllowedRevisions {
		if allowed == revision {
			return true
		}
	}
	return false
}

type gitMatch struct {
	path string
	line int
	text string
}

func parseGitGrepOutput(output []byte, revision string, limit int) ([]gitMatch, error) {
	if len(output) > localRecallGitMaxOutput || !utf8.Valid(output) {
		return nil, errors.New("git output exceeded bounds or was not utf8")
	}
	lines := bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return []gitMatch{}, nil
	}
	matches := make([]gitMatch, 0, min(limit, len(lines)))
	for _, raw := range lines {
		path, lineNumber, text, ok := splitGitGrepLine(string(raw))
		pathPrefix := revision + ":"
		if strings.HasPrefix(path, pathPrefix) {
			path = strings.TrimPrefix(path, pathPrefix)
		}
		if !ok || !safeRepositoryRelativePath(path) || lineNumber < 1 {
			return nil, errors.New("invalid git grep output line")
		}
		matches = append(matches, gitMatch{path: path, line: lineNumber, text: text})
		if len(matches) >= limit {
			break
		}
	}
	return matches, nil
}

func splitGitGrepLine(value string) (string, int, string, bool) {
	for first := strings.IndexByte(value, ':'); first >= 0; {
		rest := value[first+1:]
		next := strings.IndexByte(rest, ':')
		if next < 0 {
			return "", 0, "", false
		}
		lineNumber, err := strconv.Atoi(rest[:next])
		if err == nil {
			return value[:first], lineNumber, rest[next+1:], true
		}
		nextFirst := strings.IndexByte(value[first+1:], ':')
		if nextFirst < 0 {
			break
		}
		first += nextFirst + 1
	}
	return "", 0, "", false
}

func safeRepositoryRelativePath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\\') || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return true
}

type boundedCommandOutput struct {
	data  []byte
	limit int
}

func (b *boundedCommandOutput) Write(data []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		return 0, errors.New("command output limit exceeded")
	}
	if len(data) > remaining {
		b.data = append(b.data, data[:remaining]...)
		return remaining, errors.New("command output limit exceeded")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func boundedGitCommand(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, localRecallGitTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", args...)
	stdout := &boundedCommandOutput{limit: localRecallGitMaxOutput}
	stderr := &boundedCommandOutput{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	if commandCtx.Err() != nil {
		return nil, errors.New("git command timed out")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return nil, err
		}
		return nil, errors.New("git command failed")
	}
	return stdout.data, nil
}

func runeIndex(value, substring string) int {
	byteIndex := strings.Index(value, substring)
	if byteIndex < 0 {
		return -1
	}
	return len([]rune(value[:byteIndex]))
}

func boundedText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
