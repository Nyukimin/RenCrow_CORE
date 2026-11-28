package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	researchLocalCodeRightsBasis = "owner_registered_allowlisted_commit"
	researchLocalPDFMaxPages     = 32
	researchLocalMaterialMaxText = 8 << 20
)

type trustedResearchCodeGraph struct {
	Repository config.ResearchRepositoryConfig
	Snapshot   researchCodeGraphSnapshot
	Result     researchStructuredIndexResult
}

func (owner *researchStructuredIndexOwner) ReadLocalMaterial(ctx context.Context, envelope c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	target := request.Target
	if target == "" {
		target = "documents"
	}
	if ctx == nil || target != "documents" && target != "code" || strings.TrimSpace(request.Query) == "" ||
		len([]rune(request.Query)) > 2000 || !utf8.ValidString(request.Query) || request.Limit < 1 || request.Limit > 20 {
		return engine.LocalDocumentResult{}, errors.New("invalid structured local material request")
	}
	request.Target = target
	switch target {
	case "documents":
		return owner.readLocalPDFMaterials(ctx, envelope, request)
	case "code":
		return owner.readLocalCodeMaterials(ctx, envelope, request)
	default:
		return engine.LocalDocumentResult{}, errors.New("invalid structured local material target")
	}
}

func (owner *researchStructuredIndexOwner) VerifyLocalMaterial(ctx context.Context, envelope c.Envelope, document c.Document) error {
	if owner == nil || ctx == nil || document.LocalOrigin == nil || !document.Complete || !document.RetentionAllowed ||
		document.ContentState != "fetched" || document.Text == "" || document.ExtractedHash == "" {
		return errors.New("structured local material is incomplete or unavailable")
	}
	switch document.LocalOrigin.Kind {
	case "pdf":
		return owner.verifyLocalPDFMaterial(ctx, envelope, document)
	case "code":
		return owner.verifyLocalCodeMaterial(ctx, envelope, document)
	default:
		return errors.New("structured local material kind is unsupported")
	}
}

func (owner *researchStructuredIndexOwner) readLocalPDFMaterials(ctx context.Context, envelope c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	result := engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{}}
	if owner == nil || owner.config == nil {
		return localMaterialUnavailable(result, "documents"), nil
	}
	selected, unavailable := owner.selectTrustedPDFs(envelope, request.DocumentRefs)
	if len(selected) == 0 {
		if unavailable {
			return localMaterialUnavailable(result, "documents"), nil
		}
		return result, nil
	}
	if !owner.config.Research.StructuredIndex.PageIndex.Enabled || owner.pageIndex == nil || owner.store == nil || owner.tasks == nil {
		return localMaterialUnavailable(result, "documents"), nil
	}
	if request.TimeWindow != nil {
		return localMaterialUnavailable(result, "documents"), nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, time.Duration(owner.config.Research.StructuredIndex.PageIndex.TimeoutSeconds)*time.Second)
	defer cancel()
	for _, trusted := range selected {
		if len(result.Items) >= request.Limit {
			unavailable = true
			break
		}
		indexed, found, err := owner.readCurrentPageIndex(operationCtx, envelope, trusted)
		if err != nil || !found {
			unavailable = true
			continue
		}
		node, found := bestResearchPageIndexNode(indexed.Payload.Nodes, request.Query)
		if !found {
			unavailable = true
			continue
		}
		text, err := owner.readCompletePageIndexSection(operationCtx, indexed.Source.rawPDF, node, indexed.Payload.PageIndex.PageCount)
		if err != nil {
			unavailable = true
			continue
		}
		scope := fmt.Sprintf("pdf_pages:%d-%d", node.StartPage, node.EndPage)
		document := c.Document{
			SourceKind: "local_pdf", ProviderID: "core.pageindex." + indexed.Payload.PageIndex.BackendVersion,
			SourceRef: trusted.DocumentRef, Revision: trusted.Revision, Title: node.Title,
			ContentKind: "text", ContentScope: scope, ContentState: "fetched",
			OriginalHash: strings.TrimPrefix(trusted.OriginalSHA256, "sha256:"), ExtractedHash: researchDigest([]byte(text)),
			Text: text, RetentionAllowed: true, Complete: true, Primary: false,
			IndependentSource: "pdf:" + trusted.DocumentRef,
			RetrievedAt:       time.Now().UTC(), TimeBasis: "fetched_at",
			LocalOrigin: &c.LocalMaterialOrigin{Kind: "pdf", PDF: &c.PDFLocalOrigin{
				DocumentRef: trusted.DocumentRef, Revision: trusted.Revision, Scope: scope, RightsBasis: trusted.RightsBasis,
				OriginalHash: strings.TrimPrefix(trusted.OriginalSHA256, "sha256:"), PageCount: indexed.Payload.PageIndex.PageCount,
				Sections: []c.PDFSectionRange{{NodeID: node.NodeID, Title: node.Title, StartPage: node.StartPage, EndPage: node.EndPage}},
			}},
		}
		identity, err := c.LocalCommitIdentity(document)
		if err != nil || identity == "" {
			unavailable = true
			continue
		}
		document.ID = "local_pdf_" + identity
		result.Items = append(result.Items, engine.LocalDocumentItem{Document: document})
	}
	if unavailable {
		return localMaterialUnavailable(result, "documents"), nil
	}
	return result, nil
}

