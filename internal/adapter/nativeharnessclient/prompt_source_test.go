package nativeharnessclient

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestContextRevisionResolvesAndClonesTypedPromptSource(t *testing.T) {
	source := &llm.PromptSourceRef{
		Owner: "RenCrow_CORE", SourceID: "raw-quote", RawHash: strings.Repeat("a", 64),
		ProjectionVersion: "knowledge-recall-quote/v1", Range: llm.ByteRange{Start: 7, End: 19},
		Origin: "unknown", Sequence: 0,
	}
	messages := []llm.Message{
		{Role: "system", Content: "summary quote", Type: llm.PromptContextRecall, Metadata: map[string]string{"source_id": "untrusted-metadata"}, PromptSource: source},
		{Role: "user", Content: "question", Type: llm.PromptContextUser},
	}
	blocks, user, err := MaterializeContextRevision(messages, PromptSourceFromMessage)
	if err != nil || len(blocks) != 1 || user != "question" || blocks[0].Source == nil || *blocks[0].Source != *source {
		t.Fatalf("typed prompt source materialization blocks=%+v user=%q err=%v", blocks, user, err)
	}
	blocks[0].Source.Range.Start = 88
	if source.Range.Start != 7 || messages[0].PromptSource.Range.Start != 7 {
		t.Fatal("context block source mutation escaped the typed resolver clone")
	}
}

func TestContextRevisionSourceNullIgnoresMetadataAndUserMessageSource(t *testing.T) {
	userSource := &llm.PromptSourceRef{
		Owner: "RenCrow_CORE", SourceID: "not-user", RawHash: strings.Repeat("b", 64),
		ProjectionVersion: "test", Range: llm.ByteRange{Start: 0, End: 1}, Origin: "unknown",
	}
	messages := []llm.Message{
		{Role: "system", Content: "ordinary summary", Type: llm.PromptContextRecall, Metadata: map[string]string{"source_id": "metadata-only"}},
		{Role: "user", Content: "question", Type: llm.PromptContextUser, PromptSource: userSource},
	}
	blocks, user, err := MaterializeContextRevision(messages, PromptSourceFromMessage)
	if err != nil || len(blocks) != 1 || blocks[0].Source != nil || user != "question" {
		t.Fatalf("source-null materialization blocks=%+v user=%q err=%v", blocks, user, err)
	}
}
