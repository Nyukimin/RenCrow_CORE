package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

var legacyDCITables = []string{"dci_evidence", "dci_query_terms", "dci_search_step", "dci_search_trace"}

var legacyDCISpecs = map[string]tableSpec{
	"dci_search_trace": {Columns: []tableColumnSpec{
		{Name: "event_id", Type: "TEXT", Primary: true},
		{Name: "started_at", Type: "TEXT", NotNull: true},
		{Name: "ended_at", Type: "TEXT"},
		{Name: "actor", Type: "TEXT", NotNull: true},
		{Name: "mode", Type: "TEXT", NotNull: true},
		{Name: "user_query", Type: "TEXT"},
		{Name: "corpus_scope", Type: "TEXT"},
		{Name: "status", Type: "TEXT", NotNull: true},
		{Name: "final_evidence_count", Type: "INTEGER"},
		{Name: "error_message", Type: "TEXT"},
	}},
	"dci_search_step": {Columns: []tableColumnSpec{
		{Name: "id", Type: "INTEGER", Primary: true},
		{Name: "event_id", Type: "TEXT", NotNull: true},
		{Name: "step_no", Type: "INTEGER", NotNull: true},
		{Name: "tool", Type: "TEXT", NotNull: true},
		{Name: "command_text", Type: "TEXT"},
		{Name: "file_path", Type: "TEXT"},
		{Name: "result_count", Type: "INTEGER"},
		{Name: "status", Type: "TEXT", NotNull: true},
		{Name: "error_message", Type: "TEXT"},
		{Name: "created_at", Type: "TEXT", NotNull: true},
	}},
	"dci_evidence": {Columns: []tableColumnSpec{
		{Name: "evidence_id", Type: "TEXT", Primary: true},
		{Name: "event_id", Type: "TEXT", NotNull: true},
		{Name: "source_id", Type: "TEXT"},
		{Name: "file_path", Type: "TEXT", NotNull: true},
		{Name: "heading", Type: "TEXT"},
		{Name: "line_start", Type: "INTEGER"},
		{Name: "line_end", Type: "INTEGER"},
		{Name: "snippet", Type: "TEXT", NotNull: true},
		{Name: "reason", Type: "TEXT"},
		{Name: "confidence", Type: "REAL"},
		{Name: "created_at", Type: "TEXT", NotNull: true},
	}},
	"dci_query_terms": {Columns: []tableColumnSpec{
		{Name: "id", Type: "INTEGER", Primary: true},
		{Name: "event_id", Type: "TEXT", NotNull: true},
		{Name: "term", Type: "TEXT", NotNull: true},
		{Name: "term_type", Type: "TEXT"},
		{Name: "parent_term", Type: "TEXT"},
		{Name: "created_at", Type: "TEXT", NotNull: true},
	}},
}

func loadLegacyDCI(ctx context.Context, path string) (map[string]legacySearch, map[string]legacyEvidence, SourceCounts, sourceHashes, error) {
	before, err := fileSHA256(path)
	if err != nil {
		return nil, nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "hash legacy DCI SQLite: %v", err)
	}
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return nil, nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "open legacy DCI SQLite: %v", err)
	}
	searches, evidence, counts, logicalLines, readErr := queryLegacyDCI(ctx, db)
	var logical logicalHashes
	if readErr == nil {
		logical, readErr = hashSQLiteLogical(ctx, db, nil)
	}
	closeErr := db.Close()
	if readErr != nil {
		return searches, evidence, counts, sourceHashes{}, readErr
	}
	if closeErr != nil {
		return nil, nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "close legacy DCI SQLite: %v", closeErr)
	}
	after, err := fileSHA256(path)
	if err != nil || after != before {
		return nil, nil, SourceCounts{}, sourceHashes{}, newCodedError("source_changed", "legacy DCI SQLite changed during read")
	}
	return searches, evidence, counts, sourceHashes{
		DatabaseLogical: logical.Full,
		Schema:          logical.Schema,
		Classification:  hashCanonicalLines(logicalLines),
	}, nil
}

