package memorypromotion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type chatGPTImportExtractor struct{}

func (chatGPTImportExtractor) Extract(context.Context, *domconv.Thread, domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	return &domconv.ProfileExtractionResult{NewFacts: []string{"Renは映画ブレードランナーが好き"}}, nil
}

func TestChatGPTImportToPromptInjectableUserMemory(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := l1sqlite.ChatGPTL3ImportRecord{
		Format: l1sqlite.ChatGPTL3ArtifactFormat, ExportID: "export-e2e",
		EvidenceID:     "chatgpt_export:conversation-e2e:message-e2e",
		ConversationID: "conversation-e2e", MessageID: "message-e2e",
		MessageCreatedAt: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
		Role:             "user", Text: "私は映画ブレードランナーが好き", OnCurrentBranch: true,
	}
	imported, err := store.ImportChatGPTL3Records(context.Background(), []l1sqlite.ChatGPTL3ImportRecord{record}, true)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 1 || imported.Queued != 1 {
		t.Fatalf("imported=%+v", imported)
	}
	service := NewService(store, chatGPTImportExtractor{}, Options{UserID: "ren", BatchMessages: 24, MaxAttempts: 1, Now: func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }})
	projected, err := service.RunOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projected.CandidateCount != 1 {
		t.Fatalf("projected=%+v", projected)
	}
	confirmed, err := store.ConfirmChatGPTL3Candidates(context.Background(), "export-e2e", "Ren authorized archive promotion", true)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Confirmed != 1 || confirmed.ProjectionCompleted != 1 {
		t.Fatalf("confirmed=%+v", confirmed)
	}
	recalled, err := store.ListPromptInjectableUserMemories(context.Background(), "ren", "mio", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 1 || recalled[0].Statement != "Renは映画ブレードランナーが好き" {
		t.Fatalf("recalled=%+v", recalled)
	}
}
