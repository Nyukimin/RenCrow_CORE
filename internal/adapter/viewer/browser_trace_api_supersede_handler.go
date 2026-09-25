package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// BrowserTraceAPISupersedeStore is the artifact owner surface this route drives: the current
// artifacts, the one operation that establishes a supersession edge together with the
// supersession fact that says the edge was established, and the durable facts the owner kept
// next to the edge. An ordinary Save may not add, move or clear an edge, so this route has no
// path that writes an edge without its fact.
type BrowserTraceAPISupersedeStore interface {
	ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error)
	SupersedeAPIArtifactWithPublicationIntent(ctx context.Context, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) error
	ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error)
}

// BrowserTraceAPIArtifactSupersedeRequest names the edge to establish and the provenance the
// caller claims. Task, run and actor are asserted and then verified against the Task Store and
// the stored predecessor row: a supplied string authenticates nobody, and a caller that owns a
// different task may not re-establish an edge belonging elsewhere.
type BrowserTraceAPIArtifactSupersedeRequest struct {
	PredecessorArtifactID string            `json:"predecessor_artifact_id"`
	SuccessorArtifactID   string            `json:"successor_artifact_id"`
	TaskID                modulecore.TaskID `json:"task_id"`
	RunID                 modulecore.RunID  `json:"run_id"`
	ActorID               string            `json:"actor_id"`
}

// apiSupersedeArtifactScanLimit is the display bound of the artifact lookup this route needs
// to name the two rows it is about to join. It is not a correctness bound: the owner re-checks
// both rows and the whole successor chain under its own lock, and the facts are paged until a
// page comes back empty.
const apiSupersedeArtifactScanLimit = 500

