package runmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	browser "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	knowledge "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	super "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	bp "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	ep "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	kp "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	sp "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type databaseInput struct {
	role, dir, path string
	db              *sql.DB
	tables          map[string][]string
}

func (d *databaseInput) close() {
	if d.db != nil {
		_ = d.db.Close()
	}
	_ = os.RemoveAll(d.dir)
}
func openDB(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return sql.Open("sqlite", u.String()+"?_pragma=busy_timeout%3d5000")
}
func quoted(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func newOwnerDB(role, path string) error {
	switch role {
	case "events":
		s, e := ep.NewSQLiteStore(path)
		if e != nil {
			return e
		}
		return s.Close()
	case "superagent":
		s, e := sp.NewSQLiteStore(path, 0)
		if e != nil {
			return e
		}
		return s.Close()
	case "browser":
		s, e := bp.NewSQLiteStore(path)
		if e != nil {
			return e
		}
		return s.Close()
	case "knowledge":
		s, e := kp.NewSQLiteStore(path)
		if e != nil {
			return e
		}
		return s.Close()
	}
	return errors.New("unknown database role")
}

func databaseTables(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	rows, e := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if e != nil {
		return nil, e
	}
	var names []string
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			_ = rows.Close()
			return nil, e
		}
		names = append(names, s)
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		return nil, e
	}
	_ = rows.Close()
	out := map[string][]string{}
	for _, name := range names {
		rows, e := db.QueryContext(ctx, "PRAGMA table_info("+quoted(name)+")")
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id, notnull, pk int
			var col, typ string
			var def any
			if e = rows.Scan(&id, &col, &typ, &notnull, &def, &pk); e != nil {
				_ = rows.Close()
				return nil, e
			}
			out[name] = append(out[name], col)
		}
		if e = rows.Err(); e != nil {
			_ = rows.Close()
			return nil, e
		}
		_ = rows.Close()
	}
	return out, nil
}

func (c *cohort) readDatabases(ctx context.Context, input map[string][]byte) (out []*databaseInput, err error) {
	var created []*databaseInput
	defer func() {
		if err != nil {
			for _, d := range created {
				d.close()
			}
		}
	}()
	for _, role := range []string{"superagent", "browser", "knowledge", "events"} {
		p := c.inventory.Roles[role]
		if p == "" {
			continue
		}
		dir, e := os.MkdirTemp("", "rencrow-run-migration-")
		if e != nil {
			return nil, e
		}
		d := &databaseInput{role: role, dir: dir, path: filepath.Join(dir, "output.sqlite")}
		out = append(out, d)
		created = append(created, d)
		if e = os.WriteFile(d.path, input[p], 0600); e != nil {
			return nil, e
		}
		d.db, e = openDB(d.path)
		if e != nil {
			return nil, e
		}
		var check string
		if e = d.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); e != nil || check != "ok" {
			return nil, errors.New("source SQLite integrity failed")
		}
		tables, e := databaseTables(ctx, d.db)
		if e != nil {
			return nil, e
		}
		d.tables = tables
		baseline := filepath.Join(dir, "schema.sqlite")
		if e = newOwnerDB(role, baseline); e != nil {
			return nil, e
		}
		expectedDB, e := openDB(baseline)
		if e != nil {
			return nil, e
		}
		expected, e := databaseTables(ctx, expectedDB)
		_ = expectedDB.Close()
		if e != nil {
			return nil, e
		}
		for table, cols := range tables {
			want, ok := expected[table]
			if !ok {
				return nil, fmt.Errorf("unknown %s table", role)
			}
			actual := append([]string(nil), cols...)
			if role == "superagent" && table == "subagent_task" {
				for i, col := range actual {
					if col == "subagent_id" {
						actual[i] = "task_id"
					}
					if col == "parent_run_id" {
						actual[i] = "run_id"
					}
				}
			}
			if role == "superagent" && table == "run_queue" {
				filtered := actual[:0]
				for _, col := range actual {
					if col != "checkpoint_id" {
						filtered = append(filtered, col)
					}
				}
				actual = filtered
			}
			if !reflect.DeepEqual(actual, want) {
				return nil, fmt.Errorf("unsupported %s %s columns", role, table)
			}
		}
		var custom int
		query := "SELECT count(*) FROM sqlite_master WHERE type IN ('trigger','view')"
		if role == "events" {
			query += " AND name NOT IN ('event_envelope_append_only_update','event_envelope_append_only_delete','event_dependency_append_only_update','event_dependency_append_only_delete')"
		}
		if e = d.db.QueryRowContext(ctx, query).Scan(&custom); e != nil || custom != 0 {
			return nil, errors.New("unsupported SQLite executable schema")
		}
	}
	return out, nil
}

