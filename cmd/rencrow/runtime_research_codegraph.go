package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/localbackend"
	"golang.org/x/text/unicode/norm"
)

const (
	researchCodeGraphTreeMaxBytes  = 16 << 20
	researchCodeGraphFileMaxBytes  = 8 << 20
	researchCodeGraphTotalMaxBytes = 64 << 20
	researchCodeGraphMaxFiles      = 10000
	researchCodeGraphGitTimeout    = 30 * time.Second
	researchCodeGraphSearchMax     = 100
	researchCodeGraphTerminalKind  = "code_graph"
	researchCodeGraphProjectPrefix = "rencrow-"
)

type researchCodeGraphSnapshot struct {
	PortSnapshot engine.CodeGraphSnapshot
	Root         string
	Files        map[string][]byte
}

type researchCodeGraphTreeFile struct {
	Mode string
	Hash string
	Path string
}

func (owner *researchStructuredIndexOwner) handleCodeGraphOperation(w http.ResponseWriter, r *http.Request, input researchOwnerRequest, caller c.Envelope, trustedOwner string) {
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.CodeGraph.Enabled || owner.codeGraph == nil || owner.tasks == nil {
		writeResearchStructuredFailure(w, http.StatusServiceUnavailable, "code_graph_unavailable")
		return
	}
	if !structuredOwnerFieldsOnly(input, false) || len(input.Request) == 0 || len(input.Request) > 64*1024 {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "mixed_operation_fields")
		return
	}
	request, err := c.DecodeCodeGraphBuildRequest(input.Request)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_code_graph_request")
		return
	}
	repository, ok := owner.trustedRepository(request.RepositoryRef, request.Revision, caller, trustedOwner)
	if !ok {
		writeResearchStructuredFailure(w, http.StatusForbidden, "registered_repository_unavailable")
		return
	}
	request, _, requestDigest, err := c.CanonicalizeCodeGraphBuildRequest(request)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_code_graph_request")
		return
	}
	operationKey, digest, err := owner.codeGraphOperationIdentity(request, repository, requestDigest)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "code_graph_identity_invalid")
		return
	}
	build := input.Operation == "code_graph_build"
	response, found, err := owner.readStructuredOperation(r.Context(), trustedOwner, operationKey, digest)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusConflict, "code_graph_receipt_unavailable")
		return
	}
	if found || !build {
		if !found {
			response = researchStructuredNotFound(operationKey, digest)
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	admission, created, err := owner.tasks.EnsureResearchAdmission(r.Context(), trustedOwner, operationKey, digest, caller.PolicyRevision, researchIndexAdmissionKind)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusConflict, "code_graph_admission_unavailable")
		return
	}
	if !created {
		response, found, err = owner.readStructuredOperation(r.Context(), trustedOwner, operationKey, digest)
		if err != nil {
			writeResearchStructuredFailure(w, http.StatusConflict, "code_graph_receipt_unavailable")
			return
		}
		if !found {
			writeResearchStructuredPending(w, operationKey, digest, admission)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	if !researchIndexAdmissionIsRunning(r.Context(), owner.tasks, trustedOwner, admission) {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	guard := newResearchStartedRunGuard(owner.tasks, admission.TaskID, admission.InitialRunID, trustedOwner, r.Context())
	defer guard.DeferFallback()
	result := owner.structuredResult(researchCodeGraphTerminalKind, "failed", trustedOwner, digest, admission)
	result.RepositoryRef, result.Revision = repository.Ref, request.Revision
	buildCtx, cancel := context.WithTimeout(r.Context(), time.Duration(owner.config.Research.StructuredIndex.CodeGraph.TimeoutSeconds)*time.Second)
	defer cancel()
	snapshot, snapshotErr := owner.createCodeGraphSnapshot(buildCtx, repository, request.Revision)
	var buildErr error
	if snapshotErr != nil {
		buildErr = errors.New("create exact registered Git snapshot")
		result.ErrorCode = "code_graph_snapshot_failed"
	} else {
		defer removeResearchCodeGraphSnapshot(snapshot.Root)
		result.Project, result.SnapshotHash = snapshot.PortSnapshot.Project, snapshot.PortSnapshot.SourceHash
		receipt, indexErr := owner.codeGraph.IndexRepository(buildCtx, snapshot.PortSnapshot)
		if indexErr != nil {
			buildErr = errors.New("index exact registered Git snapshot")
			result.ErrorCode = "code_graph_index_failed"
		} else {
			result.CodeGraph = &receipt
			if !validResearchCodeGraphReceiptMetadata(&receipt, owner.config.Research.StructuredIndex.CodeGraph, snapshot.PortSnapshot) {
				buildErr = errors.New("CodeGraph receipt did not match the configured runtime or snapshot")
				result.Status, result.ErrorCode = "partial", "code_graph_receipt_invalid"
			} else if receipt.Status != engine.CodeGraphIndexIndexed {
				buildErr = errors.New("CodeGraph index was degraded")
				result.Status, result.ErrorCode = "partial", "code_graph_degraded"
			} else if receipt.Nodes < 1 {
				buildErr = errors.New("CodeGraph indexed an empty graph")
				result.Status, result.ErrorCode = "partial", "code_graph_receipt_invalid"
			} else {
				result.Status, result.ReadbackVerified = "indexed", true
			}
		}
	}
	if errors.Is(buildErr, context.Canceled) || errors.Is(buildCtx.Err(), context.Canceled) {
		result.Status, result.ErrorCode = "cancelled", "cancelled"
	}
	if err := owner.closeStructuredOperation(guard, &result, buildErr); err != nil {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(r.Context()), researchStructuredReadTimeout)
	defer readCancel()
	response, found, err = owner.readStructuredOperation(readCtx, trustedOwner, operationKey, digest)
	if err != nil || !found {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (owner *researchStructuredIndexOwner) RecallCodeGraph(ctx context.Context, envelope c.Envelope, request c.RecallRequest, query string, limit int) ([]c.Hit, bool, error) {
	if owner == nil || owner.config == nil || owner.codeGraph == nil || owner.tasks == nil || ctx == nil {
		return []c.Hit{}, true, errors.New("CodeGraph owner is unavailable")
	}
	if request.TimeWindow != nil || request.RepoRef == "" || request.Revision == "" {
		return []c.Hit{}, true, errors.New("CodeGraph recall requires one exact registered repository commit")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(owner.config.Research.StructuredIndex.CodeGraph.TimeoutSeconds)*time.Second)
	defer cancel()
	current, found, err := owner.readCurrentCodeGraph(ctx, envelope, request.RepoRef, request.Revision)
	if err != nil {
		return []c.Hit{}, true, err
	}
	if !found {
		return []c.Hit{}, true, nil
	}
	defer removeResearchCodeGraphSnapshot(current.Snapshot.Root)
	symbols, err := owner.codeGraph.SearchFunctions(ctx, current.Snapshot.PortSnapshot.Project, query, min(limit, researchCodeGraphSearchMax))
	if err != nil {
		return []c.Hit{}, true, errors.New("CodeGraph function search is unavailable")
	}
	hits := make([]c.Hit, 0, min(limit, len(symbols)))
	seen := make(map[string]struct{}, len(symbols))
	verificationCache := newResearchCodeGraphVerificationCache()
	unverifiedCandidate := false
	type verifiedCandidate struct {
		symbol   engine.CodeGraphSymbol
		verified verifiedResearchCodeGraphSymbol
	}
	candidates := make([]verifiedCandidate, 0, len(symbols))
	for _, symbol := range symbols {
		verified, ok := verifyResearchCodeGraphSymbol(ctx, current.Repository, request.Revision, symbol, current.Snapshot.Files, query)
		if !ok {
			unverifiedCandidate = true
			continue
		}
		verificationCache.add(verified)
		candidates = append(candidates, verifiedCandidate{symbol: symbol, verified: verified})
	}
	var recallErr error
	for _, candidate := range candidates {
		symbol, verified := candidate.symbol, candidate.verified
		key := fmt.Sprintf("%s:%d:%d", symbol.Path, symbol.StartLine, symbol.EndLine)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		hits = append(hits, researchCodeGraphFunctionHit(current.Repository.Ref, request.Revision, current.Result.CodeGraph.BackendVersion, symbol, verified.fileBytes, verified.excerpt))
		if len(hits) >= limit {
			break
		}
		calls, traceUnverified, traceErr := owner.traceVerifiedCodeGraphCalls(ctx, current.Snapshot.PortSnapshot.Project,
			current.Repository, request.Revision, current.Snapshot.Files, verified, verificationCache, min(limit, researchCodeGraphSearchMax))
		if traceErr != nil {
			recallErr = errors.Join(recallErr, errors.New("CodeGraph call trace is unavailable"))
			continue
		}
		unverifiedCandidate = unverifiedCandidate || traceUnverified
		for _, call := range calls {
			if !strings.Contains(call.caller.source, query) && !strings.Contains(call.callee.source, query) {
				unverifiedCandidate = true
				continue
			}
			edgeLocator := fmt.Sprintf("%s@%s:CALLS:%s:%d->%s:%d", current.Repository.Ref, request.Revision,
				call.caller.symbol.Path, call.caller.symbol.StartLine, call.callee.symbol.Path, call.callee.symbol.StartLine)
			for _, endpoint := range []verifiedResearchCodeGraphSymbol{call.caller, call.callee} {
				edgeKey := "edge:" + edgeLocator + ":" + endpoint.symbol.Path + fmt.Sprint(":", endpoint.symbol.StartLine)
				if _, duplicate := seen[edgeKey]; duplicate {
					continue
				}
				seen[edgeKey] = struct{}{}
				hits = append(hits, researchCodeGraphCallEndpointHit(current.Repository.Ref, request.Revision,
					current.Result.CodeGraph.BackendVersion, edgeLocator, endpoint))
				if len(hits) >= limit {
					break
				}
			}
			if len(hits) >= limit {
				break
			}
		}
	}
	return hits, recallErr != nil || unverifiedCandidate, recallErr
}

type verifiedResearchCodeGraphSymbol struct {
	symbol    engine.CodeGraphSymbol
	fileBytes []byte
	source    string
	excerpt   string
}

type verifiedResearchCodeGraphCall struct {
	relation engine.CodeGraphRelation
	caller   verifiedResearchCodeGraphSymbol
	callee   verifiedResearchCodeGraphSymbol
}

type researchCodeGraphEndpointResult struct {
	symbol verifiedResearchCodeGraphSymbol
	ok     bool
}

type researchCodeGraphVerificationCache struct {
	symbols           map[string]verifiedResearchCodeGraphSymbol
	qualifiedEndpoint map[string]researchCodeGraphEndpointResult
	bareEndpoint      map[string]researchCodeGraphEndpointResult
}

func newResearchCodeGraphVerificationCache() *researchCodeGraphVerificationCache {
	return &researchCodeGraphVerificationCache{
		symbols:           make(map[string]verifiedResearchCodeGraphSymbol),
		qualifiedEndpoint: make(map[string]researchCodeGraphEndpointResult),
		bareEndpoint:      make(map[string]researchCodeGraphEndpointResult),
	}
}

func (cache *researchCodeGraphVerificationCache) add(symbol verifiedResearchCodeGraphSymbol) {
	if cache == nil || len(cache.symbols) >= researchCodeGraphSearchMax {
		return
	}
	cache.symbols[researchCodeGraphSymbolKey(symbol.symbol)] = symbol
}

func (cache *researchCodeGraphVerificationCache) rememberEndpoint(qualified, name string, result researchCodeGraphEndpointResult) {
	if cache == nil {
		return
	}
	if qualified == "" {
		if _, exists := cache.bareEndpoint[name]; exists || len(cache.bareEndpoint) < researchCodeGraphSearchMax {
			cache.bareEndpoint[name] = result
		}
		return
	}
	key := qualified + "\x00" + name
	if _, exists := cache.qualifiedEndpoint[key]; exists || len(cache.qualifiedEndpoint) < researchCodeGraphSearchMax {
		cache.qualifiedEndpoint[key] = result
	}
}

func researchCodeGraphSymbolKey(symbol engine.CodeGraphSymbol) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", symbol.QualifiedName, symbol.Path, symbol.StartLine, symbol.EndLine)
}

func (owner *researchStructuredIndexOwner) verifyCodeGraphEndpoint(ctx context.Context, project string, repository config.ResearchRepositoryConfig, revision string, snapshotFiles map[string][]byte, name, qualified string) (verifiedResearchCodeGraphSymbol, bool) {
	return owner.verifyCodeGraphEndpointCached(ctx, project, repository, revision, snapshotFiles, name, qualified, newResearchCodeGraphVerificationCache())
}

func (owner *researchStructuredIndexOwner) verifyCodeGraphEndpointCached(ctx context.Context, project string, repository config.ResearchRepositoryConfig, revision string, snapshotFiles map[string][]byte, name, qualified string, cache *researchCodeGraphVerificationCache) (verifiedResearchCodeGraphSymbol, bool) {
	if owner == nil || owner.codeGraph == nil || ctx == nil || name == "" || cache == nil {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	if qualified != "" {
		lookupKey := qualified + "\x00" + name
		if result, found := cache.qualifiedEndpoint[lookupKey]; found {
			return result.symbol, result.ok
		}
		var cached verifiedResearchCodeGraphSymbol
		cachedCount := 0
		for _, verified := range cache.symbols {
			if verified.symbol.QualifiedName == qualified && name == verified.symbol.Name {
				cached, cachedCount = verified, cachedCount+1
			}
		}
		if cachedCount > 1 {
			cache.rememberEndpoint(qualified, name, researchCodeGraphEndpointResult{})
			return verifiedResearchCodeGraphSymbol{}, false
		}
		if cachedCount == 1 {
			cache.rememberEndpoint(qualified, name, researchCodeGraphEndpointResult{symbol: cached, ok: true})
			return cached, true
		}
	} else {
		if result, found := cache.bareEndpoint[name]; found {
			return result.symbol, result.ok
		}
	}
	symbols, err := owner.codeGraph.SearchFunctions(ctx, project, name, researchCodeGraphSearchMax)
	if err != nil {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	if len(symbols) >= researchCodeGraphSearchMax {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	var matched verifiedResearchCodeGraphSymbol
	matchedFound := false
	for _, symbol := range symbols {
		if qualified != "" {
			if symbol.QualifiedName != qualified || name != symbol.Name {
				continue
			}
		} else if symbol.Name != name {
			continue
		}
		verified, ok := verifyResearchCodeGraphSymbol(ctx, repository, revision, symbol, snapshotFiles, "")
		if !ok {
			if qualified == "" {
				cache.rememberEndpoint("", name, researchCodeGraphEndpointResult{})
			}
			return verifiedResearchCodeGraphSymbol{}, false
		}
		if matchedFound && researchCodeGraphSymbolKey(matched.symbol) != researchCodeGraphSymbolKey(verified.symbol) {
			if qualified == "" {
				cache.rememberEndpoint("", name, researchCodeGraphEndpointResult{})
			}
			return verifiedResearchCodeGraphSymbol{}, false
		}
		matched, matchedFound = verified, true
		cache.add(verified)
	}
	result := researchCodeGraphEndpointResult{symbol: matched, ok: matchedFound}
	cache.rememberEndpoint(qualified, name, result)
	return matched, matchedFound
}

func (owner *researchStructuredIndexOwner) traceVerifiedCodeGraphCalls(ctx context.Context, project string, repository config.ResearchRepositoryConfig, revision string, snapshotFiles map[string][]byte, subject verifiedResearchCodeGraphSymbol, cache *researchCodeGraphVerificationCache, limit int) ([]verifiedResearchCodeGraphCall, bool, error) {
	if owner == nil || owner.codeGraph == nil || ctx == nil || cache == nil || limit < 1 || limit > researchCodeGraphSearchMax ||
		subject.symbol.Name == "" || subject.symbol.Path == "" {
		return nil, true, errors.New("invalid verified CodeGraph call-trace request")
	}
	cache.add(subject)
	traceSubject := subject
	traceName := traceSubject.symbol.QualifiedName
	bareSubjectUnique := false
	bareSubjectChecked := false
	if traceName == "" {
		traceName = traceSubject.symbol.Name
		verified, unique := owner.verifyCodeGraphEndpointCached(ctx, project, repository, revision, snapshotFiles,
			traceSubject.symbol.Name, "", cache)
		if !unique || !sameResearchCodeGraphBareSymbol(verified.symbol, traceSubject.symbol) {
			return nil, true, nil
		}
		traceSubject = verified
		bareSubjectUnique, bareSubjectChecked = true, true
	}
	relations, err := owner.codeGraph.TraceCalls(ctx, project, traceName, "both", 1, limit)
	if err != nil {
		return nil, false, err
	}
	verifiedCalls := make([]verifiedResearchCodeGraphCall, 0, len(relations))
	unverified := false
	for _, relation := range relations {
		if relation.Kind != "CALLS" || relation.CallerName == "" || relation.CalleeName == "" {
			unverified = true
			continue
		}
		if !bareSubjectChecked &&
			((relation.CallerQualified == "" && relation.CallerName == traceSubject.symbol.Name) ||
				(relation.CalleeQualified == "" && relation.CalleeName == traceSubject.symbol.Name)) {
			bareSubjectChecked = true
			verified, ok := owner.verifyCodeGraphEndpointCached(ctx, project, repository, revision, snapshotFiles,
				traceSubject.symbol.Name, "", cache)
			bareSubjectUnique = ok && sameResearchCodeGraphBareSymbol(verified.symbol, traceSubject.symbol)
		}
		callerIsSubject, callerContradiction := researchCodeGraphRelationSideMatchesSubject(
			relation.CallerName, relation.CallerQualified, traceName, traceSubject.symbol, bareSubjectUnique)
		calleeIsSubject, calleeContradiction := researchCodeGraphRelationSideMatchesSubject(
			relation.CalleeName, relation.CalleeQualified, traceName, traceSubject.symbol, bareSubjectUnique)
		if callerContradiction || calleeContradiction || !callerIsSubject && !calleeIsSubject {
			unverified = true
			continue
		}
		caller := traceSubject
		if !callerIsSubject {
			var ok bool
			caller, ok = owner.verifyCodeGraphEndpointCached(ctx, project, repository, revision, snapshotFiles,
				relation.CallerName, relation.CallerQualified, cache)
			if !ok {
				unverified = true
				continue
			}
		}
		callee := traceSubject
		if !calleeIsSubject {
			var ok bool
			callee, ok = owner.verifyCodeGraphEndpointCached(ctx, project, repository, revision, snapshotFiles,
				relation.CalleeName, relation.CalleeQualified, cache)
			if !ok {
				unverified = true
				continue
			}
		}
		normalized := relation
		normalized.CallerName, normalized.CallerQualified = caller.symbol.Name, caller.symbol.QualifiedName
		normalized.CalleeName, normalized.CalleeQualified = callee.symbol.Name, callee.symbol.QualifiedName
		verifiedCalls = append(verifiedCalls, verifiedResearchCodeGraphCall{relation: normalized, caller: caller, callee: callee})
	}
	return verifiedCalls, unverified, nil
}

func sameResearchCodeGraphBareSymbol(first, second engine.CodeGraphSymbol) bool {
	return first.Name != "" && first.Name == second.Name && first.Path == second.Path &&
		first.StartLine == second.StartLine && first.EndLine == second.EndLine
}

func researchCodeGraphRelationSideMatchesSubject(name, qualified, traceName string, subject engine.CodeGraphSymbol, bareSubjectUnique bool) (bool, bool) {
	nameMatches := name == subject.Name || name == traceName
	if qualified != "" {
		if qualified != subject.QualifiedName {
			return false, false
		}
		return nameMatches, !nameMatches
	}
	if name == subject.Name && bareSubjectUnique {
		return true, false
	}
	if nameMatches {
		return false, true
	}
	return false, false
}

func verifyResearchCodeGraphSymbol(ctx context.Context, repository config.ResearchRepositoryConfig, revision string, symbol engine.CodeGraphSymbol, snapshotFiles map[string][]byte, query string) (verifiedResearchCodeGraphSymbol, bool) {
	if !safeResearchRepositoryPath(symbol.Path) || symbol.StartLine < 1 || symbol.EndLine < symbol.StartLine || symbol.EndLine-symbol.StartLine > 20000 {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	snapshotFile, found := snapshotFiles[symbol.Path]
	if !found {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	fileBytes, err := runResearchGitCommand(ctx, repository.Path, researchCodeGraphFileMaxBytes+1,
		"--no-replace-objects", "show", "--no-ext-diff", "--no-textconv", revision+":"+symbol.Path)
	if err != nil || len(fileBytes) > researchCodeGraphFileMaxBytes || !bytes.Equal(fileBytes, snapshotFile) || !utf8.Valid(fileBytes) {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	lines := strings.Split(string(fileBytes), "\n")
	if symbol.EndLine > len(lines) {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	function := strings.Join(lines[symbol.StartLine-1:symbol.EndLine], "\n")
	if symbol.Name == "" || !utf8.ValidString(function) || !strings.Contains(function, symbol.Name) ||
		(query != "" && !strings.Contains(function, query) && !strings.Contains(symbol.Name, query)) {
		return verifiedResearchCodeGraphSymbol{}, false
	}
	return verifiedResearchCodeGraphSymbol{
		symbol: symbol, fileBytes: fileBytes, source: function,
		excerpt: researchStructuredExcerpt(function, 0, len([]rune(function)), researchStructuredExcerptMax),
	}, true
}

func researchCodeGraphFunctionHit(repositoryRef, revision, backendVersion string, symbol engine.CodeGraphSymbol, fileBytes []byte, excerpt string) c.Hit {
	locator := fmt.Sprintf("%s@%s:%s:%d-%d", repositoryRef, revision, symbol.Path, symbol.StartLine, symbol.EndLine)
	return c.Hit{
		Target: "code", ProviderID: "core.codegraph." + backendVersion, CanonicalRef: repositoryRef,
		Revision: revision, Locator: locator, MatchKind: "codegraph_function", EvidenceState: "acquired",
		Title: symbol.QualifiedName, Excerpt: excerpt,
		Evidence: &c.EvidenceRef{DocumentID: repositoryRef + "@" + revision + ":" + symbol.Path,
			ArtifactHash: researchDigest(fileBytes), Start: symbol.StartLine, End: symbol.EndLine, Locator: locator},
	}
}

func researchCodeGraphCallEndpointHit(repositoryRef, revision, backendVersion, edgeLocator string, endpoint verifiedResearchCodeGraphSymbol) c.Hit {
	locator := edgeLocator + ":endpoint:" + endpoint.symbol.Path + ":" + fmt.Sprint(endpoint.symbol.StartLine)
	return c.Hit{
		Target: "code", ProviderID: "core.codegraph." + backendVersion, CanonicalRef: repositoryRef,
		Revision: revision, Locator: locator, MatchKind: "codegraph_call_endpoint", EvidenceState: "acquired",
		Title: endpoint.symbol.QualifiedName, Excerpt: researchStructuredExcerpt(endpoint.source, 0, len([]rune(endpoint.source)), researchStructuredExcerptMax),
		Evidence: &c.EvidenceRef{DocumentID: repositoryRef + "@" + revision + ":" + endpoint.symbol.Path,
			ArtifactHash: researchDigest(endpoint.fileBytes), Start: endpoint.symbol.StartLine, End: endpoint.symbol.EndLine, Locator: locator},
	}
}

func (owner *researchStructuredIndexOwner) trustedRepository(ref, revision string, envelope c.Envelope, trustedOwner string) (config.ResearchRepositoryConfig, bool) {
	if owner == nil || owner.config == nil || ref == "" || !validResearchCodeGraphRevision(revision) || envelope.OwnerID != trustedOwner ||
		envelope.DataScope != "user" || trustedOwner != owner.ownerID {
		return config.ResearchRepositoryConfig{}, false
	}
	var matched config.ResearchRepositoryConfig
	found := false
	for _, candidate := range owner.config.Research.Repositories {
		if candidate.Ref != ref || candidate.OwnerID != trustedOwner || candidate.Scope != envelope.DataScope || !filepath.IsAbs(candidate.Path) || !allowedResearchRepositoryRevision(candidate, revision) {
			continue
		}
		if found {
			return config.ResearchRepositoryConfig{}, false
		}
		matched, found = candidate, true
	}
	return matched, found
}

func allowedResearchRepositoryRevision(repository config.ResearchRepositoryConfig, revision string) bool {
	for _, allowed := range repository.AllowedRevisions {
		if allowed == revision {
			return true
		}
	}
	return false
}

func validResearchCodeGraphRevision(value string) bool {
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

func (owner *researchStructuredIndexOwner) codeGraphOperationIdentity(request c.CodeGraphBuildRequest, repository config.ResearchRepositoryConfig, requestDigest string) (string, string, error) {
	backend := owner.config.Research.StructuredIndex.CodeGraph
	canonical, err := json.Marshal(struct {
		Kind           string `json:"kind"`
		RequestDigest  string `json:"request_digest"`
		OwnerID        string `json:"owner_id"`
		DataScope      string `json:"data_scope"`
		BinarySHA256   string `json:"binary_sha256"`
		RuntimeVersion string `json:"runtime_version"`
		SourceCommit   string `json:"source_commit"`
	}{"code_graph", requestDigest, repository.OwnerID, repository.Scope, backend.BinarySHA256, backend.RuntimeVersion, backend.SourceCommit})
	if err != nil {
		return "", "", err
	}
	digest := researchDigest(canonical)
	key := researchStructuredOperationKey("code-graph", request.RepositoryRef, request.Revision, repository.Scope, repository.OwnerID, digest)
	return key, digest, nil
}

func validResearchCodeGraphReceipt(receipt *engine.CodeGraphIndexReceipt, cfg config.ResearchCodeGraphConfig, snapshot engine.CodeGraphSnapshot) bool {
	return validResearchCodeGraphReceiptMetadata(receipt, cfg, snapshot) && receipt.Status == engine.CodeGraphIndexIndexed && receipt.Nodes >= 1
}

func validResearchCodeGraphReceiptMetadata(receipt *engine.CodeGraphIndexReceipt, cfg config.ResearchCodeGraphConfig, snapshot engine.CodeGraphSnapshot) bool {
	return receipt != nil && receipt.Project == snapshot.Project && receipt.BackendVersion == localbackend.CodeGraphBackendVersion &&
		receipt.RuntimeVersion == cfg.RuntimeVersion && receipt.SourceCommit == cfg.SourceCommit && receipt.BinarySHA256 == cfg.BinarySHA256 &&
		receipt.SourceHash == snapshot.SourceHash && receipt.Nodes >= 0 &&
		(receipt.Status == engine.CodeGraphIndexIndexed || receipt.Status == engine.CodeGraphIndexDegraded)
}

func (owner *researchStructuredIndexOwner) createCodeGraphSnapshot(ctx context.Context, repository config.ResearchRepositoryConfig, revision string) (researchCodeGraphSnapshot, error) {
	if owner == nil || owner.config == nil || ctx == nil || !filepath.IsAbs(repository.Path) || !validResearchCodeGraphRevision(revision) || !allowedResearchRepositoryRevision(repository, revision) {
		return researchCodeGraphSnapshot{}, errors.New("registered repository snapshot identity is invalid")
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(owner.config.Research.StructuredIndex.CodeGraph.TimeoutSeconds)*time.Second)
	defer cancel()
	verifiedCommit, err := runResearchGitCommand(commandCtx, repository.Path, 256, "--no-replace-objects", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil || strings.TrimSpace(string(verifiedCommit)) != revision {
		return researchCodeGraphSnapshot{}, errors.New("registered commit could not be verified")
	}
	tree, err := runResearchGitCommand(commandCtx, repository.Path, researchCodeGraphTreeMaxBytes, "--no-replace-objects", "ls-tree", "-r", "-z", "--full-tree", revision)
	if err != nil {
		return researchCodeGraphSnapshot{}, errors.New("registered commit tree could not be read")
	}
	entries, err := parseResearchCodeGraphTree(tree)
	if err != nil {
		return researchCodeGraphSnapshot{}, err
	}
	if err := validateResearchCodeGraphTreePaths(entries); err != nil {
		return researchCodeGraphSnapshot{}, err
	}
	tempRoot := owner.config.Research.StructuredIndex.CodeGraph.TempRoot
	if !filepath.IsAbs(tempRoot) {
		return researchCodeGraphSnapshot{}, errors.New("CodeGraph task temp root is not absolute")
	}
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return researchCodeGraphSnapshot{}, errors.New("CodeGraph task temp root is unavailable")
	}
	root, err := os.MkdirTemp(tempRoot, "cg-")
	if err != nil {
		return researchCodeGraphSnapshot{}, errors.New("CodeGraph private snapshot directory is unavailable")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return researchCodeGraphSnapshot{}, errors.New("CodeGraph private snapshot directory is unavailable")
	}
	cleanup := true
	defer func() {
		if cleanup {
			removeResearchCodeGraphSnapshot(root)
		}
	}()
	files := make(map[string][]byte, len(entries))
	hasher := sha256.New()
	var totalBytes int64
	for _, entry := range entries {
		blob, readErr := runResearchGitCommand(commandCtx, repository.Path, researchCodeGraphFileMaxBytes+1, "--no-replace-objects", "cat-file", "blob", entry.Hash)
		if readErr != nil || len(blob) > researchCodeGraphFileMaxBytes {
			return researchCodeGraphSnapshot{}, errors.New("registered commit contains an unreadable or oversized file")
		}
		totalBytes += int64(len(blob))
		if totalBytes > researchCodeGraphTotalMaxBytes {
			return researchCodeGraphSnapshot{}, errors.New("registered commit exceeds the local source snapshot limit")
		}
		files[entry.Path] = append([]byte(nil), blob...)
		_, _ = fmt.Fprintf(hasher, "%d:%s:%s:%d:", len(entry.Path), entry.Path, entry.Mode, len(blob))
		_, _ = hasher.Write(blob)
		destination := filepath.Join(root, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return researchCodeGraphSnapshot{}, errors.New("registered commit could not be materialized safely")
		}
		mode := fs.FileMode(0o400)
		if entry.Mode == "100755" {
			mode = 0o500
		}
		if err := os.WriteFile(destination, blob, mode); err != nil {
			return researchCodeGraphSnapshot{}, errors.New("registered commit could not be materialized safely")
		}
		if err := os.Chmod(destination, mode); err != nil {
			return researchCodeGraphSnapshot{}, errors.New("registered commit could not be made read-only")
		}
	}
	if err := makeResearchCodeGraphDirectoriesReadOnly(root); err != nil {
		return researchCodeGraphSnapshot{}, errors.New("registered commit snapshot permissions are invalid")
	}
	snapshotHash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	project := researchCodeGraphProjectID(repository, revision, snapshotHash)
	cleanup = false
	return researchCodeGraphSnapshot{PortSnapshot: engine.CodeGraphSnapshot{Path: root, Project: project, SourceHash: snapshotHash}, Root: root, Files: files}, nil
}

func parseResearchCodeGraphTree(raw []byte) ([]researchCodeGraphTreeFile, error) {
	if len(raw) == 0 || len(raw) > researchCodeGraphTreeMaxBytes || raw[len(raw)-1] != 0 || !utf8.Valid(raw) {
		return nil, errors.New("registered commit tree is empty, oversized, or invalid")
	}
	records := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(records) < 1 || len(records) > researchCodeGraphMaxFiles {
		return nil, errors.New("registered commit file count is outside the local snapshot limit")
	}
	entries := make([]researchCodeGraphTreeFile, 0, len(records))
	for _, record := range records {
		separator := bytes.IndexByte(record, '\t')
		if separator <= 0 || separator == len(record)-1 {
			return nil, errors.New("registered commit tree entry is malformed")
		}
		metadata := strings.Fields(string(record[:separator]))
		if len(metadata) != 3 || metadata[1] != "blob" || metadata[0] != "100644" && metadata[0] != "100755" || !validGitObjectHash(metadata[2]) {
			return nil, errors.New("registered commit contains a symlink, submodule, or unsupported file")
		}
		path := string(record[separator+1:])
		if !safeResearchRepositoryPath(path) {
			return nil, errors.New("registered commit contains an unsafe path")
		}
		entries = append(entries, researchCodeGraphTreeFile{Mode: metadata[0], Hash: metadata[2], Path: path})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func validateResearchCodeGraphTreePaths(entries []researchCodeGraphTreeFile) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		folded := strings.ToLower(norm.NFC.String(entry.Path))
		if _, duplicate := seen[folded]; duplicate {
			return errors.New("registered commit paths collide on a case-insensitive filesystem")
		}
		seen[folded] = struct{}{}
		for parent := filepath.ToSlash(filepath.Dir(entry.Path)); parent != "." && parent != "/"; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if _, parentIsFile := seen[strings.ToLower(parent)]; parentIsFile {
				return errors.New("registered commit file path is also used as a directory")
			}
		}
	}
	return nil
}

func safeResearchRepositoryPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || norm.NFC.String(path) != path || strings.ContainsRune(path, '\\') || strings.IndexByte(path, 0) >= 0 ||
		filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validGitObjectHash(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func researchCodeGraphProjectID(repository config.ResearchRepositoryConfig, revision, snapshotHash string) string {
	identity, _ := json.Marshal(struct {
		Ref        string `json:"ref"`
		Revision   string `json:"revision"`
		Scope      string `json:"scope"`
		OwnerID    string `json:"owner_id"`
		SourceHash string `json:"source_hash"`
	}{repository.Ref, revision, repository.Scope, repository.OwnerID, snapshotHash})
	digest := sha256.Sum256(identity)
	return researchCodeGraphProjectPrefix + hex.EncodeToString(digest[:16])
}

func runResearchGitCommand(ctx context.Context, repositoryPath string, outputLimit int, args ...string) ([]byte, error) {
	if ctx == nil || !filepath.IsAbs(repositoryPath) || outputLimit < 1 {
		return nil, errors.New("registered Git command input is invalid")
	}
	commandCtx, cancel := context.WithTimeout(ctx, researchCodeGraphGitTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", append([]string{"-C", repositoryPath}, args...)...)
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
	}
	stdout := &boundedResearchCodeGraphOutput{limit: outputLimit}
	stderr := &boundedResearchCodeGraphOutput{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || commandCtx.Err() != nil || stdout.overflow || stderr.overflow {
		return nil, errors.New("registered Git command failed or exceeded its limit")
	}
	return stdout.data, nil
}

type boundedResearchCodeGraphOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (output *boundedResearchCodeGraphOutput) Write(data []byte) (int, error) {
	remaining := output.limit - len(output.data)
	if remaining <= 0 || len(data) > remaining {
		if remaining > 0 {
			output.data = append(output.data, data[:remaining]...)
		}
		output.overflow = true
		return 0, errors.New("Git output limit exceeded")
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func makeResearchCodeGraphDirectoriesReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o500)
		}
		return nil
	})
}

func removeResearchCodeGraphSnapshot(root string) {
	if !filepath.IsAbs(root) {
		return
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}
