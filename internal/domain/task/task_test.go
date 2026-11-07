package task

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskDefaultsAndCanonicalIdentity(t *testing.T) {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	value := Task{Title: "write spec"}
	value.ApplyDefaults(now)
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if value.TaskID.Validate() != nil || value.Status != StatusQueued || value.Priority != PriorityNormal || value.Route != RouteGeneral || !value.UpdatedAt.Equal(now) {
		t.Fatalf("defaults = %#v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal legacy Task: %v", err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatalf("decode legacy Task JSON: %v", err)
	}
	if _, exists := legacy["production"]; exists {
		t.Fatalf("Task without production stage changed its canonical JSON: %s", encoded)
	}
}

func TestTaskProductionStageAndActivityMustMatch(t *testing.T) {
	for _, test := range []struct {
		stage    ProductionStage
		activity ProductionActivity
	}{
		{stage: ProductionStageIdeate, activity: ProductionActivityCreativeIdeation},
		{stage: ProductionStageAuthor, activity: ProductionActivityTextDraft},
		{stage: ProductionStageVerify, activity: ProductionActivityCreativeReview},
	} {
		production, err := NewProduction(test.stage)
		if err != nil || production.Stage != test.stage || production.Activity != test.activity {
			t.Fatalf("NewProduction(%q)=(%+v,%v), want activity %q", test.stage, production, err, test.activity)
		}
		value := validTask()
		value.Production = &production
		if err := value.Validate(); err != nil {
			t.Fatalf("Task with valid production %+v is invalid: %v", production, err)
		}
	}

	value := validTask()
	invalid := Production{Stage: ProductionStageAuthor, Activity: ProductionActivityCreativeReview}
	value.Production = &invalid
	if err := value.Validate(); err == nil {
		t.Fatal("Task accepted a production activity that does not match its stage")
	}
	value = validTask()
	invalid = Production{Stage: ProductionStage("draft"), Activity: ProductionActivityTextDraft}
	value.Production = &invalid
	if err := value.Validate(); err == nil {
		t.Fatal("Task accepted an unknown production stage")
	}
}

func TestTaskVersionedTextRuneProductionJSONRoundTrips(t *testing.T) {
	legacy := validTask()
	legacyProduction := Production{Stage: ProductionStageAuthor, Activity: ProductionActivityTextDraft}
	legacy.Production = &legacyProduction
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy production Task: %v", err)
	}
	var legacyObject map[string]json.RawMessage
	if err := json.Unmarshal(legacyJSON, &legacyObject); err != nil {
		t.Fatalf("decode legacy Task: %v", err)
	}
	var legacyProductionObject map[string]json.RawMessage
	if err := json.Unmarshal(legacyObject["production"], &legacyProductionObject); err != nil {
		t.Fatalf("decode legacy production metadata: %v", err)
	}
	if _, exists := legacyProductionObject["schema_version"]; exists {
		t.Fatalf("versionless production metadata changed: %s", legacyJSON)
	}
	if _, exists := legacyProductionObject["text_runes"]; exists {
		t.Fatalf("versionless production metadata gained text constraints: %s", legacyJSON)
	}

	var taskObject map[string]json.RawMessage
	if err := json.Unmarshal(legacyJSON, &taskObject); err != nil {
		t.Fatalf("decode Task fixture: %v", err)
	}
	taskObject["production"] = json.RawMessage(`{"schema_version":"creative-task.v0.2","stage":"author","activity":"text_draft","text_runes":{"min":128,"max":256}}`)
	versionedJSON, err := json.Marshal(taskObject)
	if err != nil {
		t.Fatalf("marshal versioned Task fixture: %v", err)
	}
	var versioned Task
	if err := json.Unmarshal(versionedJSON, &versioned); err != nil {
		t.Fatalf("unmarshal versioned Task: %v", err)
	}
	if err := versioned.Validate(); err != nil {
		t.Fatalf("validate versioned Task: %v", err)
	}
	reloadedJSON, err := json.Marshal(versioned)
	if err != nil {
		t.Fatalf("marshal reloaded versioned Task: %v", err)
	}
	var reloadedObject map[string]json.RawMessage
	if err := json.Unmarshal(reloadedJSON, &reloadedObject); err != nil {
		t.Fatalf("decode reloaded Task: %v", err)
	}
	var reloadedProduction map[string]json.RawMessage
	if err := json.Unmarshal(reloadedObject["production"], &reloadedProduction); err != nil {
		t.Fatalf("decode reloaded production metadata: %v", err)
	}
	if string(reloadedProduction["schema_version"]) != `"creative-task.v0.2"` ||
		string(reloadedProduction["text_runes"]) != `{"min":128,"max":256}` {
		t.Fatalf("versioned production metadata did not survive Task JSON reload: %s", reloadedJSON)
	}
}

func TestTaskWaitingRequiresMachineReasonAndHasNoHumanStatus(t *testing.T) {
	value := validTask()
	value.Status = StatusWaiting
	if err := value.Validate(); err == nil {
		t.Fatal("waiting without reason was accepted")
	}
	value.WaitingReason = "dependency task is still running"
	if err := value.Validate(); err != nil {
		t.Fatalf("waiting with reason: %v", err)
	}
	if ValidStatus(Status("waiting_user")) {
		t.Fatal("waiting_user remains a valid Task status")
	}
}

