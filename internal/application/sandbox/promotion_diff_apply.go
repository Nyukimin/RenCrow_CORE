package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

type PromotionDiffApplyResult struct {
	DiffPath     string   `json:"diff_path"`
	ApplyRoot    string   `json:"apply_root"`
	AppliedFiles []string `json:"applied_files"`
	Status       string   `json:"status"`
}

type PromotionDiffPreviewResult struct {
	DiffPath             string                     `json:"diff_path"`
	Files                []PromotionDiffFilePreview `json:"files"`
	FileCount            int                        `json:"file_count"`
	AddedLines           int                        `json:"added_lines"`
	RemovedLines         int                        `json:"removed_lines"`
	RiskFlags            []string                   `json:"risk_flags,omitempty"`
	RequiresManualReview bool                       `json:"requires_manual_review"`
	Status               string                     `json:"status"`
}

type PromotionDiffFilePreview struct {
	Path                 string                     `json:"path"`
	HunkCount            int                        `json:"hunk_count"`
	AddedLines           int                        `json:"added_lines"`
	RemovedLines         int                        `json:"removed_lines"`
	RiskFlags            []string                   `json:"risk_flags,omitempty"`
	RequiresManualReview bool                       `json:"requires_manual_review"`
	Hunks                []PromotionDiffHunkPreview `json:"hunks"`
}

type PromotionDiffHunkPreview struct {
	OldStart int                       `json:"old_start"`
	OldCount int                       `json:"old_count"`
	NewStart int                       `json:"new_start"`
	NewCount int                       `json:"new_count"`
	Rows     []PromotionDiffRowPreview `json:"rows"`
}

type PromotionDiffRowPreview struct {
	Op      string `json:"op"`
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text,omitempty"`
}

type PromotionDiffApplier struct {
	sandboxRoot string
	applyRoot   string
}

func NewPromotionDiffApplier(sandboxRoot string, applyRoot string) *PromotionDiffApplier {
	return &PromotionDiffApplier{sandboxRoot: sandboxRoot, applyRoot: applyRoot}
}

func (a *PromotionDiffApplier) ApplyPromotionDiff(_ context.Context, req domainsandbox.PromotionApplyRequest) (PromotionDiffApplyResult, error) {
	return a.applyPromotionDiff(req, false)
}

func (a *PromotionDiffApplier) RollbackPromotionDiff(_ context.Context, req domainsandbox.PromotionApplyRequest) (PromotionDiffApplyResult, error) {
	return a.applyPromotionDiff(req, true)
}

func (a *PromotionDiffApplier) PreviewPromotionDiff(_ context.Context, req domainsandbox.PromotionRequest) (PromotionDiffPreviewResult, error) {
	if a == nil {
		return PromotionDiffPreviewResult{}, fmt.Errorf("promotion diff previewer unavailable")
	}
	diffPath, err := resolveSandboxOutputPath(a.sandboxRoot, req.DiffPath)
	if err != nil {
		return PromotionDiffPreviewResult{}, fmt.Errorf("resolve promotion diff path: %w", err)
	}
	data, err := os.ReadFile(diffPath)
	if err != nil {
		return PromotionDiffPreviewResult{}, fmt.Errorf("read promotion diff: %w", err)
	}
	raw := string(data)
	rawRiskFlags := inspectPromotionDiffRisk(raw)
	patches, err := parseUnifiedDiff(raw)
	if err != nil {
		if len(rawRiskFlags) > 0 {
			return PromotionDiffPreviewResult{
				DiffPath:             diffPath,
				RiskFlags:            rawRiskFlags,
				RequiresManualReview: true,
				Status:               "needs_manual_review",
			}, nil
		}
		return PromotionDiffPreviewResult{}, err
	}
	if len(patches) == 0 {
		return PromotionDiffPreviewResult{}, fmt.Errorf("promotion diff has no file patches")
	}
	result := PromotionDiffPreviewResult{
		DiffPath:  diffPath,
		Files:     make([]PromotionDiffFilePreview, 0, len(patches)),
		RiskFlags: rawRiskFlags,
		Status:    "previewed",
	}
	for _, patch := range patches {
		file := buildFilePreview(patch)
		file.RiskFlags = classifyPromotionPathRisk(patch.path)
		file.RequiresManualReview = len(file.RiskFlags) > 0
		result.RiskFlags = mergeRiskFlags(result.RiskFlags, file.RiskFlags...)
		if file.RequiresManualReview {
			result.RequiresManualReview = true
		}
		result.AddedLines += file.AddedLines
		result.RemovedLines += file.RemovedLines
		result.Files = append(result.Files, file)
	}
	result.FileCount = len(result.Files)
	if result.RequiresManualReview {
		result.Status = "needs_manual_review"
	}
	return result, nil
}

