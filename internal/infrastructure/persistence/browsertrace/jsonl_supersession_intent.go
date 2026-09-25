package browsertrace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// supersessionIntentFilename is the third file this store registers in the SAME batch
// WAL: the canonical supersession envelope of an established edge, stored as one typed
// core event envelope. It is a separate file because the accepted artifact reader decodes
// every record of api_artifact.jsonl as an APIArtifact, and the creation intent file
// decodes every one of its records as a creation intent. The fact is delivery state for
// publication replay, not a second canonical event store.
const supersessionIntentFilename = "api_artifact_supersession.jsonl"

// validateAPIArtifactSupersessionFactRows checks every stored supersession fact against
// the artifact rows a reader resolves right now: both rows the fact names have to exist,
// the references that cannot move over an artifact's life have to agree with the
// predecessor, and the edge stored on the predecessor has to be the edge this fact is.
// The shared domain row check owns those rules, so the loader, the list path and the
// supersede path cannot drift apart.
//
// What is deliberately not compared is either row's current content hash: a legitimate
// in-place update replaces the body and digest of the predecessor after its supersession
// fact was written, and comparing them would turn accepted history into a load failure.
func validateAPIArtifactSupersessionFactRows(artifacts []domaintrace.APIArtifact, facts []modulecore.EventEnvelope) error {
	rows := make(map[modulecore.ArtifactID]domaintrace.APIArtifact, len(artifacts))
	for _, item := range artifacts {
		rows[item.ArtifactID] = item
	}
	for _, fact := range facts {
		predecessor, found := rows[fact.ArtifactID]
		if !found {
			return fmt.Errorf("supersession fact %s is bound to artifact %s, which has no record in %s", fact.EventID, fact.ArtifactID, artifactFilename)
		}
		successorID := modulecore.ArtifactID(fact.Payload[domaintrace.APIArtifactSupersessionPayloadSupersededBy].(string))
		successor, found := rows[successorID]
		if !found {
			return fmt.Errorf("supersession fact %s points at successor artifact %s, which has no record in %s", fact.EventID, successorID, artifactFilename)
		}
		if err := domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, predecessor, successor); err != nil {
			return err
		}
	}
	return nil
}

// loadAPIArtifactSupersessionFacts reads the supersession fact file while the batch lock
// is already held by Read or Write, so it never nests batch.Read. Every record is decoded
// as the typed core event envelope it is and has to satisfy the persisted-fact contract,
// which reuses the canonical envelope validation instead of a second copy of it. A missing
// file is a broken store, not an empty history, and the reader stays bounded: the scanner
// buffer is the writer's own record ceiling plus the one delimiter byte, so a record
// larger than the writer can append fails closed instead of allocating freely, and a tail
// without a delimiter is reported rather than fused with the next append.
//
// The last record per predecessor artifact id is kept, but not by last-row-wins: a second
// record for the same predecessor that is not identical to the stored fact, and an event id
// that shows up as the fact of two different predecessors, are conflicting history and are
// returned as errors, because one supersession owns one event id. A supersession fact is
// immutable history, so stored facts are validated on their own and never re-compared
// against a predecessor row whose digest a later legitimate update replaced.
//
// The result is ordered by predecessor artifact id ascending, which is the keyset order
// ListAPIArtifactSupersessionFacts pages through.
func (s *JSONLStore) loadAPIArtifactSupersessionFacts(ctx context.Context) ([]modulecore.EventEnvelope, error) {
	file, err := os.Open(s.supersessionFactPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	latest := make(map[modulecore.ArtifactID]modulecore.EventEnvelope)
	bindings := make(map[modulecore.EventID]modulecore.ArtifactID)
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
		var fact modulecore.EventEnvelope
		if err := json.Unmarshal(record, &fact); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", supersessionIntentFilename, line, err)
		}
		if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", supersessionIntentFilename, line, err)
		}
		if stored, found := latest[fact.ArtifactID]; found && !reflect.DeepEqual(stored, fact) {
			return nil, fmt.Errorf("%s line %d: artifact %s carries a supersession fact %s that conflicts with the stored fact %s", supersessionIntentFilename, line, fact.ArtifactID, fact.EventID, stored.EventID)
		}
		if owner, found := bindings[fact.EventID]; found && owner != fact.ArtifactID {
			return nil, fmt.Errorf("%s line %d: supersession fact %s is bound to both artifact %s and %s", supersessionIntentFilename, line, fact.EventID, owner, fact.ArtifactID)
		}
		latest[fact.ArtifactID] = fact
		bindings[fact.EventID] = fact.ArtifactID
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := rejectUnterminatedAPIArtifactRecord(file, supersessionIntentFilename); err != nil {
		return nil, err
	}

	ids := make([]modulecore.ArtifactID, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	items := make([]modulecore.EventEnvelope, 0, len(ids))
	for _, id := range ids {
		items = append(items, latest[id])
	}
	return items, nil
}