func payloadRows(ctx context.Context, d *databaseInput, table string) ([]map[string]json.RawMessage, error) {
	if _, ok := d.tables[table]; !ok {
		return nil, nil
	}
	cols := d.tables[table]
	names := make([]string, len(cols))
	for i, col := range cols {
		names[i] = quoted(col)
	}
	rows, e := d.db.QueryContext(ctx, "SELECT "+strings.Join(names, ",")+" FROM "+quoted(table)+" ORDER BY rowid")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []map[string]json.RawMessage
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return nil, e
		}
		var raw []byte
		for i, col := range cols {
			if col == "payload" {
				switch v := values[i].(type) {
				case string:
					raw = []byte(v)
				case []byte:
					raw = v
				default:
					return nil, errors.New("payload must be text")
				}
			}
		}
		var m map[string]json.RawMessage
		if e = strictJSON(raw, &m); e != nil {
			return nil, e
		}
		for i, col := range cols {
			if col == "payload" {
				continue
			}
			indexed := values[i]
			if bytes, ok := indexed.([]byte); ok {
				indexed = string(bytes)
			}
			var scalar any
			if raw, ok := m[col]; ok {
				if e = json.Unmarshal(raw, &scalar); e != nil {
					return nil, e
				}
			}
			if indexed == nil || indexed == "" {
				if scalar == nil || scalar == "" {
					continue
				}
				// These secondary indexes were absent in legacy snapshots while
				// the owner payload retained the explicit Run reference. Rebuild
				// only an absent index; resolution against the cohort is still
				// mandatory, and populated disagreements remain fatal.
				if d.role == "superagent" && ((table == "context_pack" && col == "run_id") || (table == "subagent_task" && col == "parent_run_id")) {
					if reference, ok := scalar.(string); ok && reference != "" {
						continue
					}
				}
			}
			if indexed != scalar {
				return nil, errors.New("source index and payload disagree")
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (c *cohort) discoverDatabase(ctx context.Context, d *databaseInput) error {
	table := ""
	switch d.role {
	case "superagent":
		table = "agent_run"
	case "browser":
		table = "browser_trace_run"
	case "knowledge":
		table = "dream_consolidation_run"
	case "events":
		return nil
	}
	rows, e := payloadRows(ctx, d, table)
	if e != nil {
		return e
	}
	for _, m := range rows {
		if d.role == "superagent" {
			if e := closeLegacyRecoveryBlock(m, c.inventory.SnapshotAt); e != nil {
				return e
			}
		}
		field := "run_id"
		value := textField(m, field)
		if value == "" && textField(m, "trace_run_id") != "" {
			field = "trace_run_id"
			value = textField(m, field)
		}
		start := timeField(m, "started_at")
		if start.IsZero() {
			start = timeField(m, "created_at")
		}
		end := timeField(m, "completed_at")
		status := textField(m, "status")
		// A captured trace/proposal is not proof that its execution succeeded. If
		// canonical history is absent, bind an explicitly interrupted historical Run.
		if d.role == "browser" || d.role == "knowledge" {
			status = "interrupted"
			end = c.inventory.SnapshotAt
		}
		if _, e = c.bind(d.role, field, value, textField(m, "task_id"), textField(m, "actor_id"), status, start, end); e != nil {
			return e
		}
	}
	if d.role == "superagent" {
		children, e := payloadRows(ctx, d, "subagent_task")
		if e != nil {
			return e
		}
		for _, m := range children {
			legacy := textField(m, "subagent_id")
			if legacy == "" {
				continue
			}
			if e := restoreLegacyChildActor(m); e != nil {
				return e
			}
			parent, e := c.resolve("superagent", "run_id", textField(m, "parent_run_id"))
			if e != nil {
				return e
			}
			id, e := c.bind("superagent", "subagent_id", legacy, "", textField(m, "actor_id"), textField(m, "status"), timeField(m, "created_at"), timeField(m, "completed_at"))
			if e != nil {
				return e
			}
			r := c.runs[id]
			t := c.tasks[r.TaskID]
			t.ParentTaskID = parent.TaskID
			c.tasks[t.TaskID] = t
		}
	}
	return nil
}

func projectionType(role, table string) any {
	if role == "superagent" {
		switch table {
		case "agent_run":
			return &super.AgentRun{}
		case "subagent_task":
			return &super.SubagentTask{}
		case "context_pack":
			return &super.ContextPack{}
		case "message_channel":
			return &super.MessageChannel{}
		case "run_queue":
			return &super.RunQueueItem{}
		}
	}
	if role == "browser" {
		switch table {
		case "browser_trace_run":
			return &browser.TraceRun{}
		case "api_candidate":
			return &browser.APICandidate{}
		case "api_candidate_schema":
			return &browser.APICandidateSchema{}
		case "api_candidate_validation":
			return &browser.APICandidateValidationResult{}
		case "api_coverage_report":
			return &browser.APICoverageReport{}
		case "api_artifact":
			return &browser.APIArtifact{}
		}
	}
	if role == "knowledge" {
		switch table {
		case "personal_archive":
			return &knowledge.PersonalArchiveEntry{}
		case "creative_knowledge":
			return &knowledge.CreativeKnowledgeItem{}
		case "news_knowledge":
			return &knowledge.NewsKnowledgeItem{}
		case "daily_intake_rule":
			return &knowledge.DailyIntakeRule{}
		case "temporal_memory_marker":
			return &knowledge.TemporalMemoryMarker{}
		case "dream_consolidation_run":
			return &knowledge.DreamConsolidationRun{}
		}
	}
	return nil
}

func (c *cohort) transformDatabase(ctx context.Context, d *databaseInput) ([]byte, error) {
	if d.role == "events" {
		return c.transformEvents(ctx, d)
	}
	checkpoints, err := c.prepareAgentRunCheckpoints(ctx, d)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(d.tables))
	for table := range d.tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		typ := projectionType(d.role, table)
		if typ == nil {
			continue
		}
		rows, e := payloadRows(ctx, d, table)
		if e != nil {
			return nil, e
		}
		for _, m := range rows {
			oldID := textField(m, d.tables[table][0])
			if oldID == "" {
				return nil, errors.New("projection primary identity missing")
			}
			if d.role == "superagent" && table == "agent_run" {
				if e := closeLegacyRecoveryBlock(m, c.inventory.SnapshotAt); e != nil {
					return nil, e
				}
				injectAgentRunCheckpoint(m, checkpoints, c)
			}
			if d.role == "superagent" && table == "run_queue" {
				if e := c.restoreLegacyQueueReference(m); e != nil {
					return nil, e
				}
			}
			if legacy := textField(m, "subagent_id"); legacy != "" {
				if e := restoreLegacyChildActor(m); e != nil {
					return nil, e
				}
				r, e := c.resolve("superagent", "subagent_id", legacy)
				if e != nil {
					return nil, e
				}
				setField(m, "task_id", r.TaskID)
				setField(m, "run_id", r.RunID)
				delete(m, "subagent_id")
				delete(m, "parent_run_id")
			} else if textField(m, "parent_run_id") != "" {
				return nil, errors.New("orphan legacy parent Run reference")
			} else if textField(m, "run_id") != "" || textField(m, "trace_run_id") != "" {
				if e = c.rewriteIdentity(m, d.role); e != nil {
					return nil, e
				}
			}
			if _, ok := m["agent_type"]; ok {
				if textField(m, "actor_id") == "" {
					return nil, errors.New("historical attribution is missing")
				}
				delete(m, "agent_type")
			}
			// Preserve the historical detail, but a stopped snapshot cannot
			// advertise an executable AgentRun after its owner Run was closed.
			if d.role == "superagent" && (table == "agent_run" || table == "subagent_task") && textField(m, "status") == "running" {
				r := c.runs[core.RunID(textField(m, "run_id"))]
				if r.Status != "interrupted" || r.CompletedAt == nil {
					return nil, errors.New("running projection disagrees with owner Run")
				}
				setField(m, "status", "interrupted")
				setField(m, "completed_at", *r.CompletedAt)
			}
			if table == "run_queue" {
				status := textField(m, "status")
				if status != "completed" && status != "failed" && status != "cancelled" && status != "blocked" {
					return nil, errors.New("active queue requires owner drain before migration")
				}
			}
			raw, _ := json.Marshal(m)
			typ = projectionType(d.role, table)
			if e = strictJSON(raw, typ); e != nil {
				return nil, e
			}
			if e = validateProjection(typ); e != nil {
				return nil, fmt.Errorf("%s projection rejected by owner: %w", table, e)
			}
			cols := d.tables[table]
			sets := []string{"payload = ?"}
			args := []any{string(raw)}
			for _, col := range cols {
				if col == "payload" {
					continue
				}
				field := col
				if table == "subagent_task" && col == "subagent_id" {
					field = "task_id"
				}
				if table == "subagent_task" && col == "parent_run_id" {
					field = "run_id"
				}
				if v, ok := m[field]; ok {
					var scalar any
					if e = json.Unmarshal(v, &scalar); e != nil {
						return nil, e
					}
					if scalar == nil {
						continue
					}
					sets = append(sets, quoted(col)+" = ?")
					args = append(args, scalar)
				}
			}
			args = append(args, oldID)
			result, e := d.db.ExecContext(ctx, "UPDATE "+quoted(table)+" SET "+strings.Join(sets, ",")+" WHERE "+quoted(cols[0])+" = ?", args...)
			if e != nil {
				return nil, e
			}
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				return nil, errors.New("projection primary key mismatch")
			}
			c.counts[d.role+"."+table]++
		}
	}
	if d.role == "superagent" {
		for _, col := range d.tables["subagent_task"] {
			if col == "subagent_id" {
				if _, e := d.db.ExecContext(ctx, "ALTER TABLE subagent_task RENAME COLUMN subagent_id TO task_id"); e != nil {
					return nil, e
				}
			}
			if col == "parent_run_id" {
				if _, e := d.db.ExecContext(ctx, "ALTER TABLE subagent_task RENAME COLUMN parent_run_id TO run_id"); e != nil {
					return nil, e
				}
			}
		}
		for _, col := range d.tables["run_queue"] {
			if col == "checkpoint_id" {
				if _, e := d.db.ExecContext(ctx, "ALTER TABLE run_queue DROP COLUMN checkpoint_id"); e != nil {
					return nil, e
				}
			}
		}
	}
	if e := d.db.Close(); e != nil {
		return nil, e
	}
	d.db = nil
	// Owner creates only missing canonical schema objects in this fresh copy.
	if e := newOwnerDB(d.role, d.path); e != nil {
		return nil, e
	}
	return os.ReadFile(d.path)
}