func (owner *researchStructuredIndexOwner) readCompletePageIndexSection(ctx context.Context, pdf []byte, node engine.PageIndexNode, pageCount int) (string, error) {
	pageTotal := node.EndPage - node.StartPage + 1
	if node.StartPage < 1 || node.EndPage < node.StartPage || node.EndPage > pageCount || pageTotal > researchLocalPDFMaxPages {
		return "", errors.New("PageIndex section exceeds the complete local material bounds")
	}
	pages, err := owner.pageIndex.ReadPageText(ctx, pdf, node.StartPage, node.EndPage)
	if err != nil || len(pages) != pageTotal {
		return "", errors.New("PageIndex section page text is incomplete")
	}
	pageText := make([]string, 0, len(pages))
	for index, page := range pages {
		if page.Page != node.StartPage+index || !utf8.ValidString(page.Text) {
			return "", errors.New("PageIndex section page text failed exact readback")
		}
		pageText = append(pageText, page.Text)
	}
	text := strings.Join(pageText, "\n\n")
	if strings.TrimSpace(text) == "" || len([]byte(text)) > researchLocalMaterialMaxText {
		return "", errors.New("PageIndex section text is empty or exceeds the local material bound")
	}
	return text, nil
}

func localMaterialUnavailable(result engine.LocalDocumentResult, target string) engine.LocalDocumentResult {
	result.Missing = append(result.Missing, target)
	result.Code = "unavailable"
	return result
}

func (owner *researchStructuredIndexOwner) readCurrentCodeGraph(ctx context.Context, envelope c.Envelope, repositoryRef, revision string) (trustedResearchCodeGraph, bool, error) {
	current := trustedResearchCodeGraph{}
	if owner == nil || owner.config == nil || owner.codeGraph == nil || owner.tasks == nil {
		return current, false, errors.New("CodeGraph owner is unavailable")
	}
	repository, ok := owner.trustedRepository(repositoryRef, revision, envelope, envelope.OwnerID)
	if !ok {
		return current, false, nil
	}
	request := c.CodeGraphBuildRequest{SchemaVersion: c.CodeGraphRequestSchema, RepositoryRef: repository.Ref, Revision: revision}
	_, _, requestDigest, err := c.CanonicalizeCodeGraphBuildRequest(request)
	if err != nil {
		return current, false, err
	}
	operationKey, digest, err := owner.codeGraphOperationIdentity(request, repository, requestDigest)
	if err != nil {
		return current, false, err
	}
	snapshot, err := owner.createCodeGraphSnapshot(ctx, repository, revision)
	if err != nil {
		return current, false, errors.New("registered CodeGraph source snapshot is unavailable")
	}
	operation, found, err := owner.readStructuredOperation(ctx, envelope.OwnerID, operationKey, digest)
	if err != nil || !found || operation.State != "completed" || operation.Result == nil || operation.Result.Status != "indexed" ||
		!operation.Result.ReadbackVerified || operation.Result.RepositoryRef != repository.Ref || operation.Result.Revision != revision ||
		operation.Result.Project != snapshot.PortSnapshot.Project || operation.Result.SnapshotHash != snapshot.PortSnapshot.SourceHash ||
		!validResearchCodeGraphReceipt(operation.Result.CodeGraph, owner.config.Research.StructuredIndex.CodeGraph, snapshot.PortSnapshot) {
		removeResearchCodeGraphSnapshot(snapshot.Root)
		return current, false, nil
	}
	current.Repository, current.Snapshot, current.Result = repository, snapshot, *operation.Result
	return current, true, nil
}

