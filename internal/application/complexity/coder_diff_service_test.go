package complexity

import (
	"context"
	"strings"
	"testing"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type stubCoderDiffGenerator struct {
	response      string
	requests      []task.Task
	systemPrompts []string
}

func (s *stubCoderDiffGenerator) Generate(_ context.Context, t task.Task, systemPrompt string) (string, error) {
	s.requests = append(s.requests, t)
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
	coder := &stubCoderDiffGenerator{response: "```diff\n" + diff + "\n```"}
	result, err := NewCoderDiffService(coder).GenerateConcreteDiff(context.Background(), CoderDiffRequest{
		Hotspot:      hotspot,
		Evidence:     []domaincomplexity.HotspotEvidence{{EvidenceID: "ev_1", HotspotID: "hot_1", LineStart: 10, LineEnd: 12, Snippet: "for _, item := range items {\n\t_ = item\n}", Reason: "loop evidence"}},
		WorkstreamID: "ws_1",
		JobID:        "job_1",
	})
	if err != nil {
		t.Fatalf("GenerateConcreteDiff failed: %v", err)
	}
	if result.JobID != "job_1" || result.ConcreteDiff != diff {
		t.Fatalf("unexpected result=%#v", result)
	}
	if len(coder.requests) != 1 {
		t.Fatalf("coder requests=%d", len(coder.requests))
	}
	if len(coder.systemPrompts) != 1 || !strings.Contains(coder.systemPrompts[0], "Return only a minimal unified diff") {
		t.Fatalf("coder system prompt=%#v", coder.systemPrompts)
	}
	if !strings.Contains(coder.requests[0].UserMessage(), "Do not apply it") {
		t.Fatalf("prompt missing review-only boundary:\n%s", coder.requests[0].UserMessage())
	}
	if !strings.Contains(coder.requests[0].UserMessage(), "Observed Evidence Snippets") ||
		!strings.Contains(coder.requests[0].UserMessage(), "loop evidence") ||
		!strings.Contains(coder.requests[0].UserMessage(), "for _, item := range items") {
		t.Fatalf("prompt missing hotspot evidence:\n%s", coder.requests[0].UserMessage())
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