func validateProjection(value any) error {
	switch v := value.(type) {
	case *super.AgentRun:
		return super.ValidateAgentRun(*v)
	case *super.SubagentTask:
		return super.ValidateSubagentTask(*v)
	case *super.ContextPack:
		return super.ValidateContextPack(*v, 0)
	case *super.MessageChannel:
		return super.ValidateMessageChannel(*v)
	case *super.RunQueueItem:
		return super.ValidateRunQueueItem(*v)
	case *browser.TraceRun:
		return browser.ValidateTraceRun(*v)
	case *browser.APICandidate:
		return browser.ValidateAPICandidate(*v)
	case *browser.APICandidateSchema:
		return browser.ValidateAPICandidateSchema(*v)
	case *browser.APICandidateValidationResult:
		return browser.ValidateAPICandidateValidationResult(*v)
	case *browser.APICoverageReport:
		return browser.ValidateAPICoverageReport(*v)
	case *browser.APIArtifact:
		return browser.ValidateAPIArtifact(*v)
	case *knowledge.PersonalArchiveEntry:
		return knowledge.ValidatePersonalArchiveEntry(*v)
	case *knowledge.CreativeKnowledgeItem:
		return knowledge.ValidateCreativeKnowledgeItem(*v)
	case *knowledge.NewsKnowledgeItem:
		return knowledge.ValidateNewsKnowledgeItem(*v)
	case *knowledge.DailyIntakeRule:
		return knowledge.ValidateDailyIntakeRule(*v)
	case *knowledge.TemporalMemoryMarker:
		return knowledge.ValidateTemporalMemoryMarker(*v)
	case *knowledge.DreamConsolidationRun:
		return knowledge.ValidateDreamConsolidationRun(*v)
	}
	return errors.New("unknown projection validator")
}

