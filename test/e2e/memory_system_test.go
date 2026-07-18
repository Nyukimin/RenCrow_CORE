package e2e

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type memoryVectorHarness struct {
	summaries []*domconv.ThreadSummary
}

func (v *memoryVectorHarness) SaveThreadSummary(summary *domconv.ThreadSummary) {
	cp := *summary
	cp.Score = 0.98
	v.summaries = append(v.summaries, &cp)
}

func (v *memoryVectorHarness) SearchSimilar(_ []float32, topK int) []*domconv.ThreadSummary {
	if topK <= 0 || topK > len(v.summaries) {
		topK = len(v.summaries)
	}
	return append([]*domconv.ThreadSummary(nil), v.summaries[:topK]...)
}

func TestE2E_MemorySystemDailyConversationL0ToL3RecallPack(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(tmp, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()

	l2, err := archivesqlite.NewArchiveSQLiteStore(filepath.Join(tmp, "l2_archive.db"))
	if err != nil {
		t.Skipf("SQLite archive unavailable for memory E2E: %v", err)
	}
	defer l2.Close()

	sessionID := "memory-e2e-session"
	threadID := time.Now().UnixNano()
	activeThread := domconv.NewThread(sessionID, "daily")
	activeThread.ID = threadID
	vectorStore := &memoryVectorHarness{}

	for i := 1; i <= 15; i++ {
		msg := domconv.NewMessage(domconv.SpeakerUser, fmt.Sprintf("日常会話メッセージ %02d: RenCrow memory preference", i), map[string]interface{}{
			"turn_index": i,
		})
		activeThread.AddMessage(msg)
		if err := l1.SaveMessage(ctx, sessionID, activeThread.ID, fmt.Sprintf("conv:%d", activeThread.ID), msg, l1sqlite.MemoryStateObserved); err != nil {
			t.Fatalf("SaveMessage turn %d failed: %v", i, err)
		}

		if i == 12 {
			staged, err := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
				Kind:         l1sqlite.L1StagingKindMemoryCandidate,
				Namespace:    "user:memory-e2e",
				EventID:      "daily-memory-preference",
				SourceID:     "conversation",
				RawText:      "User prefers RenCrow to remember daily project context.",
				SummaryDraft: "User prefers RenCrow to remember daily project context.",
				Keywords:     []string{"RenCrow", "memory", "daily"},
				LicenseNote:  "user conversation",
				Meta: map[string]interface{}{
					"type":             "preference",
					"target_namespace": "user:memory-e2e",
					"session_id":       sessionID,
					"thread_id":        activeThread.ID,
				},
			})
			if err != nil {
				t.Fatalf("SaveStagingItem failed: %v", err)
			}
			validation, err := l1.ValidateStagingItem(ctx, staged.ID, l1sqlite.L1StagingValidationPolicy{
				Now:               time.Now().UTC(),
				SourceTrustScores: map[string]float64{"conversation": 1.0},
				MinimumTrustScore: 0.5,
			})
			if err != nil {
				t.Fatalf("ValidateStagingItem failed: %v", err)
			}
			if !validation.Passed || validation.Status != l1sqlite.L1StagingStatusValidated {
				t.Fatalf("staging validation did not pass: %+v", validation)
			}
			promoted, err := l1.PromoteValidatedStagingItemToMemory(ctx, staged.ID, "user:memory-e2e", "e2e")
			if err != nil {
				t.Fatalf("PromoteValidatedStagingItemToMemory failed: %v", err)
			}
			if promoted.MemoryState != l1sqlite.MemoryStateConfirmed {
				t.Fatalf("promoted memory state = %q", promoted.MemoryState)
			}

			summary := &domconv.ThreadSummary{
				ThreadID:  activeThread.ID,
				SessionID: sessionID,
				Domain:    "daily",
				Summary:   "Daily conversation established a RenCrow memory preference.",
				Keywords:  []string{"RenCrow", "memory", "daily"},
				Roles:     []string{"chat", "worker"},
				Embedding: []float32{0.1, 0.2, 0.3},
				StartTime: activeThread.StartTime,
				EndTime:   time.Now().UTC(),
				IsNovel:   true,
			}
			if err := l2.SaveThreadSummary(ctx, summary); err != nil {
				t.Fatalf("SaveThreadSummary failed: %v", err)
			}
			vectorStore.SaveThreadSummary(summary)

			activeThread = domconv.NewThread(sessionID, "daily")
		}
	}

	l1Observed, err := l1.RecentBySession(ctx, sessionID, 20)
	if err != nil {
		t.Fatalf("RecentBySession failed: %v", err)
	}
	if len(l1Observed) < 15 {
		t.Fatalf("expected at least 15 L1 observed messages, got %d", len(l1Observed))
	}

	validated, err := l1.RecentStagingItems(ctx, l1sqlite.L1StagingStatusValidated, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems(validated) failed: %v", err)
	}
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated staging item, got %d", len(validated))
	}

	confirmed, err := l1.RecentByState(ctx, l1sqlite.MemoryStateConfirmed, 10)
	if err != nil {
		t.Fatalf("RecentByState(confirmed) failed: %v", err)
	}
	if len(confirmed) != 1 || !strings.Contains(confirmed[0].Message, "daily project context") {
		t.Fatalf("confirmed memory missing promoted content: %+v", confirmed)
	}

	l2Summaries, err := l2.GetSessionHistory(ctx, sessionID, 3)
	if err != nil {
		t.Fatalf("GetSessionHistory failed: %v", err)
	}
	if len(l2Summaries) != 1 {
		t.Fatalf("expected 1 L2 summary, got %d", len(l2Summaries))
	}

	l3Results := vectorStore.SearchSimilar([]float32{0.1, 0.2, 0.3}, 3)
	if len(l3Results) != 1 || l3Results[0].Score <= 0 {
		t.Fatalf("expected L3 vector result with score, got %+v", l3Results)
	}

	pack := domconv.RecallPack{
		ShortContext: activeThread.Turns,
		MidSummaries: []domconv.ThreadSummary{{
			ThreadID: l2Summaries[0].ThreadID,
			Summary:  l2Summaries[0].Summary,
			Roles:    []string{"chat", "worker"},
		}},
		LongFacts: []string{l3Results[0].Summary},
		KBSnippets: []string{
			confirmed[0].Message,
		},
		Constraints: domconv.DefaultConstraints(),
	}
	if !pack.HasContext() {
		t.Fatal("RecallPack should contain L0/L1/L2/L3 context")
	}
	if len(pack.ShortContext) != 3 {
		t.Fatalf("expected 3 post-flush L0 messages, got %d", len(pack.ShortContext))
	}
	chatPack := pack.FilterForRole("chat")
	if len(chatPack.MidSummaries) != 1 || len(chatPack.KBSnippets) != 0 {
		t.Fatalf("chat role should keep L2 and reject KB snippets by default: %+v", chatPack)
	}
	workerPack := pack.FilterForRole("worker")
	if len(workerPack.MidSummaries) != 1 || len(workerPack.KBSnippets) != 1 {
		t.Fatalf("worker role should keep L2 and L1 KB snippets: %+v", workerPack)
	}
	if len(chatPack.RejectedTraceItems) == 0 {
		t.Fatal("chat role filter should record rejected trace items")
	}
}
