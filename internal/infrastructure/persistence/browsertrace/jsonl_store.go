package browsertrace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// artifactFilename is the file this store hands to the shared JSONL batch helper for
// APIArtifact records, together with the publication intent file below. The five other
// browsertrace files keep their own owner and their own append/read path, so the batch
// lock and WAL serialize APIArtifact writes without taking ownership of records that
// this package does not supersede.
const artifactFilename = "api_artifact.jsonl"

// artifactIntentFilename is the second file this store registers in the SAME batch
// WAL: the durable publication intent of a newly created APIArtifact, stored as one
// typed core event envelope. It is a separate file because the accepted artifact
// reader decodes every record of api_artifact.jsonl as an APIArtifact, so mixing
// envelopes in would break it. The intent is delivery state for publication replay,
// not a second canonical event store.
const artifactIntentFilename = "api_artifact_publication.jsonl"

// maxArtifactRecordBytes is the per-record ceiling the batch writer already enforces
// when it validates an append payload, so a record longer than this could not have
// been appended through this owner. It is named from the writer's own exported
// contract rather than restated here, and the reader sizes its bounded buffer with
// MaxJSONLReadBufferSize, which is that ceiling plus the one delimiter byte the
// scanner needs to close the largest appended record. A longer line still fails
// closed as bufio.ErrTooLong instead of letting the reader allocate freely.
const (
	maxArtifactRecordBytes  = jsonlbatch.MaxJSONLRecordBytes
	artifactReadBufferBytes = jsonlbatch.MaxJSONLReadBufferSize
)

type JSONLStore struct {
	// batch is the owner-local WAL and OS lock shared by every store instance
	// opened on the same root. It has no mutable in-process state, so it needs
	// no extra mutex: the batch lock is what orders artifact reads and writes.
	batch                *jsonlbatch.Store
	traceRunPath         string
	candidatePath        string
	schemaPath           string
	validationPath       string
	coveragePath         string
	artifactPath         string
	artifactIntentPath   string
	supersessionFactPath string
}