func (a *PromotionDiffApplier) applyPromotionDiff(req domainsandbox.PromotionApplyRequest, rollback bool) (PromotionDiffApplyResult, error) {
	if a == nil {
		return PromotionDiffApplyResult{}, fmt.Errorf("promotion diff applier unavailable")
	}
	diffPath, err := resolveSandboxOutputPath(a.sandboxRoot, req.Promotion.DiffPath)
	if err != nil {
		return PromotionDiffApplyResult{}, fmt.Errorf("resolve promotion diff path: %w", err)
	}
	applyRoot, err := resolveApplyRoot(a.applyRoot)
	if err != nil {
		return PromotionDiffApplyResult{}, err
	}
	data, err := os.ReadFile(diffPath)
	if err != nil {
		return PromotionDiffApplyResult{}, fmt.Errorf("read promotion diff: %w", err)
	}
	patches, err := parseUnifiedDiff(string(data))
	if err != nil {
		return PromotionDiffApplyResult{}, err
	}
	if len(patches) == 0 {
		return PromotionDiffApplyResult{}, fmt.Errorf("promotion diff has no file patches")
	}
	if rollback {
		for i := range patches {
			patches[i] = reverseFilePatch(patches[i])
		}
	}
	type plannedWrite struct {
		path string
		data []byte
	}
	writes := make([]plannedWrite, 0, len(patches))
	appliedFiles := make([]string, 0, len(patches))
	for _, patch := range patches {
		if flags := classifyPromotionPathRisk(patch.path); len(flags) > 0 {
			return PromotionDiffApplyResult{}, fmt.Errorf("promotion diff for %s requires manual review: %s", patch.path, strings.Join(flags, ","))
		}
		targetPath, err := resolveApplyTargetPath(applyRoot, patch.path)
		if err != nil {
			return PromotionDiffApplyResult{}, err
		}
		current, err := os.ReadFile(targetPath)
		if err != nil {
			return PromotionDiffApplyResult{}, fmt.Errorf("read apply target %s: %w", patch.path, err)
		}
		next, err := applyFilePatch(string(current), patch)
		if err != nil {
			return PromotionDiffApplyResult{}, fmt.Errorf("apply patch to %s: %w", patch.path, err)
		}
		writes = append(writes, plannedWrite{path: targetPath, data: []byte(next)})
		appliedFiles = append(appliedFiles, patch.path)
	}
	for _, write := range writes {
		if err := os.WriteFile(write.path, write.data, 0o644); err != nil {
			return PromotionDiffApplyResult{}, fmt.Errorf("write apply target %s: %w", write.path, err)
		}
	}
	return PromotionDiffApplyResult{
		DiffPath:     diffPath,
		ApplyRoot:    applyRoot,
		AppliedFiles: appliedFiles,
		Status:       promotionDiffApplyStatus(rollback),
	}, nil
}

func promotionDiffApplyStatus(rollback bool) string {
	if rollback {
		return "rolled_back"
	}
	return "applied"
}

type filePatch struct {
	path  string
	hunks []diffHunk
}

type diffHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []diffLine
}

type diffLine struct {
	op   byte
	text string
}

