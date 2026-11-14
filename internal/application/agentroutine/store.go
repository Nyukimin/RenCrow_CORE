package agentroutine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrConflict          = errors.New("agent routine revision or payload conflict")
	ErrPendingIntent     = errors.New("agent already has a pending routine intent")
	ErrIntentNotFound    = errors.New("pending routine intent not found")
	ErrChoiceNotEligible = errors.New("initiator choice is not eligible for 3:1 accounting")
	ErrNotFound          = errors.New("agent routine record not found")
	ErrInvalid           = errors.New("invalid agent routine value")
	ErrInsecurePath      = errors.New("agent routine database path is not private")
)

const sqliteBusyTimeoutMilliseconds = 5000

type Store struct {
	db *sql.DB
}

// Open creates the bounded SQLite store. State is actor-scoped; activity and diary
// payloads are always read with both actor and JST date keys.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("open agent routine store: %w: path is required", ErrInvalid)
	}
	if err := prepareSQLitePath(path); err != nil {
		return nil, err
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_time_format=sqlite", path, separator, sqliteBusyTimeoutMilliseconds)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open agent routine sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize agent routine sqlite: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("agent routine store is closed")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_routine_state (
			actor_id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS agent_routine_schedule (
			actor_id TEXT NOT NULL,
			date TEXT NOT NULL,
			sleep_at TEXT NOT NULL,
			wake_at TEXT NOT NULL,
			PRIMARY KEY (actor_id, date)
		)`,
		`CREATE TABLE IF NOT EXISTS agent_routine_activity (
			actor_id TEXT NOT NULL,
			date TEXT NOT NULL,
			activity_key TEXT NOT NULL,
			payload TEXT NOT NULL,
			PRIMARY KEY (actor_id, date, activity_key)
		)`,
		`CREATE TABLE IF NOT EXISTS agent_routine_diary (
			actor_id TEXT NOT NULL,
			date TEXT NOT NULL,
			payload TEXT NOT NULL,
			PRIMARY KEY (actor_id, date)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func prepareSQLitePath(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil
	}
	if strings.HasPrefix(path, "file:") {
		return fmt.Errorf("SQLite URI paths are not supported; provide a file path: %w", ErrInvalid)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create agent routine database directory: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if createErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				return fmt.Errorf("close new agent routine database file: %w", closeErr)
			}
			return nil
		}
		if !errors.Is(createErr, os.ErrExist) {
			return fmt.Errorf("create private agent routine database file: %w", createErr)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect agent routine database path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agent routine database must be a regular file: %w", ErrInsecurePath)
	}
	// Windows permission bits do not represent the inherited ACL. On POSIX, refuse
	// a broadly accessible existing file without changing its mode or ownership.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("existing database has group or world permissions %04o: %w", info.Mode().Perm(), ErrInsecurePath)
	}
	return nil
}

