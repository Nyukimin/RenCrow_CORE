package skillgovernance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

func TestCoderEvidenceServiceSaveCoderProposalEvidenceWritesDiffAndTranscript(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace", "logs", "skill_governance", "coder_evidence")
	transcripts := &recordingCoderTranscriptStore{}
	service := NewCoderEvidenceService(root).WithNow(func() time.Time {
		return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	}).WithTranscriptStore(transcripts)

	paths, err := service.SaveCoderProposalEvidence(context.Background(), domainskill.CoderProposalEvidence{
		JobID:            "job/unsafe id",
		SessionID:        "sess-1",
		Route:            "CODE3",
		Agent:            "coder3",
		TaskText:         "Skillを更新して",
		Plan:             "1. SKILL.mdを更新する",
		Patch:            "diff --git a/skills/core/example/SKILL.md b/skills/core/example/SKILL.md",
		Risk:             "low",
		CostHint:         "low",
		ExecutionSummary: "実行: 1 件, 成功: 1 件, 失敗: 0 件",
		FormattedResult:  "## Plan\n1. SKILL.mdを更新する",
		Success:          true,
	})
	if err != nil {
		t.Fatalf("SaveCoderProposalEvidence failed: %v", err)
	}
	if strings.Contains(paths.RootPath, "/unsafe id") {
		t.Fatalf("RootPath was not sanitized: %s", paths.RootPath)
	}

	diff, err := os.ReadFile(paths.SkillDiffPath)
	if err != nil {
		t.Fatalf("read diff evidence: %v", err)
	}
	if !strings.Contains(string(diff), "Coder Proposal Patch Evidence") || !strings.Contains(string(diff), "diff --git") {
		t.Fatalf("unexpected diff evidence: %s", diff)
	}

	transcript, err := os.ReadFile(paths.AgentTranscriptPath)
	if err != nil {
		t.Fatalf("read transcript evidence: %v", err)
	}
	body := string(transcript)
	for _, want := range []string{"Coder Proposal Transcript Evidence", "Skillを更新して", "1. SKILL.mdを更新する", "実行: 1 件"} {
		if !strings.Contains(body, want) {
			t.Fatalf("transcript missing %q: %s", want, body)
		}
	}
	if len(transcripts.entries) == 0 {
		t.Fatalf("expected coder transcript entries")
	}
	segments := map[string]domainskill.CoderTranscriptEntry{}
	for _, entry := range transcripts.entries {
		segments[entry.Segment] = entry
	}
	if segments["task"].Text != "Skillを更新して" {
		t.Fatalf("task transcript entry=%#v", segments["task"])
	}
	if segments["patch_evidence"].EvidencePath != paths.SkillDiffPath {
		t.Fatalf("patch evidence entry=%#v paths=%#v", segments["patch_evidence"], paths)
	}
	if segments["transcript_evidence"].EvidencePath != paths.AgentTranscriptPath {
		t.Fatalf("transcript evidence entry=%#v paths=%#v", segments["transcript_evidence"], paths)
	}
}

type recordingCoderTranscriptStore struct {
	entries []domainskill.CoderTranscriptEntry
}

func (s *recordingCoderTranscriptStore) SaveCoderTranscriptEntry(_ context.Context, entry domainskill.CoderTranscriptEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}