func parseUnifiedDiff(raw string) ([]filePatch, error) {
	var patches []filePatch
	var current *filePatch
	var currentHunk *diffHunk
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			currentHunk = nil
		case strings.HasPrefix(line, "Binary files "):
			return nil, fmt.Errorf("binary diffs are not supported")
		case strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to "):
			return nil, fmt.Errorf("rename diffs are not supported")
		case strings.HasPrefix(line, "--- "):
			if strings.TrimSpace(strings.TrimPrefix(line, "--- ")) == "/dev/null" {
				return nil, fmt.Errorf("new file diffs are not supported")
			}
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if path == "/dev/null" {
				return nil, fmt.Errorf("delete diffs are not supported")
			}
			path = normalizeDiffPath(path)
			if err := validateRelativePatchPath(path); err != nil {
				return nil, err
			}
			patches = append(patches, filePatch{path: path})
			current = &patches[len(patches)-1]
			currentHunk = nil
		case strings.HasPrefix(line, "@@ "):
			if current == nil {
				return nil, fmt.Errorf("hunk appears before file header")
			}
			hunk, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			current.hunks = append(current.hunks, hunk)
			currentHunk = &current.hunks[len(current.hunks)-1]
		case strings.HasPrefix(line, `\ No newline at end of file`):
			continue
		default:
			if currentHunk == nil {
				continue
			}
			if line == "" {
				return nil, fmt.Errorf("empty diff line in hunk")
			}
			op := line[0]
			if op != ' ' && op != '+' && op != '-' {
				return nil, fmt.Errorf("unsupported diff hunk line: %q", line)
			}
			currentHunk.lines = append(currentHunk.lines, diffLine{op: op, text: line[1:]})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read promotion diff: %w", err)
	}
	for _, patch := range patches {
		if len(patch.hunks) == 0 {
			return nil, fmt.Errorf("file patch %s has no hunks", patch.path)
		}
	}
	return patches, nil
}

func parseHunkHeader(header string) (diffHunk, error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return diffHunk{}, fmt.Errorf("invalid hunk header: %s", header)
	}
	oldStart, oldCount, err := parseHunkRange(strings.TrimPrefix(fields[1], "-"))
	if err != nil {
		return diffHunk{}, fmt.Errorf("invalid old hunk range: %w", err)
	}
	newStart, newCount, err := parseHunkRange(strings.TrimPrefix(fields[2], "+"))
	if err != nil {
		return diffHunk{}, fmt.Errorf("invalid new hunk range: %w", err)
	}
	return diffHunk{oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount}, nil
}

func parseHunkRange(raw string) (int, int, error) {
	parts := strings.SplitN(raw, ",", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid start %q", raw)
	}
	count := 1
	if len(parts) == 2 {
		count, err = strconv.Atoi(parts[1])
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("invalid count %q", raw)
		}
	}
	return start, count, nil
}

func applyFilePatch(current string, patch filePatch) (string, error) {
	lines := splitTextLines(current)
	out := make([]string, 0, len(lines))
	cursor := 0
	for _, hunk := range patch.hunks {
		target := hunk.oldStart - 1
		if hunk.oldStart == 0 && hunk.oldCount == 0 {
			target = 0
		}
		if target < cursor || target > len(lines) {
			return "", fmt.Errorf("hunk target outside file")
		}
		out = append(out, lines[cursor:target]...)
		cursor = target
		for _, line := range hunk.lines {
			switch line.op {
			case ' ':
				if cursor >= len(lines) || strings.TrimSuffix(lines[cursor], "\n") != line.text {
					return "", fmt.Errorf("context mismatch near line %d", cursor+1)
				}
				out = append(out, lines[cursor])
				cursor++
			case '-':
				if cursor >= len(lines) || strings.TrimSuffix(lines[cursor], "\n") != line.text {
					return "", fmt.Errorf("delete mismatch near line %d", cursor+1)
				}
				cursor++
			case '+':
				out = append(out, line.text+"\n")
			}
		}
	}
	out = append(out, lines[cursor:]...)
	return strings.Join(out, ""), nil
}

func reverseFilePatch(patch filePatch) filePatch {
	reversed := filePatch{
		path:  patch.path,
		hunks: make([]diffHunk, 0, len(patch.hunks)),
	}
	for _, hunk := range patch.hunks {
		rev := diffHunk{
			oldStart: hunk.newStart,
			oldCount: hunk.newCount,
			newStart: hunk.oldStart,
			newCount: hunk.oldCount,
			lines:    make([]diffLine, 0, len(hunk.lines)),
		}
		for _, line := range hunk.lines {
			switch line.op {
			case '+':
				line.op = '-'
			case '-':
				line.op = '+'
			}
			rev.lines = append(rev.lines, line)
		}
		reversed.hunks = append(reversed.hunks, rev)
	}
	return reversed
}