// NewJSONLStore keeps the empty-root default, then opens the APIArtifact file
// through the batch helper, repairs a pending WAL as a writer, and rejects
// persisted artifact records that the domain validator will not accept before
// returning a usable store. A caller that gets an error gets no store back, so
// a broken WAL or corrupt artifact history cannot be silently written over.
func NewJSONLStore(root string) (*JSONLStore, error) {
	if root == "" {
		root = "workspace/logs/browser_trace_to_api"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve browser trace store root: %w", err)
	}
	store := &JSONLStore{
		traceRunPath:         filepath.Join(absRoot, "browser_trace_run.jsonl"),
		candidatePath:        filepath.Join(absRoot, "api_candidate.jsonl"),
		schemaPath:           filepath.Join(absRoot, "api_candidate_schema.jsonl"),
		validationPath:       filepath.Join(absRoot, "api_candidate_validation.jsonl"),
		coveragePath:         filepath.Join(absRoot, "api_coverage_report.jsonl"),
		artifactPath:         filepath.Join(absRoot, artifactFilename),
		artifactIntentPath:   filepath.Join(absRoot, artifactIntentFilename),
		supersessionFactPath: filepath.Join(absRoot, supersessionIntentFilename),
	}
	batch, err := jsonlbatch.New(absRoot, []string{artifactFilename, artifactIntentFilename, supersessionIntentFilename})
	if err != nil {
		return nil, err
	}
	if err := batch.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("recover browser trace artifact wal: %w", err)
	}
	store.batch = batch
	if err := batch.Read(context.Background(), func() error {
		artifacts, err := store.loadAPIArtifacts(context.Background())
		if err != nil {
			return fmt.Errorf("validate browser trace artifact state: %w", err)
		}
		// Stored publication intents are validated the same way: an envelope the
		// canonical contract rejects, or two conflicting intents for one artifact id
		// or event id, leaves the caller without a store instead of a history that a
		// later write would silently build on. The intent file is created by the batch
		// helper above, so its absence here is a broken store, not an empty history.
		intents, err := store.loadAPIArtifactPublicationIntents(context.Background())
		if err != nil {
			return fmt.Errorf("validate browser trace publication intents: %w", err)
		}
		// Both files are read under this one lock, so an intent is checked against the
		// artifact state a reader would resolve: an intent with no artifact row, or one
		// whose immutable references (artifact, task, run, actor, workstream, content
		// role, kind) contradict that row, cannot be delivered and fails closed here.
		// The current body digest and the supersession edge are not compared, because a
		// legitimate update and Supersede change exactly those after the intent was
		// written. Artifact rows that carry no intent are still accepted while this is
		// source preparation: the create caller is not yet switched over.
		if err := validateAPIArtifactPublicationIntentRows(artifacts, intents); err != nil {
			return fmt.Errorf("validate browser trace publication intents: %w", err)
		}
		// Stored supersession facts are validated the same way, and against the same
		// artifact snapshot: a fact whose named row is gone, whose immutable references
		// moved, or whose stored edge is no longer the edge it names leaves the caller
		// without a store instead of a history a later write would build on. The current
		// body digest is not compared, because a legitimate in-place update replaces it
		// after the fact was written. An edge that carries no fact is still accepted while
		// this is source preparation: the plain Supersede path does not mint a fact.
		facts, err := store.loadAPIArtifactSupersessionFacts(context.Background())
		if err != nil {
			return fmt.Errorf("validate browser trace supersession facts: %w", err)
		}
		if err := validateAPIArtifactSupersessionFactRows(artifacts, facts); err != nil {
			return fmt.Errorf("validate browser trace supersession facts: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *JSONLStore) SaveTraceRun(_ context.Context, item domaintrace.TraceRun) error {
	if err := domaintrace.ValidateTraceRun(item); err != nil {
		return err
	}
	return appendJSONL(s.traceRunPath, item)
}

func (s *JSONLStore) ListTraceRuns(_ context.Context, limit int) ([]domaintrace.TraceRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.TraceRun
	if err := readJSONL(s.traceRunPath, func(line []byte) error {
		var item domaintrace.TraceRun
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICandidate(_ context.Context, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	return appendJSONL(s.candidatePath, item)
}

func (s *JSONLStore) ListAPICandidates(_ context.Context, limit int) ([]domaintrace.APICandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidate
	if err := readJSONL(s.candidatePath, func(line []byte) error {
		var item domaintrace.APICandidate
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

// FindAPICandidateByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindAPICandidateByID(_ context.Context, candidateID string) (domaintrace.APICandidate, bool, error) {
	var found domaintrace.APICandidate
	matched := false
	if err := readJSONL(s.candidatePath, func(line []byte) error {
		var item domaintrace.APICandidate
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.CandidateID == candidateID {
			found = item
			matched = true
		}
		return nil
	}); err != nil {
		return domaintrace.APICandidate{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SaveAPICandidateSchema(_ context.Context, item domaintrace.APICandidateSchema) error {
	if err := domaintrace.ValidateAPICandidateSchema(item); err != nil {
		return err
	}
	return appendJSONL(s.schemaPath, item)
}

func (s *JSONLStore) ListAPICandidateSchemas(_ context.Context, limit int) ([]domaintrace.APICandidateSchema, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidateSchema
	if err := readJSONL(s.schemaPath, func(line []byte) error {
		var item domaintrace.APICandidateSchema
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICandidateValidationResult(_ context.Context, item domaintrace.APICandidateValidationResult) error {
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return err
	}
	return appendJSONL(s.validationPath, item)
}

func (s *JSONLStore) ListAPICandidateValidationResults(_ context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidateValidationResult
	if err := readJSONL(s.validationPath, func(line []byte) error {
		var item domaintrace.APICandidateValidationResult
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

// FindAPICandidateValidationResultByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindAPICandidateValidationResultByID(_ context.Context, validationID string) (domaintrace.APICandidateValidationResult, bool, error) {
	var found domaintrace.APICandidateValidationResult
	matched := false
	if err := readJSONL(s.validationPath, func(line []byte) error {
		var item domaintrace.APICandidateValidationResult
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.ValidationID == validationID {
			found = item
			matched = true
		}
		return nil
	}); err != nil {
		return domaintrace.APICandidateValidationResult{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SaveAPICoverageReport(_ context.Context, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	return appendJSONL(s.coveragePath, item)
}

func (s *JSONLStore) ListAPICoverageReports(_ context.Context, limit int) ([]domaintrace.APICoverageReport, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICoverageReport
	if err := readJSONL(s.coveragePath, func(line []byte) error {
		var item domaintrace.APICoverageReport
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

// SaveAPIArtifact validates the artifact through the domain contract and appends
// the whole record as one batch WAL transaction: prepare with the current size and
// prefix digest, append with fsync and post-append digest check, then the commit
// record. A failure before the commit record is rolled back to the prepare offset,
// so an interrupted run never exposes a half-written logical artifact change. An
// unconfirmed commit append is returned as the batch uncertainty error rather than
// being reported as a write that did not happen.
//
// The supersession edge is not an ordinary Save field: the write-side guards run
// inside the batch Write callback, so a rejected Save requests no append at all.
// A rejection means the requested logical mutation did not happen; recovery of a
// WAL left behind by an earlier interrupted run can still change the physical files.
func (s *JSONLStore) SaveAPIArtifact(ctx context.Context, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	line, err := json.Marshal(item)
	if err != nil {
		return err
	}
	payload := append(line, '\n')
	return s.batch.Write(ctx, func() (map[string][]byte, error) {
		if err := s.guardAPIArtifactSave(ctx, item); err != nil {
			return nil, err
		}
		return map[string][]byte{artifactFilename: payload}, nil
	})
}

// guardAPIArtifactSave decides whether an ordinary Save of item may append a record.
// It runs inside the batch Write callback, where the OS write lock is already held,
// so it reads the current logical state through loadAPIArtifacts directly instead of
// nesting batch.Read. A brand new id may not carry a superseded_by at all, because
// nothing has checked that successor's existence, scope or chain, and a stored id may
// not have its edge added, moved or cleared here. A stored id also may not be relocated
// into another task, run, actor, workstream, content role or kind, because the shared
// domain scope check is what an already established edge was verified against. What
// stays allowed is the owner's normal in-place update: body, its digest, title and
// status are appended as given, and the same edge is preserved. There is no second
// scope validator and no global immutability rule here. A current state that cannot be
// read back fails closed instead of being silently overwritten.
func (s *JSONLStore) guardAPIArtifactSave(ctx context.Context, item domaintrace.APIArtifact) error {
	latest, err := s.loadAPIArtifacts(ctx)
	if err != nil {
		return err
	}
	var stored domaintrace.APIArtifact
	var found bool
	for _, candidate := range latest {
		if candidate.ArtifactID == item.ArtifactID {
			stored = candidate
			found = true
		}
	}
	if !found {
		if item.SupersededBy != "" {
			return fmt.Errorf("artifact %s is new with superseded_by %s: only SupersedeAPIArtifact may establish an edge", item.ArtifactID, item.SupersededBy)
		}
		return nil
	}
	if err := domaintrace.ValidateAPIArtifactScope(stored, item); err != nil {
		return fmt.Errorf("save of artifact %s: %w", item.ArtifactID, err)
	}
	if item.SupersededBy != stored.SupersededBy {
		return fmt.Errorf("artifact %s superseded_by %q does not match the stored edge %q: only SupersedeAPIArtifact may add, move or clear that edge", item.ArtifactID, item.SupersededBy, stored.SupersededBy)
	}
	return nil
}

// ListAPIArtifacts reads one committed snapshot under the batch lock and returns
// the latest record per artifact id, newest last occurrence first. Supersede
// appends a successor-carrying record instead of rewriting history, so an id that
// appears several times has to resolve to its final record or a reader would see a
// stale superseded_by edge next to the current one.
func (s *JSONLStore) ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APIArtifact
	if err := s.batch.Read(ctx, func() error {
		loaded, err := s.loadAPIArtifacts(ctx)
		if err != nil {
			return err
		}
		items = loaded
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

// loadAPIArtifacts reads the artifact file directly while the batch lock is already
// held by Read or Write, so it never calls batch.Read again. It keeps only the latest
// record and the last position per artifact id rather than holding the whole appended
// history, and it reads lines with a bounded scanner buffer so a valid body larger than
// 64 KiB still loads while an oversized line fails closed instead of allocating freely.
// The file is created by the batch helper during construction, so its absence here is a
// broken store rather than an empty history and is reported as an error. Every retained
// record has to satisfy the domain contract, including a content hash that reproduces
// its stored body, because an unverifiable row would make the appended history useless
// as evidence.
//
// The returned slice is ordered by last occurrence, oldest first, which is the natural
// append order of the file; callers that present newest-first output reverse it once.
func (s *JSONLStore) loadAPIArtifacts(ctx context.Context) ([]domaintrace.APIArtifact, error) {
	file, err := os.Open(s.artifactPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	latest := make(map[modulecore.ArtifactID]domaintrace.APIArtifact)
	positions := make(map[modulecore.ArtifactID]int, len(latest))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), artifactReadBufferBytes)
	for line := 1; scanner.Scan(); line++ {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		record := bytes.TrimSpace(scanner.Bytes())
		if len(record) == 0 {
			continue
		}
		var item domaintrace.APIArtifact
		if err := json.Unmarshal(record, &item); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", artifactFilename, line, err)
		}
		if err := domaintrace.ValidateAPIArtifact(item); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", artifactFilename, line, err)
		}
		latest[item.ArtifactID] = item
		positions[item.ArtifactID] = line
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Every record the scanner handed over decoded cleanly, but the scanner stops
	// silently at a tail that has no delimiter, so the tail is checked separately
	// below before this state is trusted or an append is placed behind it.
	if err := rejectUnterminatedAPIArtifactRecord(file, artifactFilename); err != nil {
		return nil, err
	}

	items := make([]domaintrace.APIArtifact, 0, len(latest))
	for _, item := range latest {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return positions[items[i].ArtifactID] < positions[items[j].ArtifactID]
	})
	return items, nil
}

// rejectUnterminatedAPIArtifactRecord reports a history whose last byte is not a
// newline, which is what an append torn by an interruption leaves behind. The torn
// record decodes as valid JSON on its own, so a reader that only walks records accepts
// it, yet the next append has no delimiter to start after and fuses the torn record
// with the new one, losing both. Only the final byte decides this, so a single byte
// read at the end of the file is enough and stays bounded. Nothing is repaired here:
// unjournaled data that cannot be trusted is reported, not rewritten, and an empty
// file has no record to leave unterminated.
func rejectUnterminatedAPIArtifactRecord(file *os.File, filename string) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filename, err)
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
		return fmt.Errorf("read the last byte of %s: %w", filename, err)
	}
	if last[0] != '\n' {
		return fmt.Errorf("%s ends inside a record: its last record is not newline terminated, so appending would concatenate it with the next record", filename)
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// SupersedeAPIArtifact establishes the edge saying that an existing APIArtifact was
// replaced by an existing successor, and it is the only JSONL operation allowed to
// create that edge. The current state load, the existence and scope checks, the walk
// over the whole successor chain and the single append of the updated predecessor all
// happen inside one batch Write, under the same OS lock that every other artifact
// writer takes, so no reader or second writer can observe a half-applied edge. The
// loader used for the checks is the latest record per id that was loaded under that
// lock, so a chain check reads what a reader would read.
//
// Only predecessor.SupersededBy changes: identity, content role, body, digest, title,
// status and created_at are carried over unchanged, and the successor record is never
// rewritten. A repeat of an edge that is already stored asks for an empty append set,
// so idempotence adds nothing to the history. A missing, undecodable or invalid row, a
// scope mismatch on task, run, actor, workstream, content role or kind, a cycle, and an
// already stored different successor are all returned as errors that append nothing.
func (s *JSONLStore) SupersedeAPIArtifact(ctx context.Context, predecessorID, successorID modulecore.ArtifactID) error {
	if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
		return err
	}
	return s.batch.Write(ctx, func() (map[string][]byte, error) {
		latest, err := s.loadAPIArtifacts(ctx)
		if err != nil {
			return nil, err
		}
		rows := make(map[modulecore.ArtifactID]domaintrace.APIArtifact, len(latest))
		for _, item := range latest {
			rows[item.ArtifactID] = item
		}
		load := func(_ context.Context, id modulecore.ArtifactID) (domaintrace.APIArtifact, error) {
			item, found := rows[id]
			if !found {
				return domaintrace.APIArtifact{}, fmt.Errorf("artifact %s is not in %s", id, artifactFilename)
			}
			return item, nil
		}
		predecessor, err := load(ctx, predecessorID)
		if err != nil {
			return nil, err
		}
		successor, err := load(ctx, successorID)
		if err != nil {
			return nil, err
		}
		// The predecessor and the successor are two distinct artifacts that have to
		// agree on task, run, actor, workstream, content role and stored kind, and every
		// row below the successor has to stay verifiable inside that same scope, because
		// the new edge joins that chain.
		if err := domaintrace.ValidateAPIArtifactSupersessionPair(predecessor, successor); err != nil {
			return nil, err
		}
		if err := verifyAPIArtifactSuccessorChain(ctx, load, predecessorID, successor); err != nil {
			return nil, err
		}

		switch {
		case predecessor.SupersededBy == successorID:
			// The edge is already stored, so the repeat asks for no append at all.
			return nil, nil
		case predecessor.SupersededBy != "":
			return nil, fmt.Errorf("artifact %s is already superseded by %s, not %s", predecessorID, predecessor.SupersededBy, successorID)
		}

		updated := predecessor
		updated.SupersededBy = successorID
		if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
			return nil, fmt.Errorf("supersede of artifact %s leaves an invalid row: %w", predecessorID, err)
		}
		line, err := json.Marshal(updated)
		if err != nil {
			return nil, fmt.Errorf("encode superseded payload for artifact %s: %w", predecessorID, err)
		}
		return map[string][]byte{artifactFilename: append(line, '\n')}, nil
	})
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func reverseLimit[T any](items []T, limit int) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}
