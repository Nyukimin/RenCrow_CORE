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

// publicationIntentTable is the browsertrace-owned table that keeps the canonical
// creation envelope of each APIArtifact next to the artifact row itself. It is delivery
// state for publication replay, not a second canonical event store, and it is written in
// the same transaction as the artifact so an interruption leaves either both rows or
// neither. Reading a row back cannot recover an EventID that was never stored, which is
// the only reason this table exists.
const publicationIntentTable = "api_artifact_publication_intent"

// decodeAPIArtifactPublicationIntentRecord turns one stored row into the typed envelope
// it claims to be. The payload is the truth and the two columns are indexes of it, so a
// column that disagrees with the payload is reported rather than trusted: a replay that
// looked up one and read the other would otherwise act on two different creations. The
// shared domain validator owns the envelope contract, so this path adds no rules of its
// own.
func decodeAPIArtifactPublicationIntentRecord(rawPayload, artifactIDColumn, eventIDColumn string) (modulecore.EventEnvelope, error) {
	var intent modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(rawPayload), &intent); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s payload is corrupt: %w", publicationIntentTable, err)
	}
	if err := domaintrace.ValidatePersistedAPIArtifactPublicationIntent(intent); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if string(intent.ArtifactID) != artifactIDColumn {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s artifact_id column %s disagrees with payload artifact_id %s", publicationIntentTable, artifactIDColumn, intent.ArtifactID)
	}
	if string(intent.EventID) != eventIDColumn {
		return modulecore.EventEnvelope{}, fmt.Errorf("%s event_id column %s disagrees with payload event_id %s", publicationIntentTable, eventIDColumn, intent.EventID)
	}
	return intent, nil
}

// scanAPIArtifactPublicationIntent reads and decodes one intent row from either a
// *sql.DB or the reserved *sql.Conn of an open Artifact transaction.
type intentScanner interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// findAPIArtifactPublicationIntentOnConn reads the creation intent of one artifact id and
// reports whether it exists. A row that is present but cannot be verified is an error,
// never a miss, so a corrupt intent is not mistaken for an artifact that was never
// created through this path.
func findAPIArtifactPublicationIntentOnConn(ctx context.Context, querier intentScanner, artifactID modulecore.ArtifactID) (modulecore.EventEnvelope, bool, error) {
	var rawPayload, artifactIDColumn, eventIDColumn string
	err := querier.QueryRowContext(ctx,
		`SELECT payload, artifact_id, event_id FROM `+publicationIntentTable+` WHERE artifact_id = ?`,
		string(artifactID)).Scan(&rawPayload, &artifactIDColumn, &eventIDColumn)
	if err == sql.ErrNoRows {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, fmt.Errorf("read publication intent of artifact %s: %w", artifactID, err)
	}
	intent, err := decodeAPIArtifactPublicationIntentRecord(rawPayload, artifactIDColumn, eventIDColumn)
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	return intent, true, nil
}

// findAPIArtifactPublicationIntentOwnerByEventID answers which artifact id already owns an
// event id, which is the lookup that keeps one creation from being published twice under
// two artifacts.
func findAPIArtifactPublicationIntentOwnerByEventID(ctx context.Context, querier intentScanner, eventID modulecore.EventID) (modulecore.ArtifactID, bool, error) {
	var owner string
	err := querier.QueryRowContext(ctx,
		`SELECT artifact_id FROM `+publicationIntentTable+` WHERE event_id = ?`,
		string(eventID)).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read publication intent owner of event %s: %w", eventID, err)
	}
	return modulecore.ArtifactID(owner), true, nil
}

