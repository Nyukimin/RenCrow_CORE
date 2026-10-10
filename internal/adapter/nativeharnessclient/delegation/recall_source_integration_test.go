package delegation

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	conversationengine "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	categoryrecall "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/categoryrecall"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNativeRecallSourceFlowsFromOwnerProjectionThroughFrozenStart(t *testing.T) {
	ctx, input, userText := nativeRecallContext(t)
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("create owner Conversation L1 store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close owner Conversation L1 store: %v", err)
		}
	})

	raw := "native provenance 先行 🧭\n検証済み引用🧪の本文\n末尾"
	summaryDraft := "  検証済み引用🧪の本文  "
	staging, err := store.SaveStagingItem(context.Background(), l1sqlite.L1StagingItem{
		Kind: l1sqlite.L1StagingKindExternalFetch, Namespace: "kb:general", EventID: "native-recall-source-1",
		SourceID: "test:knowledge", SourceURL: "https://example.test/knowledge/1",
		FetchedAt: time.Now().UTC(), RawText: raw, SummaryDraft: summaryDraft, LicenseNote: "owner",
		Meta: map[string]interface{}{"title": "Owner verified fact", "scope": "public"},
	})
	if err != nil {
		t.Fatalf("stage Knowledge source: %v", err)
	}
	if _, err := store.ValidateStagingItem(context.Background(), staging.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"test:knowledge": 1}, MinimumTrustScore: 0.5, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("validate Knowledge source: %v", err)
	}
	knowledge, err := store.PromoteValidatedStagingItemToKnowledge(context.Background(), staging.ID, "general")
	if err != nil {
		t.Fatalf("promote Knowledge source: %v", err)
	}
	userScope, err := domaintool.NewToolExecutionScope(
		"native-recall-backfill", domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("create authenticated owner scope: %v", err)
	}
	backfill, err := store.BackfillKnowledgeCommonRaw(
		domaintool.WithToolExecutionScope(context.Background(), userScope),
		"native-recall-backfill", "ren", "ren", true,
	)
	if err != nil {
		t.Fatalf("project Knowledge into owner Common Raw: %v", err)
	}

	source := categoryrecall.NewL1KnowledgeSource(store)
	registry := conversation.NewCategoryRecallRegistry(source).
		SetMarkers(map[string][]string{"general": {"native provenance"}}).
		SetNow(func() time.Time { return time.Now().UTC() })
	engine := conversationengine.NewRealConversationEngine(&recallSourceConversationManager{}, conversation.PersonaState{}).
		WithCategoryRecallRegistry(registry).
		WithCategoryRecallScope("untrusted-configured-owner")
	recallCtx := conversation.WithNativeRecallProvenanceIntent(ctx)
	pack, err := engine.BeginTurn(recallCtx, input.SessionID(), userText)
	if err != nil {
		t.Fatalf("native BeginTurn: %v", err)
	}
	workerPack := pack.FilterForRole("worker").WithoutPersonaSystemPrompt()
	if len(workerPack.CategorySnippets) != 1 {
		t.Fatalf("worker pack snippets=%+v, want the validated owner projection", workerPack.CategorySnippets)
	}
	snippet := workerPack.CategorySnippets[0]
	projectedSummary := strings.TrimSpace(summaryDraft)
	wantStart := strings.Index(raw, projectedSummary)
	wantSource := llm.PromptSourceRef{
		Owner: "RenCrow_CORE", SourceID: "", RawHash: domainmemory.SHA256Hex([]byte(raw)),
		ProjectionVersion: "knowledge-recall-quote/v1",
		Range:             llm.ByteRange{Start: uint64(wantStart), End: uint64(wantStart + len(projectedSummary))},
		Origin:            "unknown", Sequence: 0,
	}
	if snippet.RecordID != knowledge.ID || snippet.Summary != projectedSummary || snippet.PromptSource == nil ||
		snippet.PromptSource.Owner != wantSource.Owner || snippet.PromptSource.RawHash != wantSource.RawHash ||
		snippet.PromptSource.ProjectionVersion != wantSource.ProjectionVersion || snippet.PromptSource.Range != wantSource.Range ||
		snippet.PromptSource.Origin != wantSource.Origin || snippet.PromptSource.Sequence != wantSource.Sequence ||
		len(backfill.RawRecordIDs) != 1 || snippet.PromptSource.SourceID != backfill.RawRecordIDs[0] {
		t.Fatalf("worker snippet did not retain the exact owner quote proof: %+v", snippet)
	}
	wantSource.SourceID = snippet.PromptSource.SourceID

	recallMessages := workerPack.ToPromptMessages()
	if len(recallMessages) != 2 || recallMessages[0].PromptSource != nil || recallMessages[1].Content != projectedSummary ||
		recallMessages[1].PromptSource == nil || *recallMessages[1].PromptSource != wantSource {
		t.Fatalf("worker RecallPack messages must isolate envelope from exact quote: %#v", recallMessages)
	}
	messages := []llm.Message{
		{Role: "system", Content: "character", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "stable", Type: llm.PromptContextStable},
	}
	messages = append(messages, recallMessages...)
	messages = append(messages,
		llm.Message{Role: "system", Content: "variable", Type: llm.PromptContextVariable},
		llm.Message{Role: "user", Content: userText, Type: llm.PromptContextUser},
	)

	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, in protocol.StartInput) (protocol.StartResult, error) {
			if n == 1 {
				for i := range messages {
					if messages[i].Content == projectedSummary && messages[i].PromptSource != nil {
						messages[i].PromptSource.Range.Start = 0
						messages[i].PromptSource.RawHash = strings.Repeat("f", 64)
					}
				}
				return protocol.StartResult{}, client.ErrOutcomeUnknown
			}
			return c.startResult(in), nil
		}
		return c, nil
	}
	if _, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages}); err != nil {
		t.Fatalf("native Start retry: %v", err)
	}
	starts := d.client(0).starts
	if len(starts) != 2 || !reflect.DeepEqual(starts[0], starts[1]) {
		t.Fatalf("Start retry changed the frozen payload: %+v", starts)
	}
	var foundEnvelope, foundQuote bool
	for _, block := range starts[0].ContextBlocks {
		switch block.Text {
		case recallMessages[0].Content:
			foundEnvelope = block.Source == nil
		case projectedSummary:
			foundQuote = block.Source != nil && block.Source.Owner == wantSource.Owner &&
				block.Source.SourceID == wantSource.SourceID && block.Source.RawHash == wantSource.RawHash &&
				block.Source.ProjectionVersion == wantSource.ProjectionVersion &&
				block.Source.Range.Start == wantSource.Range.Start && block.Source.Range.End == wantSource.Range.End &&
				block.Source.Origin == wantSource.Origin && block.Source.Sequence == wantSource.Sequence
		}
	}
	if !foundEnvelope || !foundQuote {
		t.Fatalf("Start blocks lost the exact source/null split: %+v", starts[0].ContextBlocks)
	}
}

