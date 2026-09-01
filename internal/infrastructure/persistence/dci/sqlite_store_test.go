package dci

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	_ "modernc.org/sqlite"
)

func validSQLiteSearchResult() domaindci.SearchResult {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	stepEventID := modulecore.NewEventID()
	evidenceEventID := modulecore.NewEventID()
	evidenceID := modulecore.NewEvidenceID()
	scope := []string{"docs/", "specs/"}
	return domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			ActionID:     actionID,
			Query:        "DCI Source Registry",
			Intent:       "find canonical DCI evidence",
			CorpusScope:  append([]string(nil), scope...),
			DerivedTerms: []string{"dci", "source"},
			Confidence:   0.8,
			Limitations:  []string{"bounded corpus"},
			Evidence: []domaindci.Evidence{{
				EvidenceID:       evidenceID,
				CreatedByEventID: evidenceEventID,
				SourceID:         "src_1",
				FilePath:         "docs/spec.md",
				Heading:          "DCI",
				LineStart:        10,
				LineEnd:          12,
				Snippet:          "DCI evidence",
				Reason:           "query term matched",
				Confidence:       0.8,
			}},
		},
		Trace: domaindci.SearchTrace{
			TraceID:            traceID,
			ActionID:           actionID,
			StartedAt:          now,
			EndedAt:            now.Add(time.Second),
			ActorAttribution:   domaindci.ActorAttributionAuthenticated,
			ActorKind:          "agent",
			ActorID:            "shiro",
			IdempotencyKey:     "dci-idem-1",
			Mode:               "dci",
			UserQuery:          "DCI Source Registry",
			CorpusScope:        append([]string(nil), scope...),
			FinalEvidenceCount: 1,
			Status:             "completed",
			Steps: []domaindci.SearchStep{{
				StepNo:      1,
				EventID:     stepEventID,
				EventType:   "dci.file.read",
				Tool:        "read_file",
				CommandText: "read_file docs/spec.md",
				FilePath:    "docs/spec.md",
				ResultCount: 1,
				Status:      "ok",
				CreatedAt:   now,
			}},
		},
	}
}

func TestSQLiteStoreCreatesAndReopensCanonicalSchemaV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dci.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	var version, foreignKeys int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != dciSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, dciSchemaVersion)
	}
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	for _, table := range dciSchemaTables {
		columns, err := tableColumnNames(store.db, table)
		if err != nil {
			t.Fatalf("table %s columns: %v", table, err)
		}
		if !sameStringSet(columns, dciSchemaColumns[table]) {
			t.Fatalf("table %s columns = %#v, want %#v", table, columns, dciSchemaColumns[table])
		}
	}
	var parentSQL string
	if err := store.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='dci_search_trace'").Scan(&parentSQL); err != nil {
		t.Fatalf("parent schema: %v", err)
	}
	if strings.Contains(parentSQL, "event_id") || strings.Contains(parentSQL, "actor ") {
		t.Fatalf("legacy parent columns remain: %s", parentSQL)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen canonical schema: %v", err)
	}
	defer reopened.Close()
}

