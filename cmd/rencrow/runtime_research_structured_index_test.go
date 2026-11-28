package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/localbackend"
)

type structuredMemoryStore struct {
	objects map[string][]byte
	runs    map[string]contract.RunRecord
}

func newStructuredMemoryStore() *structuredMemoryStore {
	return &structuredMemoryStore{objects: make(map[string][]byte), runs: make(map[string]contract.RunRecord)}
}

func (store *structuredMemoryStore) Put(_ context.Context, data []byte) (contract.ArtifactRef, error) {
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	store.objects[hash] = append([]byte(nil), data...)
	return contract.ArtifactRef{Kind: "sha256", Hash: hash, Ref: "sha256:" + hash}, nil
}

func (store *structuredMemoryStore) Get(_ context.Context, ref contract.ArtifactRef) ([]byte, error) {
	data, ok := store.objects[ref.Hash]
	if !ok {
		return nil, errors.New("object not found")
	}
	return append([]byte(nil), data...), nil
}

func (store *structuredMemoryStore) Load(_ context.Context, runID string) (contract.RunRecord, error) {
	record, ok := store.runs[runID]
	if !ok {
		return contract.RunRecord{}, errors.New("run not found")
	}
	return record, nil
}

func (store *structuredMemoryStore) Save(_ context.Context, record contract.RunRecord) error {
	store.runs[record.Envelope.RunID] = record
	return nil
}

func (store *structuredMemoryStore) corrupt(hash string) {
	store.objects[hash] = []byte("tampered bytes")
}

type structuredPageIndexFake struct {
	pdf       []byte
	builds    int
	reads     int
	readStart int
	readEnd   int
}

func (fake *structuredPageIndexFake) Build(_ context.Context, pdf []byte) (engine.PageIndexResult, error) {
	fake.builds++
	fake.pdf = append([]byte(nil), pdf...)
	return engine.PageIndexResult{
		BackendVersion: localbackend.PageIndexBackendVersion, PackageVersion: localbackend.PageIndexPackageVersion,
		SourceCommit: localbackend.PageIndexSourceCommit, RuntimeManifestSHA256: "sha256:" + strings.Repeat("a", 64),
		PDFParserVersion: localbackend.PageIndexParserVersion, TreeSource: "hybrid", PageCount: 3,
		Nodes: []engine.PageIndexNode{{Title: "Ledger Records", NodeID: "section-ledger", StartPage: 2, EndPage: 3}},
	}, nil
}

func (fake *structuredPageIndexFake) ReadPageText(_ context.Context, pdf []byte, start, end int) ([]engine.PageText, error) {
	fake.reads++
	fake.readStart, fake.readEnd = start, end
	if !bytes.Equal(pdf, fake.pdf) {
		return nil, errors.New("PageIndex received different original PDF bytes")
	}
	return []engine.PageText{
		{Page: 2, Text: "Chapter Two - Ledger Records\nLedger Records: the second clause identifies the verified call site."},
		{Page: 3, Text: "End of the retained ledger section."},
	}, nil
}