// CreateAPIArtifactWithPublicationIntent writes a newly created APIArtifact and the
// canonical creation envelope that publication replays on one reserved connection, inside
// the same BEGIN IMMEDIATE transaction that SaveAPIArtifact and SupersedeAPIArtifact use,
// so the pair is committed together and serialized against every other Artifact write.
//
// Like the accepted JSONL owner it has exactly two outcomes, decided against the state a
// reader would resolve:
//
//   - no row and no intent: the artifact and its intent are both written, which is the
//     only path that mints a creation fact.
//   - the stored intent is exactly the given envelope: nothing is written at all, so a
//     rerun after an unconfirmed commit cannot produce a second event for one creation.
//     That no-op is reported only after the row the intent names still resolves and still
//     carries the identity the envelope claims.
//
// Anything else writes nothing: a different stored intent, an event id another artifact
// already owns, and a row that exists with no creation fact of its own. The last case is
// not repaired here, because the issuance, trace and verified actor of that row's creation
// belong to the original create and cannot be re-derived by this call; a migration backfill
// is its own explicit operation. Ordinary Save and Supersede touch only api_artifact, so
// neither can overwrite a creation intent.
func (s *SQLiteStore) CreateAPIArtifactWithPublicationIntent(ctx context.Context, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	artifactPayload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	intentPayload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return s.withArtifactTransaction(ctx, "artifact create", func(ctx context.Context, conn *sql.Conn) error {
		storedIntent, storedIntentFound, err := findAPIArtifactPublicationIntentOnConn(ctx, conn, item.ArtifactID)
		if err != nil {
			return err
		}
		if !storedIntentFound {
			owner, ownerFound, err := findAPIArtifactPublicationIntentOwnerByEventID(ctx, conn, intent.EventID)
			if err != nil {
				return err
			}
			if ownerFound && owner != item.ArtifactID {
				return fmt.Errorf("publication intent %s is already the creation fact of artifact %s, so artifact %s cannot claim the same event", intent.EventID, owner, item.ArtifactID)
			}
		}
		storedRow, storedRowFound, err := findAPIArtifactOnConn(ctx, conn, item.ArtifactID)
		if err != nil {
			return err
		}

		if storedIntentFound {
			if !reflect.DeepEqual(storedIntent, intent) {
				return fmt.Errorf("artifact %s already carries publication intent %s, so creation intent %s would be a second event for one creation", item.ArtifactID, storedIntent.EventID, intent.EventID)
			}
			if !storedRowFound {
				return fmt.Errorf("publication intent %s is stored for artifact %s, which has no row in api_artifact, so creating it again is not a no-op", intent.EventID, item.ArtifactID)
			}
			// A body digest a later update replaced or an edge a Supersede set is exactly
			// what the shared immutable-reference check leaves alone.
			if err := domaintrace.ValidateAPIArtifactPublicationIntentRow(intent, storedRow); err != nil {
				return err
			}
			return nil
		}
		if !storedRowFound {
			if _, err := conn.ExecContext(ctx,
				`INSERT OR REPLACE INTO api_artifact (artifact_id, run_id, created_at, payload) VALUES (?, ?, ?, ?)`,
				string(item.ArtifactID), string(item.RunID), item.CreatedAt.Format(timeFormatRFC3339Nano), string(artifactPayload)); err != nil {
				return fmt.Errorf("write artifact %s: %w", item.ArtifactID, err)
			}
			if _, err := conn.ExecContext(ctx,
				`INSERT INTO `+publicationIntentTable+` (artifact_id, event_id, payload) VALUES (?, ?, ?)`,
				string(item.ArtifactID), string(intent.EventID), string(intentPayload)); err != nil {
				return fmt.Errorf("write publication intent %s for artifact %s: %w", intent.EventID, item.ArtifactID, err)
			}
			return nil
		}
		return fmt.Errorf("artifact %s is stored in api_artifact without a publication intent, so it predates CreateAPIArtifactWithPublicationIntent and its creation fact has to be recorded by an explicit migration repair, not by this create", item.ArtifactID)
	})
}

// ListAPIArtifactPublicationIntents returns one bounded page of stored creation intents in
// ascending artifact_id order, strictly after the after cursor, so a caller drains every
// intent by feeding the last id of a page back in until a page comes back empty. The limit
// only bounds one call and is clamped to the same page ceiling the JSONL owner uses:
// nothing here makes an older source event unobservable.
//
// Every intent on the page is read back through the persisted-intent validator and
// resolved against its artifact row on one connection, so an intent pointing at a missing
// row or at a row whose task, run, actor, workstream, content role or kind moved fails
// closed instead of being replayed. Intents are returned as copies and the connection is
// released before this returns: the caller does the canonical event store I/O outside it.
func (s *SQLiteStore) ListAPIArtifactPublicationIntents(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
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
			return nil, fmt.Errorf("publication intent page cursor %s: %w", after, err)
		}
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve browser trace sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	rows, err := conn.QueryContext(ctx,
		`SELECT payload, artifact_id, event_id FROM `+publicationIntentTable+` WHERE artifact_id > ? ORDER BY artifact_id LIMIT ?`,
		string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("read publication intents after %s: %w", after, err)
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
		intent, err := decodeAPIArtifactPublicationIntentRecord(rawPayload, artifactIDColumn, eventIDColumn)
		if err != nil {
			return nil, err
		}
		storedRow, found, err := findAPIArtifactOnConn(ctx, conn, intent.ArtifactID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("publication intent %s is bound to artifact %s, which has no row in api_artifact", intent.EventID, intent.ArtifactID)
		}
		if err := domaintrace.ValidateAPIArtifactPublicationIntentRow(intent, storedRow); err != nil {
			return nil, err
		}
		page = append(page, copyAPIArtifactPublicationIntent(intent))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}