func queryLegacyDCI(ctx context.Context, db *sql.DB) (map[string]legacySearch, map[string]legacyEvidence, SourceCounts, []string, error) {
	if err := checkUserVersion(ctx, db, 0); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("unknown_schema", "legacy DCI schema version: %v", err)
	}
	tables, err := schemaUserTables(ctx, db)
	if err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("unknown_schema", "inspect legacy DCI tables: %v", err)
	}
	if err := requireTableSet(tables, legacyDCITables, true); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("unknown_schema", "%v", err)
	}
	for table, spec := range legacyDCISpecs {
		if err := inspectTable(ctx, db, table, spec); err != nil {
			return nil, nil, SourceCounts{}, nil, newCodedError("unknown_schema", "legacy DCI table %s: %v", table, err)
		}
	}
	searches := make(map[string]legacySearch)
	evidence := make(map[string]legacyEvidence)
	counts := SourceCounts{}
	var lines []string

	rows, err := db.QueryContext(ctx, `SELECT event_id, started_at, ended_at, actor, mode, user_query, corpus_scope, status, final_evidence_count, error_message FROM dci_search_trace ORDER BY rowid`)
	if err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "read legacy DCI traces: %v", err)
	}
	for rows.Next() {
		values := make([]any, 10)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "scan legacy DCI trace: %v", err)
		}
		started, err := parseLegacyTime(values[1])
		if err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI trace started_at: %v", err)
		}
		ended, err := parseLegacyTime(values[2])
		if err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI trace ended_at: %v", err)
		}
		scope, err := parseLegacyStringSlice(readText(values[6]))
		if err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI trace corpus_scope: %v", err)
		}
		finalCount, err := readNullableInt(values[8])
		if err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI trace final_evidence_count: %v", err)
		}
		search := legacySearch{ID: readText(values[0]), StartedAt: started, EndedAt: ended, Actor: readText(values[3]), Mode: readText(values[4]), Query: readText(values[5]), CorpusScope: scope, Status: readText(values[7]), FinalEvidenceCount: int(finalCount), ErrorMessage: readText(values[9]), Steps: make(map[int]legacyStep)}
		if err := validateLegacySearch(search); err != nil {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI trace %s: %v", search.ID, err)
		}
		if _, exists := searches[search.ID]; exists {
			_ = rows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("conflicting_search", "legacy DCI trace has duplicate event_id")
		}
		searches[search.ID] = search
		counts.DCITraces++
		line, _ := json.Marshal([]any{"trace", search.ID, search.StartedAt.UTC().Format(time.RFC3339Nano), search.EndedAt.UTC().Format(time.RFC3339Nano), search.Actor, search.Mode, search.Query, search.CorpusScope, search.Status, search.FinalEvidenceCount, search.ErrorMessage})
		lines = append(lines, string(line))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "iterate legacy DCI traces: %v", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "close legacy DCI traces: %v", err)
	}

	stepRows, err := db.QueryContext(ctx, `SELECT id, event_id, step_no, tool, command_text, file_path, result_count, status, error_message, created_at FROM dci_search_step ORDER BY rowid`)
	if err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "read legacy DCI steps: %v", err)
	}
	for stepRows.Next() {
		values := make([]any, 10)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := stepRows.Scan(destinations...); err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "scan legacy DCI step: %v", err)
		}
		id, err := readInt(values[0])
		if err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI step id: %v", err)
		}
		stepNo, err := readInt(values[2])
		if err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI step step_no: %v", err)
		}
		resultCount, err := readNullableInt(values[6])
		if err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI step result_count: %v", err)
		}
		created, err := parseLegacyTime(values[9])
		if err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI step created_at: %v", err)
		}
		step := legacyStep{ID: id, SearchID: readText(values[1]), StepNo: int(stepNo), Tool: readText(values[3]), CommandText: readText(values[4]), FilePath: readText(values[5]), ResultCount: int(resultCount), Status: readText(values[7]), ErrorMessage: readText(values[8]), CreatedAt: created}
		if err := validateLegacyStep(step); err != nil {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError(errorCode(err, "malformed_source"), "legacy DCI step: %v", err)
		}
		search, exists := searches[step.SearchID]
		if !exists {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("missing_parent", "legacy DCI step references missing search")
		}
		if _, exists := search.Steps[step.StepNo]; exists {
			_ = stepRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("conflicting_search", "legacy DCI search has duplicate step_no")
		}
		search.Steps[step.StepNo] = step
		searches[step.SearchID] = search
		counts.DCISteps++
		line, _ := json.Marshal([]any{"step", step.ID, step.SearchID, step.StepNo, step.Tool, step.CommandText, step.FilePath, step.ResultCount, step.Status, step.ErrorMessage, step.CreatedAt.UTC().Format(time.RFC3339Nano)})
		lines = append(lines, string(line))
	}
	if err := stepRows.Err(); err != nil {
		_ = stepRows.Close()
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "iterate legacy DCI steps: %v", err)
	}
	if err := stepRows.Close(); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "close legacy DCI steps: %v", err)
	}

	evidenceRows, err := db.QueryContext(ctx, `SELECT evidence_id, event_id, source_id, file_path, heading, line_start, line_end, snippet, reason, confidence, created_at FROM dci_evidence ORDER BY rowid`)
	if err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "read legacy DCI evidence: %v", err)
	}
	for evidenceRows.Next() {
		values := make([]any, 11)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := evidenceRows.Scan(destinations...); err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "scan legacy DCI evidence: %v", err)
		}
		lineStart, err := readNullableInt(values[5])
		if err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI evidence line_start: %v", err)
		}
		lineEnd, err := readNullableInt(values[6])
		if err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI evidence line_end: %v", err)
		}
		confidence, err := readNullableFloat(values[9])
		if err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI evidence confidence: %v", err)
		}
		created, err := parseLegacyTime(values[10])
		if err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI evidence created_at: %v", err)
		}
		item := legacyEvidence{ID: readText(values[0]), SearchID: readText(values[1]), SourceID: readText(values[2]), FilePath: readText(values[3]), Heading: readText(values[4]), LineStart: int(lineStart), LineEnd: int(lineEnd), Snippet: readText(values[7]), Reason: readText(values[8]), Confidence: confidence, CreatedAt: created}
		if err := validateLegacyEvidence(item); err != nil {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI evidence: %v", err)
		}
		if _, exists := searches[item.SearchID]; !exists {
			_ = evidenceRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("missing_parent", "legacy DCI evidence references missing search")
		}
		if prior, exists := evidence[item.ID]; exists {
			if !equalLegacyEvidence(prior, item) {
				_ = evidenceRows.Close()
				return nil, nil, SourceCounts{}, nil, newCodedError("conflicting_duplicate_evidence", "legacy DCI evidence ID conflicts")
			}
		} else {
			evidence[item.ID] = item
		}
		counts.DCIEvidence++
		line, _ := json.Marshal([]any{"evidence", item.ID, item.SearchID, item.SourceID, item.FilePath, item.Heading, item.LineStart, item.LineEnd, item.Snippet, item.Reason, item.Confidence, item.CreatedAt.UTC().Format(time.RFC3339Nano)})
		lines = append(lines, string(line))
	}
	if err := evidenceRows.Err(); err != nil {
		_ = evidenceRows.Close()
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "iterate legacy DCI evidence: %v", err)
	}
	if err := evidenceRows.Close(); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "close legacy DCI evidence: %v", err)
	}

	termRows, err := db.QueryContext(ctx, `SELECT id, event_id, term, term_type, parent_term, created_at FROM dci_query_terms ORDER BY rowid`)
	if err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "read legacy DCI query terms: %v", err)
	}
	for termRows.Next() {
		values := make([]any, 6)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := termRows.Scan(destinations...); err != nil {
			_ = termRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "scan legacy DCI query term: %v", err)
		}
		if _, err := readInt(values[0]); err != nil {
			_ = termRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI query term id: %v", err)
		}
		if strings.TrimSpace(readText(values[1])) == "" || strings.TrimSpace(readText(values[2])) == "" {
			_ = termRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI query term parent/term is required")
		}
		if _, exists := searches[readText(values[1])]; !exists {
			_ = termRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("missing_parent", "legacy DCI query term references missing search")
		}
		created, err := parseLegacyTime(values[5])
		if err != nil {
			_ = termRows.Close()
			return nil, nil, SourceCounts{}, nil, newCodedError("malformed_source", "legacy DCI query term created_at: %v", err)
		}
		line, _ := json.Marshal([]any{"term", readIntMust(values[0]), readText(values[1]), readText(values[2]), readText(values[3]), readText(values[4]), created.UTC().Format(time.RFC3339Nano)})
		lines = append(lines, string(line))
		counts.DCIQueryTerms++
	}
	if err := termRows.Err(); err != nil {
		_ = termRows.Close()
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "iterate legacy DCI query terms: %v", err)
	}
	if err := termRows.Close(); err != nil {
		return nil, nil, SourceCounts{}, nil, newCodedError("source_read", "close legacy DCI query terms: %v", err)
	}
	if counts.DCIQueryTerms > 0 {
		return searches, evidence, counts, lines, newCodedError("unsupported_query_terms", "legacy DCI query terms are not part of the Step 03 one-shot migration")
	}
	return searches, evidence, counts, lines, nil
}