// HandleBrowserTraceAPIArtifactSupersede establishes one supersession edge on the canonical
// route and publishes its fact before it answers.
//
// The order is deliberate. The edge and its fact become durable in the artifact owner in one
// operation, and only then is the fact delivered to the one canonical event store: a response
// that reports success therefore names a canonical event, and a delivery that fails leaves the
// edge and fact in place so the explicit recovery pass on the next start retries the very same
// EventID instead of minting a second event for one supersession.
//
// Provenance is verified, not inherited from the request body, and every refusal happens before
// the store is asked to write, so a rejected request leaves no edge, no fact and no canonical
// event. This is a handler-boundary contract: what the artifact and event owners actually
// persist is what their own store tests establish.
func HandleBrowserTraceAPIArtifactSupersede(store BrowserTraceAPISupersedeStore, verifier BrowserTraceRunVerifier, events browsertraceapp.APIArtifactCreationPublisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || verifier == nil || events == nil {
			http.Error(w, "browser trace api artifact supersede unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req BrowserTraceAPIArtifactSupersedeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid supersede request", http.StatusBadRequest)
			return
		}
		predecessorID := modulecore.ArtifactID(req.PredecessorArtifactID)
		successorID := modulecore.ArtifactID(req.SuccessorArtifactID)
		if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Ownership of the task and run is read from the Task Store. A caller that proves a
		// task it owns still cannot move an edge that belongs to another task, which is the
		// check below, not this one.
		assignee, err := verifier.VerifyTaskRun(r.Context(), req.TaskID, req.RunID)
		if err != nil {
			http.Error(w, "browser trace task/run ownership verification failed", http.StatusForbidden)
			return
		}
		if assignee != "" && assignee != req.ActorID {
			http.Error(w, "browser trace actor does not own run", http.StatusForbidden)
			return
		}
		artifacts, err := store.ListAPIArtifacts(r.Context(), apiSupersedeArtifactScanLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var predecessor, successor *domaintrace.APIArtifact
		for i := range artifacts {
			switch artifacts[i].ArtifactID {
			case predecessorID:
				predecessor = &artifacts[i]
			case successorID:
				successor = &artifacts[i]
			}
		}
		if predecessor == nil {
			http.Error(w, fmt.Sprintf("predecessor artifact %s is not stored", predecessorID), http.StatusNotFound)
			return
		}
		if successor == nil {
			http.Error(w, fmt.Sprintf("successor artifact %s is not stored", successorID), http.StatusNotFound)
			return
		}
		if predecessor.TaskID != req.TaskID || predecessor.RunID != req.RunID || predecessor.ActorID != req.ActorID {
			http.Error(w, fmt.Sprintf("artifact %s belongs to task %s run %s actor %s, so task %s run %s actor %s cannot supersede it",
				predecessor.ArtifactID, predecessor.TaskID, predecessor.RunID, predecessor.ActorID,
				req.TaskID, req.RunID, req.ActorID), http.StatusForbidden)
			return
		}

		// The edge this artifact already carries is read out of the persisted row, so the
		// response names the successor that is actually established rather than one this
		// request would have preferred. A same-edge retry re-delivers the fact the owner
		// already kept: re-minting an EventID here would be a second event for one supersession,
		// which the owner refuses.
		if predecessor.SupersededBy == successorID {
			fact, found, err := establishedAPISupersessionFact(r.Context(), store, predecessorID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if !found {
				// An edge without a fact predates this route and carries an EventID this request
				// cannot re-derive, so it is reported rather than silently backfilled.
				writeJSON(w, http.StatusConflict, map[string]any{
					"status":                            "conflict",
					"reason":                            "established successor has no durable supersession fact to re-deliver, so the edge predates the supersede route and needs an explicit fact repair",
					"predecessor_artifact_id":           predecessorID,
					"established_successor_artifact_id": predecessor.SupersededBy,
				})
				return
			}
			report, err := browsertraceapp.PublishAPIArtifactSupersessionFacts(r.Context(), events, fact)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			respondSupersededAPIArtifact(w, r, store, events, predecessorID, fact, report)
			return
		}
		if predecessor.SupersededBy != "" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":                            "conflict",
				"reason":                            "artifact is already superseded by another successor",
				"predecessor_artifact_id":           predecessorID,
				"established_successor_artifact_id": predecessor.SupersededBy,
				"requested_successor_artifact_id":   successorID,
			})
			return
		}

		fact, err := browsertraceapp.NewAPIArtifactSupersessionFact(*predecessor, *successor, modulecore.NewTraceID(), time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := store.SupersedeAPIArtifactWithPublicationIntent(r.Context(), predecessorID, successorID, fact); err != nil {
			// The store answers its own reason. A rejection this route did not already classify
			// is reported as a store failure instead of being guessed at, because the artifact
			// owners do not yet expose typed conflict sentinels; the writes they refused are
			// recorded in the store tests that assert on those reasons.
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		report, err := browsertraceapp.PublishAPIArtifactSupersessionFacts(r.Context(), events, fact)
		if err != nil {
			// The edge and its fact survived in the artifact owner, so this delivery is not lost:
			// the recovery pass on the next start retries this very EventID.
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		respondSupersededAPIArtifact(w, r, store, events, predecessorID, fact, report)
	}
}

// respondSupersededAPIArtifact answers with what the owners now hold: the persisted predecessor
// row and the canonical envelope the canonical store assigned its sequence to. Both are read
// back rather than echoed from the request, so a successful response cannot rest on a record
// that was never written.
func respondSupersededAPIArtifact(w http.ResponseWriter, r *http.Request, store BrowserTraceAPISupersedeStore, events browsertraceapp.APIArtifactCreationPublisher, predecessorID modulecore.ArtifactID, fact modulecore.EventEnvelope, report browsertraceapp.APIArtifactPublicationReport) {
	published, found, err := events.GetByID(r.Context(), fact.EventID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, fmt.Sprintf("supersession fact %s is not readable from the canonical event store", fact.EventID), http.StatusInternalServerError)
		return
	}
	artifacts, err := store.ListAPIArtifacts(r.Context(), apiSupersedeArtifactScanLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range artifacts {
		if artifacts[i].ArtifactID != predecessorID {
			continue
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"api_artifact":             artifacts[i],
			"supersession_event":       published,
			"supersession_publication": report,
			"official_promotion":       false,
			"implementation_apply":     false,
		})
		return
	}
	http.Error(w, fmt.Sprintf("superseded artifact %s is not readable from the artifact store", predecessorID), http.StatusInternalServerError)
}

// establishedAPISupersessionFact walks the durable supersession facts the artifact owner kept
// for one predecessor. It pages the artifact_id keyset until a page comes back empty, so an
// older edge is reachable, and it stops on a cursor that does not advance instead of spinning.
func establishedAPISupersessionFact(ctx context.Context, store BrowserTraceAPISupersedeStore, predecessorID modulecore.ArtifactID) (modulecore.EventEnvelope, bool, error) {
	after := modulecore.ArtifactID("")
	for {
		if err := ctx.Err(); err != nil {
			return modulecore.EventEnvelope{}, false, err
		}
		page, err := store.ListAPIArtifactSupersessionFacts(ctx, after, 0)
		if err != nil {
			return modulecore.EventEnvelope{}, false, err
		}
		if len(page) == 0 {
			return modulecore.EventEnvelope{}, false, nil
		}
		for _, fact := range page {
			if fact.ArtifactID == predecessorID {
				return fact, true, nil
			}
		}
		last := page[len(page)-1].ArtifactID
		if last <= after {
			return modulecore.EventEnvelope{}, false, fmt.Errorf("supersession fact cursor %q did not advance past %q", last, after)
		}
		after = last
	}
}