type localCodeFileMaterial struct {
	bytes     []byte
	functions map[string]c.CodeFunctionRange
	calls     map[string]c.CodeCallRelation
}

func (owner *researchStructuredIndexOwner) readLocalCodeMaterials(ctx context.Context, envelope c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	result := engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{}}
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.CodeGraph.Enabled || owner.codeGraph == nil || owner.tasks == nil {
		return localMaterialUnavailable(result, "code"), nil
	}
	if request.TimeWindow != nil || request.RepoRef == "" || request.Revision == "" {
		return localMaterialUnavailable(result, "code"), nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, time.Duration(owner.config.Research.StructuredIndex.CodeGraph.TimeoutSeconds)*time.Second)
	defer cancel()
	current, found, err := owner.readCurrentCodeGraph(operationCtx, envelope, request.RepoRef, request.Revision)
	if err != nil || !found {
		return localMaterialUnavailable(result, "code"), nil
	}
	defer removeResearchCodeGraphSnapshot(current.Snapshot.Root)
	symbols, err := owner.codeGraph.SearchFunctions(operationCtx, current.Snapshot.PortSnapshot.Project, request.Query, min(request.Limit, researchCodeGraphSearchMax))
	if err != nil || len(symbols) == 0 {
		return localMaterialUnavailable(result, "code"), nil
	}
	files := make(map[string]*localCodeFileMaterial)
	addFunction := func(verified verifiedResearchCodeGraphSymbol) bool {
		path := verified.symbol.Path
		material := files[path]
		if material == nil {
			if len(files) >= request.Limit || len(verified.fileBytes) == 0 || len(verified.fileBytes) > researchCodeGraphFileMaxBytes || !utf8.Valid(verified.fileBytes) {
				return false
			}
			material = &localCodeFileMaterial{bytes: append([]byte(nil), verified.fileBytes...), functions: map[string]c.CodeFunctionRange{}, calls: map[string]c.CodeCallRelation{}}
			files[path] = material
		} else if !bytes.Equal(material.bytes, verified.fileBytes) {
			return false
		}
		function := c.CodeFunctionRange{
			Name: verified.symbol.Name, QualifiedName: verified.symbol.QualifiedName,
			StartLine: verified.symbol.StartLine, EndLine: verified.symbol.EndLine,
		}
		key := fmt.Sprintf("%s:%d:%d", function.QualifiedName, function.StartLine, function.EndLine)
		if _, exists := material.functions[key]; !exists && len(material.functions) >= c.MaxLocalCodeFunctions {
			return false
		}
		material.functions[key] = function
		return true
	}
	unavailable := false
	seenFunctions := make(map[string]struct{}, len(symbols))
	seenRelations := make(map[string]struct{})
	for _, symbol := range symbols {
		verified, valid := verifyResearchCodeGraphSymbol(operationCtx, current.Repository, request.Revision, symbol, current.Snapshot.Files, request.Query)
		if !valid {
			unavailable = true
			continue
		}
		functionKey := fmt.Sprintf("%s:%d:%d", symbol.Path, symbol.StartLine, symbol.EndLine)
		if _, duplicate := seenFunctions[functionKey]; duplicate {
			continue
		}
		seenFunctions[functionKey] = struct{}{}
		if !addFunction(verified) {
			unavailable = true
			continue
		}
		traceFunction := symbol.QualifiedName
		if traceFunction == "" {
			traceFunction = symbol.Name
		}
		relations, traceErr := owner.codeGraph.TraceCalls(operationCtx, current.Snapshot.PortSnapshot.Project, traceFunction, "both", 1, min(request.Limit, researchCodeGraphSearchMax))
		if traceErr != nil {
			unavailable = true
			continue
		}
		for _, relation := range relations {
			if relation.Kind != "CALLS" || relation.CallerName == "" || relation.CalleeName == "" {
				unavailable = true
				continue
			}
			caller, callerOK := owner.verifyCodeGraphEndpoint(operationCtx, current.Snapshot.PortSnapshot.Project, current.Repository, request.Revision, current.Snapshot.Files, relation.CallerName, relation.CallerQualified)
			callee, calleeOK := owner.verifyCodeGraphEndpoint(operationCtx, current.Snapshot.PortSnapshot.Project, current.Repository, request.Revision, current.Snapshot.Files, relation.CalleeName, relation.CalleeQualified)
			if !callerOK || !calleeOK {
				unavailable = true
				continue
			}
			call := c.CodeCallRelation{
				CallerPath: caller.symbol.Path, CallerName: caller.symbol.Name, CallerQualifiedName: caller.symbol.QualifiedName,
				CallerStartLine: caller.symbol.StartLine, CallerEndLine: caller.symbol.EndLine,
				CalleePath: callee.symbol.Path, CalleeName: callee.symbol.Name, CalleeQualifiedName: callee.symbol.QualifiedName,
				CalleeStartLine: callee.symbol.StartLine, CalleeEndLine: callee.symbol.EndLine, Kind: relation.Kind,
			}
			relationKey := fmt.Sprintf("%s:%d:%s:%d:%s", call.CallerPath, call.CallerStartLine, call.CalleePath, call.CalleeStartLine, call.Kind)
			if _, duplicate := seenRelations[relationKey]; duplicate {
				continue
			}
			if len(seenRelations) >= c.MaxLocalCodeRelations {
				unavailable = true
				break
			}
			if !addFunction(caller) || !addFunction(callee) {
				unavailable = true
				continue
			}
			seenRelations[relationKey] = struct{}{}
			files[call.CallerPath].calls[relationKey] = call
			files[call.CalleePath].calls[relationKey] = call
		}
	}
	if len(files) == 0 {
		return localMaterialUnavailable(result, "code"), nil
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		material := files[path]
		fileHash := researchDigest(material.bytes)
		functions := make([]c.CodeFunctionRange, 0, len(material.functions))
		for _, function := range material.functions {
			functions = append(functions, function)
		}
		sort.Slice(functions, func(i, j int) bool {
			if functions[i].StartLine == functions[j].StartLine {
				return functions[i].EndLine < functions[j].EndLine
			}
			return functions[i].StartLine < functions[j].StartLine
		})
		calls := make([]c.CodeCallRelation, 0, len(material.calls))
		for _, call := range material.calls {
			calls = append(calls, call)
		}
		sort.Slice(calls, func(i, j int) bool {
			if calls[i].CallerPath == calls[j].CallerPath {
				if calls[i].CallerStartLine == calls[j].CallerStartLine {
					return calls[i].CalleePath+calls[i].CalleeQualifiedName < calls[j].CalleePath+calls[j].CalleeQualifiedName
				}
				return calls[i].CallerStartLine < calls[j].CallerStartLine
			}
			return calls[i].CallerPath < calls[j].CallerPath
		})
		content := string(material.bytes)
		document := c.Document{
			SourceKind: "local_code", ProviderID: "core.codegraph." + current.Result.CodeGraph.BackendVersion,
			SourceRef: current.Repository.Ref, Revision: request.Revision, Title: path,
			ContentKind: "source_code", ContentScope: "repository_file", ContentState: "fetched",
			OriginalHash: fileHash, ExtractedHash: researchDigest(material.bytes), Text: content,
			RetentionAllowed: true, Complete: true, Primary: false,
			IndependentSource: "repo:" + current.Repository.Ref, RetrievedAt: time.Now().UTC(), TimeBasis: "fetched_at",
			LocalOrigin: &c.LocalMaterialOrigin{Kind: "code", Code: &c.CodeLocalOrigin{
				RepoRef: current.Repository.Ref, Revision: request.Revision, Scope: "repository_file", RightsBasis: researchLocalCodeRightsBasis,
				Path: path, FileHash: fileHash, Functions: functions, Calls: calls,
			}},
		}
		identity, identityErr := c.LocalCommitIdentity(document)
		if identityErr != nil || identity == "" {
			unavailable = true
			continue
		}
		document.ID = "local_code_" + identity
		result.Items = append(result.Items, engine.LocalDocumentItem{Document: document})
	}
	if unavailable {
		return localMaterialUnavailable(result, "code"), nil
	}
	return result, nil
}