func buildFilePreview(patch filePatch) PromotionDiffFilePreview {
	file := PromotionDiffFilePreview{
		Path:      patch.path,
		HunkCount: len(patch.hunks),
		Hunks:     make([]PromotionDiffHunkPreview, 0, len(patch.hunks)),
	}
	for _, hunk := range patch.hunks {
		preview := PromotionDiffHunkPreview{
			OldStart: hunk.oldStart,
			OldCount: hunk.oldCount,
			NewStart: hunk.newStart,
			NewCount: hunk.newCount,
			Rows:     make([]PromotionDiffRowPreview, 0, len(hunk.lines)),
		}
		oldLine := hunk.oldStart
		newLine := hunk.newStart
		for _, line := range hunk.lines {
			switch line.op {
			case ' ':
				preview.Rows = append(preview.Rows, PromotionDiffRowPreview{
					Op:      "context",
					OldLine: oldLine,
					NewLine: newLine,
					OldText: line.text,
					NewText: line.text,
				})
				oldLine++
				newLine++
			case '-':
				file.RemovedLines++
				preview.Rows = append(preview.Rows, PromotionDiffRowPreview{
					Op:      "removed",
					OldLine: oldLine,
					OldText: line.text,
				})
				oldLine++
			case '+':
				file.AddedLines++
				preview.Rows = append(preview.Rows, PromotionDiffRowPreview{
					Op:      "added",
					NewLine: newLine,
					NewText: line.text,
				})
				newLine++
			}
		}
		file.Hunks = append(file.Hunks, preview)
	}
	return file
}

func inspectPromotionDiffRisk(raw string) []string {
	flags := make([]string, 0)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Binary files "):
			flags = mergeRiskFlags(flags, "binary_diff")
		case strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to "):
			flags = mergeRiskFlags(flags, "rename_diff")
		case strings.HasPrefix(line, "--- ") && strings.TrimSpace(strings.TrimPrefix(line, "--- ")) == "/dev/null":
			flags = mergeRiskFlags(flags, "new_file_diff")
		case strings.HasPrefix(line, "+++ ") && strings.TrimSpace(strings.TrimPrefix(line, "+++ ")) == "/dev/null":
			flags = mergeRiskFlags(flags, "delete_file_diff")
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			path := strings.TrimSpace(line[4:])
			if path == "/dev/null" {
				continue
			}
			flags = mergeRiskFlags(flags, classifyPromotionPathRisk(normalizeDiffPath(path))...)
		}
	}
	return flags
}

func classifyPromotionPathRisk(path string) []string {
	clean := filepath.ToSlash(filepath.Clean(path))
	base := filepath.Base(clean)
	var flags []string
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "requirements.txt", "pyproject.toml", "poetry.lock", "Cargo.toml", "Cargo.lock":
		flags = append(flags, "dependency_change")
	}
	lower := strings.ToLower(clean)
	if strings.Contains(lower, "migration") || strings.Contains(lower, "migrations/") || strings.Contains(lower, "schema/") || strings.HasSuffix(lower, ".sql") {
		flags = append(flags, "db_migration")
	}
	return flags
}

func mergeRiskFlags(existing []string, flags ...string) []string {
	if len(flags) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(flags))
	out := make([]string, 0, len(existing)+len(flags))
	for _, flag := range existing {
		if flag == "" {
			continue
		}
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}
	for _, flag := range flags {
		if flag == "" {
			continue
		}
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}
	return out
}

func splitTextLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func normalizeDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return filepath.Clean(path)
}

func resolveApplyRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("sandbox.promotion.apply_root is required for promotion diff apply")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve promotion apply root: %w", err)
	}
	return abs, nil
}

func resolveApplyTargetPath(root string, relPath string) (string, error) {
	if err := validateRelativePatchPath(relPath); err != nil {
		return "", err
	}
	target := filepath.Join(root, relPath)
	targetAbs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", fmt.Errorf("resolve apply target: %w", err)
	}
	rel, err := filepath.Rel(root, targetAbs)
	if err != nil {
		return "", fmt.Errorf("check apply target: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("apply target must be inside promotion apply root")
	}
	return targetAbs, nil
}

func validateRelativePatchPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("patch path is required")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute patch paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("patch path must stay inside apply root")
	}
	base := filepath.Base(clean)
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return fmt.Errorf("patch path is denied by secret guard")
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		switch part {
		case ".git", "secrets", "private":
			return fmt.Errorf("patch path is denied by path guard")
		}
	}
	return nil
}