func TestResearchStructuredPDFOwnerPersistsOriginalAndReadsExactPage(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, e, tasks := researchOwnerFixture(t)
	store := newStructuredMemoryStore()
	e.Ports.Store = store
	pdf := []byte("%PDF-1.7\noriginal retained ledger bytes\n")
	originalHash := researchSHA256(pdf)
	const documentRef, revision = "ledger-original", "revision-v1"
	cfg.Research = config.ResearchConfig{
		Enabled: true, ArtifactRoot: filepath.Join(t.TempDir(), "artifacts"),
		StructuredIndex: config.ResearchStructuredIndexConfig{
			PageIndex: config.ResearchPageIndexConfig{Enabled: true, RuntimeManifestSHA256: "sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 60},
			TrustedPDFs: []config.ResearchTrustedPDFConfig{{
				DocumentRef: documentRef, Revision: revision, OriginalSHA256: originalHash,
				DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true,
			}},
		},
	}
	pageIndex := &structuredPageIndexFake{}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren", store: store, tasks: tasks, pageIndex: pageIndex}
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil, owner)
	if err != nil {
		t.Fatal(err)
	}
	registerRequest, _ := json.Marshal(researchPDFRegisterRequest{
		SchemaVersion: researchPDFRegisterSchema, DocumentRef: documentRef, Revision: revision,
		PDFBase64: base64.StdEncoding.EncodeToString(pdf),
	})
	registeredHTTP := researchIndexPost(t, handler, map[string]any{"operation": "source_register", "request": json.RawMessage(registerRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var registered researchStructuredOperationResponse
	if err := json.Unmarshal(registeredHTTP.Body.Bytes(), &registered); err != nil || registeredHTTP.Code != 200 || registered.State != "completed" || registered.Result == nil ||
		registered.Result.Status != "registered" || registered.Result.Kind != "source_register" || !registered.Result.ReadbackVerified ||
		registered.Result.OriginalSHA256 != originalHash || registered.Result.RightsBasis != "owner-authorized-retention" || registered.Result.SourceArtifact == nil {
		t.Fatalf("original source registration = status %d response %+v err=%v body=%s", registeredHTTP.Code, registered, err, registeredHTTP.Body.String())
	}
	storedPDF, err := store.Get(context.Background(), *registered.Result.SourceArtifact)
	if err != nil || !bytes.Equal(storedPDF, pdf) {
		t.Fatalf("registered original PDF readback differs: equal=%t err=%v", bytes.Equal(storedPDF, pdf), err)
	}
	indexRequest, _ := json.Marshal(contract.PageIndexBuildRequest{
		SchemaVersion: contract.PageIndexRequestSchema,
		Documents:     []contract.PageIndexDocumentRef{{DocumentRef: documentRef, Revision: revision}},
	})
	indexHTTP := researchIndexPost(t, handler, map[string]any{"operation": "page_index_build", "request": json.RawMessage(indexRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var indexed researchStructuredBatchResponse
	if err := json.Unmarshal(indexHTTP.Body.Bytes(), &indexed); err != nil || indexHTTP.Code != 200 || indexed.State != "completed" || len(indexed.Operations) != 1 ||
		indexed.Operations[0].Result == nil || indexed.Operations[0].Result.Status != "indexed" || !indexed.Operations[0].Result.ReadbackVerified || pageIndex.builds != 1 || !bytes.Equal(pageIndex.pdf, pdf) {
		t.Fatalf("PageIndex original-byte build = status %d response %+v err=%v builds=%d body=%s", indexHTTP.Code, indexed, err, pageIndex.builds, indexHTTP.Body.String())
	}
	request := contract.RecallRequest{Query: "Ledger Records", Targets: []string{"documents"}, DocumentRefs: []string{documentRef + "@" + revision}}
	hits, unavailable, err := owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, request, request.Query, 4)
	if err != nil || unavailable || len(hits) != 1 || hits[0].Revision != revision || hits[0].Evidence == nil || hits[0].Evidence.ArtifactHash != strings.TrimPrefix(originalHash, "sha256:") ||
		pageIndex.reads != 1 || pageIndex.readStart != 2 || pageIndex.readEnd != 3 || !strings.Contains(hits[0].Excerpt, "Chapter Two - Ledger Records") || !strings.Contains(hits[0].Locator, "/pages/2-3") {
		t.Fatalf("exact PageIndex page evidence = hits=%+v unavailable=%t err=%v page reads=%d range=%d-%d", hits, unavailable, err, pageIndex.reads, pageIndex.readStart, pageIndex.readEnd)
	}
	wrongRevision := request
	wrongRevision.DocumentRefs = []string{documentRef + "@revision-wrong"}
	wrongHits, unavailable, err := owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, wrongRevision, request.Query, 4)
	if err != nil || !unavailable || len(wrongHits) != 0 {
		t.Fatalf("wrong trusted PDF revision must be unavailable: hits=%+v unavailable=%t err=%v", wrongHits, unavailable, err)
	}
	wrongOwnerHits, unavailable, err := owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "other", DataScope: "user"}, request, request.Query, 4)
	if err != nil || !unavailable || len(wrongOwnerHits) != 0 {
		t.Fatalf("wrong PDF owner must be unavailable: hits=%+v unavailable=%t err=%v", wrongOwnerHits, unavailable, err)
	}
	localMaterial, err := owner.ReadLocalMaterial(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, engine.LocalDocumentRequest{
		Target: "documents", Query: "Ledger Records", DocumentRefs: []string{documentRef + "@" + revision}, Limit: 4,
	})
	if err != nil || localMaterial.Code != "" || len(localMaterial.Missing) != 0 || len(localMaterial.Items) != 1 {
		t.Fatalf("complete PageIndex section material = %+v err=%v", localMaterial, err)
	}
	pdfDocument := localMaterial.Items[0].Document
	expectedSection := "Chapter Two - Ledger Records\nLedger Records: the second clause identifies the verified call site.\n\nEnd of the retained ledger section."
	if localMaterial.Items[0].Receipt != (contract.Receipt{}) || pdfDocument.Text != expectedSection || pdfDocument.OriginalHash != strings.TrimPrefix(originalHash, "sha256:") ||
		pdfDocument.ExtractedHash != researchDigest([]byte(expectedSection)) || pdfDocument.ContentScope != "pdf_pages:2-3" ||
		pdfDocument.LocalOrigin == nil || pdfDocument.LocalOrigin.PDF == nil || pdfDocument.LocalOrigin.PDF.OriginalHash != strings.TrimPrefix(originalHash, "sha256:") ||
		pdfDocument.IndependentSource != "pdf:"+documentRef {
		t.Fatalf("PageIndex material lost source hash, complete page text, range, or stable independence: %+v", pdfDocument)
	}
	if err := owner.VerifyLocalMaterial(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, pdfDocument); err != nil {
		t.Fatalf("exact current PDF source failed local material verification: %v", err)
	}
	store.corrupt(registered.Result.SourceArtifact.Hash)
	wrongHits, unavailable, err = owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, request, request.Query, 4)
	if err == nil || !unavailable || len(wrongHits) != 0 {
		t.Fatalf("tampered original PDF must be unavailable: hits=%+v unavailable=%t err=%v", wrongHits, unavailable, err)
	}
	cfg.Research.StructuredIndex.TrustedPDFs[0].RetentionAllowed = false
	wrongHits, unavailable, err = owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, request, request.Query, 4)
	if err != nil || !unavailable || len(wrongHits) != 0 {
		t.Fatalf("revoked PDF retention rights must be unavailable: hits=%+v unavailable=%t err=%v", wrongHits, unavailable, err)
	}
}