// Load reads only one actor's checkpoint. A missing actor has revision zero and is
// ready for its first compare-and-swap save.
func (s *Store) Load(ctx context.Context, actorID string) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if err := s.ensureOpen(); err != nil {
		return State{}, err
	}
	var revision int64
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT revision, payload FROM agent_routine_state WHERE actor_id = ?`, actorID).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return State{ActorID: actorID}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("load actor routine state: %w", err)
	}
	var state State
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		return State{}, fmt.Errorf("decode actor routine state for %q: %w", actorID, err)
	}
	if state.ActorID != actorID {
		return State{}, fmt.Errorf("actor routine payload key mismatch for %q", actorID)
	}
	state.Revision = revision
	state.Pending = cloneIntent(state.Pending)
	state.PausedDiary = cloneIntent(state.PausedDiary)
	return state, nil
}

// Save replaces one actor checkpoint using its expected revision. Revision zero
// creates a state row; stale writes return ErrConflict.
func (s *Store) Save(ctx context.Context, state State, expectedRevision int64) (State, error) {
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if expectedRevision < 0 {
		return State{}, fmt.Errorf("expected revision: %w", ErrInvalid)
	}
	if err := s.ensureOpen(); err != nil {
		return State{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, fmt.Errorf("begin actor routine save: %w", err)
	}
	defer tx.Rollback()
	saved, err := saveStateTx(ctx, tx, state, expectedRevision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit actor routine save: %w", err)
	}
	return saved, nil
}

// EnsureSchedule durably creates the actor/date schedule once and makes it the
// actor's current schedule checkpoint. Older date schedules remain queryable only
// by exact key in SQLite; this method never scans the schedule table.
func (s *Store) EnsureSchedule(ctx context.Context, actorID, date string) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if !validDate(date) {
		return State{}, fmt.Errorf("schedule date %q: %w", date, ErrInvalid)
	}
	if err := s.ensureOpen(); err != nil {
		return State{}, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		state, err := s.Load(ctx, actorID)
		if err != nil {
			return State{}, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return State{}, fmt.Errorf("begin agent routine schedule ensure: %w", err)
		}
		sleepAt, wakeAt, err := ScheduleFor(actorID, date)
		if err != nil {
			_ = tx.Rollback()
			return State{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_routine_schedule(actor_id,date,sleep_at,wake_at) VALUES(?,?,?,?)`,
			actorID, date, sleepAt.Format(time.RFC3339Nano), wakeAt.Format(time.RFC3339Nano))
		if err != nil {
			_ = tx.Rollback()
			return State{}, fmt.Errorf("persist actor routine schedule: %w", err)
		}
		var sleepText, wakeText string
		err = tx.QueryRowContext(ctx, `SELECT sleep_at,wake_at FROM agent_routine_schedule WHERE actor_id=? AND date=?`, actorID, date).Scan(&sleepText, &wakeText)
		if err != nil {
			_ = tx.Rollback()
			return State{}, fmt.Errorf("read actor routine schedule: %w", err)
		}
		persistedSleep, err := time.Parse(time.RFC3339Nano, sleepText)
		if err != nil {
			_ = tx.Rollback()
			return State{}, fmt.Errorf("decode actor routine sleep schedule: %w", err)
		}
		persistedWake, err := time.Parse(time.RFC3339Nano, wakeText)
		if err != nil {
			_ = tx.Rollback()
			return State{}, fmt.Errorf("decode actor routine wake schedule: %w", err)
		}
		if state.ScheduleDate == date && state.PlannedSleep.Equal(persistedSleep) && state.PlannedWake.Equal(persistedWake) {
			if err := tx.Commit(); err != nil {
				return State{}, fmt.Errorf("commit actor routine schedule read: %w", err)
			}
			return state, nil
		}
		state.ScheduleDate = date
		state.PlannedSleep = persistedSleep.In(jstLocation)
		state.PlannedWake = persistedWake.In(jstLocation)
		state.LogicalSleepUntil = time.Time{}
		saved, err := saveStateTx(ctx, tx, state, state.Revision)
		if err != nil {
			_ = tx.Rollback()
			if errors.Is(err, ErrConflict) {
				continue
			}
			return State{}, err
		}
		if err := tx.Commit(); err != nil {
			return State{}, fmt.Errorf("commit actor routine schedule: %w", err)
		}
		return saved, nil
	}
	return State{}, fmt.Errorf("ensure actor routine schedule: %w", ErrConflict)
}