func TestSQLiteStoreRejectsNonEmptyLegacySchemaWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE dci_search_trace (
event_id TEXT PRIMARY KEY,
started_at TEXT NOT NULL,
ended_at TEXT,
actor TEXT NOT NULL,
mode TEXT NOT NULL,
user_query TEXT,
corpus_scope TEXT,
status TEXT NOT NULL,
final_evidence_count INTEGER,
error_message TEXT
)`)
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy before: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("legacy schema unexpectedly accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("legacy schema was mutated after fail-closed rejection")
	}
}

func TestSQLiteStoreRejectsUnknownVersionWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set unknown version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unknown db: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unknown before: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("unknown schema version unexpectedly accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unknown after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unknown schema was mutated after fail-closed rejection")
	}
}

func TestSQLiteStoreRejectsMalformedV2WithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open malformed: %v", err)
	}
	if _, err := db.Exec("DROP INDEX " + dciTraceTraceIDIndex); err != nil {
		t.Fatalf("drop required index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close malformed: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed before: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("malformed v2 schema unexpectedly accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("malformed v2 schema was mutated after rejection")
	}
}

func TestSQLiteStoreRejectsWrongV2ColumnTypeWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-type.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rewriteSQLiteObjectSQL(t, path, "table", "dci_search_trace", "actor_kind TEXT", "actor_kind BLOB")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wrong-type before: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("wrong v2 column type unexpectedly accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wrong-type after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("wrong v2 column type was mutated after rejection")
	}
}

func TestSQLiteStoreRejectsWrongV2IdempotencyPredicateWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-predicate.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rewriteSQLiteObjectSQL(t, path, "index", dciTraceIdempotencyIndex, "idempotency_key <> ''", "idempotency_key IS NOT NULL")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wrong-predicate before: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("wrong v2 idempotency predicate unexpectedly accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wrong-predicate after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("wrong v2 idempotency predicate was mutated after rejection")
	}
}

func TestSQLiteStoreSaveAndReadCompleteSearchResultByAction(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	want := validSQLiteSearchResult()
	if err := store.SaveSearchResult(ctx, want); err != nil {
		t.Fatalf("SaveSearchResult: %v", err)
	}
	got, found, err := store.FindSearchResultByActionID(ctx, want.Trace.ActionID)
	if err != nil || !found {
		t.Fatalf("FindSearchResultByActionID found=%v err=%v", found, err)
	}
	if got.Trace.ActionID != want.Trace.ActionID || got.Trace.TraceID != want.Trace.TraceID || got.Trace.ActorAttribution != domaindci.ActorAttributionAuthenticated || got.Trace.ActorID != want.Trace.ActorID || got.Trace.IdempotencyKey != want.Trace.IdempotencyKey {
		t.Fatalf("trace identity round trip = %#v", got.Trace)
	}
	if got.Pack.Query != want.Pack.Query || got.Pack.Intent != want.Pack.Intent || got.Pack.Confidence != want.Pack.Confidence || !sameStringSlice(got.Pack.CorpusScope, want.Pack.CorpusScope) || !sameStringSlice(got.Pack.DerivedTerms, want.Pack.DerivedTerms) || !sameStringSlice(got.Pack.Limitations, want.Pack.Limitations) {
		t.Fatalf("pack round trip = %#v", got.Pack)
	}
	if len(got.Trace.Steps) != 1 || got.Trace.Steps[0].EventID != want.Trace.Steps[0].EventID || got.Trace.Steps[0].EventType != "dci.file.read" {
		t.Fatalf("steps round trip = %#v", got.Trace.Steps)
	}
	if len(got.Pack.Evidence) != 1 || got.Pack.Evidence[0].EvidenceID != want.Pack.Evidence[0].EvidenceID || got.Pack.Evidence[0].CreatedByEventID != want.Pack.Evidence[0].CreatedByEventID {
		t.Fatalf("evidence round trip = %#v", got.Pack.Evidence)
	}
	if err := domaindci.ValidateSearchResult(got); err != nil {
		t.Fatalf("round-trip result validation: %v", err)
	}
	trace, found, err := store.FindSearchTraceByActionID(ctx, want.Trace.ActionID)
	if err != nil || !found || trace.ActionID != want.Trace.ActionID || len(trace.Steps) != 1 {
		t.Fatalf("FindSearchTraceByActionID found=%v err=%v trace=%#v", found, err, trace)
	}
	byKey, found, err := store.FindSearchTraceByIdempotencyKey(ctx, want.Trace.IdempotencyKey)
	if err != nil || !found || byKey.ActionID != want.Trace.ActionID {
		t.Fatalf("FindSearchTraceByIdempotencyKey found=%v err=%v trace=%#v", found, err, byKey)
	}
	byKeyResult, found, err := store.FindSearchResultByIdempotencyKey(ctx, want.Trace.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("FindSearchResultByIdempotencyKey found=%v err=%v", found, err)
	}
	if byKeyResult.Trace.ActionID != want.Trace.ActionID || byKeyResult.Trace.IdempotencyKey != want.Trace.IdempotencyKey || !sameStringSlice(byKeyResult.Pack.CorpusScope, want.Pack.CorpusScope) || !sameStringSlice(byKeyResult.Pack.DerivedTerms, want.Pack.DerivedTerms) || len(byKeyResult.Pack.Evidence) != len(want.Pack.Evidence) {
		t.Fatalf("idempotency result replay = %#v", byKeyResult)
	}
	recent, err := store.ListRecent(1)
	if err != nil || len(recent) != 1 || recent[0].ActionID != want.Trace.ActionID {
		t.Fatalf("ListRecent = %#v err=%v", recent, err)
	}
	var termAction string
	if err := store.db.QueryRow("SELECT action_id FROM dci_query_terms LIMIT 1").Scan(&termAction); err != nil {
		t.Fatalf("query term action: %v", err)
	}
	if termAction != string(want.Trace.ActionID) {
		t.Fatalf("query term action_id = %q, want %q", termAction, want.Trace.ActionID)
	}
}

func TestSQLiteStoreRejectsCorruptQueryTermMetadataOnRead(t *testing.T) {
	for _, tt := range []struct {
		name   string
		column string
		value  string
		want   string
	}{
		{name: "term type", column: "term_type", value: "other", want: "term_type"},
		{name: "parent term", column: "parent_term", value: "wrong query", want: "parent_term"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
			if err != nil {
				t.Fatalf("NewSQLiteStore: %v", err)
			}
			defer store.Close()
			want := validSQLiteSearchResult()
			ctx := context.Background()
			if err := store.SaveSearchResult(ctx, want); err != nil {
				t.Fatalf("SaveSearchResult: %v", err)
			}
			if _, err := store.db.Exec("UPDATE dci_query_terms SET "+tt.column+" = ? WHERE action_id = ? AND id = (SELECT MIN(id) FROM dci_query_terms WHERE action_id = ?)", tt.value, string(want.Trace.ActionID), string(want.Trace.ActionID)); err != nil {
				t.Fatalf("corrupt %s: %v", tt.column, err)
			}
			_, found, err := store.FindSearchResultByActionID(ctx, want.Trace.ActionID)
			if err == nil || found || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("corrupt %s read found=%v err=%v, want %q error", tt.column, found, err, tt.want)
			}
		})
	}
}

func TestSQLiteStoreSaveUsesPlainInsertAndRollsBackDuplicateChildren(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	first := validSQLiteSearchResult()
	if err := store.SaveSearchResult(ctx, first); err != nil {
		t.Fatalf("first SaveSearchResult: %v", err)
	}
	if err := store.SaveSearchResult(ctx, first); err == nil {
		t.Fatal("duplicate action unexpectedly replaced existing result")
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM dci_search_trace").Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace count after duplicate = %d err=%v", count, err)
	}
	second := validSQLiteSearchResult()
	second.Trace.IdempotencyKey = first.Trace.IdempotencyKey
	if err := store.SaveSearchResult(ctx, second); err == nil {
		t.Fatal("duplicate idempotency key unexpectedly accepted")
	}
	third := validSQLiteSearchResult()
	third.Trace.Steps[0].EventID = first.Trace.Steps[0].EventID
	if err := store.SaveSearchResult(ctx, third); err == nil {
		t.Fatal("duplicate step event unexpectedly accepted")
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM dci_search_trace").Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace count after child duplicate = %d err=%v", count, err)
	}
	fourth := validSQLiteSearchResult()
	fourth.Pack.Evidence[0].EvidenceID = first.Pack.Evidence[0].EvidenceID
	if err := store.SaveSearchResult(ctx, fourth); err == nil {
		t.Fatal("duplicate evidence ID unexpectedly accepted")
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM dci_search_trace").Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace count after evidence duplicate = %d err=%v", count, err)
	}
}

func TestSQLiteStoreSaveSearchTraceOnlyAcceptsCompleteAuthenticatedZeroEvidence(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	trace := domaindci.SearchTrace{
		TraceID:          modulecore.NewTraceID(),
		ActionID:         modulecore.NewActionID(),
		StartedAt:        now,
		EndedAt:          now.Add(time.Second),
		ActorAttribution: domaindci.ActorAttributionAuthenticated,
		ActorKind:        "user",
		ActorID:          "user-1",
		Mode:             "dci",
		UserQuery:        "trace only",
		CorpusScope:      []string{"docs/"},
		Status:           "completed",
	}
	if err := store.SaveSearchTrace(context.Background(), trace); err != nil {
		t.Fatalf("SaveSearchTrace: %v", err)
	}
	result, found, err := store.FindSearchResultByActionID(context.Background(), trace.ActionID)
	if err != nil || !found || len(result.Pack.Evidence) != 0 || result.Trace.FinalEvidenceCount != 0 {
		t.Fatalf("saved trace result found=%v err=%v result=%#v", found, err, result)
	}
	legacy := trace
	legacy.TraceID = modulecore.NewTraceID()
	legacy.ActionID = modulecore.NewActionID()
	legacy.ActorAttribution = domaindci.ActorAttributionLegacyUnattributed
	legacy.ActorKind = ""
	legacy.ActorID = ""
	if err := store.SaveSearchTrace(context.Background(), legacy); err == nil {
		t.Fatal("runtime SaveSearchTrace accepted legacy_unattributed")
	}
	nonzero := trace
	nonzero.TraceID = modulecore.NewTraceID()
	nonzero.ActionID = modulecore.NewActionID()
	nonzero.FinalEvidenceCount = 1
	if err := store.SaveSearchTrace(context.Background(), nonzero); err == nil {
		t.Fatal("SaveSearchTrace accepted nonzero evidence count")
	}
}

func TestSQLiteStoreReadAcceptsOnlyExplicitLegacyUnattributedRows(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	legacy := validSQLiteSearchResult()
	legacy.Trace.ActorAttribution = domaindci.ActorAttributionLegacyUnattributed
	legacy.Trace.ActorKind = ""
	legacy.Trace.ActorID = ""
	legacy.Trace.IdempotencyKey = "legacy-idem"
	insertPersistedTraceRow(t, store, persistedTraceRow{
		actionID:    legacy.Trace.ActionID,
		traceID:     legacy.Trace.TraceID,
		attribution: string(legacy.Trace.ActorAttribution),
		idempotency: legacy.Trace.IdempotencyKey,
		startedAt:   legacy.Trace.StartedAt,
		endedAt:     legacy.Trace.EndedAt,
		mode:        legacy.Trace.Mode,
		query:       legacy.Trace.UserQuery,
		scope:       legacy.Trace.CorpusScope,
		status:      legacy.Trace.Status,
		finalCount:  legacy.Trace.FinalEvidenceCount,
		packIntent:  legacy.Pack.Intent,
		confidence:  legacy.Pack.Confidence,
		limitations: legacy.Pack.Limitations,
	})
	step := legacy.Trace.Steps[0]
	if _, err := store.db.Exec(`
