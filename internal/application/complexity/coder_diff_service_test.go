package complexity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubCoderDiffGenerator struct {
	response      string
	requests      []conversation.TurnInput
	systemPrompts []string
}

func (s *stubCoderDiffGenerator) Generate(_ context.Context, input conversation.TurnInput, systemPrompt string) (string, error) {
	s.requests = append(s.requests, input)
	s.systemPrompts = append(s.systemPrompts, systemPrompt)
	return s.response, nil
}

func TestCoderDiffServiceGenerateConcreteDiffExtractsAndValidatesReviewOnlyDiff(t *testing.T) {
	hotspot := domaincomplexity.Hotspot{
		HotspotID:   "hot_1",
		ScanID:      "scan_1",
		FilePath:    "internal/application/example.go",
		HotspotType: "repeated_lookup",
		RiskLevel:   "medium",
		Summary:     "repeated lookup",
	}
	diff := `diff --git a/internal/application/example.go b/internal/application/example.go
--- a/internal/application/example.go
+++ b/internal/application/example.go
@@ -1 +1 @@
-old
+new`
	taskID := modulecore.NewTaskID()
	coder := &stubCoderDiffGenerator{response: "```diff\n" + diff + "\n```"}
	result, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{
		Hotspot:      hotspot,
		Evidence:     []domaincomplexity.HotspotEvidence{{EvidenceID: modulecore.NewEvidenceID(), HotspotID: "hot_1", LineStart: 10, LineEnd: 12, Snippet: "for _, item := range items {\n\t_ = item\n}", Reason: "loop evidence"}},
		WorkstreamID: "ws_1",
		TaskID:       taskID,
	})
	if err != nil {
		t.Fatalf("GenerateConcreteDiff failed: %v", err)
	}
	if result.TaskID != taskID || result.ConcreteDiff != diff {
		t.Fatalf("unexpected result=%#v", result)
	}
	if len(coder.requests) != 1 {
		t.Fatalf("coder requests=%d", len(coder.requests))
	}
	if len(coder.systemPrompts) != 1 || !strings.Contains(coder.systemPrompts[0], "Return only a minimal unified diff") {
		t.Fatalf("coder system prompt=%#v", coder.systemPrompts)
	}
	input := coder.requests[0]
	if err := input.Validate(); err != nil {
		t.Fatalf("coder input invalid: %v", err)
	}
	if input.ChannelAddress().ChannelType() != "viewer" || input.ChannelAddress().ExternalConversationID() != "ws_1" {
		t.Fatalf("unexpected coder input address: %#v", input.ChannelAddress())
	}
	if input.Route() != routing.RouteCODE || input.ForcedRoute() != "" {
		t.Fatalf("unexpected coder input route: route=%q forced=%q", input.Route(), input.ForcedRoute())
	}
	identities := []string{
		string(input.RootTaskID()), string(input.TurnID()), string(input.TraceID()),
		string(input.UserMessageID()), string(input.AgentMessageID()),
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if _, exists := seen[identity]; exists {
			t.Fatalf("coder canonical identities must be distinct: %v", identities)
		}
		seen[identity] = struct{}{}
	}
	if input.RootTaskID() != result.TaskID {
		t.Fatal("result must identify the coder input task")
	}

	if !strings.Contains(input.MessageText(), "Do not apply it") {
		t.Fatalf("prompt missing review-only boundary:\n%s", input.MessageText())
	}
	if !strings.Contains(input.MessageText(), "Observed Evidence Snippets") ||
		!strings.Contains(input.MessageText(), "loop evidence") ||
		!strings.Contains(input.MessageText(), "for _, item := range items") {
		t.Fatalf("prompt missing hotspot evidence:\n%s", input.MessageText())
	}
}

func TestCoderDiffServiceUsesHotspotAsExternalConversationFallback(t *testing.T) {
	hotspot := domaincomplexity.Hotspot{HotspotID: "hot_1", FilePath: "internal/application/example.go"}
	coder := &stubCoderDiffGenerator{response: "diff --git a/internal/application/example.go b/internal/application/example.go\n--- a/internal/application/example.go\n+++ b/internal/application/example.go\n@@ -1 +1 @@\n-old\n+new"}
	if _, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{
		Hotspot:      hotspot,
		WorkstreamID: "   ",
	}); err != nil {
		t.Fatalf("GenerateConcreteDiff failed: %v", err)
	}
	if got := coder.requests[0].ChannelAddress().ExternalConversationID(); got != "hot_1" {
		t.Fatalf("external conversation fallback = %q, want hotspot ID", got)
	}
}

func TestCoderDiffServiceRejectsCoderDiffOutsideHotspotFile(t *testing.T) {
	hotspot := domaincomplexity.Hotspot{
		HotspotID:   "hot_1",
		ScanID:      "scan_1",
		FilePath:    "internal/application/example.go",
		HotspotType: "repeated_lookup",
		RiskLevel:   "medium",
		Summary:     "repeated lookup",
	}
	diff := `diff --git a/internal/application/other.go b/internal/application/other.go
--- a/internal/application/other.go
+++ b/internal/application/other.go
@@ -1 +1 @@
-old
+new`
	coder := &stubCoderDiffGenerator{response: "```diff\n" + diff + "\n```"}
	if _, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{
		Hotspot: hotspot,
	}); err == nil {
		t.Fatal("expected coder diff outside hotspot file to be rejected")
	}
}

func TestExtractUnifiedDiffRejectsNonDiffOutput(t *testing.T) {
	if _, err := ExtractUnifiedDiff("I cannot safely change this."); err == nil {
		t.Fatal("expected non-diff output to be rejected")
	}
}

func TestCoderDiffResultUsesCanonicalTaskIdentity(t *testing.T) {
	coder := &stubCoderDiffGenerator{response: "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+new"}
	result, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{Hotspot: domaincomplexity.Hotspot{HotspotID: "hot_1", FilePath: "example.go"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["job_id"]; exists {
		t.Fatal("legacy job_id exposed")
	}
	if fields["task_id"] != coder.requests[0].RootTaskID().String() {
		t.Fatalf("result and coder task differ: %s", data)
	}
}

func TestCoderDiffRejectsInvalidTaskBeforeGeneration(t *testing.T) {
	for _, id := range []modulecore.TaskID{"job_1", modulecore.TaskID(modulecore.NewTraceID()), " "} {
		coder := &stubCoderDiffGenerator{}
		_, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{TaskID: id, Hotspot: domaincomplexity.Hotspot{HotspotID: "hot_1", FilePath: "example.go"}})
		if err == nil || len(coder.requests) != 0 {
			t.Fatalf("invalid task accepted: %q", id)
		}
	}
}