// ListAPIArtifactSupersessionFacts returns one bounded page of stored supersession facts
// in ascending predecessor artifact id order, strictly after the after cursor, so a caller
// drains every fact by feeding the last id of a page back in until a page comes back empty.
// The page size bounds a single call only and is clamped to the same page ceiling the
// creation intent owner uses: nothing here makes an older source event unobservable.
//
// The page is read under the same batch lock as every artifact write, so it is one
// committed snapshot, and every fact is resolved against both rows it names before it is
// returned: a fact pointing at a missing row, at an edge that is no longer the edge it
// names, or at references that moved fails closed instead of being replayed. Envelopes are
// returned as copies and the lock is released when this returns, because the caller does
// the canonical event store I/O outside it.
func (s *JSONLStore) ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	if limit <= 0 {
		limit = defaultPublicationIntentPageLimit
	}
	if limit > maxPublicationIntentPageLimit {
		limit = maxPublicationIntentPageLimit
	}
	if after != "" {
		if err := after.Validate(); err != nil {
			return nil, fmt.Errorf("supersession fact page cursor %s: %w", after, err)
		}
	}
	var page []modulecore.EventEnvelope
	if err := s.batch.Read(ctx, func() error {
		artifacts, err := s.loadAPIArtifacts(ctx)
		if err != nil {
			return err
		}
		facts, err := s.loadAPIArtifactSupersessionFacts(ctx)
		if err != nil {
			return err
		}
		if err := validateAPIArtifactSupersessionFactRows(artifacts, facts); err != nil {
			return err
		}
		page = make([]modulecore.EventEnvelope, 0, limit)
		for _, fact := range facts {
			if err := contextErr(ctx); err != nil {
				return err
			}
			if fact.ArtifactID <= after {
				continue
			}
			page = append(page, copyAPIArtifactPublicationIntent(fact))
			if len(page) == limit {
				break
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return page, nil
}

// SupersedeAPIArtifactWithPublicationIntent establishes the edge saying that an existing
// APIArtifact was replaced by an existing successor and, in the SAME batch WAL write,
// stores the canonical supersession envelope that publication replays: the updated
// predecessor goes to api_artifact.jsonl and the typed envelope to
// api_artifact_supersession.jsonl, so an interruption leaves either both records or
// neither. Without that coupling a crash after the edge append would leave an edge whose
// event envelope was never durably saved, and reading the edge back cannot recover an
// EventID that was not stored.
//
// This owner mints no event and infers no provenance: the caller owns canonical v7 EventID
// issuance, the trace and the verified actor, and the domain contract only refuses a fact
// whose digests are not the bytes being superseded, whose successor is not the row it
// names, or whose predecessor already carries an edge. Inside the write lock the decision
// is made against the state a reader would resolve, with the same outcomes as
// SupersedeAPIArtifactWithPublicationIntent on SQLite:
//
//   - No edge and no fact: the predecessor record is updated and the fact record is
//     appended, which is the only path that mints a supersession fact into history.
//   - The stored fact is exactly the given envelope and the edge it names is stored: the
//     append set is empty, so a rerun after an unconfirmed commit cannot produce a second
//     event for one supersession. That no-op is reported only after both rows the fact
//     names still resolve and still carry the references that cannot move.
//
// Anything else appends nothing: a different stored fact, an event id another predecessor
// already owns, a stored fact whose edge is not there, and an edge that exists while its
// fact does not. The last case is not repaired here, because an edge established by the
// plain SupersedeAPIArtifact carries a fact this call did not mint and cannot re-derive, so
// backfilling it is its own explicit migration operation. An ordinary Save may not add,
// move or clear an edge and touches only api_artifact.jsonl, so neither an update nor the
// plain supersede can overwrite a stored supersession fact.
func (s *JSONLStore) SupersedeAPIArtifactWithPublicationIntent(ctx context.Context, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) error {
	if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
		return err
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return err
	}
	factRecord, err := json.Marshal(fact)
	if err != nil {
		return err
	}
	return s.batch.Write(ctx, func() (map[string][]byte, error) {
		// Both files are loaded directly under the lock the batch helper already holds;
		// batch.Read is not nested here.
		storedArtifacts, err := s.loadAPIArtifacts(ctx)
		if err != nil {
			return nil, err
		}
		storedFacts, err := s.loadAPIArtifactSupersessionFacts(ctx)
		if err != nil {
			return nil, err
		}
		rows := make(map[modulecore.ArtifactID]domaintrace.APIArtifact, len(storedArtifacts))
		for _, item := range storedArtifacts {
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
		if err := domaintrace.ValidateAPIArtifactSupersessionPair(predecessor, successor); err != nil {
			return nil, err
		}
		if err := verifyAPIArtifactSuccessorChain(ctx, load, predecessorID, successor); err != nil {
			return nil, err
		}

		var storedFact modulecore.EventEnvelope
		storedFactFound := false
		for _, candidate := range storedFacts {
			if candidate.ArtifactID == predecessorID {
				storedFact, storedFactFound = candidate, true
				continue
			}
			if candidate.EventID == fact.EventID {
				return nil, fmt.Errorf("supersession fact %s is already the fact of artifact %s, so artifact %s cannot claim the same event", fact.EventID, candidate.ArtifactID, predecessorID)
			}
		}

		if storedFactFound {
			if !reflect.DeepEqual(storedFact, fact) {
				return nil, fmt.Errorf("artifact %s already carries supersession fact %s, so supersession fact %s would be a second event for one supersession", predecessorID, storedFact.EventID, fact.EventID)
			}
			if predecessor.SupersededBy != successorID {
				return nil, fmt.Errorf("supersession fact %s is stored for artifact %s, which stores superseded_by %q, so superseding it again is not a no-op", fact.EventID, predecessorID, predecessor.SupersededBy)
			}
			// References a later legitimate body update replaced are exactly what the
			// shared row check leaves alone: the digests in this fact are history.
			if err := domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, predecessor, successor); err != nil {
				return nil, err
			}
			return nil, nil
		}
		switch {
		case predecessor.SupersededBy == successorID:
			return nil, fmt.Errorf("artifact %s is stored with superseded_by %s but no supersession fact, so the edge predates SupersedeAPIArtifactWithPublicationIntent and its fact has to be recorded by an explicit migration repair, not by this supersede", predecessorID, successorID)
		case predecessor.SupersededBy != "":
			return nil, fmt.Errorf("artifact %s is already superseded by %s, not %s", predecessorID, predecessor.SupersededBy, successorID)
		}

		// The create-only contract is checked against the rows this write resolved, so the
		// two digests the fact carries are the bytes actually being superseded and replaced.
		if err := domaintrace.ValidateAPIArtifactSupersessionFact(predecessor, successor, fact); err != nil {
			return nil, err
		}
		updated := predecessor
		updated.SupersededBy = successorID
		if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
			return nil, fmt.Errorf("supersede of artifact %s leaves an invalid row: %w", predecessorID, err)
		}
		record, err := json.Marshal(updated)
		if err != nil {
			return nil, fmt.Errorf("encode superseded payload for artifact %s: %w", predecessorID, err)
		}
		return map[string][]byte{
			artifactFilename:           append(record, '\n'),
			supersessionIntentFilename: append(factRecord, '\n'),
		}, nil
	})
}