INSERT INTO dci_search_step (
  action_id, step_no, event_id, event_type, tool, command_text, file_path,
  result_count, status, error_message, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(legacy.Trace.ActionID), step.StepNo, string(step.EventID), step.EventType, step.Tool,
		step.CommandText, step.FilePath, step.ResultCount, step.Status, step.ErrorMessage, formatTime(step.CreatedAt)); err != nil {
		t.Fatalf("insert legacy step: %v", err)
	}
	evidence := legacy.Pack.Evidence[0]
	if _, err := store.db.Exec(`
INSERT INTO dci_evidence (
  evidence_id, action_id, created_by_event_id, source_id, file_path, heading,
  line_start, line_end, snippet, reason, confidence, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(evidence.EvidenceID), string(legacy.Trace.ActionID), string(evidence.CreatedByEventID), evidence.SourceID,
		evidence.FilePath, evidence.Heading, evidence.LineStart, evidence.LineEnd, evidence.Snippet, evidence.Reason,
		evidence.Confidence, formatTime(legacy.Trace.EndedAt)); err != nil {
		t.Fatalf("insert legacy evidence: %v", err)
	}
	for _, term := range legacy.Pack.DerivedTerms {
		if _, err := store.db.Exec(`
INSERT INTO dci_query_terms (action_id, term, term_type, parent_term, created_at)
VALUES (?, ?, ?, ?, ?)`, string(legacy.Trace.ActionID), term, "derived", legacy.Pack.Query, formatTime(legacy.Trace.EndedAt)); err != nil {
			t.Fatalf("insert legacy term: %v", err)
		}
	}
	actionID := lastActionID(t, store)
	trace, found, err := store.FindSearchTraceByActionID(ctx, actionID)
	if err != nil || !found || trace.ActorAttribution != domaindci.ActorAttributionLegacyUnattributed || trace.ActorKind != "" || trace.ActorID != "" {
		t.Fatalf("legacy trace found=%v err=%v trace=%#v", found, err, trace)
	}
	full, found, err := store.FindSearchResultByIdempotencyKey(ctx, legacy.Trace.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("legacy full result found=%v err=%v", found, err)
	}
	if full.Trace.ActorAttribution != domaindci.ActorAttributionLegacyUnattributed || full.Trace.ActionID != legacy.Trace.ActionID || !sameStringSlice(full.Pack.CorpusScope, legacy.Pack.CorpusScope) || !sameStringSlice(full.Pack.DerivedTerms, legacy.Pack.DerivedTerms) || full.Pack.Intent != legacy.Pack.Intent || full.Pack.Confidence != legacy.Pack.Confidence || !sameStringSlice(full.Pack.Limitations, legacy.Pack.Limitations) || len(full.Pack.Evidence) != 1 {
		t.Fatalf("legacy full result round trip = %#v", full)
	}
	insertPersistedTraceRow(t, store, persistedTraceRow{
		actionID:    modulecore.NewActionID(),
		traceID:     modulecore.NewTraceID(),
		attribution: string(domaindci.ActorAttributionLegacyUnattributed),
		actorKind:   "agent",
		startedAt:   now,
		endedAt:     now.Add(time.Second),
		mode:        "dci",
		query:       "invalid legacy",
		scope:       []string{"docs/"},
		status:      "completed",
	})
	invalidAction := lastActionID(t, store)
	if _, found, err := store.FindSearchTraceByActionID(ctx, invalidAction); err == nil || found {
		t.Fatalf("one-empty legacy actor accepted: found=%v err=%v", found, err)
	}
}

func TestSQLiteStoreRejectsInvalidExactLookups(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.FindSearchTraceByActionID(ctx, modulecore.ActionID("")); err == nil {
		t.Fatal("blank action lookup accepted")
	}
	if _, _, err := store.FindSearchTraceByActionID(ctx, modulecore.ActionID("act_not-a-uuid")); err == nil {
		t.Fatal("malformed action lookup accepted")
	}
	if _, _, err := store.FindSearchTraceByIdempotencyKey(ctx, " "); err == nil {
		t.Fatal("blank idempotency lookup accepted")
	}
	if _, _, err := store.FindSearchResultByActionID(ctx, modulecore.ActionID("")); err == nil {
		t.Fatal("blank result action lookup accepted")
	}
	if _, _, err := store.FindSearchResultByIdempotencyKey(ctx, " "); err == nil {
		t.Fatal("blank result idempotency lookup accepted")
	}
	if _, _, err := store.FindSearchResultByIdempotencyKey(ctx, " idem "); err == nil {
		t.Fatal("noncanonical result idempotency lookup accepted")
	}
	if _, _, err := store.FindSearchTraceByIdempotencyKey(ctx, " idem "); err == nil {
		t.Fatal("noncanonical trace idempotency lookup accepted")
	}
}

type persistedTraceRow struct {
	actionID    modulecore.ActionID
	traceID     modulecore.TraceID
	attribution string
	actorKind   string
	actorID     string
	idempotency string
	startedAt   time.Time
	endedAt     time.Time
	mode        string
	query       string
	scope       []string
	status      string
	finalCount  int
	packIntent  string
	confidence  float64
	limitations []string
}

func insertPersistedTraceRow(t *testing.T, store *SQLiteStore, row persistedTraceRow) {
	t.Helper()
	scopeJSON, err := marshalStringSlice(row.scope)
	if err != nil {
		t.Fatalf("marshal scope: %v", err)
	}
	limitationsJSON, err := marshalStringSlice(row.limitations)
	if err != nil {
		t.Fatalf("marshal limitations: %v", err)
	}
	_, err = store.db.Exec(`
INSERT INTO dci_search_trace (
  action_id, trace_id, actor_attribution, actor_kind, actor_id, idempotency_key,
  started_at, ended_at, mode, user_query, corpus_scope, status,
  final_evidence_count, error_message, pack_intent, pack_confidence, pack_limitations
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		string(row.actionID), string(row.traceID), row.attribution, row.actorKind, row.actorID,
		row.idempotency, formatTime(row.startedAt), formatTime(row.endedAt), row.mode, row.query, scopeJSON,
		row.status, row.finalCount, row.packIntent, row.confidence, limitationsJSON)
	if err != nil {
		t.Fatalf("insert persisted row: %v", err)
	}
}