func nativeRecallContext(t *testing.T) (context.Context, conversation.TurnInput, string) {
	t.Helper()
	userText := "native provenance"
	address, err := conversation.NewChannelAddress("line", "ren")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(core.NewTaskID(), userText, address)
	if err != nil {
		t.Fatal(err)
	}
	parentScope, err := domaintool.NewToolExecutionScope(
		"native-worker-parent", domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		domaintool.WithToolExecutionScope(context.Background(), parentScope),
		"native-worker-recall", "shiro", "worker", "ops", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithIdentity(ctx, input.RootTaskID(), core.NewRunID(), input.TraceID())
	if err != nil {
		t.Fatal(err)
	}
	return ctx, input, userText
}

type recallSourceConversationManager struct{}

func (*recallSourceConversationManager) Recall(context.Context, string, string, int) ([]conversation.Message, error) {
	return nil, nil
}
func (*recallSourceConversationManager) Store(context.Context, string, conversation.Message) error {
	return nil
}
func (*recallSourceConversationManager) FlushThread(context.Context, core.ThreadID) (*conversation.ThreadSummary, error) {
	return nil, nil
}
func (*recallSourceConversationManager) IsNovelInformation(context.Context, conversation.Message) (bool, float32, error) {
	return false, 0, nil
}
func (*recallSourceConversationManager) GetActiveThread(context.Context, string) (*conversation.Thread, error) {
	return nil, nil
}
func (*recallSourceConversationManager) CreateThread(context.Context, string, string) (*conversation.Thread, error) {
	return nil, nil
}
func (*recallSourceConversationManager) GetAgentStatus(context.Context, string) (*conversation.AgentStatus, error) {
	return nil, nil
}
func (*recallSourceConversationManager) UpdateAgentStatus(context.Context, *conversation.AgentStatus) error {
	return nil
}
