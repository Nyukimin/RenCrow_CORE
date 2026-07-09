package complexity

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

type DCITraceLister interface {
	ListRecent(limit int) ([]domaindci.SearchTrace, error)
}

type DCITraceContextLister interface {
	ListRecent(ctx context.Context, limit int) ([]domaindci.SearchTrace, error)
}

func DeriveCandidatePatterns(ctx context.Context, dciTraces any, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	var traces []domaindci.SearchTrace
	var err error
	switch store := dciTraces.(type) {
	case DCITraceContextLister:
		traces, err = store.ListRecent(ctx, limit)
	case DCITraceLister:
		traces, err = store.ListRecent(limit)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, trace := range traces {
		candidates = append(candidates, candidateWords(trace.UserQuery)...)
		for _, step := range trace.Steps {
			if step.Status != "" && step.Status != "ok" && step.Status != "completed" {
				continue
			}
			candidates = append(candidates, candidateWords(step.CommandText)...)
			if base := strings.TrimSuffix(filepath.Base(step.FilePath), filepath.Ext(step.FilePath)); base != "." && base != "" {
				candidates = append(candidates, base)
			}
		}
	}
	return MergeCandidatePatterns(nil, candidates), nil
}

func candidateWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.')
	})
}

func MergeCandidatePatterns(primary []string, derived []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(primary)+len(derived))
	for _, pattern := range append(primary, derived...) {
		pattern = strings.TrimSpace(pattern)
		if len(pattern) < 3 {
			continue
		}
		lower := strings.ToLower(pattern)
		switch lower {
		case "rg", "grep", "find", "docs", "src", "internal", "complexity", "hotspot", "search":
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
		if len(out) >= 50 {
			break
		}
	}
	return out
}