func readIntMust(value any) int64 {
	parsed, _ := readInt(value)
	return parsed
}

func parseLegacyStringSlice(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	if values == nil {
		return []string{}, nil
	}
	for _, value := range values {
		if strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("corpus_scope entry has surrounding whitespace")
		}
	}
	return values, nil
}

func readNullableInt(value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	return readInt(value)
}

func readNullableFloat(value any) (float64, error) {
	if value == nil {
		return 0, nil
	}
	return readFloat(value)
}

func parseLegacyTime(value any) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	if parsed, ok := value.(time.Time); ok {
		return parsed.UTC(), nil
	}
	text := strings.TrimSpace(readText(value))
	if text == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
	}
	if number, err := readInt(value); err == nil {
		return time.Unix(number, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", text)
}

func validateLegacyEvidence(item legacyEvidence) error {
	if item.ID == "" || strings.TrimSpace(item.ID) != item.ID {
		return fmt.Errorf("evidence_id is required and must not have surrounding whitespace")
	}
	if strings.TrimSpace(item.SearchID) == "" || strings.TrimSpace(item.SearchID) != item.SearchID {
		return fmt.Errorf("event_id is required")
	}
	if strings.TrimSpace(item.FilePath) == "" || strings.TrimSpace(item.Snippet) == "" {
		return fmt.Errorf("file_path and snippet are required")
	}
	if item.LineStart <= 0 || item.LineEnd < item.LineStart {
		return fmt.Errorf("line range is invalid")
	}
	if math.IsNaN(item.Confidence) || item.Confidence < 0 || item.Confidence > 1 {
		return fmt.Errorf("confidence is outside [0,1]")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func equalLegacyEvidence(left, right legacyEvidence) bool {
	// created_at is source provenance, not part of the canonical Evidence
	// content.  DCI and L1 may record it at different persistence times while
	// still describing the same legacy evidence value.
	return left.ID == right.ID && left.SearchID == right.SearchID && left.SourceID == right.SourceID && left.FilePath == right.FilePath && left.Heading == right.Heading && left.LineStart == right.LineStart && left.LineEnd == right.LineEnd && left.Snippet == right.Snippet && left.Reason == right.Reason && left.Confidence == right.Confidence
}