func TestResearchStructuredPDFTimeWindowOnlyRejectsSelectedPDFs(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, _, tasks := researchOwnerFixture(t)
	store := newStructuredMemoryStore()
	pageIndex := &structuredPageIndexFake{}
	cfg.Research = config.ResearchConfig{
		Enabled: true, ArtifactRoot: filepath.Join(t.TempDir(), "artifacts"),
		StructuredIndex: config.ResearchStructuredIndexConfig{
			PageIndex: config.ResearchPageIndexConfig{Enabled: true, RuntimeManifestSHA256: "sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 60},
			TrustedPDFs: []config.ResearchTrustedPDFConfig{{
				DocumentRef: "trusted-pdf", Revision: "revision-v1", OriginalSHA256: "sha256:" + strings.Repeat("b", 64),
				DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true,
			}},
		},
	}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren", store: store, tasks: tasks, pageIndex: pageIndex}
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	timeWindow := &contract.TimeWindow{Since: since, Until: since.Add(24 * time.Hour)}
	request := contract.RecallRequest{
		Query: "ledger", Targets: []string{"documents"}, DocumentRefs: []string{"l1://unrelated-document"}, TimeWindow: timeWindow,
	}
	hits, unavailable, err := owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, request, request.Query, 4)
	if err != nil || unavailable || len(hits) != 0 || pageIndex.builds != 0 || pageIndex.reads != 0 {
		t.Fatalf("unselected PDF with L1 time filter should leave PageIndex unchanged: hits=%+v unavailable=%t err=%v builds=%d reads=%d", hits, unavailable, err, pageIndex.builds, pageIndex.reads)
	}

	request.DocumentRefs = []string{"trusted-pdf@revision-v1"}
	hits, unavailable, err = owner.RecallPageIndexDocuments(context.Background(), contract.Envelope{OwnerID: "ren", DataScope: "user"}, request, request.Query, 4)
	if err == nil || !unavailable || len(hits) != 0 || pageIndex.builds != 0 || pageIndex.reads != 0 {
		t.Fatalf("selected PDF with unsupported time filter should remain unavailable: hits=%+v unavailable=%t err=%v builds=%d reads=%d", hits, unavailable, err, pageIndex.builds, pageIndex.reads)
	}
}

func TestResearchLocalPDFReadDoesNotDegradeUnselectedL1WhenBackendIsDisabled(t *testing.T) {
	cfg, _, _ := researchOwnerFixture(t)
	pageIndex := &structuredPageIndexFake{}
	cfg.Research.StructuredIndex = config.ResearchStructuredIndexConfig{
		CodeGraph: config.ResearchCodeGraphConfig{Enabled: true},
	}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren", pageIndex: pageIndex}
	envelope := contract.Envelope{OwnerID: "ren", DataScope: "user"}
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	result, err := owner.ReadLocalMaterial(context.Background(), envelope, engine.LocalDocumentRequest{
		Target: "documents", Query: "existing retained source", DocumentRefs: []string{"l1-event-id"},
		TimeWindow: &contract.TimeWindow{Since: since, Until: since.Add(time.Hour)}, Limit: 4,
	})
	if err != nil || result.Code != "" || len(result.Items) != 0 || len(result.Missing) != 0 || pageIndex.builds != 0 || pageIndex.reads != 0 {
		t.Fatalf("unselected L1 source became unavailable because PageIndex is disabled: result=%+v err=%v builds=%d reads=%d", result, err, pageIndex.builds, pageIndex.reads)
	}

	cfg.Research.StructuredIndex.TrustedPDFs = []config.ResearchTrustedPDFConfig{{
		DocumentRef: "trusted-pdf", Revision: "revision-v1", OriginalSHA256: "sha256:" + strings.Repeat("b", 64),
		DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true,
	}}
	selected, err := owner.ReadLocalMaterial(context.Background(), envelope, engine.LocalDocumentRequest{
		Target: "documents", Query: "ledger", DocumentRefs: []string{"trusted-pdf@revision-v1"}, Limit: 4,
	})
	if err != nil || selected.Code != "unavailable" || len(selected.Missing) == 0 || pageIndex.builds != 0 || pageIndex.reads != 0 {
		t.Fatalf("explicit trusted PDF remained silently available while its backend is disabled: result=%+v err=%v reads=%d", selected, err, pageIndex.reads)
	}
}

func TestResearchStructuredPDFSelectionUsesExactVersionAcrossConfiguredRevisions(t *testing.T) {
	cfg, _, _ := researchOwnerFixture(t)
	cfg.Research.StructuredIndex.TrustedPDFs = []config.ResearchTrustedPDFConfig{
		{DocumentRef: "spec", Revision: "v1", OriginalSHA256: "sha256:" + strings.Repeat("a", 64), DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true},
		{DocumentRef: "spec", Revision: "v2", OriginalSHA256: "sha256:" + strings.Repeat("b", 64), DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true},
		{DocumentRef: "outside", Revision: "v1", OriginalSHA256: "sha256:" + strings.Repeat("c", 64), DataScope: "public", RightsBasis: "public-owner-registration", RetentionAllowed: true},
	}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren"}
	envelope := contract.Envelope{OwnerID: "ren", DataScope: "user"}
	for _, test := range []struct {
		name            string
		ref             string
		wantRevision    string
		wantUnavailable bool
		wantSelected    int
	}{
		{name: "version one ignores sibling revision", ref: "spec@v1", wantRevision: "v1", wantSelected: 1},
		{name: "version two selected exactly", ref: "spec@v2", wantRevision: "v2", wantSelected: 1},
		{name: "unknown version unavailable", ref: "spec@v3", wantUnavailable: true},
		{name: "scope outside unavailable", ref: "outside@v1", wantUnavailable: true},
		{name: "bare ref ambiguous across versions", ref: "spec", wantUnavailable: true},
		{name: "unrelated L1 ref remains unselected", ref: "l1-event-id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, unavailable := owner.selectTrustedPDFs(envelope, []string{test.ref})
			if len(selected) != test.wantSelected || unavailable != test.wantUnavailable {
				t.Fatalf("select %q = %+v unavailable=%t", test.ref, selected, unavailable)
			}
			if len(selected) == 1 && selected[0].Revision != test.wantRevision {
				t.Fatalf("select %q returned revision %q, want %q", test.ref, selected[0].Revision, test.wantRevision)
			}
		})
	}
}

