package agent

import (
	"context"
	"strings"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

type userMemoryCandidate struct {
	memoryType string
	statement  string
	confidence float64
}

func (m *MioAgent) captureUserMemoryCandidate(ctx context.Context, t task.Task) error {
	if m.userMemoryManager == nil {
		return nil
	}
	candidate := extractUserMemoryCandidate(t.UserMessage())
	if candidate.statement == "" {
		return nil
	}
	if exists, err := m.userMemoryCandidateExists(ctx, candidate.statement); err != nil {
		return err
	} else if exists {
		return nil
	}
	evidenceID := "chat_memory_candidate:" + strings.TrimSpace(t.ChatID())
	if strings.TrimSpace(t.ChatID()) == "" {
		evidenceID = "chat_memory_candidate:unknown_session"
	}
	if jobID := strings.TrimSpace(t.JobID().String()); jobID != "" {
		evidenceID += ":" + jobID
	}
	_, err := m.userMemoryManager.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             candidate.memoryType,
		Statement:        candidate.statement,
		State:            domainmemory.MemoryStateCandidate,
		EvidenceEventIDs: []string{evidenceID},
		Confidence:       candidate.confidence,
		Sensitivity:      "normal",
		Scope:            "global",
		Source:           "chat_auto_candidate",
	})
	return err
}

func (m *MioAgent) userMemoryCandidateExists(ctx context.Context, statement string) (bool, error) {
	items, err := m.userMemoryManager.ListUserMemories(ctx, "ren", "", false, 50)
	if err != nil {
		return false, err
	}
	normalized := normalizeMemoryStatement(statement)
	for _, item := range items {
		if !item.Active {
			continue
		}
		if normalizeMemoryStatement(item.Statement) == normalized {
			return true, nil
		}
	}
	return false, nil
}

func extractUserMemoryCandidate(message string) userMemoryCandidate {
	text := cleanupUserMemoryCommandBody(message)
	if text == "" || strings.HasPrefix(text, "/") || looksLikeQuestion(text) {
		return userMemoryCandidate{}
	}
	if action, _ := parseUserMemoryCommand(text); action != "" {
		return userMemoryCandidate{}
	}
	if len([]rune(text)) > 80 {
		return userMemoryCandidate{}
	}

	if statement := extractPreferenceStatement(text, "が好き"); statement != "" {
		return userMemoryCandidate{memoryType: domainmemory.UserMemoryTypePreference, statement: statement, confidence: 0.62}
	}
	if statement := extractPreferenceStatement(text, "は好き"); statement != "" {
		return userMemoryCandidate{memoryType: domainmemory.UserMemoryTypePreference, statement: statement, confidence: 0.62}
	}
	if statement := extractPreferenceStatement(text, "が嫌い"); statement != "" {
		return userMemoryCandidate{memoryType: domainmemory.UserMemoryTypePreference, statement: statement, confidence: 0.62}
	}
	if statement := extractPreferenceStatement(text, "は嫌い"); statement != "" {
		return userMemoryCandidate{memoryType: domainmemory.UserMemoryTypePreference, statement: statement, confidence: 0.62}
	}
	if statement := extractConstraintStatement(text); statement != "" {
		return userMemoryCandidate{memoryType: domainmemory.UserMemoryTypeConstraint, statement: statement, confidence: 0.58}
	}
	return userMemoryCandidate{}
}

func extractPreferenceStatement(text string, marker string) string {
	text = stripSelfSubject(text)
	idx := strings.Index(text, marker)
	if idx <= 0 {
		return ""
	}
	subject := strings.TrimSpace(text[:idx])
	if subject == "" || len([]rune(subject)) > 40 {
		return ""
	}
	predicate := marker
	if strings.HasPrefix(marker, "は") {
		predicate = "が" + strings.TrimPrefix(marker, "は")
	}
	return normalizeMemoryStatement(subject + predicate)
}

func extractConstraintStatement(text string) string {
	prefixes := []string{"今後は", "これからは", "これから", "次から", "以後は", "以後"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			body := cleanupUserMemoryCommandBody(strings.TrimPrefix(text, prefix))
			if body == "" || len([]rune(body)) > 60 {
				return ""
			}
			return normalizeMemoryStatement(body)
		}
	}
	return ""
}

func stripSelfSubject(text string) string {
	text = strings.TrimSpace(text)
	subjects := []string{"私は", "わたしは", "俺は", "僕は", "ぼくは", "自分は", "私が", "俺が", "僕が", "自分が"}
	for _, subject := range subjects {
		if strings.HasPrefix(text, subject) {
			return strings.TrimSpace(strings.TrimPrefix(text, subject))
		}
	}
	return text
}

func looksLikeQuestion(text string) bool {
	questionMarkers := []string{"?", "？", "知ってる", "覚えてる", "なんで", "なぜ", "どう", "どれ", "どこ", "いつ"}
	for _, marker := range questionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func normalizeMemoryStatement(s string) string {
	s = cleanupUserMemoryCommandBody(s)
	s = strings.ReplaceAll(s, "　", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
