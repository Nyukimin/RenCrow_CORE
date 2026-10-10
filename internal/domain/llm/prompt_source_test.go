package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptSourceRemainsInternalAndCloneIsIndependent(t *testing.T) {
	source := &PromptSourceRef{
		Owner: "RenCrow_CORE", SourceID: "raw-1", RawHash: strings.Repeat("a", 64),
		ProjectionVersion: "knowledge-recall-quote/v1", Range: ByteRange{Start: 4, End: 11},
		Origin: "unknown", Sequence: 0,
	}
	message := Message{Role: "system", Content: "quoted", PromptSource: source}
	clone := ClonePromptSourceRef(message.PromptSource)
	if clone == nil || clone == source || *clone != *source {
		t.Fatalf("source clone=%+v source=%+v", clone, source)
	}
	clone.Range.Start = 100
	if source.Range.Start != 4 {
		t.Fatalf("source was mutated through clone: %+v", source)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "prompt_source") || strings.Contains(string(encoded), "raw-1") {
		t.Fatalf("CORE source metadata escaped JSON: %s", encoded)
	}
}