func TestTaskIdentityAndRelationshipValidation(t *testing.T) {
	value := validTask()
	value.ParentTaskID = value.TaskID
	if err := value.Validate(); err == nil {
		t.Fatal("self parent was accepted")
	}
	value = validTask()
	value.DependencyTaskIDs = []modulecore.TaskID{value.TaskID}
	if err := value.Validate(); err == nil {
		t.Fatal("self dependency was accepted")
	}
	value = validTask()
	value.DependencyTaskIDs = []modulecore.TaskID{modulecore.NewTaskID(), modulecore.NewTaskID()}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid dependencies: %v", err)
	}
}

func TestTaskRejectsUnknownEnumsAndRegressingTimestamp(t *testing.T) {
	for name, mutate := range map[string]func(*Task){
		"route":            func(value *Task) { value.Route = Route("DIRECT") },
		"routing event id": func(value *Task) { value.RoutingEventID = modulecore.EventID("invalid-event-id") },
		"assignment event id": func(value *Task) {
			value.AssignmentEventID = modulecore.EventID("invalid-event-id")
		},
		"interrupt policy": func(value *Task) { value.InterruptPolicy = InterruptPolicy("ask_user") },
		"updated at":       func(value *Task) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			value := validTask()
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid Task was accepted")
			}
		})
	}
}

func TestTaskRejectsInvalidExpectedCriteriaRevision(t *testing.T) {
	value := validTask()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["expected_criteria_revision"] = strings.Repeat("A", 64)
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := value.Validate(); err == nil {
		t.Fatal("Task accepted an uppercase criteria SHA-256 revision")
	}
}

func TestTaskOriginPublicationProvenanceValidationAndJSONRoundTrip(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	address, err := conversation.NewChannelAddress("viewer", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	incarnation := "0123456789abcdef0123456789abcdef"
	binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
		sessionID, address, conversation.SessionAudiencePublic, "", 0, incarnation,
	)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := NewOriginPublicationProvenance(binding)
	if err != nil {
		t.Fatalf("NewOriginPublicationProvenance: %v", err)
	}
	task := validTask()
	task.OriginSessionID = sessionID
	task.OriginPublication = &provenance
	if err := task.Validate(); err != nil {
		t.Fatalf("Task with current public origin proof is invalid: %v", err)
	}

	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Task
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped Task is invalid: %v", err)
	}
	if decoded.OriginPublication == nil || *decoded.OriginPublication != provenance {
		t.Fatalf("round-tripped origin publication = %#v, want %#v", decoded.OriginPublication, provenance)
	}
	if decoded.OwnerID != task.OwnerID {
		t.Fatalf("publication provenance changed Agent Task.OwnerID: got %q want %q", decoded.OwnerID, task.OwnerID)
	}

	invalid := task
	bad := provenance
	bad.PublicationIncarnation = "legacy-or-invalid"
	invalid.OriginPublication = &bad
	if err := invalid.Validate(); err == nil {
		t.Fatal("Task accepted an invalid origin publication incarnation")
	}
	invalid = task
	bad = provenance
	bad.Audience = conversation.SessionAudiencePrivate
	bad.OwnerID = ""
	invalid.OriginPublication = &bad
	if err := invalid.Validate(); err == nil {
		t.Fatal("Task accepted private origin provenance without an authenticated User owner")
	}
}

func TestTaskSupportsEveryOrchestratorRouteAndEventReferences(t *testing.T) {
	routes := []Route{
		RouteCHAT,
		RoutePLAN,
		RouteANALYZE,
		RouteOperations,
		RouteResearch,
		RouteWILD,
		RouteCode,
		RouteCODE1,
		RouteCODE2,
		RouteCODE3,
		RouteCODE4,
		RouteGeneral,
	}
	for _, route := range routes {
		t.Run(string(route), func(t *testing.T) {
			value := validTask()
			value.Route = route
			value.RoutingEventID = modulecore.NewEventID()
			value.AssignmentEventID = modulecore.NewEventID()
			if err := value.Validate(); err != nil {
				t.Fatalf("route %q: Validate: %v", route, err)
			}
		})
	}
}

func TestTaskTransitionsAndNotification(t *testing.T) {
	if !CanTransition(StatusQueued, StatusRunning) || !CanTransition(StatusQueued, StatusFailed) || !CanTransition(StatusRunning, StatusWaiting) || !CanTransition(StatusWaiting, StatusQueued) {
		t.Fatal("expected lifecycle transitions are unavailable")
	}
	if CanTransition(StatusSucceeded, StatusRunning) || CanTransition(StatusQueued, StatusSucceeded) {
		t.Fatal("invalid lifecycle transition accepted")
	}
	value := validTask()
	value.Status = StatusSucceeded
	notification := NewNotification(value, value.UpdatedAt)
	if notification.Type != "task.notification" || notification.TaskID != value.TaskID || notification.Level != NotificationDone || !notification.Interrupt {
		t.Fatalf("notification = %#v", notification)
	}
	value.InterruptPolicy = InterruptSilent
	if ShouldNotify(value) {
		t.Fatal("silent Task should not notify")
	}
}

func TestQueuedTaskCanFailBeforeStart(t *testing.T) {
	if !CanTransition(StatusQueued, StatusFailed) {
		t.Fatal("routing or pre-start failure must be able to fail a queued task")
	}
	if CanTransition(StatusQueued, StatusSucceeded) {
		t.Fatal("queued task must not succeed before it starts")
	}
}

func validTask() Task {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	value := Task{TaskID: modulecore.NewTaskID(), Title: "task", Status: StatusQueued, Priority: PriorityNormal, Route: RouteCode, InterruptPolicy: InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	return value
}
