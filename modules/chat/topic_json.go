package chat

import (
	"encoding/json"
	"strings"
)

type topicCandidatesEnvelope struct {
	Candidates []topicCandidateJSON `json:"candidates"`
}

type topicCandidateJSON struct {
	TopicCandidate
}

func (c *topicCandidateJSON) UnmarshalJSON(data []byte) error {
	var topic string
	if err := json.Unmarshal(data, &topic); err == nil {
		c.Topic = topic
		return nil
	}
	var obj TopicCandidate
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	c.TopicCandidate = obj
	return nil
}

func ParseTopicCandidates(raw string) ([]TopicCandidate, error) {
	text := strings.TrimSpace(ExtractJSONPayload(raw))
	if text == "" {
		return nil, ErrTopicGenerationInvalidJSON
	}
	var env topicCandidatesEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return nil, ErrTopicGenerationInvalidJSON
	}
	if len(env.Candidates) == 0 {
		return nil, ErrTopicGenerationNoCandidates
	}
	out := make([]TopicCandidate, 0, len(env.Candidates))
	for _, candidate := range env.Candidates {
		out = append(out, candidate.TopicCandidate)
	}
	return out, nil
}

func ParseTopicJudgeResult(raw string) (TopicJudgeResult, error) {
	text := strings.TrimSpace(ExtractJSONPayload(raw))
	if text == "" {
		return TopicJudgeResult{}, ErrTopicJudgeInvalidJSON
	}
	var judge TopicJudgeResult
	if err := json.Unmarshal([]byte(text), &judge); err != nil {
		return TopicJudgeResult{}, ErrTopicJudgeInvalidJSON
	}
	if strings.TrimSpace(judge.WinnerTopic) == "" {
		return TopicJudgeResult{}, ErrTopicJudgeWinnerMissing
	}
	for i := range judge.Scores {
		judge.Scores[i] = NormalizeJudgeScoreTotal(judge.Scores[i])
	}
	return judge, nil
}

func ExtractJSONPayload(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
			text = strings.TrimSpace(text)
		}
	}
	if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
		return text
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(text[start : end+1])
	}
	return ""
}