type structuredCodeGraphFake struct {
	config         config.ResearchCodeGraphConfig
	indexCalls     int
	searchCalls    int
	traceCalls     int
	workerBytes    []byte
	failIndex      bool
	searchOverride map[string][]engine.CodeGraphSymbol
}

func (fake *structuredCodeGraphFake) IndexRepository(_ context.Context, snapshot engine.CodeGraphSnapshot) (engine.CodeGraphIndexReceipt, error) {
	fake.indexCalls++
	if fake.failIndex {
		return engine.CodeGraphIndexReceipt{}, errors.New("injected CodeGraph failure")
	}
	info, err := os.Stat(filepath.Join(snapshot.Path, "worker.go"))
	if err != nil || info.Mode().Perm()&0o222 != 0 {
		return engine.CodeGraphIndexReceipt{}, errors.New("snapshot is absent or writable")
	}
	worker, err := os.ReadFile(filepath.Join(snapshot.Path, "worker.go"))
	if err != nil {
		return engine.CodeGraphIndexReceipt{}, err
	}
	fake.workerBytes = worker
	return engine.CodeGraphIndexReceipt{
		Project: snapshot.Project, BackendVersion: localbackend.CodeGraphBackendVersion,
		RuntimeVersion: fake.config.RuntimeVersion, SourceCommit: fake.config.SourceCommit,
		BinarySHA256: fake.config.BinarySHA256, SourceHash: snapshot.SourceHash,
		Status: engine.CodeGraphIndexIndexed, Nodes: 9,
	}, nil
}

func (fake *structuredCodeGraphFake) SearchFunctions(_ context.Context, project, namePattern string, limit int) ([]engine.CodeGraphSymbol, error) {
	fake.searchCalls++
	if project == "" || limit < 1 {
		return nil, errors.New("invalid search request")
	}
	if symbols, ok := fake.searchOverride[namePattern]; ok {
		return append([]engine.CodeGraphSymbol(nil), symbols...), nil
	}
	switch namePattern {
	case "HandleLedger":
		return []engine.CodeGraphSymbol{{Name: "HandleLedger", QualifiedName: "fixture.HandleLedger", Path: "worker.go", StartLine: 2, EndLine: 4}}, nil
	case "StoreLedger":
		return []engine.CodeGraphSymbol{{Name: "StoreLedger", QualifiedName: "fixture.StoreLedger", Path: "worker.go", StartLine: 5, EndLine: 7}}, nil
	default:
		return []engine.CodeGraphSymbol{}, nil
	}
}

func (fake *structuredCodeGraphFake) TraceCalls(_ context.Context, project, functionName, direction string, depth, limit int) ([]engine.CodeGraphRelation, error) {
	fake.traceCalls++
	if project == "" || (functionName != "fixture.HandleLedger" && functionName != "HandleLedger") || direction != "both" || depth != 1 || limit < 1 {
		return nil, errors.New("invalid trace request")
	}
	return []engine.CodeGraphRelation{{
		CallerName: "HandleLedger", CallerQualified: "fixture.HandleLedger",
		CalleeName: "StoreLedger", CalleeQualified: "fixture.StoreLedger", Kind: "CALLS",
	}}, nil
}