func (c *cohort) transformEvents(ctx context.Context, d *databaseInput) ([]byte, error) {
	source, e := ep.NewSQLiteStore(d.path)
	if e != nil {
		return nil, e
	}
	defer source.Close()
	rows, e := d.db.QueryContext(ctx, "SELECT event_id, envelope_json FROM event_envelope ORDER BY event_seq")
	if e != nil {
		return nil, e
	}
	var events []core.EventEnvelope
	var ids []core.EventID
	rawEnvelopes := map[core.EventID][]byte{}
	for rows.Next() {
		var id core.EventID
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		var checked map[string]json.RawMessage
		if e = strictJSON(raw, &checked); e != nil {
			_ = rows.Close()
			return nil, e
		}
		rawEnvelopes[id] = raw
		ids = append(ids, id)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	expectedDependencies := map[string]bool{}
	for _, id := range ids {
		original, found, e := source.GetByID(ctx, id)
		if e != nil || !found {
			return nil, errors.New("source Event index and envelope disagree")
		}
		if original.CausationEventID != "" {
			expectedDependencies[string(id)+"\x00"+string(original.CausationEventID)+"\x00causation"] = true
		}
		for _, dep := range original.DependencyEventIDs {
			expectedDependencies[string(id)+"\x00"+string(dep)+"\x00dependency"] = true
		}
		var event core.EventEnvelope
		if e = strictJSON(rawEnvelopes[id], &event); e != nil {
			_ = rows.Close()
			return nil, e
		}
		if e = c.bindEventIdentity(&event); e != nil {
			return nil, fmt.Errorf("Event %s identity: %w", id, e)
		}
		events = append(events, event)
	}
	dependencyRows, e := d.db.QueryContext(ctx, "SELECT event_id,dependency_event_id,relation_type FROM event_dependency")
	if e != nil {
		return nil, e
	}
	for dependencyRows.Next() {
		var a, b, kind string
		if e = dependencyRows.Scan(&a, &b, &kind); e != nil {
			_ = dependencyRows.Close()
			return nil, e
		}
		key := a + "\x00" + b + "\x00" + kind
		if !expectedDependencies[key] {
			_ = dependencyRows.Close()
			return nil, errors.New("source Event dependency mismatch")
		}
		delete(expectedDependencies, key)
	}
	e = dependencyRows.Err()
	_ = dependencyRows.Close()
	if e != nil {
		return nil, e
	}
	if len(expectedDependencies) != 0 {
		return nil, errors.New("source Event dependency missing")
	}
	target := filepath.Join(d.dir, "events-new.sqlite")
	store, e := ep.NewSQLiteStore(target)
	if e != nil {
		return nil, e
	}
	defer store.Close()
	if e = store.AppendBatch(ctx, events); e != nil {
		return nil, e
	}
	if e = store.Close(); e != nil {
		return nil, e
	}
	c.counts["events"] = len(events)
	return os.ReadFile(target)
}