func lastActionID(t *testing.T, store *SQLiteStore) modulecore.ActionID {
	t.Helper()
	var raw string
	if err := store.db.QueryRow("SELECT action_id FROM dci_search_trace ORDER BY rowid DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatalf("last action: %v", err)
	}
	return modulecore.ActionID(raw)
}

func tableColumnNames(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info('" + strings.ReplaceAll(table, "'", "''") + "')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func rewriteSQLiteObjectSQL(t *testing.T, path, objectType, name, old, replacement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open schema rewrite db: %v", err)
	}
	defer db.Close()
	var schemaVersion int
	if err := db.QueryRow("PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if _, err := db.Exec("PRAGMA writable_schema = ON"); err != nil {
		t.Fatalf("enable writable_schema: %v", err)
	}
	result, err := db.Exec(`
UPDATE sqlite_master
SET sql = REPLACE(sql, ?, ?)
WHERE type = ? AND name = ?`, old, replacement, objectType, name)
	if err != nil {
		t.Fatalf("rewrite %s %s: %v", objectType, name, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("rewrite %s %s affected=%d err=%v", objectType, name, affected, err)
	}
	if _, err := db.Exec("PRAGMA writable_schema = OFF"); err != nil {
		t.Fatalf("disable writable_schema: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA schema_version = %d", schemaVersion+1)); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
}

func migrationTestResult(t *testing.T, actorAttribution domaindci.ActorAttribution, actorKind, actorID string, offset time.Duration) domaindci.SearchResult {
	t.Helper()
	result := validSQLiteSearchResult()
	result.Trace.TraceID = modulecore.NewTraceID()
	result.Trace.ActionID = modulecore.NewActionID()
	result.Trace.ActorAttribution = actorAttribution
	result.Trace.ActorKind = actorKind
	result.Trace.ActorID = actorID
	result.Trace.IdempotencyKey = ""
	result.Trace.StartedAt = result.Trace.StartedAt.Add(offset)
	result.Trace.EndedAt = result.Trace.EndedAt.Add(offset)
	result.Pack.ActionID = result.Trace.ActionID
	result.Trace.Steps[0].EventID = modulecore.NewEventID()
	result.Trace.Steps[0].CreatedAt = result.Trace.StartedAt
	result.Pack.Evidence[0].EvidenceID = modulecore.NewEvidenceID()
	result.Pack.Evidence[0].CreatedByEventID = modulecore.NewEventID()
	return result
}

func migrationTestRecords(t *testing.T) ([]MigrationRecord, time.Time, time.Time) {
	t.Helper()
	authenticated := migrationTestResult(t, domaindci.ActorAttributionAuthenticated, "agent", "shiro", 0)
	legacy := migrationTestResult(t, domaindci.ActorAttributionLegacyUnattributed, "", "", 2*time.Hour)
	authenticatedAt := time.Date(2026, 8, 31, 1, 2, 3, 456000000, time.FixedZone("JST", 9*60*60))
	legacyAt := time.Date(2026, 8, 31, 4, 5, 6, 789000000, time.FixedZone("legacy", -5*60*60))
	return []MigrationRecord{
		{Result: authenticated, EvidenceCreatedAt: map[modulecore.EvidenceID]time.Time{authenticated.Pack.Evidence[0].EvidenceID: authenticatedAt}},
		{Result: legacy, EvidenceCreatedAt: map[modulecore.EvidenceID]time.Time{legacy.Pack.Evidence[0].EvidenceID: legacyAt}},
	}, authenticatedAt, legacyAt
}

func TestCreateMigrationSnapshotWritesAuthenticatedAndLegacyHistoryWithExactEvidenceTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	records, authenticatedAt, legacyAt := migrationTestRecords(t)
	if err := CreateMigrationSnapshot(context.Background(), path, records); err != nil {
		t.Fatalf("CreateMigrationSnapshot: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat migration snapshot: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("migration snapshot mode=%#o, want 0600", info.Mode().Perm())
	}
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open migration snapshot: %v", err)
	}
	defer store.Close()
	for index, record := range records {
		got, found, err := store.FindSearchResultByActionID(context.Background(), record.Result.Trace.ActionID)
		if err != nil || !found {
			t.Fatalf("record %d read found=%v err=%v", index, found, err)
		}
		if got.Trace.ActorAttribution != record.Result.Trace.ActorAttribution || got.Trace.ActionID != record.Result.Trace.ActionID {
			t.Fatalf("record %d identity = %#v", index, got.Trace)
		}
	}
	for _, expected := range []struct {
		id   modulecore.EvidenceID
		want string
	}{
		{id: records[0].Result.Pack.Evidence[0].EvidenceID, want: formatTime(authenticatedAt)},
		{id: records[1].Result.Pack.Evidence[0].EvidenceID, want: formatTime(legacyAt)},
	} {
		var createdAt string
		if err := store.db.QueryRow("SELECT created_at FROM dci_evidence WHERE evidence_id = ?", string(expected.id)).Scan(&createdAt); err != nil {
			t.Fatalf("evidence %s created_at: %v", expected.id, err)
		}
		if createdAt != expected.want {
			t.Fatalf("evidence %s created_at=%q, want %q", expected.id, createdAt, expected.want)
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("migration snapshot sidecar %s exists or stat failed: %v", suffix, err)
		}
	}
}

