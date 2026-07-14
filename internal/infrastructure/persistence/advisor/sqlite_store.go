package advisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/advisor.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("advisor sqlite store is closed")
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS advisor_run (run_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS advisor_adoption (adoption_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS advisor_score_snapshot (snapshot_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS agent_policy_decision (decision_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SaveAdviceRun(ctx context.Context, item advisorDomain.AdviceRunRecord) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.save(ctx, "advisor_run", "run_id", item.RunID, item.FinishedAt, item)
}

func (s *SQLiteStore) ListAdviceRuns(ctx context.Context, limit int) ([]advisorDomain.AdviceRunRecord, error) {
	return listSQLite[advisorDomain.AdviceRunRecord](ctx, s, "advisor_run", limit)
}

func (s *SQLiteStore) SaveAdvisorAdoption(ctx context.Context, item advisorDomain.AdvisorAdoptionRecord) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.save(ctx, "advisor_adoption", "adoption_id", item.AdoptionID, item.CreatedAt, item)
}

func (s *SQLiteStore) ListAdvisorAdoptions(ctx context.Context, limit int) ([]advisorDomain.AdvisorAdoptionRecord, error) {
	return listSQLite[advisorDomain.AdvisorAdoptionRecord](ctx, s, "advisor_adoption", limit)
}

func (s *SQLiteStore) SaveAdvisorScoreSnapshot(ctx context.Context, item advisorDomain.AdvisorScoreSnapshot) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.save(ctx, "advisor_score_snapshot", "snapshot_id", item.SnapshotID, item.CreatedAt, item)
}

func (s *SQLiteStore) ListAdvisorScoreSnapshots(ctx context.Context, limit int) ([]advisorDomain.AdvisorScoreSnapshot, error) {
	return listSQLite[advisorDomain.AdvisorScoreSnapshot](ctx, s, "advisor_score_snapshot", limit)
}

func (s *SQLiteStore) SaveAgentPolicyDecision(ctx context.Context, item domainagentprofile.PolicyDecision) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.save(ctx, "agent_policy_decision", "decision_id", item.DecisionID, item.CreatedAt, item)
}

func (s *SQLiteStore) ListAgentPolicyDecisions(ctx context.Context, limit int) ([]domainagentprofile.PolicyDecision, error) {
	return listSQLite[domainagentprofile.PolicyDecision](ctx, s, "agent_policy_decision", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table, idColumn, id string, createdAt time.Time, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("advisor sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(
		"INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)", table, idColumn,
	), id, createdAt.UTC().Format(time.RFC3339Nano), string(payload))
	return err
}

func listSQLite[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("advisor sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT payload FROM %s ORDER BY created_at DESC, rowid DESC LIMIT ?", table), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
