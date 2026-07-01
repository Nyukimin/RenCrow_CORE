package complexity

import (
	"context"
	"errors"
	"fmt"
	"testing"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

type fakeContextTraceLister struct {
	limit  int
	traces []domaindci.SearchTrace
	err    error
}

func (f *fakeContextTraceLister) ListRecent(ctx context.Context, limit int) ([]domaindci.SearchTrace, error) {
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.traces, nil
}

type fakeTraceLister struct {
	limit  int
	traces []domaindci.SearchTrace
}

func (f *fakeTraceLister) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	f.limit = limit
	return f.traces, nil
}

func TestDeriveCandidatePatternsFromDCITraces(t *testing.T) {
	source := &fakeContextTraceLister{
		traces: []domaindci.SearchTrace{
			{
				UserQuery: "refactor complexity handler",
				Steps: []domaindci.SearchStep{
					{Status: "ok", CommandText: "rg BuildReportMarkdown", FilePath: "internal/application/complexity/report.go"},
					{Status: "failed", CommandText: "ignored_candidate", FilePath: "internal/application/complexity/ignored.go"},
					{Status: "completed", CommandText: "scan analyzer", FilePath: "internal/application/complexity/analyzer.go"},
				},
			},
		},
	}

	got, err := DeriveCandidatePatterns(context.Background(), source, 0)
	if err != nil {
		t.Fatalf("DeriveCandidatePatterns failed: %v", err)
	}
	if source.limit != 5 {
		t.Fatalf("default limit should be 5, got %d", source.limit)
	}
	assertContainsPattern(t, got, "refactor")
	assertContainsPattern(t, got, "BuildReportMarkdown")
	assertContainsPattern(t, got, "report")
	assertContainsPattern(t, got, "analyzer")
	assertMissingPattern(t, got, "ignored_candidate")
	assertMissingPattern(t, got, "complexity")
}

func TestDeriveCandidatePatternsSupportsLegacyTraceListerAndLimitCap(t *testing.T) {
	source := &fakeTraceLister{traces: []domaindci.SearchTrace{{UserQuery: "movie catalog"}}}

	got, err := DeriveCandidatePatterns(context.Background(), source, 99)
	if err != nil {
		t.Fatalf("DeriveCandidatePatterns failed: %v", err)
	}
	if source.limit != 20 {
		t.Fatalf("limit should be capped at 20, got %d", source.limit)
	}
	assertContainsPattern(t, got, "movie")
	assertContainsPattern(t, got, "catalog")
}

func TestDeriveCandidatePatternsHandlesUnsupportedSourceAndErrors(t *testing.T) {
	got, err := DeriveCandidatePatterns(context.Background(), struct{}{}, 3)
	if err != nil {
		t.Fatalf("unsupported source should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("unsupported source should return nil, got %#v", got)
	}

	sourceErr := errors.New("trace store failed")
	_, err = DeriveCandidatePatterns(context.Background(), &fakeContextTraceLister{err: sourceErr}, 3)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("expected source error, got %v", err)
	}
}

func TestMergeCandidatePatternsFiltersDuplicatesAndCapsOutput(t *testing.T) {
	derived := []string{"secondary", "rg", "ab", "primary", "newvalue"}
	for i := 0; i < 80; i++ {
		derived = append(derived, fmt.Sprintf("candidate_%02d", i))
	}

	got := MergeCandidatePatterns([]string{"primary", " docs ", "secondary"}, derived)
	if len(got) != 50 {
		t.Fatalf("expected cap at 50 patterns, got %d", len(got))
	}
	if got[0] != "primary" || got[1] != "secondary" {
		t.Fatalf("primary patterns should keep order and de-duplicate, got %#v", got[:3])
	}
	assertMissingPattern(t, got, "docs")
	assertMissingPattern(t, got, "rg")
	assertMissingPattern(t, got, "ab")
}

func assertContainsPattern(t *testing.T, patterns []string, want string) {
	t.Helper()
	for _, pattern := range patterns {
		if pattern == want {
			return
		}
	}
	t.Fatalf("missing pattern %q in %#v", want, patterns)
}

func assertMissingPattern(t *testing.T, patterns []string, want string) {
	t.Helper()
	for _, pattern := range patterns {
		if pattern == want {
			t.Fatalf("unexpected pattern %q in %#v", want, patterns)
		}
	}
}