func TestResearchStructuredCodeGraphBuildAndRecallRequireExactCommitSources(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, e, tasks := researchOwnerFixture(t)
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	if err := os.MkdirAll(repositoryPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "init", "-q")
	runGit(t, repositoryPath, "config", "user.email", "test@example.invalid")
	runGit(t, repositoryPath, "config", "user.name", "Research Test")
	firstSource := []byte("package fixture\nfunc HandleLedger() {\n\tStoreLedger()\n}\nfunc StoreLedger() {\n\treturn\n}\n")
	if err := os.WriteFile(filepath.Join(repositoryPath, "worker.go"), firstSource, 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "worker.go")
	runGit(t, repositoryPath, "commit", "-q", "-m", "ledger functions")
	firstRevision := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	graphConfig := config.ResearchCodeGraphConfig{
		Enabled: true, BinarySHA256: "sha256:" + strings.Repeat("a", 64), RuntimeVersion: "0.12.1",
		SourceCommit: localbackend.CodeGraphSourceCommit, CacheRoot: filepath.Join(root, "cache"),
		RuntimeRoot: filepath.Join(root, "cache", "runtime"), TempRoot: filepath.Join(root, "tmp"), TimeoutSeconds: 60,
	}
	cfg.Research = config.ResearchConfig{
		Enabled: true, ArtifactRoot: filepath.Join(root, "artifacts"),
		Repositories:    []config.ResearchRepositoryConfig{{Ref: "repo-fixture", Path: repositoryPath, AllowedRevisions: []string{firstRevision}, Scope: "user", OwnerID: "ren"}},
		StructuredIndex: config.ResearchStructuredIndexConfig{CodeGraph: graphConfig},
	}
	fake := &structuredCodeGraphFake{config: graphConfig}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren", store: e.Ports.Store, tasks: tasks, codeGraph: fake}
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil, owner)
	if err != nil {
		t.Fatal(err)
	}
	buildRequest, _ := json.Marshal(contract.CodeGraphBuildRequest{SchemaVersion: contract.CodeGraphRequestSchema, RepositoryRef: "repo-fixture", Revision: firstRevision})
	builtHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(buildRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var built researchStructuredOperationResponse
	if err := json.Unmarshal(builtHTTP.Body.Bytes(), &built); err != nil || builtHTTP.Code != 200 || built.State != "completed" || built.Result == nil ||
		built.Result.Status != "indexed" || !built.Result.ReadbackVerified || built.Result.Revision != firstRevision || built.Result.SnapshotHash == "" || fake.indexCalls != 1 || !bytes.Equal(fake.workerBytes, firstSource) {
		t.Fatalf("exact CodeGraph build = status %d response %+v err=%v indexes=%d body=%s", builtHTTP.Code, built, err, fake.indexCalls, builtHTTP.Body.String())
	}
	replayedHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(buildRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	if replayedHTTP.Code != 200 || fake.indexCalls != 1 {
		t.Fatalf("terminal CodeGraph build replay rebuilt the same snapshot: status=%d calls=%d body=%s", replayedHTTP.Code, fake.indexCalls, replayedHTTP.Body.String())
	}
	envelope := contract.Envelope{OwnerID: "ren", DataScope: "user"}
	request := contract.RecallRequest{Query: "HandleLedger", Targets: []string{"code"}, RepoRef: "repo-fixture", Revision: firstRevision}
	hits, unavailable, err := owner.RecallCodeGraph(context.Background(), envelope, request, request.Query, 8)
	if err != nil || unavailable || len(hits) < 2 || fake.searchCalls < 3 || fake.traceCalls != 1 {
		t.Fatalf("same-commit CodeGraph source evidence = hits=%+v unavailable=%t err=%v searches=%d traces=%d", hits, unavailable, err, fake.searchCalls, fake.traceCalls)
	}
	foundFunction, foundEndpoints := false, 0
	for _, hit := range hits {
		if hit.Revision != firstRevision || hit.EvidenceState != "acquired" || hit.Evidence == nil || hit.Evidence.ArtifactHash != researchDigest(firstSource) {
			t.Fatalf("CodeGraph evidence was not exact source: %+v", hit)
		}
		if hit.MatchKind == "codegraph_function" {
			foundFunction = true
		}
		if hit.MatchKind == "codegraph_call_endpoint" {
			if !strings.Contains(hit.Locator, ":CALLS:") || strings.Contains(hit.Locator, ":call:") {
				t.Fatalf("CodeGraph CALLS locator claimed an unverified call-site line: %q", hit.Locator)
			}
			foundEndpoints++
		}
	}
	if !foundFunction || foundEndpoints != 2 {
		t.Fatalf("function/call endpoint evidence = function=%t endpoints=%d hits=%+v", foundFunction, foundEndpoints, hits)
	}
	localCode, err := owner.ReadLocalMaterial(context.Background(), envelope, engine.LocalDocumentRequest{
		Target: "code", RepoRef: "repo-fixture", Revision: firstRevision, Query: "HandleLedger", Limit: 8,
	})
	if err != nil || localCode.Code != "" || len(localCode.Missing) != 0 || len(localCode.Items) != 1 {
		t.Fatalf("complete same-commit source file material = %+v err=%v", localCode, err)
	}
	codeDocument := localCode.Items[0].Document
	if localCode.Items[0].Receipt != (contract.Receipt{}) || codeDocument.Text != string(firstSource) || codeDocument.OriginalHash != researchDigest(firstSource) ||
		codeDocument.ExtractedHash != researchDigest(firstSource) || codeDocument.IndependentSource != "repo:repo-fixture" ||
		codeDocument.LocalOrigin == nil || codeDocument.LocalOrigin.Code == nil || len(codeDocument.LocalOrigin.Code.Functions) != 2 || len(codeDocument.LocalOrigin.Code.Calls) != 1 {
		t.Fatalf("CodeGraph material was excerpted or lost source provenance: %+v", codeDocument)
	}
	if err := owner.VerifyLocalMaterial(context.Background(), envelope, codeDocument); err != nil {
		t.Fatalf("exact CodeGraph source file failed current source verification: %v", err)
	}
	secondSource := []byte("package fixture\nfunc StoreLedger() {\n\treturn\n}\n")
	if err := os.WriteFile(filepath.Join(repositoryPath, "worker.go"), secondSource, 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "commit", "-q", "-am", "remove handler")
	secondRevision := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	cfg.Research.Repositories[0].AllowedRevisions = append(cfg.Research.Repositories[0].AllowedRevisions, secondRevision)
	missingRequest := request
	missingRequest.Revision = secondRevision
	newHits, unavailable, err := owner.RecallCodeGraph(context.Background(), envelope, missingRequest, request.Query, 8)
	if err != nil || !unavailable || len(newHits) != 0 {
		t.Fatalf("deleted function in a new exact commit must be unconfirmed: hits=%+v unavailable=%t err=%v", newHits, unavailable, err)
	}

	secondBuildRequest, _ := json.Marshal(contract.CodeGraphBuildRequest{SchemaVersion: contract.CodeGraphRequestSchema, RepositoryRef: "repo-fixture", Revision: secondRevision})
	secondBuildHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(secondBuildRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var secondBuild researchStructuredOperationResponse
	if err := json.Unmarshal(secondBuildHTTP.Body.Bytes(), &secondBuild); err != nil || secondBuildHTTP.Code != 200 || secondBuild.State != "completed" || secondBuild.Result == nil ||
		secondBuild.Result.Status != "indexed" || secondBuild.Result.Revision != secondRevision || fake.indexCalls != 2 {
		t.Fatalf("second exact CodeGraph commit build = status %d response %+v err=%v indexes=%d body=%s", secondBuildHTTP.Code, secondBuild, err, fake.indexCalls, secondBuildHTTP.Body.String())
	}
	newHits, unavailable, err = owner.RecallCodeGraph(context.Background(), envelope, missingRequest, request.Query, 8)
	if err != nil || !unavailable || len(newHits) != 0 {
		t.Fatalf("deleted function candidate from old graph lines became evidence for the new commit: hits=%+v unavailable=%t err=%v", newHits, unavailable, err)
	}
	deletedMaterial, err := owner.ReadLocalMaterial(context.Background(), envelope, engine.LocalDocumentRequest{
		Target: "code", RepoRef: "repo-fixture", Revision: secondRevision, Query: "HandleLedger", Limit: 8,
	})
	if err != nil || deletedMaterial.Code != "unavailable" || len(deletedMaterial.Items) != 0 || len(deletedMaterial.Missing) == 0 {
		t.Fatalf("function deleted from the exact new commit remained complete local material: result=%+v err=%v", deletedMaterial, err)
	}

	cfg.Research.Repositories = append(cfg.Research.Repositories, config.ResearchRepositoryConfig{
		Ref: "repo-failure", Path: repositoryPath, AllowedRevisions: []string{secondRevision}, Scope: "user", OwnerID: "ren",
	})
	fake.failIndex = true
	failureRequest, _ := json.Marshal(contract.CodeGraphBuildRequest{SchemaVersion: contract.CodeGraphRequestSchema, RepositoryRef: "repo-failure", Revision: secondRevision})
	failedBuildHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(failureRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var failedBuild researchStructuredOperationResponse
	if err := json.Unmarshal(failedBuildHTTP.Body.Bytes(), &failedBuild); err != nil || failedBuildHTTP.Code != 200 || failedBuild.State != "completed" || failedBuild.Result == nil ||
		failedBuild.Result.Status != "failed" || failedBuild.Result.ErrorCode != "code_graph_index_failed" || failedBuild.Result.ReadbackVerified || fake.indexCalls != 3 {
		t.Fatalf("durable CodeGraph build failure was reported as unknown or success: status=%d response=%+v err=%v index_calls=%d body=%s", failedBuildHTTP.Code, failedBuild, err, fake.indexCalls, failedBuildHTTP.Body.String())
	}
	replayFailedHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(failureRequest)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var replayFailed researchStructuredOperationResponse
	if err := json.Unmarshal(replayFailedHTTP.Body.Bytes(), &replayFailed); err != nil || replayFailedHTTP.Code != 200 || replayFailed.State != "completed" || replayFailed.Result == nil ||
		replayFailed.Result.Status != "failed" || replayFailed.Result.RunID != failedBuild.Result.RunID || fake.indexCalls != 3 {
		t.Fatalf("failed CodeGraph terminal receipt was not replayed without rebuilding: status=%d response=%+v err=%v index_calls=%d body=%s", replayFailedHTTP.Code, replayFailed, err, fake.indexCalls, replayFailedHTTP.Body.String())
	}
}

func TestResearchCodeGraphEndpointRequiresExactQualifiedPackage(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	if err := os.MkdirAll(repositoryPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "init", "-q")
	runGit(t, repositoryPath, "config", "user.email", "test@example.invalid")
	runGit(t, repositoryPath, "config", "user.name", "Research Test")
	firstSource := []byte("package first\nfunc HandleLedger() {}\n")
	secondSource := []byte("package second\nfunc HandleLedger() {}\n")
	if err := os.WriteFile(filepath.Join(repositoryPath, "first.go"), firstSource, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "second.go"), secondSource, 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "first.go", "second.go")
	runGit(t, repositoryPath, "commit", "-q", "-m", "same symbol names in separate packages")
	revision := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	repository := config.ResearchRepositoryConfig{Path: repositoryPath, AllowedRevisions: []string{revision}}
	fake := &structuredCodeGraphFake{searchOverride: map[string][]engine.CodeGraphSymbol{
		"HandleLedger": {
			{Name: "HandleLedger", QualifiedName: "fixture.second.HandleLedger", Path: "second.go", StartLine: 2, EndLine: 2},
			{Name: "HandleLedger", QualifiedName: "fixture.first.HandleLedger", Path: "first.go", StartLine: 2, EndLine: 2},
		},
	}}
	owner := &researchStructuredIndexOwner{codeGraph: fake}
	endpoint, ok := owner.verifyCodeGraphEndpoint(context.Background(), "project", repository, revision,
		map[string][]byte{"first.go": firstSource, "second.go": secondSource}, "HandleLedger", "fixture.first.HandleLedger")
	if !ok || endpoint.symbol.QualifiedName != "fixture.first.HandleLedger" || endpoint.symbol.Path != "first.go" || !bytes.Equal(endpoint.fileBytes, firstSource) {
		t.Fatalf("same-name symbol from another package satisfied a qualified CALLS endpoint: endpoint=%+v found=%t", endpoint.symbol, ok)
	}
}

func TestResearchCodeGraphSnapshotRejectsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	if err := os.MkdirAll(repositoryPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "init", "-q")
	runGit(t, repositoryPath, "config", "user.email", "test@example.invalid")
	runGit(t, repositoryPath, "config", "user.name", "Research Test")
	if err := os.Symlink("../../outside", filepath.Join(repositoryPath, "source-link")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "source-link")
	runGit(t, repositoryPath, "commit", "-q", "-m", "symlink")
	revision := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	cfg := &config.Config{Research: config.ResearchConfig{StructuredIndex: config.ResearchStructuredIndexConfig{
		CodeGraph: config.ResearchCodeGraphConfig{Enabled: true, TempRoot: filepath.Join(root, "tmp"), TimeoutSeconds: 60},
	}}}
	owner := &researchStructuredIndexOwner{config: cfg}
	_, err := owner.createCodeGraphSnapshot(context.Background(), config.ResearchRepositoryConfig{Path: repositoryPath, AllowedRevisions: []string{revision}}, revision)
	if err == nil {
		t.Fatal("CodeGraph snapshot accepted a Git symlink")
	}
}

func TestResearchStructuredCodeGraphReceiptBindsRuntimeAndSnapshot(t *testing.T) {
	cfg := config.ResearchCodeGraphConfig{RuntimeVersion: "0.12.1", SourceCommit: localbackend.CodeGraphSourceCommit, BinarySHA256: "sha256:" + strings.Repeat("a", 64)}
	snapshot := engine.CodeGraphSnapshot{Path: "/private/snapshot", Project: "project", SourceHash: "sha256:" + strings.Repeat("b", 64)}
	receipt := &engine.CodeGraphIndexReceipt{Project: snapshot.Project, BackendVersion: localbackend.CodeGraphBackendVersion,
		RuntimeVersion: cfg.RuntimeVersion, SourceCommit: cfg.SourceCommit, BinarySHA256: cfg.BinarySHA256,
		SourceHash: snapshot.SourceHash, Status: engine.CodeGraphIndexIndexed, Nodes: 9}
	if !validResearchCodeGraphReceipt(receipt, cfg, snapshot) {
		t.Fatal("matching CodeGraph receipt was rejected")
	}
	receipt.SourceHash = "sha256:" + strings.Repeat("c", 64)
	if validResearchCodeGraphReceipt(receipt, cfg, snapshot) {
		t.Fatal("CodeGraph receipt from a different snapshot was accepted")
	}
	if got := researchCodeGraphProjectID(config.ResearchRepositoryConfig{Ref: "repo", Scope: "user", OwnerID: "ren"}, strings.Repeat("a", 40), snapshot.SourceHash); got == "" || !strings.HasPrefix(got, researchCodeGraphProjectPrefix) {
		t.Fatalf("invalid deterministic CodeGraph project identity %q", got)
	}
}

func TestResearchStructuredCodeGraphUnknownReceiptRetryDoesNotRebuild(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, e, tasks, taskStore := researchIndexFaultRuntimeFixture(t)
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	if err := os.MkdirAll(repositoryPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "init", "-q")
	runGit(t, repositoryPath, "config", "user.email", "test@example.invalid")
	runGit(t, repositoryPath, "config", "user.name", "Research Test")
	if err := os.WriteFile(filepath.Join(repositoryPath, "worker.go"), []byte("package fixture\nfunc HandleLedger() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "worker.go")
	runGit(t, repositoryPath, "commit", "-q", "-m", "ledger function")
	revision := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	graphConfig := config.ResearchCodeGraphConfig{
		Enabled: true, BinarySHA256: "sha256:" + strings.Repeat("a", 64), RuntimeVersion: "0.12.1",
		SourceCommit: localbackend.CodeGraphSourceCommit, CacheRoot: filepath.Join(root, "cache"),
		RuntimeRoot: filepath.Join(root, "cache", "runtime"), TempRoot: filepath.Join(root, "tmp"), TimeoutSeconds: 60,
	}
	cfg.Research = config.ResearchConfig{
		Enabled: true, ArtifactRoot: filepath.Join(root, "artifacts"),
		Repositories:    []config.ResearchRepositoryConfig{{Ref: "repo-fixture", Path: repositoryPath, AllowedRevisions: []string{revision}, Scope: "user", OwnerID: "ren"}},
		StructuredIndex: config.ResearchStructuredIndexConfig{CodeGraph: graphConfig},
	}
	fake := &structuredCodeGraphFake{config: graphConfig}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: "ren", store: e.Ports.Store, tasks: tasks, codeGraph: fake}
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil, owner)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(contract.CodeGraphBuildRequest{SchemaVersion: contract.CodeGraphRequestSchema, RepositoryRef: "repo-fixture", Revision: revision})
	taskStore.armReadbackAfterTerminalWrite = true
	firstHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(request)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var first researchStructuredOperationResponse
	firstDecodeErr := json.Unmarshal(firstHTTP.Body.Bytes(), &first)
	faultAdmission, admissionFound, admissionErr := tasks.LookupResearchAdmission(context.Background(), "ren", first.OperationKey, first.RequestDigest, researchIndexAdmissionKind)
	var durableTaskStatus, durableTaskSummary, durableRunStatus, durableRunSummary string
	var durableTaskErr, durableRunErr error
	var durableTaskValid, durableRunValid, durableReceiptValid, durableStatusValid, durableSummaryMatches, durableCanonicalMatches bool
	var durableReceiptDecodeErr error
	if admissionFound && admissionErr == nil {
		if task, err := taskStore.JSONLStore.GetTask(context.Background(), faultAdmission.TaskID); err != nil {
			durableTaskErr = err
		} else {
			durableTaskStatus, durableTaskSummary = string(task.Status), task.Summary
			durableTaskValid = validResearchIndexTask(task, "ren", faultAdmission)
		}
		if run, err := taskStore.JSONLStore.GetRun(context.Background(), faultAdmission.InitialRunID); err != nil {
			durableRunErr = err
		} else {
			durableRunStatus, durableRunSummary = string(run.Status), run.Summary
			durableRunValid = validResearchIndexRun(run, "ren", faultAdmission)
			var stored researchStructuredIndexResult
			durableReceiptDecodeErr = decodeResearchStructuredJSON([]byte(run.Summary), &stored)
			durableReceiptValid = durableReceiptDecodeErr == nil && validResearchStructuredResult(stored, "ren", first.RequestDigest, faultAdmission)
			durableStatusValid = researchIndexTaskRunStatusMatches(domaintask.Status(durableTaskStatus), domaintask.RunStatus(durableRunStatus), structuredTaskIndexStatus(stored.Status))
			durableSummaryMatches = durableTaskSummary == durableRunSummary
			canonical, canonicalErr := json.Marshal(stored)
			durableCanonicalMatches = canonicalErr == nil && string(canonical) == durableRunSummary
		}
	}
	t.Logf("one-shot terminal readback fault snapshot: writes=%d readback_failures=%d fail_next=%t admission_found=%t admission_error=%v task_status=%q task_valid=%t task_error=%v task_summary=%q run_status=%q run_valid=%t run_error=%v run_summary=%q receipt_valid=%t receipt_decode_error=%v task_run_status_matches=%t summaries_match=%t canonical_summary=%t",
		taskStore.terminalRunWrites, taskStore.readbackFailures, taskStore.failNextReadback, admissionFound, admissionErr,
		durableTaskStatus, durableTaskValid, durableTaskErr, durableTaskSummary, durableRunStatus, durableRunValid, durableRunErr,
		durableRunSummary, durableReceiptValid, durableReceiptDecodeErr, durableStatusValid, durableSummaryMatches, durableCanonicalMatches)
	if firstDecodeErr != nil || firstHTTP.Code != 200 || first.State != "pending" || first.ErrorCode != "outcome_unknown" || fake.indexCalls != 1 {
		t.Fatalf("first unknown CodeGraph outcome = status %d response %+v err=%v calls=%d", firstHTTP.Code, first, firstDecodeErr, fake.indexCalls)
	}
	if taskStore.terminalRunWrites != 1 || taskStore.readbackFailures != 1 || taskStore.failNextReadback || !admissionFound || admissionErr != nil || durableTaskErr != nil || durableRunErr != nil || !durableTaskValid || !durableRunValid || !durableReceiptValid || !durableStatusValid || !durableSummaryMatches || !durableCanonicalMatches {
		t.Fatalf("single terminal readback loss did not leave an exact durable Task/Run receipt: task_store_writes=%d readback_failures=%d fail_next=%t admission_found=%t admission_error=%v task=%q valid=%t error=%v run=%q valid=%t error=%v receipt_valid=%t receipt_decode_error=%v status_matches=%t summaries_match=%t canonical=%t", taskStore.terminalRunWrites, taskStore.readbackFailures, taskStore.failNextReadback, admissionFound, admissionErr, durableTaskStatus, durableTaskValid, durableTaskErr, durableRunStatus, durableRunValid, durableRunErr, durableReceiptValid, durableReceiptDecodeErr, durableStatusValid, durableSummaryMatches, durableCanonicalMatches)
	}
	secondHTTP := researchIndexPost(t, handler, map[string]any{"operation": "code_graph_build", "request": json.RawMessage(request)}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	var second researchStructuredOperationResponse
	decodeErr := json.Unmarshal(secondHTTP.Body.Bytes(), &second)
	if decodeErr != nil || secondHTTP.Code != 200 || second.State != "completed" || second.Result == nil || second.Result.Status != "indexed" || fake.indexCalls != 1 {
		admission, found, admissionErr := tasks.LookupResearchAdmission(context.Background(), "ren", second.OperationKey, second.RequestDigest, researchIndexAdmissionKind)
		var taskStatus, taskSummary, runStatus, runSummary string
		var taskErr, runErr error
		var taskValid, runValid, resultValid, statusValid, summaryMatches bool
		var canonicalMatches bool
		var resultDecodeErr error
		if found {
			if task, err := tasks.Get(context.Background(), admission.TaskID); err != nil {
				taskErr = err
			} else {
				taskStatus, taskSummary = string(task.Status), task.Summary
				taskValid = validResearchIndexTask(task, "ren", admission)
			}
			if run, err := tasks.GetRun(context.Background(), admission.InitialRunID); err != nil {
				runErr = err
			} else {
				runStatus, runSummary = string(run.Status), run.Summary
				runValid = validResearchIndexRun(run, "ren", admission)
				var stored researchStructuredIndexResult
				resultDecodeErr = decodeResearchStructuredJSON([]byte(run.Summary), &stored)
				resultValid = resultDecodeErr == nil && validResearchStructuredResult(stored, "ren", second.RequestDigest, admission)
				statusValid = researchIndexTaskRunStatusMatches(domaintask.Status(taskStatus), domaintask.RunStatus(runStatus), structuredTaskIndexStatus(stored.Status))
				taskSummaryEqual := taskSummary == runSummary
				summaryMatches = taskSummaryEqual
				canonical, canonicalErr := json.Marshal(stored)
				canonicalMatches = canonicalErr == nil && string(canonical) == run.Summary
				if !taskValid || !runValid || !resultValid || !statusValid || !summaryMatches || !canonicalMatches {
					runSummary = run.Summary + fmt.Sprintf(" [diagnostic task_valid=%t run_valid=%t result_valid=%t status_valid=%t summary_matches=%t canonical_matches=%t result_decode_error=%v]", taskValid, runValid, resultValid, statusValid, summaryMatches, canonicalMatches, resultDecodeErr)
				}
			}
		}
		t.Fatalf("terminal readback replay did not reconcile without rebuilding: status %d response %+v decode=%v calls=%d task_store_writes=%d readback_failures=%d fail_next=%t admission_found=%t admission_error=%v task=%q/%q task_error=%v run=%q/%q run_error=%v task_valid=%t run_valid=%t result_valid=%t status_valid=%t summary_matches=%t canonical_matches=%t result_decode_error=%v body=%s", secondHTTP.Code, second, decodeErr, fake.indexCalls, taskStore.terminalRunWrites, taskStore.readbackFailures, taskStore.failNextReadback, found, admissionErr, taskStatus, taskSummary, taskErr, runStatus, runSummary, runErr, taskValid, runValid, resultValid, statusValid, summaryMatches, canonicalMatches, resultDecodeErr, secondHTTP.Body.String())
	}
}

func runGit(t *testing.T, repositoryPath string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repositoryPath}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
