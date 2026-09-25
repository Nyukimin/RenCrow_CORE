package browsertrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// supersessionIntentTable is the browsertrace-owned table that keeps the canonical
// supersession envelope of an established edge next to the predecessor row whose edge it
// is. Like the creation intent table it is delivery state for publication replay, not a
// second canonical event store, and it is written in the same BEGIN IMMEDIATE transaction
// as the edge UPDATE, so an interruption leaves either both rows or neither.
//
// It is keyed by the predecessor artifact id, which is the artifact the fact is about: an
// established edge cannot be moved or cleared, so one predecessor carries at most one
// supersession fact, and the UNIQUE event_id is what stops a second event id from claiming
// the same supersession. Keying by the successor instead would collide with the successor's
// own history and would not describe the row that actually changed.
const supersessionIntentTable = "api_artifact_supersession_intent"

// decodeAPIArtifactSupersessionIntentRecord turns one stored row into the typed envelope it
// claims to be. The payload is the truth and the two columns are indexes of it, so a column
// that disagrees with the payload is reported rather than trusted: a replay that looked one
// id up and read the other would otherwise act on two different supersessions. The shared
// domain validator owns the envelope contract, so this path adds no rules of its own and
// reuses the canonical envelope validation instead of a second copy of it.
func decodeAPIArtifactSupersessionIntentRecord(rawPayload, artifactIDColumn, eventIDColumn string) (modulecore.EventEnvelope, error) {
	var fact modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(rawPayload), &fact); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s payload is corrupt: %w", supersessionIntentTable, err)
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if string(fact.ArtifactID) != artifactIDColumn {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s artifact_id column %s disagrees with payload artifact_id %s", supersessionIntentTable, artifactIDColumn, fact.ArtifactID)
	}
	if string(fact.EventID) != eventIDColumn {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s event_id column %s disagrees with payload event_id %s", supersessionIntentTable, artifactIDColumn, fact.EventID)
	}
	return fact, nil
}

// findAPIArtifactSupersessionIntentOnConn reads the supersession fact of one predecessor
// artifact id and reports whether it exists. A row that is present but cannot be verified is
// an error, never a miss, so a corrupt fact is not mistaken for an edge that was established
// without one.
func findAPIArtifactSupersessionIntentOnConn(ctx context.Context, querier intentScanner, predecessorID modulecore.ArtifactID) (modulecore.EventEnvelope, bool, error) {
	var rawPayload, artifactIDColumn, eventIDColumn string
	err := querier.QueryRowContext(ctx,
		`SELECT payload, artifact_id, event_id FROM `+supersessionIntentTable+` WHERE artifact_id = ?`,
		string(predecessorID)).Scan(&rawPayload, &artifactIDColumn, &eventIDColumn)
	if err == sql.ErrNoRows {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, fmt.Errorf("read supersession fact of artifact %s: %w", predecessorID, err)
	}
	fact, err := decodeAPIArtifactSupersessionIntentRecord(rawPayload, artifactIDColumn, eventIDColumn)
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	return fact, true, nil
}

// findAPIArtifactSupersessionIntentOwnerByEventID answers which predecessor already owns an
// event id, which is the lookup that keeps one event id from being spent on two supersessions.
func findAPIArtifactSupersessionIntentOwnerByEventID(ctx context.Context, querier intentScanner, eventID modulecore.EventID) (modulecore.ArtifactID, bool, error) {
	var owner string
	err := querier.QueryRowContext(ctx,
		`SELECT artifact_id FROM `+supersessionIntentTable+` WHERE event_id = ?`,
		string(eventID)).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read supersession fact owner of event %s: %w", eventID, err)
	}
	return modulecore.ArtifactID(owner), true, nil
}