func TestCreateMigrationSnapshotCleansTargetAndSidecarsAfterPostCreateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed-migration.db")
	records, _, _ := migrationTestRecords(t)
	originalSync := syncMigrationSnapshotForMigration
	syncMigrationSnapshotForMigration = func(targetPath string) error {
		for _, suffix := range migrationSnapshotSidecarSuffixes() {
			if err := os.WriteFile(targetPath+suffix, []byte("injected sidecar"), 0o600); err != nil {
				return fmt.Errorf("inject sidecar %s: %w", suffix, err)
			}
		}
		return fmt.Errorf("injected migration snapshot sync failure")
	}
	t.Cleanup(func() { syncMigrationSnapshotForMigration = originalSync })

	if err := CreateMigrationSnapshot(context.Background(), path, records); err == nil {
		t.Fatal("CreateMigrationSnapshot unexpectedly accepted injected failure")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed migration target remains or lstat failed: %v", err)
	}
	for _, suffix := range migrationSnapshotSidecarSuffixes() {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("failed migration sidecar %q remains or lstat failed: %v", suffix, err)
		}
	}
}

func cloneMigrationRecords(records []MigrationRecord) []MigrationRecord {
	cloned := make([]MigrationRecord, len(records))
	for index, record := range records {
		cloned[index] = record
		cloned[index].Result.Trace.CorpusScope = append([]string(nil), record.Result.Trace.CorpusScope...)
		cloned[index].Result.Trace.Steps = append([]domaindci.SearchStep(nil), record.Result.Trace.Steps...)
		cloned[index].Result.Pack.CorpusScope = append([]string(nil), record.Result.Pack.CorpusScope...)
		cloned[index].Result.Pack.DerivedTerms = append([]string(nil), record.Result.Pack.DerivedTerms...)
		cloned[index].Result.Pack.Limitations = append([]string(nil), record.Result.Pack.Limitations...)
		cloned[index].Result.Pack.Evidence = append([]domaindci.Evidence(nil), record.Result.Pack.Evidence...)
		cloned[index].EvidenceCreatedAt = make(map[modulecore.EvidenceID]time.Time, len(record.EvidenceCreatedAt))
		for id, timestamp := range record.EvidenceCreatedAt {
			cloned[index].EvidenceCreatedAt[id] = timestamp
		}
	}
	return cloned
}

