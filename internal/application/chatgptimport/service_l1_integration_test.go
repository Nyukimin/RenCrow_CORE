package chatgptimport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestServiceImportsVerifiedBundleThroughRealL1StoreAndReplaysAfterReopen(t *testing.T) {
	fixture := newBundleFixture(t)
	databasePath := filepath.Join(t.TempDir(), "conversation-l1.db")
	rawSourceRoot := filepath.Join(t.TempDir(), "raw-sources")
	store, err := l1sqlite.NewL1SQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	if err := store.SetCommonRawSourceRoot(rawSourceRoot); err != nil {
		_ = store.Close()
		t.Fatalf("SetCommonRawSourceRoot: %v", err)
	}

	request := serviceFixtureRequest(fixture)
	request.Apply = true
	ctx := chatGPTServiceL1Context(t, request.RequestID, request.OwnerID)
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(ctx, request)
	if err != nil {
		_ = store.Close()
		t.Fatalf("Import with real L1 store: %v", err)
	}
	if result.View.State != domainmemory.ChatGPTImportStateCompleted || !result.View.Apply || result.IdempotentReplay {
		_ = store.Close()
		t.Fatalf("unexpected completed result: %+v", result)
	}
	if result.View.Counts.RawCount != fixture.manifest.SourceChunkCount+fixture.manifest.Messages || result.View.Counts.ProjectionCount != fixture.manifest.Messages || result.View.Counts.JobCount != fixture.manifest.UserMessages {
		_ = store.Close()
		t.Fatalf("unexpected real-store counts: %+v", result.View.Counts)
	}

	events, err := store.RecentByState(context.Background(), l1sqlite.MemoryStateObserved, 10)
	if err != nil {
		_ = store.Close()
		t.Fatalf("RecentByState: %v", err)
	}
	if len(events) != fixture.manifest.Messages {
		_ = store.Close()
		t.Fatalf("observed ChatGPT L3 rows = %d, want %d", len(events), fixture.manifest.Messages)
	}
	jobs, err := store.ListProfilePromotionJobs(context.Background(), 10)
	if err != nil {
		_ = store.Close()
		t.Fatalf("ListProfilePromotionJobs: %v", err)
	}
	if len(jobs) != fixture.manifest.UserMessages || jobs[0].State != domainmemory.ProfilePromotionPending {
		_ = store.Close()
		t.Fatalf("unexpected profile promotion jobs: %+v", jobs)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close imported store: %v", err)
	}

	reopened, err := l1sqlite.NewL1SQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("reopen L1 store: %v", err)
	}
	defer reopened.Close()
	if err := reopened.SetCommonRawSourceRoot(rawSourceRoot); err != nil {
		t.Fatalf("reconcile Common Raw after reopen: %v", err)
	}
	replayRequest := request
	replayRequest.RequestID = "request-replay"
	replay, err := NewService(reopened, serviceTestOptions()).Import(chatGPTServiceL1Context(t, replayRequest.RequestID, replayRequest.OwnerID), replayRequest)
	if err != nil {
		t.Fatalf("replay after reopen: %v", err)
	}
	if !replay.IdempotentReplay || replay.View.State != domainmemory.ChatGPTImportStateCompleted || !replay.View.Apply {
		t.Fatalf("unexpected durable replay: %+v", replay)
	}
	events, err = reopened.RecentByState(context.Background(), l1sqlite.MemoryStateObserved, 10)
	if err != nil || len(events) != fixture.manifest.Messages {
		t.Fatalf("replayed L3 rows = %d err=%v, want unchanged %d", len(events), err, fixture.manifest.Messages)
	}
	jobs, err = reopened.ListProfilePromotionJobs(context.Background(), 10)
	if err != nil || len(jobs) != fixture.manifest.UserMessages {
		t.Fatalf("replayed jobs = %d err=%v, want unchanged %d", len(jobs), err, fixture.manifest.UserMessages)
	}

	promotion := memorypromotion.NewService(reopened, chatGPTServiceL1Extractor{}, memorypromotion.Options{
		UserID: "ren", BatchMessages: 24, MaxAttempts: 1,
		Now: func() time.Time { return time.Date(2026, 8, 16, 18, 0, 0, 0, time.UTC) },
	})
	promoted, err := promotion.RunOne(context.Background())
	if err != nil {
		t.Fatalf("promote imported ChatGPT evidence: %v", err)
	}
	if !promoted.Processed || promoted.CandidateCount != 1 {
		t.Fatalf("unexpected promotion result: %+v", promoted)
	}
	confirmRequestID := "confirm-imported"
	confirmed, err := reopened.ConfirmChatGPTImportCandidates(
		chatGPTServiceL1Context(t, confirmRequestID, "ren"),
		domainmemory.ChatGPTImportConfirmInput{
			RequestID: confirmRequestID, OwnerID: "ren", ActorID: "ren",
			ExportID: fixture.manifest.ExportID, Reason: "real L1 integration", Apply: true,
		},
	)
	if err != nil {
		t.Fatalf("confirm imported candidate: %v", err)
	}
	if confirmed.Matched != 1 || confirmed.Confirmed != 1 || confirmed.AuditReference == "" {
		t.Fatalf("unexpected confirmation result: %+v", confirmed)
	}
	recallRequestID := "recall-imported"
	recalled, err := reopened.OwnerRecallUserMemories(chatGPTServiceL1Context(t, recallRequestID, "ren"), recallRequestID, "ren", "violet", 10)
	if err != nil {
		t.Fatalf("recall confirmed imported candidate: %v", err)
	}
	if len(recalled.Items) != 1 || recalled.Items[0].Statement != "Ren remembers the violet integration marker" || recalled.Trace.SelectedCount != 1 {
		t.Fatalf("unexpected imported Recall result: %+v", recalled)
	}
}

type chatGPTServiceL1Extractor struct{}

func (chatGPTServiceL1Extractor) Extract(context.Context, *domconv.Thread, domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	return &domconv.ProfileExtractionResult{NewFacts: []string{"Ren remembers the violet integration marker"}}, nil
}

func chatGPTServiceL1Context(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		ownerID,
		ownerID,
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
