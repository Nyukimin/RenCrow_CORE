package skillgovernance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

const defaultCoderEvidenceRoot = "workspace/logs/skill_governance/coder_evidence"

type CoderEvidenceService struct {
	root            string
	now             func() time.Time
	transcriptStore CoderTranscriptStore
}

type CoderTranscriptStore interface {
	SaveCoderTranscriptEntry(ctx context.Context, entry domainskill.CoderTranscriptEntry) error
}

func NewCoderEvidenceService(root string) *CoderEvidenceService {
	if strings.TrimSpace(root) == "" {
		root = defaultCoderEvidenceRoot
	}
	return &CoderEvidenceService{root: filepath.Clean(root), now: time.Now}
}

func (s *CoderEvidenceService) WithNow(now func() time.Time) *CoderEvidenceService {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *CoderEvidenceService) WithTranscriptStore(store CoderTranscriptStore) *CoderEvidenceService {
	if s != nil {
		s.transcriptStore = store
	}
	return s
}

func (s *CoderEvidenceService) SaveCoderProposalEvidence(ctx context.Context, item domainskill.CoderProposalEvidence) (domainskill.CoderProposalEvidencePaths, error) {
	if s == nil {
		return domainskill.CoderProposalEvidencePaths{}, nil
	}
	id := sanitizeEvidenceID(firstNonEmpty(item.JobID, item.SessionID, "coder"))
	if id == "" {
		id = fmt.Sprintf("coder_%d", s.now().UTC().UnixNano())
	}
	root := filepath.Join(s.root, id)
	if err := os.MkdirAll(root, 0755); err != nil {
		return domainskill.CoderProposalEvidencePaths{}, fmt.Errorf("create coder evidence dir: %w", err)
	}

	diffPath := filepath.Join(root, "skill_diff.md")
	transcriptPath := filepath.Join(root, "agent_transcript.md")
	if err := os.WriteFile(diffPath, []byte(buildCoderSkillDiffEvidence(item)), 0644); err != nil {
		return domainskill.CoderProposalEvidencePaths{}, fmt.Errorf("write skill diff evidence: %w", err)
	}
	if err := os.WriteFile(transcriptPath, []byte(buildCoderTranscriptEvidence(item)), 0644); err != nil {
		return domainskill.CoderProposalEvidencePaths{}, fmt.Errorf("write agent transcript evidence: %w", err)
	}

	paths := domainskill.CoderProposalEvidencePaths{
		RootPath:            filepath.ToSlash(root),
		SkillDiffPath:       filepath.ToSlash(diffPath),
		AgentTranscriptPath: filepath.ToSlash(transcriptPath),
	}
	if err := s.saveCoderTranscriptEntries(ctx, item, paths); err != nil {
		return domainskill.CoderProposalEvidencePaths{}, err
	}
	return paths, nil
}

func (s *CoderEvidenceService) saveCoderTranscriptEntries(ctx context.Context, item domainskill.CoderProposalEvidence, paths domainskill.CoderProposalEvidencePaths) error {
	if s == nil || s.transcriptStore == nil {
		return nil
	}
	now := s.now().UTC()
	segments := []struct {
		role    string
		segment string
		text    string
		path    string
	}{
		{role: "user", segment: "task", text: item.TaskText},
		{role: "coder", segment: "plan", text: item.Plan},
		{role: "coder", segment: "risk", text: item.Risk},
		{role: "coder", segment: "patch_evidence", path: paths.SkillDiffPath},
		{role: "worker", segment: "execution_summary", text: item.ExecutionSummary},
		{role: "worker", segment: "formatted_result", text: item.FormattedResult},
		{role: "worker", segment: "execution_error", text: item.ExecutionError},
		{role: "system", segment: "transcript_evidence", path: paths.AgentTranscriptPath},
	}
	baseID := sanitizeEvidenceID(firstNonEmpty(item.JobID, item.SessionID, "coder"))
	for _, segment := range segments {
		text := strings.TrimSpace(segment.text)
		path := strings.TrimSpace(segment.path)
		if text == "" && path == "" {
			continue
		}
		entry := domainskill.CoderTranscriptEntry{
			EventID:      fmt.Sprintf("evt_coder_transcript_%d_%s_%s", now.UnixNano(), baseID, sanitizeEvidenceID(segment.segment)),
			JobID:        item.JobID,
			SessionID:    item.SessionID,
			Route:        item.Route,
			Agent:        item.Agent,
			Role:         segment.role,
			Segment:      segment.segment,
			Text:         text,
			EvidencePath: path,
			CreatedAt:    now,
		}
		if err := s.transcriptStore.SaveCoderTranscriptEntry(ctx, entry); err != nil {
			return fmt.Errorf("save coder transcript entry: %w", err)
		}
	}
	return nil
}

func buildCoderSkillDiffEvidence(item domainskill.CoderProposalEvidence) string {
	var b strings.Builder
	b.WriteString("# Coder Proposal Patch Evidence\n\n")
	writeEvidenceField(&b, "Job ID", item.JobID)
	writeEvidenceField(&b, "Route", item.Route)
	writeEvidenceField(&b, "Agent", item.Agent)
	b.WriteString("\n## Patch\n\n")
	patch := strings.TrimSpace(item.Patch)
	if patch == "" {
		patch = "(empty patch evidence)"
	}
	b.WriteString(patch)
	if !strings.HasSuffix(patch, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func buildCoderTranscriptEvidence(item domainskill.CoderProposalEvidence) string {
	var b strings.Builder
	b.WriteString("# Coder Proposal Transcript Evidence\n\n")
	writeEvidenceField(&b, "Job ID", item.JobID)
	writeEvidenceField(&b, "Session ID", item.SessionID)
	writeEvidenceField(&b, "Route", item.Route)
	writeEvidenceField(&b, "Agent", item.Agent)
	writeEvidenceField(&b, "Success", fmt.Sprintf("%t", item.Success))
	if item.ExecutionError != "" {
		writeEvidenceField(&b, "Execution Error", item.ExecutionError)
	}
	b.WriteString("\n## Task\n\n")
	b.WriteString(firstNonEmpty(strings.TrimSpace(item.TaskText), "(empty task)"))
	b.WriteString("\n\n## Plan\n\n")
	b.WriteString(firstNonEmpty(strings.TrimSpace(item.Plan), "(empty plan)"))
	b.WriteString("\n\n## Risk\n\n")
	b.WriteString(firstNonEmpty(strings.TrimSpace(item.Risk), "(empty risk)"))
	if strings.TrimSpace(item.CostHint) != "" {
		b.WriteString("\n\n## Cost Hint\n\n")
		b.WriteString(strings.TrimSpace(item.CostHint))
	}
	if strings.TrimSpace(item.ExecutionSummary) != "" {
		b.WriteString("\n\n## Execution Summary\n\n")
		b.WriteString(strings.TrimSpace(item.ExecutionSummary))
	}
	if strings.TrimSpace(item.FormattedResult) != "" {
		b.WriteString("\n\n## Formatted Result\n\n")
		b.WriteString(strings.TrimSpace(item.FormattedResult))
	}
	b.WriteByte('\n')
	return b.String()
}

func writeEvidenceField(b *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString("- ")
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

var unsafeEvidenceID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeEvidenceID(value string) string {
	value = strings.TrimSpace(value)
	value = unsafeEvidenceID.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