func TestCreateMigrationSnapshotRejectsInvalidInputBeforeCreatingTarget(t *testing.T) {
	tests := []struct {
		name    string
		prepare func([]MigrationRecord)
	}{
		{name: "stored validation", prepare: func(records []MigrationRecord) {
			records[1].Result.Trace.ActorKind = "agent"
		}},
		{name: "idempotency", prepare: func(records []MigrationRecord) {
			records[0].Result.Trace.IdempotencyKey = "must-be-empty"
		}},
		{name: "missing timestamp", prepare: func(records []MigrationRecord) {
			records[0].EvidenceCreatedAt = map[modulecore.EvidenceID]time.Time{}
		}},
		{name: "extra timestamp", prepare: func(records []MigrationRecord) {
			records[0].EvidenceCreatedAt[modulecore.NewEvidenceID()] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{name: "zero timestamp", prepare: func(records []MigrationRecord) {
			records[0].EvidenceCreatedAt[records[0].Result.Pack.Evidence[0].EvidenceID] = time.Time{}
		}},
		{name: "global duplicate action", prepare: func(records []MigrationRecord) {
			records[1].Result.Trace.ActionID = records[0].Result.Trace.ActionID
			records[1].Result.Pack.ActionID = records[0].Result.Pack.ActionID
		}},
		{name: "global duplicate trace", prepare: func(records []MigrationRecord) {
			records[1].Result.Trace.TraceID = records[0].Result.Trace.TraceID
		}},
		{name: "global duplicate step event", prepare: func(records []MigrationRecord) {
			records[1].Result.Trace.Steps[0].EventID = records[0].Result.Trace.Steps[0].EventID
		}},
		{name: "global duplicate evidence id", prepare: func(records []MigrationRecord) {
			records[1].Result.Pack.Evidence[0].EvidenceID = records[0].Result.Pack.Evidence[0].EvidenceID
			delete(records[1].EvidenceCreatedAt, records[1].Result.Pack.Evidence[0].EvidenceID)
			records[1].EvidenceCreatedAt[records[0].Result.Pack.Evidence[0].EvidenceID] = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		}},
		{name: "global duplicate created event", prepare: func(records []MigrationRecord) {
			records[1].Result.Pack.Evidence[0].CreatedByEventID = records[0].Result.Pack.Evidence[0].CreatedByEventID
		}},
		{name: "cross category duplicate event", prepare: func(records []MigrationRecord) {
			records[1].Result.Pack.Evidence[0].CreatedByEventID = records[0].Result.Trace.Steps[0].EventID
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseRecords, _, _ := migrationTestRecords(t)
			records := cloneMigrationRecords(baseRecords)
			tt.prepare(records)
			path := filepath.Join(t.TempDir(), "rejected.db")
			if err := CreateMigrationSnapshot(context.Background(), path, records); err == nil {
				t.Fatal("CreateMigrationSnapshot unexpectedly accepted invalid input")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("rejected target exists or lstat failed: %v", err)
			}
		})
	}
}

func TestCreateMigrationSnapshotRejectsBlankCanceledExistingAndSymlinkTargetsWithoutMutation(t *testing.T) {
	records, _, _ := migrationTestRecords(t)
	for _, targetPath := range []string{"", "   "} {
		if err := CreateMigrationSnapshot(context.Background(), targetPath, records); err == nil {
			t.Fatalf("blank target %q unexpectedly accepted", targetPath)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	canceledPath := filepath.Join(t.TempDir(), "canceled.db")
	if err := CreateMigrationSnapshot(canceled, canceledPath, records); err == nil {
		t.Fatal("canceled context unexpectedly accepted")
	}
	if _, err := os.Lstat(canceledPath); !os.IsNotExist(err) {
		t.Fatalf("canceled target exists or lstat failed: %v", err)
	}
	nilContextPath := filepath.Join(t.TempDir(), "nil-context.db")
	if err := CreateMigrationSnapshot(nil, nilContextPath, records); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	if _, err := os.Lstat(nilContextPath); !os.IsNotExist(err) {
		t.Fatalf("nil-context target exists or lstat failed: %v", err)
	}
	existingPath := filepath.Join(t.TempDir(), "existing.db")
	before := []byte("preserve-existing-target")
	if err := os.WriteFile(existingPath, before, 0o600); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	if err := CreateMigrationSnapshot(context.Background(), existingPath, records); err == nil {
		t.Fatal("existing target unexpectedly accepted")
	}
	after, err := os.ReadFile(existingPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("existing target changed=%v err=%v", after, err)
	}
	linkDir := t.TempDir()
	linkSource := filepath.Join(linkDir, "source.db")
	linkTarget := filepath.Join(linkDir, "target.db")
	if err := os.WriteFile(linkSource, before, 0o600); err != nil {
		t.Fatalf("write symlink source: %v", err)
	}
	if err := os.Symlink(linkSource, linkTarget); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := CreateMigrationSnapshot(context.Background(), linkTarget, records); err == nil {
		t.Fatal("symlink target unexpectedly accepted")
	}
	after, err = os.ReadFile(linkSource)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("symlink source changed=%v err=%v", after, err)
	}
}