// SupersedeAPIArtifactWithPublicationIntent establishes the edge that says an existing
// APIArtifact was replaced by an existing successor and, in the SAME transaction, stores the
// canonical supersession envelope that publication replays. It is the same reserved
// connection, the same BEGIN IMMEDIATE, the same reads and chain checks as
// SupersedeAPIArtifact, so the edge and its fact commit together and are serialized against
// every other Artifact write: a crash cannot leave an edge whose fact was never durably
// saved, which is the only reason the fact row exists, because reading the edge back cannot
// recover an EventID that was not stored.
//
// Outcomes are decided against the state a reader would resolve:
//
//   - No edge and no fact: the predecessor payload is updated and the fact row is written,
//     which is the only path that mints a supersession fact into history. The create-only
//     domain check runs on the rows read inside this transaction, so the digests the fact
//     names are the bytes being superseded and the successor is the row it names.
//   - The stored fact is exactly the given envelope and the edge it names is stored: nothing
//     is written at all, so a rerun after an unconfirmed commit cannot produce a second event
//     for one supersession. That no-op is reported only after both rows the fact names still
//     resolve and still carry the references that cannot move.
//
// Anything else writes nothing: a different stored fact, an event id another artifact already
// owns, a stored fact whose edge is not there, and an edge that exists while its fact does
// not. The last case is not repaired here, because an edge established by the plain
// SupersedeAPIArtifact carries a fact that this call did not mint and cannot re-derive, so
// backfilling it is its own explicit migration operation. An ordinary Save may not add,
// move or clear an edge and touches only api_artifact, so neither an update nor the plain
// supersede can overwrite a stored supersession fact.
func (s *SQLiteStore) SupersedeAPIArtifactWithPublicationIntent(ctx context.Context, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
		return err
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return err
	}
	factPayload, err := json.Marshal(fact)
	if err != nil {
		return err
	}
	return s.withArtifactTransaction(ctx, "supersede with publication fact", func(ctx context.Context, conn *sql.Conn) error {
		predecessor, err := loadAPIArtifactOnConn(ctx, conn, predecessorID)
		if err != nil {
			return err
		}
		successor, err := loadAPIArtifactOnConn(ctx, conn, successorID)
		if err != nil {
			return err
		}
		// The predecessor and successor must agree on task, run, actor, workstream, content
		// role and stored kind, and every row below the successor has to stay verifiable in
		// that same scope, because the new edge joins that chain. Nothing is written before
		// all of it has been read on this one reserved connection.
		if err := domaintrace.ValidateAPIArtifactSupersessionPair(predecessor, successor); err != nil {
			return err
		}
		if err := verifyAPIArtifactSuccessorChain(ctx, func(ctx context.Context, id modulecore.ArtifactID) (domaintrace.APIArtifact, error) {
			return loadAPIArtifactOnConn(ctx, conn, id)
		}, predecessorID, successor); err != nil {
			return err
		}
		storedFact, storedFactFound, err := findAPIArtifactSupersessionIntentOnConn(ctx, conn, predecessorID)
		if err != nil {
			return err
		}
		if !storedFactFound {
			owner, ownerFound, err := findAPIArtifactSupersessionIntentOwnerByEventID(ctx, conn, fact.EventID)
			if err != nil {
				return err
			}
			if ownerFound && owner != predecessorID {
				return fmt.Errorf("supersession fact %s is already the fact of artifact %s, so artifact %s cannot claim the same event", fact.EventID, owner, predecessorID)
			}
		}

		if storedFactFound {
			if !reflect.DeepEqual(storedFact, fact) {
				return fmt.Errorf("artifact %s already carries supersession fact %s, so supersession fact %s would be a second event for one supersession", predecessorID, storedFact.EventID, fact.EventID)
			}
			if predecessor.SupersededBy != successorID {
				return fmt.Errorf("supersession fact %s is stored for artifact %s, which stores superseded_by %q, so superseding it again is not a no-op", fact.EventID, predecessorID, predecessor.SupersededBy)
			}
			// References a later legitimate body update replaced are exactly what the shared
			// row check leaves alone: the digests in this fact are history, not the current row.
			return domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, predecessor, successor)
		}
		switch {
		case predecessor.SupersededBy == successorID:
			return fmt.Errorf("artifact %s is stored with superseded_by %s but no supersession fact, so the edge predates SupersedeAPIArtifactWithPublicationIntent and its fact has to be recorded by an explicit migration repair, not by this supersede", predecessorID, successorID)
		case predecessor.SupersededBy != "":
			return fmt.Errorf("artifact %s is already superseded by %s, not %s", predecessorID, predecessor.SupersededBy, successorID)
		}

		// The create-only contract is checked on the rows this transaction read, so the two
		// digests the fact carries are the bytes actually being superseded and replaced.
		if err := domaintrace.ValidateAPIArtifactSupersessionFact(predecessor, successor, fact); err != nil {
			return err
		}
		updated := predecessor
		updated.SupersededBy = successorID
		if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
			return fmt.Errorf("supersede of artifact %s leaves an invalid row: %w", predecessorID, err)
		}
		payload, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("encode superseded payload for artifact %s: %w", predecessorID, err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), string(predecessorID)); err != nil {
			return fmt.Errorf("write supersede edge for artifact %s: %w", predecessorID, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO `+supersessionIntentTable+` (artifact_id, event_id, payload) VALUES (?, ?, ?)`,
			string(predecessorID), string(fact.EventID), string(factPayload)); err != nil {
			return fmt.Errorf("write supersession fact %s for artifact %s: %w", fact.EventID, predecessorID, err)
		}
		return nil
	})
}

// ListAPIArtifactSupersessionFacts returns one bounded page of stored supersession facts in
// ascending predecessor artifact_id order, strictly after the after cursor, so a caller drains
// every fact by feeding the last id of a page back in until a page comes back empty. The limit
// only bounds one call and is clamped to the same page ceiling the creation intent owner
// uses: nothing here makes an older source event unobservable.
//
// Every fact on the page is read back through the persisted-fact validator and resolved
// against both rows it names on one connection, so a fact pointing at a missing row, at an
// edge that is no longer the edge it names, or at references that moved fails closed instead
// of being replayed. Facts are returned as copies and the connection is released before this
// returns: the caller does the canonical event store I/O outside it.
func (s *SQLiteStore) ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("browser trace sqlite store is closed")
	}
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
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve browser trace sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	rows, err := conn.QueryContext(ctx,
		`SELECT payload, artifact_id, event_id FROM `+supersessionIntentTable+` WHERE artifact_id > ? ORDER BY artifact_id LIMIT ?`,
		string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("read supersession facts after %s: %w", after, err)
	}
	defer rows.Close()
	page := []modulecore.EventEnvelope{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var rawPayload, artifactIDColumn, eventIDColumn string
		if err := rows.Scan(&rawPayload, &artifactIDColumn, &eventIDColumn); err != nil {
			return nil, err
		}
		fact, err := decodeAPIArtifactSupersessionIntentRecord(rawPayload, artifactIDColumn, eventIDColumn)
		if err != nil {
			return nil, err
		}
		predecessor, err := loadAPIArtifactOnConn(ctx, conn, fact.ArtifactID)
		if err != nil {
			return nil, err
		}
		successor, err := loadAPIArtifactOnConn(ctx, conn, modulecore.ArtifactID(fact.Payload[domaintrace.APIArtifactSupersessionPayloadSupersededBy].(string)))
		if err != nil {
			return nil, err
		}
		if err := domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, predecessor, successor); err != nil {
			return nil, err
		}
		page = append(page, copyAPIArtifactPublicationIntent(fact))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}
