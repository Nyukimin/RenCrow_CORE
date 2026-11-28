package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type structuredMaterialOwnerStub struct {
	byTarget map[string]engine.LocalDocumentResult
	allowed  bool
	reads    []engine.LocalDocumentRequest
	verifies int
}

func (owner *structuredMaterialOwnerStub) ReadLocalMaterial(_ context.Context, _ c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	owner.reads = append(owner.reads, request)
	return owner.byTarget[request.Target], nil
}

func (owner *structuredMaterialOwnerStub) VerifyLocalMaterial(_ context.Context, _ c.Envelope, document c.Document) error {
	owner.verifies++
	if !owner.allowed {
		return errors.New("current local source rights were revoked")
	}
	return c.ValidateLocalMaterial(document)
}

type countingLocalStagingStore struct {
	L1StagingStore
	writes              int
	failAfterDurability bool
}

func (store *countingLocalStagingStore) SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	store.writes++
	saved, err := store.L1StagingStore.SaveStagingItem(ctx, item)
	if err != nil {
		return saved, err
	}
	if store.failAfterDurability {
		store.failAfterDurability = false
		return nil, errors.New("injected lost save response after durable write")
	}
	return saved, nil
}

func localMaterialTestHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func localPDFTestDocument(ref, revision string) c.Document {
	text := "Ledger Records: exact retained section text."
	original := []byte("%PDF-1.7 original bytes for " + ref + "@" + revision)
	originalHash := localMaterialTestHash(original)
	return c.Document{
		ID: "pdf-" + ref, SourceKind: "local_pdf", ProviderID: "core.pageindex.v5",
		SourceRef: ref, Revision: revision, Title: "Ledger Records", ContentKind: "text",
		ContentScope: "pdf_pages:2-2", ContentState: "fetched", OriginalHash: originalHash,
		ExtractedHash: localMaterialTestHash([]byte(text)), Text: text, RetentionAllowed: true, Complete: true,
		IndependentSource: "pdf:" + ref, RetrievedAt: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC), TimeBasis: "fetched_at",
		LocalOrigin: &c.LocalMaterialOrigin{Kind: "pdf", PDF: &c.PDFLocalOrigin{
			DocumentRef: ref, Revision: revision, Scope: "pdf_pages:2-2", RightsBasis: "owner-authorized-retention",
			OriginalHash: originalHash, PageCount: 3,
			Sections: []c.PDFSectionRange{{NodeID: "section-ledger", Title: "Ledger Records", StartPage: 2, EndPage: 2}},
		}},
	}
}

func localCodeTestDocument(functions []c.CodeFunctionRange) c.Document {
	text := "package fixture\nfunc HandleLedger() {}\nfunc StoreLedger() {}\n"
	hash := localMaterialTestHash([]byte(text))
	return c.Document{
		ID: "code-repo-fixture-worker", SourceKind: "local_code", ProviderID: "core.codegraph.v1",
		SourceRef: "repo-fixture", Revision: strings.Repeat("a", 40), Title: "worker.go", ContentKind: "source_code",
		ContentScope: "repository_file", ContentState: "fetched", OriginalHash: hash, ExtractedHash: hash,
		Text: text, RetentionAllowed: true, Complete: true, IndependentSource: "repo:repo-fixture",
		RetrievedAt: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC), TimeBasis: "fetched_at",
		LocalOrigin: &c.LocalMaterialOrigin{Kind: "code", Code: &c.CodeLocalOrigin{
			RepoRef: "repo-fixture", Revision: strings.Repeat("a", 40), Scope: "repository_file",
			RightsBasis: "owner_registered_allowlisted_commit", Path: "worker.go", FileHash: hash,
			Functions: functions,
		}},
	}
}

func localMaterialCommitRequest(document c.Document, runID string) engine.CommitRequest {
	envelope := c.Envelope{TaskID: "task-local-material", RunID: runID, TraceID: "trace-local-material", RequestID: "request-local-material", Actor: "owner", OwnerID: "ren", DataScope: "user", PolicyRevision: "policy-1"}
	target := c.SaveTarget{Target: KnowledgeStagingTarget, Required: true}
	key, _ := c.CommitIdempotencyKey(runID, target, document.ExtractedHash, &document)
	return engine.CommitRequest{Envelope: envelope, ResearchRunID: runID, Target: target, Digest: document.ExtractedHash, IdempotencyKey: key, Document: &document}
}

