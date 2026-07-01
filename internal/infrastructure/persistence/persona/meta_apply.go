package persona

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
)

func (s *JSONLStore) ApplyMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) (string, error) {
	return applyMetaProfileUpdate(s.metaRoot, item)
}

func (s *SQLiteStore) ApplyMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) (string, error) {
	return applyMetaProfileUpdate(s.metaRoot, item)
}

func applyMetaProfileUpdate(metaRoot string, item domainpersona.MetaProfileUpdate) (string, error) {
	if strings.TrimSpace(metaRoot) == "" {
		return "", fmt.Errorf("persona meta root is not configured")
	}
	if item.ReviewStatus != "approved" {
		return "", fmt.Errorf("meta profile update must be approved before apply")
	}
	if strings.TrimSpace(item.ProposedContent) == "" {
		return "", fmt.Errorf("proposed_content is required for meta profile apply")
	}
	target, err := metaProfilePath(metaRoot, item.ObserverID, item.TargetID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	block := buildMetaProfileBlock(item)
	if _, err := f.WriteString(block); err != nil {
		return "", err
	}
	return target, nil
}

func metaProfilePath(root, observerID, targetID string) (string, error) {
	cleanObserver, err := safePersonaPathSegment(observerID)
	if err != nil {
		return "", fmt.Errorf("invalid observer_id: %w", err)
	}
	cleanTarget, err := safePersonaPathSegment(targetID)
	if err != nil {
		return "", fmt.Errorf("invalid target_id: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absRoot, "observers", cleanObserver, "meta", cleanTarget+".md")
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("meta profile path escapes meta root")
	}
	return target, nil
}

func safePersonaPathSegment(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty segment")
	}
	if value == "." || value == ".." || strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("path separator is not allowed")
	}
	return value, nil
}

func buildMetaProfileBlock(item domainpersona.MetaProfileUpdate) string {
	var b strings.Builder
	b.WriteString("\n\n## ")
	b.WriteString(strings.TrimSpace(item.Section))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(item.ProposedContent))
	b.WriteString("\n\n")
	b.WriteString("Review: ")
	b.WriteString(item.ReviewStatus)
	if !item.ReviewedAt.IsZero() {
		b.WriteString(" at ")
		b.WriteString(item.ReviewedAt.Format(timeFormatRFC3339Nano))
	}
	if len(item.EvidenceRefs) > 0 {
		b.WriteString("\n\nEvidence:\n")
		for _, ref := range item.EvidenceRefs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(ref)
			b.WriteString("\n")
		}
	}
	return b.String()
}
