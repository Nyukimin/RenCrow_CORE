package complexity

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

type CoderDiffGenerator interface {
	Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error)
}

type CoderDiffRequest struct {
	Hotspot      domaincomplexity.Hotspot
	Evidence     []domaincomplexity.HotspotEvidence
	WorkstreamID string
	JobID        string
	SystemPrompt string
}

type CoderDiffResult struct {
	JobID        string `json:"job_id"`
	Prompt       string `json:"prompt"`
	RawResponse  string `json:"raw_response"`
	ConcreteDiff string `json:"concrete_diff"`
}

type CoderDiffService struct {
	coder CoderDiffGenerator
}

const defaultCoderDiffSystemPrompt = `You are a code patch generator for RenCrow Complexity Hotspot review.
Return only a minimal unified diff for the requested target file when it is safe.
Do not apply changes, do not include prose outside the diff, and do not touch unrelated files.
If a safe behavior-compatible diff cannot be generated from the provided evidence, explain briefly without a diff.`

func NewCoderDiffService(coder CoderDiffGenerator) *CoderDiffService {
	return &CoderDiffService{coder: coder}
}

func (s *CoderDiffService) GenerateConcreteDiff(ctx context.Context, req CoderDiffRequest) (CoderDiffResult, error) {
	if s == nil || s.coder == nil {
		return CoderDiffResult{}, fmt.Errorf("coder diff generator unavailable")
	}
	if strings.TrimSpace(req.Hotspot.HotspotID) == "" {
		return CoderDiffResult{}, fmt.Errorf("hotspot is required")
	}
	prompt := BuildCoderDiffGenerationPrompt(req.Hotspot, req.Evidence)
	jobID := strings.TrimSpace(req.JobID)
	var jid task.JobID
	if jobID == "" {
		jid = task.NewJobID()
		jobID = jid.String()
	} else {
		jid = task.JobIDFromString(jobID)
	}
	t := task.NewTask(jid, prompt, "viewer", strings.TrimSpace(req.WorkstreamID)).WithRoute(routing.RouteCODE)
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultCoderDiffSystemPrompt
	}
	raw, err := s.coder.Generate(ctx, t, systemPrompt)
	if err != nil {
		return CoderDiffResult{}, err
	}
	diff, err := ExtractUnifiedDiff(raw)
	if err != nil {
		return CoderDiffResult{}, err
	}
	if err := ValidateConcreteDiffForHotspot(req.Hotspot, diff); err != nil {
		return CoderDiffResult{}, err
	}
	return CoderDiffResult{
		JobID:        jobID,
		Prompt:       prompt,
		RawResponse:  raw,
		ConcreteDiff: diff,
	}, nil
}

func BuildCoderDiffGenerationPrompt(hotspot domaincomplexity.Hotspot, evidence []domaincomplexity.HotspotEvidence) string {
	var b strings.Builder
	b.WriteString(BuildCoderDiffRequestMarkdown(hotspot))
	writeCoderDiffEvidence(&b, hotspot, evidence)
	b.WriteString("\n## Output Contract\n\n")
	b.WriteString("Return exactly one unified diff for the target file. Do not apply it. Do not include unrelated files. Prefer a ```diff fenced block. If you cannot produce a safe behavior-compatible diff, return no diff and explain why.\n")
	return b.String()
}

func writeCoderDiffEvidence(b *strings.Builder, hotspot domaincomplexity.Hotspot, evidence []domaincomplexity.HotspotEvidence) {
	if len(evidence) == 0 {
		return
	}
	wroteHeader := false
	for _, item := range evidence {
		if strings.TrimSpace(item.HotspotID) != strings.TrimSpace(hotspot.HotspotID) {
			continue
		}
		snippet := strings.TrimSpace(item.Snippet)
		if snippet == "" {
			continue
		}
		if !wroteHeader {
			b.WriteString("\n## Observed Evidence Snippets\n\n")
			wroteHeader = true
		}
		fmt.Fprintf(b, "### Evidence `%s`\n\n", fallbackText(item.EvidenceID, "unknown"))
		if item.LineStart > 0 {
			if item.LineEnd > item.LineStart {
				fmt.Fprintf(b, "- Lines: %d-%d\n", item.LineStart, item.LineEnd)
			} else {
				fmt.Fprintf(b, "- Line: %d\n", item.LineStart)
			}
		}
		if strings.TrimSpace(item.Reason) != "" {
			fmt.Fprintf(b, "- Reason: %s\n", strings.TrimSpace(item.Reason))
		}
		fmt.Fprintf(b, "\n```go\n%s\n```\n\n", snippet)
	}
}

func ExtractUnifiedDiff(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("coder output is empty")
	}
	if diff := extractDiffFence(raw); diff != "" {
		return diff, nil
	}
	if looksLikeUnifiedDiff(raw) {
		return raw, nil
	}
	return "", fmt.Errorf("coder output did not contain unified diff")
}

func extractDiffFence(raw string) string {
	re := regexp.MustCompile("(?s)```(?:diff|patch)?\\s*\\n(.*?)\\n```")
	for _, match := range re.FindAllStringSubmatch(raw, -1) {
		if len(match) < 2 {
			continue
		}
		diff := strings.TrimSpace(match[1])
		if looksLikeUnifiedDiff(diff) {
			return diff
		}
	}
	return ""
}