func newLocalMaterialTestStore(t *testing.T) *l1sqlite.L1SQLiteStore {
	t.Helper()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestLocalMaterialCommitSeparatesOriginsAndReusesStableCodeFile(t *testing.T) {
	base := newLocalMaterialTestStore(t)
	store := &countingLocalStagingStore{L1StagingStore: base}
	owner := &structuredMaterialOwnerStub{allowed: true}
	adapter := NewAdapters(nil, nil, store).WithStructuredMaterialOwner(owner)

	pdfOne := localPDFTestDocument("spec-ledger", "revision-v1")
	pdfTwo := localPDFTestDocument("spec-ledger", "revision-v2")
	if pdfOne.Text != pdfTwo.Text {
		t.Fatal("fixture requires identical extracted text across PDF revisions")
	}
	firstPDF, err := adapter.Commit(context.Background(), localMaterialCommitRequest(pdfOne, "run-local-material"))
	if err != nil || firstPDF.LocalIdentity == "" || firstPDF.Digest != pdfOne.ExtractedHash || firstPDF.AuditRef == "" || firstPDF.CanonicalID == "" || firstPDF.VerifiedAt.IsZero() {
		t.Fatalf("first PDF staging commit = %+v err=%v", firstPDF, err)
	}
	secondPDF, err := adapter.Commit(context.Background(), localMaterialCommitRequest(pdfTwo, "run-local-material"))
	if err != nil || secondPDF.LocalIdentity == "" || secondPDF.LocalIdentity == firstPDF.LocalIdentity || secondPDF.AuditRef == firstPDF.AuditRef || secondPDF.CanonicalID == firstPDF.CanonicalID {
		t.Fatalf("same-text different-revision PDF was not kept distinct: first=%+v second=%+v err=%v", firstPDF, secondPDF, err)
	}

	handle := c.CodeFunctionRange{Name: "HandleLedger", QualifiedName: "fixture.HandleLedger", StartLine: 2, EndLine: 2}
	storeFunction := c.CodeFunctionRange{Name: "StoreLedger", QualifiedName: "fixture.StoreLedger", StartLine: 3, EndLine: 3}
	codeOne := localCodeTestDocument([]c.CodeFunctionRange{handle})
	codeTwo := localCodeTestDocument([]c.CodeFunctionRange{storeFunction})
	codeReceiptOne, err := adapter.Commit(context.Background(), localMaterialCommitRequest(codeOne, "run-local-material"))
	if err != nil || codeReceiptOne.LocalIdentity == "" {
		t.Fatalf("first code staging commit = %+v err=%v", codeReceiptOne, err)
	}
	codeReceiptTwo, err := adapter.Commit(context.Background(), localMaterialCommitRequest(codeTwo, "run-local-material"))
	if err != nil || codeReceiptTwo.LocalIdentity != codeReceiptOne.LocalIdentity || codeReceiptTwo.AuditRef != codeReceiptOne.AuditRef || codeReceiptTwo.CanonicalID != codeReceiptOne.CanonicalID {
		t.Fatalf("same exact code file with different query annotations did not reuse its stable record: first=%+v second=%+v err=%v", codeReceiptOne, codeReceiptTwo, err)
	}
	if store.writes != 3 {
		t.Fatalf("expected one durable row per source identity, got writes=%d", store.writes)
	}
	for _, receipt := range []c.Receipt{firstPDF, secondPDF, codeReceiptOne} {
		verified, verifyErr := adapter.Verify(context.Background(), localMaterialCommitRequestEnvelope(), receipt)
		if verifyErr != nil || verified.LocalIdentity != receipt.LocalIdentity || verified.AuditRef != receipt.AuditRef || verified.CanonicalID != receipt.CanonicalID {
			t.Fatalf("exact local receipt verification = %+v err=%v", verified, verifyErr)
		}
	}
	owner.allowed = false
	if _, err := adapter.Verify(context.Background(), localMaterialCommitRequestEnvelope(), codeReceiptOne); err == nil {
		t.Fatal("local receipt remained verifiable after source rights were revoked")
	}
}

func TestLocalMaterialReconcileRecoversDurableWriteWithLostResponse(t *testing.T) {
	base := newLocalMaterialTestStore(t)
	store := &countingLocalStagingStore{L1StagingStore: base, failAfterDurability: true}
	owner := &structuredMaterialOwnerStub{allowed: true}
	adapter := NewAdapters(nil, nil, store).WithStructuredMaterialOwner(owner)
	document := localPDFTestDocument("spec-ledger", "revision-v1")
	request := localMaterialCommitRequest(document, "run-local-reconcile")
	if _, err := adapter.Commit(context.Background(), request); err == nil {
		t.Fatal("injected post-write response loss was not reported")
	}
	reconciled, err := adapter.Reconcile(context.Background(), request)
	if err != nil || reconciled.RetrySafe || reconciled.Receipt.LocalIdentity == "" || reconciled.Receipt.AuditRef == "" || reconciled.Receipt.CanonicalID == "" || reconciled.Receipt.VerifiedAt.IsZero() {
		t.Fatalf("durable local write was not reconciled from exact readback: result=%+v err=%v", reconciled, err)
	}
	if store.writes != 1 {
		t.Fatalf("reconcile repeated the durable save: writes=%d", store.writes)
	}
}

func localMaterialCommitRequestEnvelope() c.Envelope {
	return c.Envelope{TaskID: "task-local-material", RunID: "run-local-material", TraceID: "trace-local-material", RequestID: "request-local-material", Actor: "owner", OwnerID: "ren", DataScope: "user", PolicyRevision: "policy-1"}
}