// CommitInitiatorSelection advances the 3:1 index at most once for a matching
// eligible pending intent and only when the caller confirms both alternatives.
func (s *Store) CommitInitiatorSelection(ctx context.Context, actorID, intentID string, expectedRevision int64, bothValid bool) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(intentID) == "" {
		return State{}, fmt.Errorf("intent ID: %w", ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadStateTx(ctx, tx, actorID)
	if err != nil {
		return State{}, err
	}
	if state.Pending != nil && state.Pending.ID == intentID && state.Pending.ChoiceCommitted {
		if err := tx.Commit(); err != nil {
			return State{}, fmt.Errorf("commit idempotent 3:1 selection read: %w", err)
		}
		return state, nil
	}
	if state.Revision != expectedRevision {
		return State{}, ErrConflict
	}
	if state.Pending == nil || state.Pending.ID != intentID {
		return State{}, ErrIntentNotFound
	}
	if !bothValid || !state.Pending.ChoiceEligible ||
		(state.Pending.Kind != ActivityMorningChat && state.Pending.Kind != ActivityHobby) {
		return State{}, ErrChoiceNotEligible
	}
	if state.ChoiceIndex == ^uint64(0) {
		return State{}, fmt.Errorf("3:1 choice index overflow: %w", ErrInvalid)
	}
	state.ChoiceIndex++
	state.Pending.ChoiceCommitted = true
	saved, err := saveStateTx(ctx, tx, state, expectedRevision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit 3:1 selection: %w", err)
	}
	return saved, nil
}

// DraftIntent reserves the actor's sole pending routine intent before any external
// Task creation. TaskID may be supplied now or persisted with LinkTask before run creation.
func (s *Store) DraftIntent(ctx context.Context, intent Intent) (State, error) {
	if err := validateIntent(intent); err != nil {
		return State{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadStateTx(ctx, tx, intent.ActorID)
	if err != nil {
		return State{}, err
	}
	if state.Pending != nil {
		if sameIntentDraft(*state.Pending, intent) {
			if err := tx.Commit(); err != nil {
				return State{}, fmt.Errorf("commit idempotent intent draft read: %w", err)
			}
			return state, nil
		}
		return State{}, ErrPendingIntent
	}
	if state.PausedDiary != nil && intent.Kind != ActivitySleep && intent.Kind != ActivityWake {
		return State{}, ErrPendingIntent
	}
	state.Pending = cloneIntent(&intent)
	saved, err := saveStateTx(ctx, tx, state, state.Revision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit routine intent draft: %w", err)
	}
	return saved, nil
}

// LinkTask persists the caller-owned canonical TaskID in the intent. Call this
// before CreateAndStartRun so recovery can look up that exact task.
func (s *Store) LinkTask(ctx context.Context, actorID, intentID, taskID string, expectedRevision int64) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(intentID) == "" || strings.TrimSpace(taskID) == "" {
		return State{}, fmt.Errorf("intent ID and TaskID are required: %w", ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadStateTx(ctx, tx, actorID)
	if err != nil {
		return State{}, err
	}
	if state.Pending == nil || state.Pending.ID != intentID {
		return State{}, ErrIntentNotFound
	}
	if state.Pending.TaskID == taskID {
		if err := tx.Commit(); err != nil {
			return State{}, fmt.Errorf("commit idempotent TaskID link read: %w", err)
		}
		return state, nil
	}
	if state.Pending.TaskID != "" {
		return State{}, ErrConflict
	}
	if state.Revision != expectedRevision {
		return State{}, ErrConflict
	}
	state.Pending.TaskID = taskID
	saved, err := saveStateTx(ctx, tx, state, expectedRevision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit routine TaskID link: %w", err)
	}
	return saved, nil
}

// Checkpoint persists the current routine step and its result for safe task resume.
func (s *Store) Checkpoint(ctx context.Context, actorID, intentID, step, output string, expectedRevision int64) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(intentID) == "" {
		return State{}, fmt.Errorf("intent ID: %w", ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadStateTx(ctx, tx, actorID)
	if err != nil {
		return State{}, err
	}
	if state.Pending == nil || state.Pending.ID != intentID {
		return State{}, ErrIntentNotFound
	}
	if state.Pending.Step == step && state.Pending.Output == output {
		if err := tx.Commit(); err != nil {
			return State{}, fmt.Errorf("commit idempotent routine checkpoint read: %w", err)
		}
		return state, nil
	}
	if state.Revision != expectedRevision {
		return State{}, ErrConflict
	}
	state.Pending.Step = step
	state.Pending.Output = output
	saved, err := saveStateTx(ctx, tx, state, expectedRevision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit routine checkpoint: %w", err)
	}
	return saved, nil
}

// FinishIntent clears the matching pending activity and stores its bounded retry time.
func (s *Store) FinishIntent(ctx context.Context, actorID, intentID string, expectedRevision int64, retryAfter time.Time) (State, error) {
	if err := validateActorID(actorID); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(intentID) == "" {
		return State{}, fmt.Errorf("intent ID: %w", ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadStateTx(ctx, tx, actorID)
	if err != nil {
		return State{}, err
	}
	if state.Pending == nil && state.LastFinishedIntentID == intentID {
		if err := tx.Commit(); err != nil {
			return State{}, fmt.Errorf("commit idempotent routine finish read: %w", err)
		}
		return state, nil
	}
	if state.Pending == nil || state.Pending.ID != intentID {
		return State{}, ErrIntentNotFound
	}
	if state.Revision != expectedRevision {
		return State{}, ErrConflict
	}
	finishedKind := state.Pending.Kind
	state.Pending = nil
	if finishedKind == ActivityWake && state.PausedDiary != nil {
		state.Pending = cloneIntent(state.PausedDiary)
		state.PausedDiary = nil
	}
	state.LastFinishedIntentID = intentID
	state.CooldownUntil = retryAfter
	saved, err := saveStateTx(ctx, tx, state, expectedRevision)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit routine finish: %w", err)
	}
	return saved, nil
}

// SaveFact writes one actor/date fact with caller-supplied evidence. Repeated writes
// under the same key are idempotent only when the evidence payload is identical.
func (s *Store) SaveFact(ctx context.Context, fact Activity) (Activity, error) {
	if err := validateActivity(fact); err != nil {
		return Activity{}, err
	}
	if err := s.ensureOpen(); err != nil {
		return Activity{}, err
	}
	payload, err := json.Marshal(fact)
	if err != nil {
		return Activity{}, fmt.Errorf("encode activity fact: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Activity{}, fmt.Errorf("begin activity fact save: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_routine_activity(actor_id,date,activity_key,payload) VALUES(?,?,?,?)`,
		fact.ActorID, fact.Date, fact.Key, string(payload))
	if err != nil {
		return Activity{}, fmt.Errorf("save activity fact: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Activity{}, fmt.Errorf("check activity fact insert: %w", err)
	}
	if rows == 0 {
		var existingPayload string
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM agent_routine_activity WHERE actor_id=? AND date=? AND activity_key=?`, fact.ActorID, fact.Date, fact.Key).Scan(&existingPayload); err != nil {
			return Activity{}, fmt.Errorf("read duplicate activity fact: %w", err)
		}
		if existingPayload != string(payload) {
			return Activity{}, ErrConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return Activity{}, fmt.Errorf("commit activity fact: %w", err)
	}
	return fact, nil
}

// ListFacts reads a single actor/date partition in deterministic chronological order.
func (s *Store) ListFacts(ctx context.Context, actorID, date string) ([]Activity, error) {
	return s.ListFactsInDateRange(ctx, actorID, date, date)
}

// ListFactsInDateRange reads one actor's inclusive date partitions in chronological order.
func (s *Store) ListFactsInDateRange(ctx context.Context, actorID, fromDate, throughDate string) ([]Activity, error) {
	if err := validateActorID(actorID); err != nil {
		return nil, err
	}
	if !validDate(fromDate) || !validDate(throughDate) || fromDate > throughDate {
		return nil, fmt.Errorf("activity date range %q..%q: %w", fromDate, throughDate, ErrInvalid)
	}
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM agent_routine_activity WHERE actor_id=? AND date>=? AND date<=? ORDER BY date,activity_key`,
		actorID, fromDate, throughDate)
	if err != nil {
		return nil, fmt.Errorf("list activity facts in date range: %w", err)
	}
	defer rows.Close()
	facts := make([]Activity, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan activity fact in date range: %w", err)
		}
		var fact Activity
		if err := json.Unmarshal([]byte(payload), &fact); err != nil {
			return nil, fmt.Errorf("decode activity fact in date range: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activity facts in date range: %w", err)
	}
	sort.Slice(facts, func(i, j int) bool {
		left, right := facts[i], facts[j]
		if left.Date != right.Date {
			return left.Date < right.Date
		}
		if left.At.IsZero() != right.At.IsZero() {
			return !left.At.IsZero()
		}
		if !left.At.Equal(right.At) {
			return left.At.Before(right.At)
		}
		return left.Key < right.Key
	})
	return facts, nil
}

// GetDiary reads one actor/date draft and its latest BOARD receipt.
func (s *Store) GetDiary(ctx context.Context, actorID, date string) (Diary, error) {
	if err := validateDiaryKey(actorID, date); err != nil {
		return Diary{}, err
	}
	if err := s.ensureOpen(); err != nil {
		return Diary{}, err
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM agent_routine_diary WHERE actor_id=? AND date=?`, actorID, date).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Diary{}, ErrNotFound
	}
	if err != nil {
		return Diary{}, fmt.Errorf("get actor diary: %w", err)
	}
	var diary Diary
	if err := json.Unmarshal([]byte(payload), &diary); err != nil {
		return Diary{}, fmt.Errorf("decode actor diary: %w", err)
	}
	return diary, nil
}

// SaveDiary creates or advances one date's text revision, or persists a receipt
// without changing that revision. Identical retries retain the same PostingKey.
func (s *Store) SaveDiary(ctx context.Context, diary Diary) (Diary, error) {
	if err := validateDiaryKey(diary.ActorID, diary.Date); err != nil {
		return Diary{}, err
	}
	if strings.TrimSpace(diary.Body) == "" {
		return Diary{}, fmt.Errorf("diary body: %w", ErrInvalid)
	}
	if err := s.ensureOpen(); err != nil {
		return Diary{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Diary{}, fmt.Errorf("begin diary save: %w", err)
	}
	defer tx.Rollback()
	var previousPayload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM agent_routine_diary WHERE actor_id=? AND date=?`, diary.ActorID, diary.Date).Scan(&previousPayload)
	if errors.Is(err, sql.ErrNoRows) {
		if diary.Revision > 1 || diary.Revision < 0 {
			return Diary{}, ErrConflict
		}
		if diary.Revision == 0 {
			diary.Revision = 1
		}
		diary.PostingKey = stablePostingKey(diary.ActorID, diary.Date, diary.Revision, diary.PostingKey)
		if diary.UpdatedAt.IsZero() {
			diary.UpdatedAt = time.Now().UTC()
		}
		payload, err := json.Marshal(diary)
		if err != nil {
			return Diary{}, fmt.Errorf("encode diary: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_routine_diary(actor_id,date,payload) VALUES(?,?,?)`, diary.ActorID, diary.Date, string(payload)); err != nil {
			return Diary{}, fmt.Errorf("insert actor diary: %w", err)
		}
	} else if err != nil {
		return Diary{}, fmt.Errorf("load diary before save: %w", err)
	} else {
		var previous Diary
		if err := json.Unmarshal([]byte(previousPayload), &previous); err != nil {
			return Diary{}, fmt.Errorf("decode prior diary: %w", err)
		}
		sameText := diary.Body == previous.Body && diary.Reflection == previous.Reflection
		if sameText {
			if diary.Revision != 0 && diary.Revision != previous.Revision {
				return Diary{}, ErrConflict
			}
			if diary.PostingKey != "" && diary.PostingKey != previous.PostingKey {
				return Diary{}, ErrConflict
			}
			if diary.Receipt != "" {
				previous.Receipt = diary.Receipt
			}
			if diary.ReceiptRevision != "" {
				previous.ReceiptRevision = diary.ReceiptRevision
			}
			if diary.Receipt != "" || diary.ReceiptRevision != "" {
				previous.UpdatedAt = diary.UpdatedAt
				if previous.UpdatedAt.IsZero() {
					previous.UpdatedAt = time.Now().UTC()
				}
				payload, err := json.Marshal(previous)
				if err != nil {
					return Diary{}, fmt.Errorf("encode diary receipt update: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE agent_routine_diary SET payload=? WHERE actor_id=? AND date=?`, string(payload), diary.ActorID, diary.Date); err != nil {
					return Diary{}, fmt.Errorf("update actor diary receipt: %w", err)
				}
			}
			diary = previous
		} else {
			if diary.Revision != previous.Revision+1 || diary.Revision < 1 {
				return Diary{}, ErrConflict
			}
			diary.PostingKey = stablePostingKey(diary.ActorID, diary.Date, diary.Revision, diary.PostingKey)
			if diary.PostingKey == previous.PostingKey {
				return Diary{}, ErrConflict
			}
			diary.Receipt = ""
			diary.ReceiptRevision = ""
			if diary.UpdatedAt.IsZero() {
				diary.UpdatedAt = time.Now().UTC()
			}
			payload, err := json.Marshal(diary)
			if err != nil {
				return Diary{}, fmt.Errorf("encode diary revision: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_routine_diary SET payload=? WHERE actor_id=? AND date=?`, string(payload), diary.ActorID, diary.Date); err != nil {
				return Diary{}, fmt.Errorf("update actor diary revision: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Diary{}, fmt.Errorf("commit actor diary: %w", err)
	}
	return diary, nil
}

func (s *Store) begin(ctx context.Context) (*sql.Tx, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin actor routine transaction: %w", err)
	}
	return tx, nil
}

func (s *Store) ensureOpen() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("agent routine store is closed")
	}
	return nil
}

func loadStateTx(ctx context.Context, tx *sql.Tx, actorID string) (State, error) {
	var revision int64
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT revision,payload FROM agent_routine_state WHERE actor_id=?`, actorID).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return State{ActorID: actorID}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("load actor routine state in transaction: %w", err)
	}
	var state State
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		return State{}, fmt.Errorf("decode actor routine state in transaction: %w", err)
	}
	if state.ActorID != actorID {
		return State{}, fmt.Errorf("actor routine state key mismatch for %q", actorID)
	}
	state.Revision = revision
	state.Pending = cloneIntent(state.Pending)
	state.PausedDiary = cloneIntent(state.PausedDiary)
	return state, nil
}

func saveStateTx(ctx context.Context, tx *sql.Tx, state State, expectedRevision int64) (State, error) {
	state.Revision = expectedRevision + 1
	payload, err := json.Marshal(state)
	if err != nil {
		return State{}, fmt.Errorf("encode actor routine state: %w", err)
	}
	if expectedRevision == 0 {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_routine_state(actor_id,revision,payload) VALUES(?,?,?)`, state.ActorID, state.Revision, string(payload))
		if err != nil {
			return State{}, fmt.Errorf("insert actor routine state: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return State{}, fmt.Errorf("check actor routine state insert: %w", err)
		}
		if rows != 1 {
			return State{}, ErrConflict
		}
		return state, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_routine_state SET revision=?,payload=? WHERE actor_id=? AND revision=?`, state.Revision, string(payload), state.ActorID, expectedRevision)
	if err != nil {
		return State{}, fmt.Errorf("update actor routine state: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return State{}, fmt.Errorf("check actor routine state update: %w", err)
	}
	if rows != 1 {
		return State{}, ErrConflict
	}
	return state, nil
}

func validateState(state State) error {
	if err := validateActorID(state.ActorID); err != nil {
		return err
	}
	if state.Revision < 0 {
		return fmt.Errorf("state revision: %w", ErrInvalid)
	}
	for label, date := range map[string]string{"activated on": state.ActivatedOn, "schedule date": state.ScheduleDate, "next diary date": state.NextDiaryDate} {
		if date != "" && !validDate(date) {
			return fmt.Errorf("%s %q: %w", label, date, ErrInvalid)
		}
	}
	if state.ScheduleDate != "" && !validSchedule(state.ScheduleDate, state.PlannedSleep, state.PlannedWake) {
		return fmt.Errorf("planned sleep/wake schedule is invalid: %w", ErrInvalid)
	}
	if state.Pending != nil {
		if err := validateIntent(*state.Pending); err != nil {
			return err
		}
		if state.Pending.ActorID != state.ActorID {
			return fmt.Errorf("pending intent actor does not match state actor: %w", ErrInvalid)
		}
	}
	if state.PausedDiary != nil {
		if err := validateIntent(*state.PausedDiary); err != nil {
			return fmt.Errorf("paused diary intent: %w", err)
		}
		if state.PausedDiary.Kind != ActivityDiary || state.PausedDiary.ActorID != state.ActorID ||
			(state.Pending != nil && (state.Pending.Kind != ActivitySleep && state.Pending.Kind != ActivityWake || state.Pending.ID == state.PausedDiary.ID)) {
			return fmt.Errorf("paused diary does not match the actor's sleep/wake boundary: %w", ErrInvalid)
		}
	}
	return nil
}

func validateIntent(intent Intent) error {
	if strings.TrimSpace(intent.ID) == "" || strings.TrimSpace(intent.ActorID) == "" || !validDate(intent.Date) || !isRoutineIntentKind(intent.Kind) {
		return fmt.Errorf("routine intent identity, date, or kind: %w", ErrInvalid)
	}
	if intent.ChoiceEligible && intent.Kind != ActivityMorningChat && intent.Kind != ActivityHobby {
		return fmt.Errorf("choice-eligible intent kind %q: %w", intent.Kind, ErrInvalid)
	}
	return nil
}

func isRoutineIntentKind(kind ActivityKind) bool {
	switch kind {
	case ActivitySleep, ActivityWake, ActivityMorningChat, ActivityHobby, ActivityRelax, ActivityDiary:
		return true
	default:
		return false
	}
}

func validateActivity(fact Activity) error {
	if strings.TrimSpace(fact.ActorID) == "" || !validDate(fact.Date) || strings.TrimSpace(fact.Key) == "" || strings.TrimSpace(fact.Source) == "" || !isRecordedActivityKind(fact.Kind) {
		return fmt.Errorf("activity actor/date/key/source/kind: %w", ErrInvalid)
	}
	if !fact.At.IsZero() && fact.At.In(jstLocation).Format(dateLayout) != fact.Date {
		return fmt.Errorf("activity timestamp is outside JST date %q: %w", fact.Date, ErrInvalid)
	}
	return nil
}

func isRecordedActivityKind(kind ActivityKind) bool {
	switch kind {
	case ActivitySleep, ActivityWake, ActivityMorningChat, ActivityHobby, ActivityRelax, ActivityDiary, ActivityWork, ActivityConversation:
		return true
	default:
		return false
	}
}

func validateDiaryKey(actorID, date string) error {
	if strings.TrimSpace(actorID) == "" || !validDate(date) {
		return fmt.Errorf("diary actor/date: %w", ErrInvalid)
	}
	return nil
}

func validateActorID(actorID string) error {
	if strings.TrimSpace(actorID) == "" {
		return fmt.Errorf("actor ID: %w", ErrInvalid)
	}
	return nil
}

func stablePostingKey(actorID, date string, revision int64, proposed string) string {
	if proposed != "" {
		return proposed
	}
	return fmt.Sprintf("agent-diary:%s:%s:%d", actorID, date, revision)
}

func sameIntentDraft(existing, proposed Intent) bool {
	if existing.ID != proposed.ID || existing.ActorID != proposed.ActorID || existing.Date != proposed.Date ||
		existing.Kind != proposed.Kind || existing.Label != proposed.Label || existing.ChoiceEligible != proposed.ChoiceEligible ||
		existing.Digest != proposed.Digest || existing.Participants == nil != (proposed.Participants == nil) || len(existing.Participants) != len(proposed.Participants) {
		return false
	}
	for i := range existing.Participants {
		if existing.Participants[i] != proposed.Participants[i] {
			return false
		}
	}
	return true
}

func cloneIntent(intent *Intent) *Intent {
	if intent == nil {
		return nil
	}
	copy := *intent
	copy.Participants = cloneStrings(intent.Participants)
	return &copy
}
