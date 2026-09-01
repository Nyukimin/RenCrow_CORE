package dcimigration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// legacyJSONTrace is intentionally separate from the current DCI domain
// types.  The source is the retired JSONL representation and its schema is
// not a compatibility route for runtime reads.
type legacyJSONTrace struct {
	EventID            string           `json:"event_id"`
	StartedAt          time.Time        `json:"started_at"`
	EndedAt            time.Time        `json:"ended_at"`
	Actor              string           `json:"actor"`
	Mode               string           `json:"mode"`
	UserQuery          string           `json:"user_query"`
	CorpusScope        []string         `json:"corpus_scope"`
	Steps              []legacyJSONStep `json:"steps"`
	FinalEvidenceCount int              `json:"final_evidence_count"`
	Status             string           `json:"status"`
	ErrorMessage       string           `json:"error_message"`
}

type legacyJSONStep struct {
	StepNo       int       `json:"step_no"`
	Tool         string    `json:"tool"`
	CommandText  string    `json:"command_text"`
	FilePath     string    `json:"file_path"`
	ResultCount  int       `json:"result_count"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

func loadLegacyJSONL(ctx context.Context, path string) (map[string]legacySearch, int, int, string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, 0, "", newCodedError("source_read", "legacy DCI JSONL is missing or not regular")
	}
	if info.Size() > maxJSONLBytes {
		return nil, 0, 0, "", newCodedError("oversized_jsonl", "legacy DCI JSONL exceeds the size bound")
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return nil, 0, 0, "", newCodedError("source_read", "hash legacy DCI JSONL: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, "", newCodedError("source_read", "open legacy DCI JSONL: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLine)
	searches := make(map[string]legacySearch)
	lineNumber := 0
	nonEmpty := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return nil, nonEmpty, 0, "", err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		nonEmpty++
		if nonEmpty > maxJSONLRecords {
			return nil, nonEmpty, 0, "", newCodedError("oversized_jsonl", "legacy DCI JSONL exceeds the record bound")
		}
		var raw legacyJSONTrace
		if err := decodeStrictJSON(line, &raw, legacyJSONTraceKeys); err != nil {
			return nil, nonEmpty, 0, "", newCodedError("malformed_jsonl", "legacy DCI JSONL record %d: %v", lineNumber, err)
		}
		search, err := validateLegacyJSONTrace(raw)
		if err != nil {
			return nil, nonEmpty, 0, "", newCodedError("malformed_jsonl", "legacy DCI JSONL record %d: %v", lineNumber, err)
		}
		if prior, exists := searches[search.ID]; exists {
			merged, err := mergeLegacySearch(prior, search)
			if err != nil {
				return nil, nonEmpty, 0, "", newCodedError("conflicting_search", "legacy JSONL duplicate search %q conflicts: %v", search.ID, err)
			}
			searches[search.ID] = merged
		} else {
			searches[search.ID] = search
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return nil, nonEmpty, 0, "", newCodedError("oversized_jsonl", "legacy DCI JSONL record exceeds the line bound")
		}
		return nil, nonEmpty, 0, "", newCodedError("malformed_jsonl", "read legacy DCI JSONL: %v", err)
	}
	if after, hashErr := fileSHA256(path); hashErr != nil || after != hash {
		return nil, nonEmpty, 0, "", newCodedError("source_changed", "legacy DCI JSONL changed during read")
	}
	steps := 0
	for _, search := range searches {
		steps += len(search.Steps)
	}
	return searches, nonEmpty, steps, hash, nil
}

var legacyJSONTraceKeys = map[string]struct{}{
	"event_id": {}, "started_at": {}, "ended_at": {}, "actor": {}, "mode": {},
	"user_query": {}, "corpus_scope": {}, "steps": {}, "final_evidence_count": {},
	"status": {}, "error_message": {},
}

var legacyJSONStepKeys = map[string]struct{}{
	"step_no": {}, "tool": {}, "command_text": {}, "file_path": {}, "result_count": {},
	"status": {}, "error_message": {}, "created_at": {},
}

func validateLegacyJSONTrace(raw legacyJSONTrace) (legacySearch, error) {
	search := legacySearch{
		ID: raw.EventID, StartedAt: raw.StartedAt.UTC(), EndedAt: raw.EndedAt.UTC(), Actor: raw.Actor,
		Mode: raw.Mode, Query: raw.UserQuery, CorpusScope: append([]string(nil), raw.CorpusScope...),
		Status: raw.Status, FinalEvidenceCount: raw.FinalEvidenceCount, ErrorMessage: raw.ErrorMessage,
		Steps: make(map[int]legacyStep, len(raw.Steps)),
	}
	if err := validateLegacySearch(search); err != nil {
		return legacySearch{}, err
	}
	for index, rawStep := range raw.Steps {
		// A separate duplicate-key pass is needed for nested step objects.
		// The top-level decoder has already rejected unknown fields.
		_ = index
		step := legacyStep{
			SearchID: search.ID, StepNo: rawStep.StepNo, Tool: rawStep.Tool,
			CommandText: rawStep.CommandText, FilePath: rawStep.FilePath,
			ResultCount: rawStep.ResultCount, Status: rawStep.Status,
			ErrorMessage: rawStep.ErrorMessage, CreatedAt: rawStep.CreatedAt.UTC(),
		}
		if err := validateLegacyStep(step); err != nil {
			return legacySearch{}, err
		}
		if _, exists := search.Steps[step.StepNo]; exists {
			return legacySearch{}, fmt.Errorf("duplicate step_no %d", step.StepNo)
		}
		search.Steps[step.StepNo] = step
	}
	return search, nil
}

func validateLegacySearch(search legacySearch) error {
	if strings.TrimSpace(search.ID) == "" || strings.TrimSpace(search.ID) != search.ID {
		return fmt.Errorf("event_id is required and must not have surrounding whitespace")
	}
	if len(search.Actor) > maxActorLabel {
		return fmt.Errorf("actor label exceeds the bound")
	}
	if search.StartedAt.IsZero() || search.EndedAt.IsZero() {
		return fmt.Errorf("started_at and ended_at are required")
	}
	if search.EndedAt.Before(search.StartedAt) {
		return fmt.Errorf("ended_at must be >= started_at")
	}
	if strings.TrimSpace(search.Mode) != "dci" || search.Mode != "dci" {
		return fmt.Errorf("mode must be dci")
	}
	if strings.TrimSpace(search.Query) == "" {
		return fmt.Errorf("user_query is required")
	}
	if search.Status != "completed" && search.Status != "failed" {
		return fmt.Errorf("status must be completed or failed")
	}
	if search.Status == "failed" && strings.TrimSpace(search.ErrorMessage) == "" {
		return fmt.Errorf("failed status requires error_message")
	}
	if search.Status == "completed" && strings.TrimSpace(search.ErrorMessage) != "" {
		return fmt.Errorf("completed status must not carry error_message")
	}
	if search.FinalEvidenceCount < 0 {
		return fmt.Errorf("final_evidence_count must be non-negative")
	}
	for _, scope := range search.CorpusScope {
		if strings.TrimSpace(scope) != scope {
			return fmt.Errorf("corpus_scope entries must not have surrounding whitespace")
		}
	}
	if search.CorpusScope == nil {
		search.CorpusScope = []string{}
	}
	return nil
}

func validateLegacyStep(step legacyStep) error {
	if step.StepNo <= 0 {
		return fmt.Errorf("step_no must be positive")
	}
	if step.Tool != "read_file" && step.Tool != "limit" {
		return newCodedError("unexpected_tool", "unexpected legacy DCI tool %q", step.Tool)
	}
	if step.CreatedAt.IsZero() {
		return fmt.Errorf("step created_at is required")
	}
	if step.Status == "" {
		return fmt.Errorf("step status is required")
	}
	if step.Status != "ok" && step.Status != "error" && step.Status != "stopped" && step.Status != "completed" {
		return fmt.Errorf("invalid step status %q", step.Status)
	}
	if step.Status == "error" && strings.TrimSpace(step.ErrorMessage) == "" {
		return fmt.Errorf("error step requires error_message")
	}
	if step.ResultCount < 0 {
		return fmt.Errorf("step result_count must be non-negative")
	}
	if step.Tool == "read_file" && strings.TrimSpace(step.FilePath) == "" {
		return fmt.Errorf("read_file step file_path is required")
	}
	if step.Tool == "limit" && strings.TrimSpace(step.ErrorMessage) == "" && strings.TrimSpace(step.CommandText) == "" {
		return fmt.Errorf("limit step requires a limitation reason")
	}
	return nil
}

func decodeStrictJSON(data []byte, target any, allowed map[string]struct{}) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	if allowed != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for key := range fields {
			if _, ok := allowed[key]; !ok {
				return fmt.Errorf("unknown field %q", key)
			}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONTokens(decoder); err != nil {
		return err
	}
	return nil
}

func walkJSONTokens(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walkJSONTokens(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walkJSONTokens(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
	}
	return nil
}

func mergeLegacySearch(left, right legacySearch) (legacySearch, error) {
	if left.ID != right.ID || !left.StartedAt.Equal(right.StartedAt) || !left.EndedAt.Equal(right.EndedAt) || left.Actor != right.Actor || left.Mode != right.Mode || left.Query != right.Query || !equalStrings(left.CorpusScope, right.CorpusScope) || left.Status != right.Status || left.FinalEvidenceCount != right.FinalEvidenceCount || left.ErrorMessage != right.ErrorMessage {
		return legacySearch{}, fmt.Errorf("trace fields differ")
	}
	merged := left
	if merged.Steps == nil {
		merged.Steps = make(map[int]legacyStep)
	}
	for stepNo, rightStep := range right.Steps {
		if leftStep, exists := merged.Steps[stepNo]; exists {
			if !equalLegacyStep(leftStep, rightStep) {
				return legacySearch{}, fmt.Errorf("step %d differs", stepNo)
			}
			continue
		}
		merged.Steps[stepNo] = rightStep
	}
	return merged, nil
}

func equalLegacyStep(left, right legacyStep) bool {
	return left.SearchID == right.SearchID && left.StepNo == right.StepNo && left.Tool == right.Tool && left.CommandText == right.CommandText && left.FilePath == right.FilePath && left.ResultCount == right.ResultCount && left.Status == right.Status && left.ErrorMessage == right.ErrorMessage && left.CreatedAt.Equal(right.CreatedAt)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
