//go:build realworld

package worldread

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

// TestRealWorldStopPlanningConsumesOwnerDecisions reads WORLD's safe-stop
// planning decision for a running activity through CORE's read client against a
// real WORLD binary, real stored WorkSet/LifePolicy heads, and the real
// assessment route. It is the cross-repo check for D-11 and D-12 on the CORE
// side: the posture, cleanup action, occupancy decision, and hold all come from
// WORLD's owner mapping, CORE sends only the activity ID, and consuming the
// decision writes no WORLD State.
//
// It also fixes the boundary this unit stays inside: no CORE production route,
// port, or Viewer consumes the decision yet (D-27), and no real resource,
// reservation, or measured geometry is bound (D-17, D-18).
func TestRealWorldStopPlanningConsumesOwnerDecisions(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	if strings.TrimSpace(os.Getenv("RENCROW_WORLD_READ_E2E_SCOPES")) != "" {
		t.Skip("RENCROW_WORLD_READ_E2E_SCOPES drops the read/projection pairing this case needs")
	}
	baseURL, token, stop := startRealWorldRead(t, bin)
	defer stop()

	target := domainprojection.Target{
		WorldID:          "rencrow-home",
		TimelineID:       "timeline-1",
		TimelineEpoch:    "epoch-e2e",
		ActorID:          "mio",
		ActorInstanceID:  "core-instance-mio",
		ActorTransportID: "mio",
	}
	validUntil := time.Now().UTC().Add(10 * time.Minute)

	putLifePolicy := func(revision uint64, permitted, fallback []string) {
		t.Helper()
		policy, err := domainprojection.NewLifePolicy(
			target.WorldID, target.ActorID, target.TimelineEpoch, revision, validUntil,
			"persona-e2e-1", "condition-e2e-1", permitted, fallback)
		if err != nil {
			t.Fatalf("NewLifePolicy(revision %d) error = %v", revision, err)
		}
		putRealProjection(t, baseURL, token, target, domainprojection.KindLifePolicy,
			mustRealEnvelope(t, target, domainprojection.KindLifePolicy, policy))
	}
	putWorkSet := func(revision uint64, items []domainprojection.WorkItem) {
		t.Helper()
		workSet, err := domainprojection.NewWorkSet(
			target.WorldID, target.ActorID, target.TimelineEpoch, revision, validUntil, items)
		if err != nil {
			t.Fatalf("NewWorkSet(revision %d) error = %v", revision, err)
		}
		putRealProjection(t, baseURL, token, target, domainprojection.KindWorkSet,
			mustRealEnvelope(t, target, domainprojection.KindWorkSet, workSet))
	}

	putLifePolicy(1, []string{"idle", "read", "walk", "rest"}, []string{"rest"})
	putWorkSet(1, []domainprojection.WorkItem{})

	client, err := New(Config{
		BaseURL:       baseURL,
		Credential:    token,
		WorldID:       target.WorldID,
		TimelineID:    target.TimelineID,
		TimelineEpoch: target.TimelineEpoch,
		ActorIDs:      []string{"mio"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stateBefore, err := realWorldStateSeq(t, client)
	if err != nil {
		t.Fatalf("state before stop planning error = %v", err)
	}

	// A fresh LifePolicy that still permits the running activity continues it.
	// WORLD publishes the reason and grants no stop effect, and only a fresh
	// empty WorkSet clears the work presentation (D-07, D-08).
	decision, err := client.StopPlanning(context.Background(), "mio", "read")
	if err != nil {
		t.Fatalf("StopPlanning(read under a fresh policy) error = %v", err)
	}
	if decision.ActivityID != "read" || decision.Action != StopPlanningContinue ||
		decision.Reason != "permitted" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.StopState != "" || len(decision.StopActions) != 0 ||
		decision.ReleaseOccupancy || decision.RetainOccupancyReason != "" {
		t.Fatalf("a continue decision granted a stop effect: %+v", decision)
	}
	if !decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v; the empty WorkSet clears the presentation", decision)
	}

	// WORLD's owner mapping gives sleep no safe interruption point, so a
	// permission that no longer covers it holds the Actor instead of granting a
	// posture (D-11). CORE reads that as a hold with no stop effect.
	decision, err = client.StopPlanning(context.Background(), "mio", "sleep")
	if err != nil {
		t.Fatalf("StopPlanning(sleep, which the policy does not permit) error = %v", err)
	}
	if decision.ActivityID != "sleep" || decision.Action != StopPlanningHold ||
		decision.Reason != "not_interruptible" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.StopState != "" || len(decision.StopActions) != 0 || decision.ReleaseOccupancy {
		t.Fatalf("a hold grants no posture, action, or release: %+v", decision)
	}

	// Presence-required work keeps the presentation, so the same decision must
	// not clear it while an assignment is published.
	putWorkSet(2, []domainprojection.WorkItem{
		{TaskID: "task-presence", RunID: "run-1", ExecutionState: domainprojection.ExecutionRunning,
			WorkClass: domainprojection.WorkClassAssignedWork, RequiresHousePresence: true,
			ClassificationReasonCode: "core_assigned_work"},
	})
	decision, err = client.StopPlanning(context.Background(), "mio", "read")
	if err != nil {
		t.Fatalf("StopPlanning(read under presence work) error = %v", err)
	}
	if decision.Action != StopPlanningContinue || decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v; presence work keeps the presentation", decision)
	}

	// Revoking the activity is the only reason change a consumer may act on:
	// WORLD supplies the stop posture and the cleanup action from its own table.
	putLifePolicy(2, []string{"idle"}, []string{})
	decision, err = client.StopPlanning(context.Background(), "mio", "read")
	if err != nil {
		t.Fatalf("StopPlanning(read after the policy revoked it) error = %v", err)
	}
	if decision.Action != StopPlanningStop || decision.Reason != "activity_not_permitted" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.StopState != "standing" || strings.Join(decision.StopActions, ",") != "stop:read" {
		t.Fatalf("decision = %+v; the posture and cleanup come from WORLD's owner mapping", decision)
	}
	if decision.ReleaseOccupancy || decision.RetainOccupancyReason != "" ||
		decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v; no occupancy release exists without measured geometry (D-17, D-18)", decision)
	}

	// An activity outside WORLD's owner mapping is rejected rather than guessed
	// at, so CORE never receives a decision built on a caller-supplied meaning.
	decision, err = client.StopPlanning(context.Background(), "mio", "fly")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("StopPlanning(unknown activity) error = %v; want ErrContract", err)
	}
	if !isEmptyStopDecision(decision) {
		t.Fatalf("decision = %+v", decision)
	}

	// The plain assessment stays a plain assessment: it asks for no decision and
	// receives none.
	assessment, err := client.Assessment(context.Background(), "mio")
	if err != nil {
		t.Fatalf("Assessment() error = %v", err)
	}
	assertExecutableSubset(t, assessment)

	// An Actor outside CORE's configured view scope is refused before any request.
	unbound, err := New(Config{
		BaseURL:       baseURL,
		Credential:    token,
		WorldID:       target.WorldID,
		TimelineID:    target.TimelineID,
		TimelineEpoch: target.TimelineEpoch,
		ActorIDs:      []string{"shiro"},
	})
	if err != nil {
		t.Fatalf("New(unbound) error = %v", err)
	}
	decision, err = unbound.StopPlanning(context.Background(), "shiro", "read")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("StopPlanning(unbound actor) error = %v; want ErrForbidden", err)
	}
	if !isEmptyStopDecision(decision) {
		t.Fatalf("decision = %+v", decision)
	}

	stateAfter, err := realWorldStateSeq(t, client)
	if err != nil {
		t.Fatalf("state after stop planning error = %v", err)
	}
	if stateAfter != stateBefore {
		t.Fatalf("WORLD StateSeq moved from %d to %d; a planning decision changes nothing", stateBefore, stateAfter)
	}
}

func realWorldStateSeq(t *testing.T, client *Client) (uint64, error) {
	t.Helper()
	feed, err := client.ReadFeed(context.Background(), 0)
	if err != nil {
		return 0, err
	}
	return feed.Snapshot.StateSeq, nil
}