func (owner *researchStructuredIndexOwner) verifyLocalPDFMaterial(ctx context.Context, envelope c.Envelope, document c.Document) error {
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.PageIndex.Enabled || owner.pageIndex == nil || owner.store == nil || owner.tasks == nil {
		return errors.New("PageIndex owner is unavailable")
	}
	origin := document.LocalOrigin.PDF
	if origin == nil || document.LocalOrigin.Code != nil || origin.Scope != document.ContentScope || len(origin.Sections) != 1 ||
		document.SourceKind != "local_pdf" || document.ContentKind != "text" || document.TimeBasis != "fetched_at" {
		return errors.New("local PDF material provenance is invalid")
	}
	trusted, found := owner.trustedPDF(origin.DocumentRef, origin.Revision, envelope, envelope.OwnerID)
	trustedOriginalHash := strings.TrimPrefix(trusted.OriginalSHA256, "sha256:")
	if !found || !trusted.RetentionAllowed || origin.RightsBasis != trusted.RightsBasis || origin.OriginalHash != trustedOriginalHash ||
		document.SourceRef != trusted.DocumentRef || document.Revision != trusted.Revision || document.OriginalHash != trustedOriginalHash {
		return errors.New("local PDF source is outside current owner policy")
	}
	indexed, found, err := owner.readCurrentPageIndex(ctx, envelope, trusted)
	if err != nil || !found {
		return errors.New("current PageIndex source receipt is unavailable")
	}
	if origin.PageCount != indexed.Payload.PageIndex.PageCount {
		return errors.New("local PDF page count differs from the current PageIndex receipt")
	}
	if document.ProviderID != "core.pageindex."+indexed.Payload.PageIndex.BackendVersion || document.IndependentSource != "pdf:"+trusted.DocumentRef {
		return errors.New("local PDF provider or independent source identity is invalid")
	}
	section := origin.Sections[0]
	if section.StartPage < 1 || section.EndPage < section.StartPage || section.EndPage-section.StartPage+1 > researchLocalPDFMaxPages ||
		origin.Scope != fmt.Sprintf("pdf_pages:%d-%d", section.StartPage, section.EndPage) {
		return errors.New("local PDF page range is invalid")
	}
	node, found := researchPageIndexNodeByID(indexed.Payload.Nodes, section.NodeID)
	if !found || node.Title != section.Title || node.Title != document.Title || node.StartPage != section.StartPage || node.EndPage != section.EndPage {
		return errors.New("local PDF section no longer matches the current PageIndex source")
	}
	text, err := owner.readCompletePageIndexSection(ctx, indexed.Source.rawPDF, node, indexed.Payload.PageIndex.PageCount)
	if err != nil || text != document.Text || researchDigest([]byte(text)) != document.ExtractedHash {
		return errors.New("local PDF text no longer matches the exact original page range")
	}
	return nil
}

