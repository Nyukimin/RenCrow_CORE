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

// Publication intents are paged, so a page size only bounds one call:
// defaultPublicationIntentPageLimit is what a caller gets when it asks for no bound of
// its own, and maxPublicationIntentPageLimit is the largest page this owner will return,
// which is also what caps the allocation a request can trigger. A request above the
// maximum is clamped rather than refused, so a caller that asks for everything still
// drains every stored intent by feeding the last artifact id of a page back in until a
// page comes back empty. Neither number is a total history cap: an older source event
// never becomes unobservable because of a display bound.
const (
	defaultPublicationIntentPageLimit = 50
	maxPublicationIntentPageLimit     = 1000
)

// validateAPIArtifactPublicationIntentRows checks every stored intent against the
// current artifact rows that a reader resolves: the intent has to belong to a row that
// exists, and the references that cannot change over an artifact's life have to agree
// with it. The shared domain check owns those references, so the reader and the create
// path cannot drift apart, and the row's body is never rebuilt or re-digested here.
//
// What is deliberately not compared is the row's current content hash and its
// supersession edge: a legitimate in-place update replaces the body and digest, and the
// owner Supersede operation sets the edge after the creation intent was written, so
// comparing them would turn accepted history into a load failure. Artifacts that carry
// no intent are not an error at this source preparation stage; the create caller that
// writes both together is what closes that gap.
func validateAPIArtifactPublicationIntentRows(artifacts []domaintrace.APIArtifact, intents []modulecore.EventEnvelope) error {
	rows := make(map[modulecore.ArtifactID]domaintrace.APIArtifact, len(artifacts))
	for _, item := range artifacts {
		rows[item.ArtifactID] = item
	}
	for _, intent := range intents {
		stored, found := rows[intent.ArtifactID]
		if !found {
			return fmt.Errorf("publication intent %s is bound to artifact %s, which has no record in %s", intent.EventID, intent.ArtifactID, artifactFilename)
		}
		if err := domaintrace.ValidateAPIArtifactPublicationIntentRow(intent, stored); err != nil {
			return err
		}
	}
	return nil
}

