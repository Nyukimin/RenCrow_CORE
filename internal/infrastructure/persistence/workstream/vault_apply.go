package workstream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func (s *JSONLStore) ApplyVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (string, error) {
	return applyVaultUpdate(s.vaultRoot, item)
}

func (s *SQLiteStore) ApplyVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (string, error) {
	return applyVaultUpdate(s.vaultRoot, item)
}

func (s *JSONLStore) PreviewVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (*domainworkstream.VaultUpdatePreview, error) {
	return previewVaultUpdate(s.vaultRoot, item)
}

func (s *SQLiteStore) PreviewVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (*domainworkstream.VaultUpdatePreview, error) {
	return previewVaultUpdate(s.vaultRoot, item)
}

func applyVaultUpdate(vaultRoot string, item domainworkstream.VaultUpdateLog) (string, error) {
	if strings.TrimSpace(vaultRoot) == "" {
		return "", fmt.Errorf("vault root is not configured")
	}
	if item.ReviewStatus != domainworkstream.VaultReviewApproved {
		return "", fmt.Errorf("vault update must be approved before apply")
	}
	if strings.TrimSpace(item.ProposedContent) == "" {
		return "", fmt.Errorf("proposed_content is required for vault apply")
	}
	target, err := vaultTargetPath(vaultRoot, item.FilePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	current, err := readVaultFileIfExists(target)
	if err != nil {
		return "", err
	}
	nextContent := renderVaultUpdateContent(current, item)
	if err := os.WriteFile(target, []byte(nextContent), 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func previewVaultUpdate(vaultRoot string, item domainworkstream.VaultUpdateLog) (*domainworkstream.VaultUpdatePreview, error) {
	if strings.TrimSpace(vaultRoot) == "" {
		return nil, fmt.Errorf("vault root is not configured")
	}
	target, err := vaultTargetPath(vaultRoot, item.FilePath)
	if err != nil {
		return nil, err
	}
	currentBytes, err := os.ReadFile(target)
	currentMissing := false
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		currentMissing = true
	}
	current := string(currentBytes)
	proposed := renderVaultUpdateContent(current, item)
	diff, added, removed := compactUnifiedDiff(current, proposed)
	return &domainworkstream.VaultUpdatePreview{
		UpdateID:        item.UpdateID,
		FilePath:        item.FilePath,
		CurrentContent:  current,
		ProposedContent: proposed,
		CurrentMissing:  currentMissing,
		AddedLines:      added,
		RemovedLines:    removed,
		UnifiedDiff:     diff,
	}, nil
}

func readVaultFileIfExists(path string) (string, error) {
	current, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(current), nil
}

func renderVaultUpdateContent(current string, item domainworkstream.VaultUpdateLog) string {
	if !isStructuredVaultAppend(item.UpdateType) {
		return item.ProposedContent
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(current, "\n"))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	title := strings.TrimPrefix(strings.TrimSpace(item.UpdateType), "append_")
	if title == "" || title == "section" {
		title = "update"
	}
	b.WriteString("## ")
	if !item.CreatedAt.IsZero() {
		b.WriteString(item.CreatedAt.Format("2006-01-02"))
		b.WriteString(" ")
	}
	b.WriteString(title)
	if strings.TrimSpace(item.UpdateID) != "" {
		b.WriteString(" ")
		b.WriteString(item.UpdateID)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(item.ProposedContent, "\n"))
	b.WriteString("\n")
	return b.String()
}

func isStructuredVaultAppend(updateType string) bool {
	switch strings.TrimSpace(updateType) {
	case "append_section", "append_status", "append_todo", "append_open_loop", "append_decision", "append_note", "append_artifact":
		return true
	default:
		return false
	}
}

func compactUnifiedDiff(current, proposed string) (string, int, int) {
	currentLines := splitDiffLines(current)
	proposedLines := splitDiffLines(proposed)
	prefix := 0
	for prefix < len(currentLines) && prefix < len(proposedLines) && currentLines[prefix] == proposedLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(currentLines)-prefix && suffix < len(proposedLines)-prefix &&
		currentLines[len(currentLines)-1-suffix] == proposedLines[len(proposedLines)-1-suffix] {
		suffix++
	}
	removedLines := currentLines[prefix : len(currentLines)-suffix]
	addedLines := proposedLines[prefix : len(proposedLines)-suffix]
	var b strings.Builder
	b.WriteString("--- current\n+++ proposed\n")
	if len(removedLines) == 0 && len(addedLines) == 0 {
		return b.String(), 0, 0
	}
	b.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", prefix+1, len(removedLines), prefix+1, len(addedLines)))
	for _, line := range removedLines {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteString("\n")
	}
	for _, line := range addedLines {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), len(addedLines), len(removedLines)
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func vaultTargetPath(vaultRoot, filePath string) (string, error) {
	root, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", err
	}
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	var target string
	if filepath.IsAbs(filePath) {
		target, err = filepath.Abs(filePath)
		if err != nil {
			return "", err
		}
	} else {
		clean := filepath.Clean(filePath)
		if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return "", fmt.Errorf("vault update path escapes vault root")
		}
		target = filepath.Join(root, clean)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("vault update path escapes vault root")
	}
	return target, nil
}