func researchPageIndexNodeByID(nodes []engine.PageIndexNode, nodeID string) (engine.PageIndexNode, bool) {
	if nodeID == "" {
		return engine.PageIndexNode{}, false
	}
	for _, node := range nodes {
		if node.NodeID == nodeID {
			return node, true
		}
		if child, found := researchPageIndexNodeByID(node.Children, nodeID); found {
			return child, true
		}
	}
	return engine.PageIndexNode{}, false
}

func (owner *researchStructuredIndexOwner) verifyLocalCodeMaterial(ctx context.Context, envelope c.Envelope, document c.Document) error {
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.CodeGraph.Enabled || owner.codeGraph == nil || owner.tasks == nil {
		return errors.New("CodeGraph owner is unavailable")
	}
	origin := document.LocalOrigin.Code
	if origin == nil || document.LocalOrigin.PDF != nil || origin.Scope != "repository_file" || document.ContentScope != "repository_file" ||
		origin.Scope != document.ContentScope || origin.RightsBasis != researchLocalCodeRightsBasis || document.SourceKind != "local_code" ||
		document.ContentKind != "source_code" || document.TimeBasis != "fetched_at" || len(origin.Functions) == 0 ||
		origin.FileHash == "" || origin.FileHash != document.OriginalHash || origin.FileHash != document.ExtractedHash {
		return errors.New("local CodeGraph material provenance is invalid")
	}
	repository, ok := owner.trustedRepository(origin.RepoRef, origin.Revision, envelope, envelope.OwnerID)
	if !ok || document.SourceRef != repository.Ref || document.Revision != origin.Revision {
		return errors.New("local code source is outside current owner policy")
	}
	current, found, err := owner.readCurrentCodeGraph(ctx, envelope, repository.Ref, origin.Revision)
	if err != nil || !found {
		return errors.New("current CodeGraph source receipt is unavailable")
	}
	defer removeResearchCodeGraphSnapshot(current.Snapshot.Root)
	if document.ProviderID != "core.codegraph."+current.Result.CodeGraph.BackendVersion || document.Title != origin.Path ||
		document.IndependentSource != "repo:"+repository.Ref {
		return errors.New("local code provider or independent source identity is invalid")
	}
	fileBytes, found := current.Snapshot.Files[origin.Path]
	if !found || !safeResearchRepositoryPath(origin.Path) || len(fileBytes) == 0 || len(fileBytes) > researchCodeGraphFileMaxBytes ||
		!utf8.Valid(fileBytes) || string(fileBytes) != document.Text || researchDigest(fileBytes) != origin.FileHash {
		return errors.New("local code file no longer matches the exact registered commit")
	}
	for _, function := range origin.Functions {
		verified, valid := owner.verifyCodeGraphEndpoint(ctx, current.Snapshot.PortSnapshot.Project, repository, origin.Revision,
			current.Snapshot.Files, function.Name, function.QualifiedName)
		if !valid || verified.symbol.Path != origin.Path || verified.symbol.StartLine != function.StartLine || verified.symbol.EndLine != function.EndLine {
			return errors.New("local code function range no longer matches the current CodeGraph source")
		}
	}
	for _, relation := range origin.Calls {
		if relation.Kind != "CALLS" {
			return errors.New("local code call relation kind is invalid")
		}
		caller, callerOK := owner.verifyCodeGraphEndpoint(ctx, current.Snapshot.PortSnapshot.Project, repository, origin.Revision,
			current.Snapshot.Files, relation.CallerName, relation.CallerQualifiedName)
		callee, calleeOK := owner.verifyCodeGraphEndpoint(ctx, current.Snapshot.PortSnapshot.Project, repository, origin.Revision,
			current.Snapshot.Files, relation.CalleeName, relation.CalleeQualifiedName)
		if !callerOK || !calleeOK || caller.symbol.Path != relation.CallerPath || caller.symbol.StartLine != relation.CallerStartLine ||
			caller.symbol.EndLine != relation.CallerEndLine || callee.symbol.Path != relation.CalleePath ||
			callee.symbol.StartLine != relation.CalleeStartLine || callee.symbol.EndLine != relation.CalleeEndLine {
			return errors.New("local code call endpoint no longer matches the exact commit")
		}
		traceName := relation.CallerQualifiedName
		if traceName == "" {
			traceName = relation.CallerName
		}
		relations, traceErr := owner.codeGraph.TraceCalls(ctx, current.Snapshot.PortSnapshot.Project, traceName, "both", 1, researchCodeGraphSearchMax)
		if traceErr != nil || !researchCodeGraphRelationExists(relations, relation) {
			return errors.New("local code call relation is unavailable in the current graph")
		}
	}
	return nil
}

func researchCodeGraphRelationExists(relations []engine.CodeGraphRelation, expected c.CodeCallRelation) bool {
	for _, relation := range relations {
		if relation.Kind != expected.Kind {
			continue
		}
		callerMatches := relation.CallerName == expected.CallerName
		if expected.CallerQualifiedName != "" {
			callerMatches = callerMatches && relation.CallerQualified == expected.CallerQualifiedName
		}
		calleeMatches := relation.CalleeName == expected.CalleeName
		if expected.CalleeQualifiedName != "" {
			calleeMatches = calleeMatches && relation.CalleeQualified == expected.CalleeQualifiedName
		}
		if callerMatches && calleeMatches {
			return true
		}
	}
	return false
}
