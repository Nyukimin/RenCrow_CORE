//go:build realworld

package worldread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

// TestRealWorldAssessmentConsumesPublishedProjections sends CORE's own
// WorkSet/LifePolicy envelopes over WORLD's real projection route and reads
// the result back through CORE's read client. It is the cross-repo check for
// D-28 consumption: real binary, real credential scopes, real stored heads,
// and real projection payloads - no test double and no CORE production route.
func TestRealWorldAssessmentConsumesPublishedProjections(t *testing.T) {
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

	policy, err := domainprojection.NewLifePolicy(
		target.WorldID, target.ActorID, target.TimelineEpoch, 1, validUntil,
		"persona-e2e-1", "condition-e2e-1",
		[]string{"idle", "read", "walk", "rest"}, []string{"rest"},
	)
	if err != nil {
		t.Fatalf("NewLifePolicy() error = %v", err)
	}
	putRealProjection(t, baseURL, token, target, domainprojection.KindLifePolicy, mustRealEnvelope(t, target, domainprojection.KindLifePolicy, policy))

	emptyWorkSet, err := domainprojection.NewWorkSet(target.WorldID, target.ActorID, target.TimelineEpoch, 1, validUntil, []domainprojection.WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet(empty) error = %v", err)
	}
	putRealProjection(t, baseURL, token, target, domainprojection.KindWorkSet, mustRealEnvelope(t, target, domainprojection.KindWorkSet, emptyWorkSet))

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

	assessment, err := client.Assessment(context.Background(), "mio")
	if err != nil {
		t.Fatalf("Assessment(empty work set) error = %v", err)
	}
	if assessment.ActorID != "mio" || assessment.WorldID != target.WorldID ||
		assessment.TimelineID != target.TimelineID || assessment.TimelineEpoch != target.TimelineEpoch {
		t.Fatalf("scope = %+v", assessment)
	}
	if assessment.WorkSetState != "empty" || assessment.PolicyState != "fresh" {
		t.Fatalf("states = %q %q", assessment.WorkSetState, assessment.PolicyState)
	}
	if assessment.WorkPresentation.Kind != "none" || len(assessment.WorkPresentation.QueuedTaskIDs) != 0 {
		t.Fatalf("presentation = %+v", assessment.WorkPresentation)
	}
	if strings.Join(assessment.PermittedActivityIDs, ",") != "idle,read,rest,walk" {
		t.Fatalf("permitted = %v", assessment.PermittedActivityIDs)
	}
	assertExecutableSubset(t, assessment)
	if len(assessment.ExecutableActivityIDs) == 0 {
		t.Fatalf("executable = %v; want at least one candidate for an empty work set", assessment.ExecutableActivityIDs)
	}
	if !assessment.NewActivityAllowed || assessment.BlockedReason != "" || assessment.WorkSuccess {
		t.Fatalf("flags = %+v", assessment)
	}

	presentWorkSet, err := domainprojection.NewWorkSet(target.WorldID, target.ActorID, target.TimelineEpoch, 2, validUntil,
		[]domainprojection.WorkItem{
			{TaskID: "task-presence", RunID: "run-1", ExecutionState: domainprojection.ExecutionRunning, WorkClass: domainprojection.WorkClassAssignedWork, RequiresHousePresence: true, ClassificationReasonCode: "core_assigned_work"},
			{TaskID: "task-queued", ExecutionState: domainprojection.ExecutionQueued, WorkClass: domainprojection.WorkClassAssignedWork, ClassificationReasonCode: "core_assigned_work"},
			{TaskID: "task-waiting", ExecutionState: domainprojection.ExecutionWaiting, WorkClass: domainprojection.WorkClassAssignedWork, ClassificationReasonCode: "core_assigned_work"},
		})
	if err != nil {
		t.Fatalf("NewWorkSet(present) error = %v", err)
	}
	putRealProjection(t, baseURL, token, target, domainprojection.KindWorkSet, mustRealEnvelope(t, target, domainprojection.KindWorkSet, presentWorkSet))

	assessment, err = client.Assessment(context.Background(), "mio")
	if err != nil {
		t.Fatalf("Assessment(presence work set) error = %v", err)
	}
	if assessment.WorkSetState != "assigned" {
		t.Fatalf("work set state = %q", assessment.WorkSetState)
	}
	if assessment.WorkPresentation.Kind != "presence" ||
		strings.Join(assessment.WorkPresentation.PresenceTaskIDs, ",") != "task-presence" ||
		strings.Join(assessment.WorkPresentation.RunningTaskIDs, ",") != "task-presence" ||
		strings.Join(assessment.WorkPresentation.QueuedTaskIDs, ",") != "task-queued" ||
		strings.Join(assessment.WorkPresentation.WaitingTaskIDs, ",") != "task-waiting" {
		t.Fatalf("presentation = %+v", assessment.WorkPresentation)
	}
	assertExecutableSubset(t, assessment)
	if assessment.WorkSuccess {
		t.Fatalf("work_success = true; WORLD never claims work success")
	}

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
	if _, err := unbound.Assessment(context.Background(), "shiro"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Assessment(unbound actor) error = %v; want ErrForbidden", err)
	}
}

func assertExecutableSubset(t *testing.T, assessment Assessment) {
	t.Helper()
	permitted := make(map[string]bool, len(assessment.PermittedActivityIDs))
	for _, id := range assessment.PermittedActivityIDs {
		permitted[id] = true
	}
	previous := ""
	for _, id := range assessment.ExecutableActivityIDs {
		if !permitted[id] {
			t.Fatalf("executable %q is outside the permitted set %v", id, assessment.PermittedActivityIDs)
		}
		if id <= previous && previous != "" {
			t.Fatalf("executable set is not sorted: %v", assessment.ExecutableActivityIDs)
		}
		previous = id
	}
}

func mustRealEnvelope(t *testing.T, target domainprojection.Target, kind string, payload any) domainprojection.Envelope {
	t.Helper()
	envelope, err := domainprojection.NewEnvelope(target, kind, payload)
	if err != nil {
		t.Fatalf("NewEnvelope(%s) error = %v", kind, err)
	}
	return envelope
}

func putRealProjection(t *testing.T, baseURL, token string, target domainprojection.Target, kind string, envelope domainprojection.Envelope) {
	t.Helper()
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", kind, err)
	}
	worldTransport, err := encodeTransportID(target.WorldID)
	if err != nil {
		t.Fatalf("world transport ID: %v", err)
	}
	timelineTransport, err := encodeTransportID(target.TimelineID)
	if err != nil {
		t.Fatalf("timeline transport ID: %v", err)
	}
	path := fmt.Sprintf("%s/v1/worlds/%s/timelines/%s/core-projections/%s/%s",
		baseURL, worldTransport, timelineTransport, target.ActorTransportID, kind)
	request, err := http.NewRequest(http.MethodPut, path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build projection request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("put projection %s: %v", kind, err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode projection ack %s: %v", kind, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put projection %s status = %d: %v", kind, response.StatusCode, body)
	}
	projection, _ := body["projection"].(map[string]any)
	if status, _ := projection["status"].(string); status != "accepted" {
		t.Fatalf("put projection %s ack = %v", kind, body)
	}
}