// loadAPIArtifactPublicationIntents reads the intent file while the batch lock is already
// held by Read or Write, so it never nests batch.Read. Every record is decoded as the
// typed core event envelope it is and has to satisfy the persisted-intent contract, which
// reuses the canonical envelope validation instead of a second copy of it. A missing file
// is a broken store, not an empty history, and the reader stays bounded: the scanner
// buffer is the writer's own record ceiling plus the one delimiter byte, so a record
// larger than the writer can append fails closed instead of allocating freely, and a tail
// without a delimiter is reported rather than fused with the next append.
//
// The last record per artifact id is kept, but not by last-row-wins: a second record for
// the same artifact id that is not byte-identical to the stored intent, and an event id
// that shows up bound to two different artifacts, are conflicting history and are returned
// as errors, because publication replay binds one creation to one event id. A creation
// intent is immutable history, so stored intents are validated on their own and never
// re-compared against a later mutable artifact row whose digest or supersession edge a
// legitimate update or Supersede changed.
//
// The result is ordered by artifact id ascending, which is the keyset order
// ListAPIArtifactPublicationIntents pages through.
func (s *JSONLStore) loadAPIArtifactPublicationIntents(ctx context.Context) ([]modulecore.EventEnvelope, error) {
	file, err := os.Open(s.artifactIntentPath)
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
		var intent modulecore.EventEnvelope
		if err := json.Unmarshal(record, &intent); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", artifactIntentFilename, line, err)
		}
		if err := domaintrace.ValidatePersistedAPIArtifactPublicationIntent(intent); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", artifactIntentFilename, line, err)
		}
		if stored, found := latest[intent.ArtifactID]; found && !reflect.DeepEqual(stored, intent) {
			return nil, fmt.Errorf("%s line %d: artifact %s carries a publication intent %s that conflicts with the stored intent %s", artifactIntentFilename, line, intent.ArtifactID, intent.EventID, stored.EventID)
		}
		if owner, found := bindings[intent.EventID]; found && owner != intent.ArtifactID {
			return nil, fmt.Errorf("%s line %d: publication intent %s is bound to both artifact %s and %s", artifactIntentFilename, line, intent.EventID, owner, intent.ArtifactID)
		}
		latest[intent.ArtifactID] = intent
		bindings[intent.EventID] = intent.ArtifactID
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := rejectUnterminatedAPIArtifactRecord(file, artifactIntentFilename); err != nil {
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

// ListAPIArtifactPublicationIntents returns one bounded page of the stored creation
// intents in artifact id order, starting strictly after the after cursor, so a caller can
// drain every intent by feeding the last id of a page back in until a page comes back
// empty. The page size bounds a single call only: there is no total cap that would make an
// older source event unobservable, and an empty page means the owner has no more intents.
//
// The page is read under the same batch lock as every artifact write, so it is one
// committed snapshot, and the envelopes are returned as copies so a caller that edits a
// payload cannot reach back into the loaded state. The lock is released when this returns:
// the caller does the canonical event store I/O outside it, because the batch lock is not
// held across another owner's storage.
func (s *JSONLStore) ListAPIArtifactPublicationIntents(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
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
	var page []modulecore.EventEnvelope
	if err := s.batch.Read(ctx, func() error {
		artifacts, err := s.loadAPIArtifacts(ctx)
		if err != nil {
			return err
		}
		intents, err := s.loadAPIArtifactPublicationIntents(ctx)
		if err != nil {
			return err
		}
		if err := validateAPIArtifactPublicationIntentRows(artifacts, intents); err != nil {
			return err
		}
		page = make([]modulecore.EventEnvelope, 0, limit)
		for _, intent := range intents {
			if err := contextErr(ctx); err != nil {
				return err
			}
			if intent.ArtifactID <= after {
				continue
			}
			page = append(page, copyAPIArtifactPublicationIntent(intent))
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

// copyAPIArtifactPublicationIntent returns an envelope that shares no mutable container
// with the loaded state. The envelope carries maps and slices, so a plain assignment would
// still let a caller write through to the copy this store is holding.
func copyAPIArtifactPublicationIntent(intent modulecore.EventEnvelope) modulecore.EventEnvelope {
	out := intent
	if intent.Payload != nil {
		out.Payload = make(map[string]any, len(intent.Payload))
		for key, value := range intent.Payload {
			out.Payload[key] = value
		}
	}
	if intent.DependencyEventIDs != nil {
		out.DependencyEventIDs = append([]modulecore.EventID(nil), intent.DependencyEventIDs...)
	}
	return out
}

// CreateAPIArtifactWithPublicationIntent stores a newly created APIArtifact together with
// the canonical event envelope that publication replays, as ONE batch WAL transaction:
// the artifact record goes to api_artifact.jsonl and the typed envelope to
// api_artifact_publication.jsonl, so an interruption leaves either both records or
// neither. They are separate files because the accepted artifact reader decodes every
// record of api_artifact.jsonl as an APIArtifact, and the intent is delivery state for
// publication replay, not a second canonical event store. Without this coupling a crash
// after the artifact append would leave a row whose event envelope was never durably
// saved, and reading the row back cannot recover an EventID that was not stored.
//
// This owner mints no event and infers no provenance: the caller owns canonical v7
// EventID issuance, the trace and the verified actor, and the domain contract only
// refuses a pair whose payload digest is not the created digest or whose creation already
// carries a supersession edge. Inside the write lock the decision is made against the
// state a reader would resolve, and it has exactly two outcomes:
//
//   - The artifact has no row and no intent, so this call creates both records together.
//     This is the only path that mints a creation fact into history, and it is what makes
//     the pair indivisible: a creation fact never enters the file without its row.
//   - The artifact already carries the given intent, so the retry asks for no append at
//     all and a rerun after an unconfirmed commit cannot create a second event for one
//     creation. That no-op is only reported once the row the intent names is still
//     resolvable and still carries the identity the intent claims, so a stored pair whose
//     row is gone or whose immutable references moved is an error, not a success that
//     quietly points at nothing.
//
// Everything else is a conflict that overwrites nothing: a stored intent that differs from
// the given envelope, an event id another artifact already claims, and a row that exists
// while its creation fact does not. That last case is deliberately not repaired here.
// A row without a creation fact can only predate this create path, because the WAL writes
// the pair together, and the identity a creation fact carries is v7 issuance, a trace and
// a verified actor that the original creation owned and this call cannot re-derive.
// Backfilling a migration row is therefore its own explicit owner operation, not a
// side effect of a new creation, and it must not be reported as a create that worked.
//
// An ordinary Save and a Supersede touch only the artifact file, so neither can overwrite
// an original creation intent.
func (s *JSONLStore) CreateAPIArtifactWithPublicationIntent(ctx context.Context, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		return err
	}
	artifactRecord, err := json.Marshal(item)
	if err != nil {
		return err
	}
	intentRecord, err := json.Marshal(intent)
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
		storedIntents, err := s.loadAPIArtifactPublicationIntents(ctx)
		if err != nil {
			return nil, err
		}
		var storedIntent modulecore.EventEnvelope
		storedIntentFound := false
		for _, candidate := range storedIntents {
			if candidate.ArtifactID == item.ArtifactID {
				storedIntent, storedIntentFound = candidate, true
				continue
			}
			if candidate.EventID == intent.EventID {
				return nil, fmt.Errorf("publication intent %s is already the creation fact of artifact %s, so artifact %s cannot claim the same event", intent.EventID, candidate.ArtifactID, item.ArtifactID)
			}
		}
		var storedRow domaintrace.APIArtifact
		storedRowFound := false
		for _, candidate := range storedArtifacts {
			if candidate.ArtifactID == item.ArtifactID {
				storedRow, storedRowFound = candidate, true
			}
		}

		if storedIntentFound {
			if !reflect.DeepEqual(storedIntent, intent) {
				return nil, fmt.Errorf("artifact %s already carries publication intent %s, so creation intent %s would be a second event for one creation", item.ArtifactID, storedIntent.EventID, intent.EventID)
			}
			// The exact intent is durable. Only the row the intent names can make that a
			// delivered creation, so the retry is validated against it before it is called
			// a no-op: the row has to resolve, and the identity that cannot move over an
			// artifact's life has to still be the identity the envelope claims. A row whose
			// body digest a later update replaced, or whose edge a Supersede set, is not a
			// conflict here; that is exactly what the shared row check leaves alone.
			if !storedRowFound {
				return nil, fmt.Errorf("publication intent %s is stored for artifact %s, which has no record in %s, so creating it again is not a no-op", intent.EventID, item.ArtifactID, artifactFilename)
			}
			if err := domaintrace.ValidateAPIArtifactPublicationIntentRow(intent, storedRow); err != nil {
				return nil, err
			}
			// An empty append set: the retry adds no history.
			return nil, nil
		}
		if !storedRowFound {
			return map[string][]byte{
				artifactFilename:       append(artifactRecord, '\n'),
				artifactIntentFilename: append(intentRecord, '\n'),
			}, nil
		}
		// The row predates this call and carries no creation fact, so this call did not
		// create it and mints nothing: appending an intent here would date a creation fact
		// from an issuance, trace and actor that belong to now, not to the row, and would
		// let a migration backfill look like an ordinary create that worked.
		return nil, fmt.Errorf("artifact %s is stored in %s without a publication intent, so it predates CreateAPIArtifactWithPublicationIntent and its creation fact has to be recorded by an explicit migration repair, not by this create", item.ArtifactID, artifactFilename)
	})
}
