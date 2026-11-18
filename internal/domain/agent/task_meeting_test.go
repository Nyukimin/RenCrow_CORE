package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskMeetingTestContextKey = "task-meeting-test-context"

type taskMeetingTestProvider struct {
	name          string
	content       string
	request       llm.GenerateRequest
	observation   llm.ExecutionObservation
	contextMarker any
	admissionHits int
	toolCalls     bool
}

func (p *taskMeetingTestProvider) Name() string { return p.name }

func (p *taskMeetingTestProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	if request.RequestAdmission == nil {
		return llm.GenerateResponse{}, errors.New("missing one-shot request admission")
	}
	if err := request.RequestAdmission.Admit(); err != nil {
		return llm.GenerateResponse{}, err
	}
	p.admissionHits++
	p.request = request
	p.observation, _ = llm.ExecutionObservationFromContext(ctx)
	if ctx.Value(taskMeetingTestContextKey) != p.contextMarker {
		return llm.GenerateResponse{}, errors.New("runtime context was not preserved")
	}
	return llm.GenerateResponse{Content: p.content, HasToolCalls: p.toolCalls}, nil
}

type taskMeetingTestActor struct {
	actor    TaskMeetingActor
	id       string
	provider *taskMeetingTestProvider
	mutate   func()
}

func TestTaskMeetingActorsPrepareFreezeRestoreAndGenerateClosedInputs(t *testing.T) {
	actors := newTaskMeetingTestActors(t)
	for _, fixture := range actors {
		t.Run(fixture.id, func(t *testing.T) {
			requireTaskMeetingActorHasNoNativePlanningBinding(t, fixture.actor)
			prepareRequest := newTaskMeetingPrepareFixture(t, fixture.id)
			ctxMarker := &struct{ value string }{value: fixture.id}
			ctx := context.WithValue(context.Background(), taskMeetingTestContextKey, ctxMarker)
			fixture.provider.contextMarker = ctxMarker

			prepared, err := fixture.actor.PrepareTaskMeeting(ctx, prepareRequest)
			if err != nil {
				t.Fatalf("PrepareTaskMeeting: %v", err)
			}
			requestJSON := prepared.FrozenRequestJSON()
			inputJSON := prepared.FrozenInputJSON()
			if len(requestJSON) == 0 || len(inputJSON) == 0 || len(inputJSON) > domaintask.TaskMeetingMaxFrozenInputBytes {
				t.Fatalf("prepared bytes have invalid sizes: request=%d input=%d", len(requestJSON), len(inputJSON))
			}
			if bytes.Contains(requestJSON, []byte("private coder instructions")) || bytes.Contains(requestJSON, prepareRequest.CurrentTurn.GenerationInputJSON) {
				t.Fatal("prepared provider input contains private coder instructions or the full recursive seed")
			}
			if !bytes.Contains(requestJSON, []byte("speaker_actor_id")) || !bytes.Contains(requestJSON, []byte("accepted-context-body")) {
				t.Fatal("prepared input omitted the accepted group context")
			}
			var frozen taskMeetingProviderRequest
			if err := decodeTaskMeetingJSON(requestJSON, &frozen); err != nil {
				t.Fatalf("decode frozen provider request: %v", err)
			}
			if strings.Contains(frozen.SystemPrompt, "private coder instructions") || !strings.Contains(frozen.SystemPrompt, fixture.id+" persona before") {
				t.Fatalf("provider system prompt is not the canonical persona snapshot: %q", frozen.SystemPrompt)
			}
			mutableCopy := prepared.FrozenRequestJSON()
			mutableCopy[0] = 'x'
			if !bytes.Equal(prepared.FrozenRequestJSON(), requestJSON) {
				t.Fatal("FrozenRequestJSON exposed mutable prepared storage")
			}
			intentCopy := prepared.GenerationIntent()
			intentCopy.ActorID = "spoofed"
			if prepared.GenerationIntent().ActorID != fixture.id {
				t.Fatal("GenerationIntent exposed mutable prepared storage")
			}

			fixture.mutate()
			if !bytes.Equal(prepared.FrozenRequestJSON(), requestJSON) {
				t.Fatal("live persona mutation changed prepared provider bytes")
			}

			task, run := newTaskMeetingGenerationTaskFixture(t, prepareRequest, prepared)
			restored, err := fixture.actor.RestoreTaskMeetingPreparedGeneration(task, run, prepareRequest.Intent.SeedSHA256)
			if err != nil {
				t.Fatalf("RestoreTaskMeetingPreparedGeneration: %v", err)
			}
			if !bytes.Equal(restored.FrozenRequestJSON(), requestJSON) || !bytes.Equal(restored.FrozenInputJSON(), inputJSON) {
				t.Fatal("recovered prepared bytes differ from their durable input")
			}
			if !sameTaskMeetingGenerationIntent(prepared.GenerationIntent(), restored.GenerationIntent()) {
				t.Fatalf("restored generation intent differs from the pre-admission intent: before=%+v after=%+v", prepared.GenerationIntent(), restored.GenerationIntent())
			}
			if _, err := fixture.actor.RestoreTaskMeetingPreparedGeneration(task, run, taskMeetingSHA256([]byte("wrong durable seed"))); !errors.Is(err, ErrTaskMeetingActorReplay) {
				t.Fatalf("restore with wrong Coordinator seed = %v, want replay rejection", err)
			}

			var admissionHits int
			admission := llm.NewGenerateRequestAdmission(func() error {
				admissionHits++
				return nil
			})
			unboundAdmission := llm.NewGenerateRequestAdmission(func() error { return nil })
			if _, err := fixture.actor.GenerateTaskMeeting(ctx, restored, task, run, unboundAdmission); !errors.Is(err, ErrTaskMeetingActorUnbound) {
				t.Fatalf("GenerateTaskMeeting without native identity = %v, want actor identity denial", err)
			}
			wrongContext, err := execution.WithIdentity(ctx, modulecore.NewTaskID(), run.RunID, run.TraceID)
			if err != nil {
				t.Fatalf("bind mismatched identity fixture: %v", err)
			}
			if _, err := fixture.actor.GenerateTaskMeeting(wrongContext, restored, task, run, llm.NewGenerateRequestAdmission(func() error { return nil })); !errors.Is(err, ErrTaskMeetingActorUnbound) {
				t.Fatalf("GenerateTaskMeeting with mismatched native identity = %v, want actor identity denial", err)
			}
			effectCtx, err := execution.WithIdentity(ctx, task.TaskID, run.RunID, run.TraceID)
			if err != nil {
				t.Fatalf("bind exact execution identity: %v", err)
			}
			result, err := fixture.actor.GenerateTaskMeeting(effectCtx, prepared, task, run, admission)
			if err != nil {
				t.Fatalf("GenerateTaskMeeting: %v", err)
			}
			if result.Report == nil || result.Answer != nil || result.Report.Current.TaskRef != nil {
				t.Fatalf("semantic report result is not bound to the allowed task reference: %+v", result)
			}
			if fixture.provider.admissionHits != 1 || admissionHits != 1 {
				t.Fatalf("request admission consumed provider=%d capability=%d times", fixture.provider.admissionHits, admissionHits)
			}
			if fixture.provider.observation.TaskID != task.TaskID || fixture.provider.observation.TraceID != string(run.TraceID) ||
				fixture.provider.observation.Initiator != fixture.id || fixture.provider.observation.Purpose != string(domaintask.TaskMeetingPurposeReport) {
				t.Fatalf("provider did not receive actual Task/Run observation: %+v", fixture.provider.observation)
			}
			if fixture.provider.request.RequestAdmission == nil || fixture.provider.request.OnToken != nil || fixture.provider.request.OnMetrics != nil || fixture.provider.request.OnLogicalOutcomeEvent != nil {
				t.Fatal("provider request has missing admission or a serialized callback")
			}
			gotJSON, err := json.Marshal(taskMeetingProviderRequest{
				Messages: fixture.provider.request.Messages, MaxTokens: fixture.provider.request.MaxTokens,
				Temperature: fixture.provider.request.Temperature, SystemPrompt: fixture.provider.request.SystemPrompt,
				ResponseFormat: fixture.provider.request.ResponseFormat, ReasoningEffort: fixture.provider.request.ReasoningEffort,
				ProviderOptions: fixture.provider.request.ProviderOptions, LogicalOutcomeStream: fixture.provider.request.LogicalOutcomeStream,
			})
			if err != nil || !bytes.Equal(gotJSON, requestJSON) {
				t.Fatal("provider did not receive the exact frozen request")
			}
		})
	}
}

func TestTaskMeetingPrepareRejectsChangedIdentityAndUnboundedPrompt(t *testing.T) {
	provider := &taskMeetingTestProvider{name: "fake", content: `{"body":"x","current":{"state":"none"},"next":{"state":"none"},"concern_declaration":"none","concerns":[]}`}
	actor := NewMioAgent(provider, nil, nil, nil, nil, nil).WithTaskMeetingActorID("mio")
	base := newTaskMeetingPrepareFixture(t, "mio")
	wrongActor := base
	wrongActor.Intent.ActorID = "shiro"
	if _, err := actor.PrepareTaskMeeting(context.Background(), wrongActor); !errors.Is(err, ErrTaskMeetingActorUnbound) {
		t.Fatalf("wrong actor prepare = %v, want bound actor rejection", err)
	}
	wrongPurpose := base
	wrongPurpose.Intent.Purpose = domaintask.TaskMeetingPurposeAnswer
	if _, err := actor.PrepareTaskMeeting(context.Background(), wrongPurpose); !errors.Is(err, ErrTaskMeetingActorInput) {
		t.Fatalf("wrong purpose prepare = %v, want input rejection", err)
	}
	oversized := base
	oversized.SourceMessages = append([]TaskMeetingSourceMessage(nil), base.SourceMessages...)
	oversized.SourceMessages[0].Body = strings.Repeat("x", TaskMeetingMaxMessageBytes+1)
	oversized.SourceMessages[0].Proof.Reference.BodySHA256 = taskMeetingSHA256([]byte(oversized.SourceMessages[0].Body))
	proofs := []domaintask.TaskMeetingConsumedMessage{oversized.SourceMessages[0].Proof}
	oversized.Intent.SourceContextSHA256, _ = domaintask.TaskMeetingConsumedMessagesSHA256(proofs)
	if _, err := actor.PrepareTaskMeeting(context.Background(), oversized); !errors.Is(err, ErrTaskMeetingActorInput) {
		t.Fatalf("oversized body prepare = %v, want bounded rejection", err)
	}
	noAdmission := TaskMeetingPreparedGeneration{}
	if _, err := actor.GenerateTaskMeeting(context.Background(), noAdmission, domaintask.Task{}, domaintask.Run{}, nil); !errors.Is(err, ErrTaskMeetingActorEffect) && !errors.Is(err, ErrTaskMeetingActorUnbound) {
		t.Fatalf("unadmitted generation = %v, want no-effect rejection", err)
	}
}

func TestTaskMeetingSemanticOutputRejectsInventedIDsAndRequiresExplicitStates(t *testing.T) {
	taskRef := modulecore.NewTaskID()
	statuses := []TaskMeetingShareableStatus{{ActorID: "mio", TaskRef: taskRef, Status: domaintask.StatusRunning}}
	valid := fmt.Sprintf(`{"body":"report","current":{"state":"in_progress","text":"working","task_ref":%q},"next":{"state":"none"},"concern_declaration":"none","concerns":[]}`, taskRef)
	if result, err := parseTaskMeetingGeneratedResponse(domaintask.TaskMeetingPurposeReport, valid, statuses); err != nil || result.Report == nil || result.Report.Current.TaskRef == nil || *result.Report.Current.TaskRef != taskRef {
		t.Fatalf("allowlisted report reference rejected: result=%+v err=%v", result, err)
	}
	for _, tc := range []struct {
		name     string
		purpose  domaintask.TaskMeetingPurpose
		content  string
		statuses []TaskMeetingShareableStatus
	}{
		{name: "unallowlisted task ref", purpose: domaintask.TaskMeetingPurposeReport, content: fmt.Sprintf(`{"body":"report","current":{"state":"in_progress","text":"working","task_ref":%q},"next":{"state":"none"},"concern_declaration":"none","concerns":[]}`, modulecore.NewTaskID()), statuses: statuses},
		{name: "model supplied actor identity", purpose: domaintask.TaskMeetingPurposeReport, content: `{"actor_id":"mio","body":"report","current":{"state":"none"},"next":{"state":"none"},"concern_declaration":"none","concerns":[]}`},
		{name: "missing field is not none", purpose: domaintask.TaskMeetingPurposeReport, content: `{"body":"report","next":{"state":"none"},"concern_declaration":"none","concerns":[]}`},
		{name: "answer cannot choose concern id", purpose: domaintask.TaskMeetingPurposeAnswer, content: `{"body":"answer","kind":"advice","concern_id":"made-up"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTaskMeetingGeneratedResponse(tc.purpose, tc.content, tc.statuses); !errors.Is(err, ErrTaskMeetingActorOutput) {
				t.Fatalf("semantic output = %v, want rejection", err)
			}
		})
	}
	if result, err := parseTaskMeetingGeneratedResponse(domaintask.TaskMeetingPurposeReport, `{"body":"report","current":{"state":"none"},"next":{"state":"undecided"},"concern_declaration":"not_disclosed","concerns":[]}`, nil); err != nil || result.Report == nil {
		t.Fatalf("explicit none/undecided values rejected: result=%+v err=%v", result, err)
	}
	undecided := fmt.Sprintf(`{"body":"report","current":{"state":"undecided"},"next":{"state":"undecided","text":"The next action is not established yet.","task_ref":%q},"concern_declaration":"none","concerns":[]}`, taskRef)
	if result, err := parseTaskMeetingGeneratedResponse(domaintask.TaskMeetingPurposeReport, undecided, statuses); err != nil || result.Report == nil || result.Report.Current.State != domainmeeting.ReportFieldUndecided || result.Report.Next.TaskRef == nil || *result.Report.Next.TaskRef != taskRef {
		t.Fatalf("partial-view undecided fields rejected: result=%+v err=%v", result, err)
	}
	if result, err := parseTaskMeetingGeneratedResponse(domaintask.TaskMeetingPurposeAnswer, `{"body":"answer","kind":"advice"}`, nil); err != nil || result.Answer == nil {
		t.Fatalf("valid semantic answer rejected: result=%+v err=%v", result, err)
	}
}

func TestTaskMeetingActorsReceiveThroughTheirOwnContextConsumer(t *testing.T) {
	for _, fixture := range newTaskMeetingTestActors(t) {
		t.Run(fixture.id, func(t *testing.T) {
			requireTaskMeetingActorHasNoNativePlanningBinding(t, fixture.actor)
			task, run, _, request := newTaskMeetingRecipientFixture(t, fixture.id)
			var authorized bool
			authorize := domaintask.TaskMeetingMessageReadAuthorizationCheck(func(_ context.Context, gotTask domaintask.Task, gotRun domaintask.Run, _ domaintask.SharedRoleContext, gotRef domaintask.TaskMeetingMessageReference) error {
				if gotTask.TaskID != task.TaskID || gotRun.RunID != run.RunID || gotRef != request.Reference {
					return errors.New("authorization callback received a different recipient or message")
				}
				authorized = true
				return nil
			})
			request.AuthorizeRead = authorize
			prepared, err := fixture.actor.PrepareTaskMeetingReceive(context.Background(), request)
			if err != nil {
				t.Fatalf("PrepareTaskMeetingReceive: %v", err)
			}
			consumer := &taskMeetingTestConsumer{task: task, run: run, expectedBody: request.Body}
			proof, err := fixture.actor.ReceiveTaskMeetingMessage(context.Background(), prepared, consumer)
			if err != nil {
				t.Fatalf("ReceiveTaskMeetingMessage: %v", err)
			}
			if !authorized || consumer.calls != 1 || consumer.actorID != fixture.id || consumer.body != request.Body || consumer.taskID != task.TaskID || consumer.runID != run.RunID || proof.Reference != request.Reference {
				t.Fatalf("consumer identity/body/auth mismatch: authorized=%v calls=%d actor=%q body_matches=%v task=%q run=%q proof=%+v", authorized, consumer.calls, consumer.actorID, consumer.body == request.Body, consumer.taskID, consumer.runID, proof)
			}
			badAudience := request
			badAudience.Audience = []string{"shiro"}
			if _, err := fixture.actor.PrepareTaskMeetingReceive(context.Background(), badAudience); !errors.Is(err, ErrTaskMeetingActorInput) {
				t.Fatalf("non-target audience receive = %v, want rejection", err)
			}
		})
	}
}

func TestTaskMeetingActorBindingsRejectMismatchedRegisteredIdentity(t *testing.T) {
	for _, fixture := range newTaskMeetingTestActors(t) {
		t.Run(fixture.id, func(t *testing.T) {
			request := newTaskMeetingPrepareFixture(t, fixture.id)
			request.Intent.ActorID = "mio"
			if fixture.id == "mio" {
				request.Intent.ActorID = "shiro"
			}
			if _, err := fixture.actor.PrepareTaskMeeting(context.Background(), request); !errors.Is(err, ErrTaskMeetingActorUnbound) {
				t.Fatalf("PrepareTaskMeeting with mismatched registered identity = %v, want actor binding rejection", err)
			}
		})
	}
}

type taskMeetingTestConsumer struct {
	task         domaintask.Task
	run          domaintask.Run
	calls        int
	taskID       modulecore.TaskID
	runID        modulecore.RunID
	actorID      string
	body         string
	expectedBody string
}

func (c *taskMeetingTestConsumer) AppendTaskMeetingConsumedMessage(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingMessageReference, body string, authorize domaintask.TaskMeetingMessageReadAuthorizationCheck) (domaintask.TaskMeetingConsumedMessage, error) {
	c.calls++
	c.taskID, c.runID, c.actorID, c.body = taskID, runID, c.task.Assignee, body
	if body != c.expectedBody || taskID != c.task.TaskID || runID != c.run.RunID || taskMeetingSHA256([]byte(body)) != reference.BodySHA256 {
		return domaintask.TaskMeetingConsumedMessage{}, errors.New("consumer did not receive the exact canonical body for its Task/Run")
	}
	if err := authorize(ctx, c.task, c.run, domaintask.SharedRoleContext{}, reference); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	return domaintask.TaskMeetingConsumedMessage{
		Reference: reference, ActorID: c.task.Assignee, ConsumerTaskID: taskID, ConsumerRunID: runID,
		ContextSHA256: taskMeetingSHA256([]byte("accepted-context")), AcceptedAt: time.Now().UTC(), EvidenceID: modulecore.NewEvidenceID(),
	}, nil
}

func newTaskMeetingTestActors(t *testing.T) []taskMeetingTestActor {
	t.Helper()
	actorIDs := []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}
	fixtures := make([]taskMeetingTestActor, 0, len(actorIDs))
	for _, id := range actorIDs {
		provider := &taskMeetingTestProvider{name: "fake-" + id}
		provider.content = fmt.Sprintf(`{"body":"%s report","current":{"state":"in_progress","text":"working"},"next":{"state":"none"},"concern_declaration":"none","concerns":[]}`, id)
		var actor TaskMeetingActor
		var mutate func()
		switch id {
		case "mio":
			value := NewMioAgent(provider, nil, nil, nil, nil, nil).WithTaskMeetingActorID(id).WithSystemPrompt(id + " persona before")
			actor = value
			mutate = func() { value.WithSystemPrompt(id + " persona after") }
		case "shiro":
			value := NewShiroAgent(provider, nil, nil, id+" prompt before", nil).WithTaskMeetingActorID(id).WithPersona(AgentPersona{Name: id, Personality: id + " persona before"})
			actor = value
			mutate = func() { value.WithPersona(AgentPersona{Name: id, Personality: id + " persona after"}) }
		case "kuro":
			value := NewHeavyAgent(provider, id+" persona before").WithTaskMeetingActorID(id)
			actor = value
			mutate = func() { value.systemPrompt = id + " persona after" }
		case "midori":
			value := NewWildAgent(provider, id+" persona before").WithTaskMeetingActorID(id)
			actor = value
			mutate = func() { value.systemPrompt = id + " persona after" }
		default:
			value := NewCoderAgent(provider, nil, nil, "private coder instructions").WithTaskMeetingActorID(id).WithPersona(AgentPersona{Name: id, Personality: id + " persona before", Tone: "calm"})
			actor = value
			mutate = func() { value.WithPersona(AgentPersona{Name: id, Personality: id + " persona after", Tone: "loud"}) }
		}
		fixtures = append(fixtures, taskMeetingTestActor{actor: actor, id: id, provider: provider, mutate: mutate})
	}
	return fixtures
}

func requireTaskMeetingActorHasNoNativePlanningBinding(t *testing.T, actor TaskMeetingActor) {
	t.Helper()
	switch concrete := actor.(type) {
	case *MioAgent:
		if concrete.nativePlanningPort != nil || concrete.nativePlanningActor != "" {
			t.Fatal("TaskMeeting fixture unexpectedly configured the TaskCenter planning binding")
		}
	case *ShiroAgent:
		if concrete.nativePlanningPort != nil || concrete.nativePlanningActor != "" {
			t.Fatal("TaskMeeting fixture unexpectedly configured the TaskCenter planning binding")
		}
	case *HeavyAgent:
		if concrete.nativePlanningPort != nil || concrete.nativePlanningActor != "" {
			t.Fatal("TaskMeeting fixture unexpectedly configured the TaskCenter planning binding")
		}
	case *WildAgent:
		if concrete.nativePlanningPort != nil || concrete.nativePlanningActor != "" {
			t.Fatal("TaskMeeting fixture unexpectedly configured the TaskCenter planning binding")
		}
	case *CoderAgent:
		return
	default:
		t.Fatalf("unexpected TaskMeeting actor test type %T", actor)
	}
}

func newTaskMeetingPrepareFixture(t *testing.T, actorID string) TaskMeetingPrepareRequest {
	t.Helper()
	audience := []string{"aka", "ao", "gin", "kin", "kuro", "midori", "mio", "shiro"}
	configJSON := []byte(`{"audience":"scheduled"}`)
	configSHA := taskMeetingSHA256(configJSON)
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(audience)
	if err != nil {
		t.Fatalf("TaskMeetingAudienceSHA256: %v", err)
	}
	seed := []byte(`{"seed":"coordinator meeting input"}`)
	seedSHA := taskMeetingSHA256(seed)
	rootTaskID, rootRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	recipientTaskID, recipientRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	scope := domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: modulecore.NewEvidenceID(), Generation: 3, Epoch: 2}
	body := "accepted-context-body"
	proof := domaintask.TaskMeetingConsumedMessage{
		Reference: domaintask.TaskMeetingMessageReference{
			OccurrenceKey: "occurrence-20261118", SessionID: modulecore.NewSessionID(), ThreadID: modulecore.NewThreadID(),
			MessageID: modulecore.NewMessageID(), EventID: modulecore.NewEventID(), BodySHA256: taskMeetingSHA256([]byte(body)),
			AudienceSHA256: audienceSHA, ConfigRevision: 4, ConfigSHA256: configSHA,
			AttemptGeneration: 1, AttemptEpoch: 1, ScopeEvidenceID: modulecore.NewEvidenceID(),
		},
		ActorID: actorID, ConsumerTaskID: recipientTaskID, ConsumerRunID: recipientRunID,
		ContextSHA256: taskMeetingSHA256([]byte("previous context")), AcceptedAt: time.Now().UTC(), EvidenceID: modulecore.NewEvidenceID(),
	}
	proofs := []domaintask.TaskMeetingConsumedMessage{proof}
	sourceSHA, err := domaintask.TaskMeetingConsumedMessagesSHA256(proofs)
	if err != nil {
		t.Fatalf("TaskMeetingConsumedMessagesSHA256: %v", err)
	}
	requestID := modulecore.NewRequestID()
	turn := domainmeeting.Turn{
		Kind: domainmeeting.TurnReport, ActorID: actorID, GenerationRequestID: string(requestID),
		GenerationInputSHA256: seedSHA, GenerationInputJSON: append(json.RawMessage(nil), seed...),
		Generation: scope.Generation, Epoch: scope.Epoch, ScopeEvidenceID: scope.ScopeEvidenceID,
	}
	statusRef := modulecore.NewTaskID()
	return TaskMeetingPrepareRequest{
		Intent: TaskMeetingPrepareIntent{
			OccurrenceKey: "occurrence-20261118", Purpose: domaintask.TaskMeetingPurposeReport,
			CoordinatorActorID: "mio", ActorID: actorID, RequestID: requestID, SeedSHA256: seedSHA, Scope: scope,
			ConfigRevision: 4, ConfigSHA256: configSHA, RegistrationEvidenceID: modulecore.NewEvidenceID(), Audience: audience,
			RootTaskID: rootTaskID, RootRunID: rootRunID, RecipientTaskID: recipientTaskID, RecipientRunID: recipientRunID,
			SourceContextSHA256: sourceSHA,
		},
		CurrentTurn:       turn,
		SourceMessages:    []TaskMeetingSourceMessage{{Proof: proof, SpeakerActorID: "shiro", Body: body, Audience: append([]string(nil), audience...)}},
		ShareableStatuses: []TaskMeetingShareableStatus{{ActorID: actorID, TaskRef: statusRef, Status: domaintask.StatusRunning}},
	}
}

func newTaskMeetingGenerationTaskFixture(t *testing.T, request TaskMeetingPrepareRequest, prepared TaskMeetingPreparedGeneration) (domaintask.Task, domaintask.Run) {
	t.Helper()
	timing := modulecore.TaskTiming{Timezone: "Asia/Tokyo"}
	frozenConfig := []byte(`{"audience":"scheduled"}`)
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(request.Intent.Audience)
	if err != nil {
		t.Fatalf("TaskMeetingAudienceSHA256: %v", err)
	}
	frozenInput := prepared.FrozenInputJSON()
	invocation := domaintask.TaskMeetingInvocation{
		OccurrenceKey: request.Intent.OccurrenceKey, Purpose: request.Intent.Purpose,
		CoordinatorActorID: request.Intent.CoordinatorActorID, ActorID: request.Intent.ActorID,
		ConfiguredOwnerUserID: "meeting-owner", ConfigRevision: request.Intent.ConfigRevision,
		ConfigSHA256: taskMeetingSHA256(frozenConfig), RegistrationEvidenceID: request.Intent.RegistrationEvidenceID,
		FrozenConfig: frozenConfig, Audience: append([]string(nil), request.Intent.Audience...), AudienceSHA256: audienceSHA,
		InputSHA256: taskMeetingSHA256(frozenInput), SourceContextSHA256: request.Intent.SourceContextSHA256,
		FrozenInput: frozenInput, FrozenSourceMessages: sourceProofs(request.SourceMessages), GenerationScope: request.Intent.Scope,
		GenerationRequestID: request.Intent.RequestID, Timing: timing,
		ParentTaskID: request.Intent.RecipientTaskID, ParentRunID: request.Intent.RecipientRunID,
		AdmissionEvidenceID: modulecore.NewEvidenceID(),
	}
	if invocation.AdmissionEvidenceID == invocation.RegistrationEvidenceID {
		invocation.AdmissionEvidenceID = modulecore.NewEvidenceID()
	}
	taskID := modulecore.NewTaskID()
	task := domaintask.Task{
		TaskID: taskID, Assignee: request.Intent.ActorID, Status: domaintask.StatusRunning,
		ParentTaskID: request.Intent.RecipientTaskID, ReadOnly: true, Timing: &timing,
		TaskMeetingInvocation: &invocation,
	}
	run := domaintask.Run{
		RunID: modulecore.NewRunID(), TaskID: taskID, TraceID: modulecore.NewTraceID(),
		StartReason: domaintask.RunStartReasonFirst, Assignee: request.Intent.ActorID,
		Status: domaintask.RunStatusRunning, StartedAt: time.Now().UTC(),
	}
	return task, run
}

func newTaskMeetingRecipientFixture(t *testing.T, actorID string) (domaintask.Task, domaintask.Run, domaintask.TaskMeetingGenerationScope, TaskMeetingReceiveRequest) {
	t.Helper()
	audience := []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(audience)
	if err != nil {
		t.Fatalf("TaskMeetingAudienceSHA256: %v", err)
	}
	config := []byte(`{"audience":"scheduled"}`)
	timing := modulecore.TaskTiming{Timezone: "Asia/Tokyo"}
	rootTaskID, rootRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	contextTaskID := modulecore.NewTaskID()
	contextRunID := modulecore.NewRunID()
	invocation := domaintask.TaskMeetingInvocation{
		OccurrenceKey: "occurrence-20261118", Purpose: domaintask.TaskMeetingPurposeRecipientContext,
		CoordinatorActorID: "mio", ActorID: actorID, ConfiguredOwnerUserID: "meeting-owner", ConfigRevision: 4,
		ConfigSHA256: taskMeetingSHA256(config), RegistrationEvidenceID: modulecore.NewEvidenceID(), FrozenConfig: config,
		Audience: audience, AudienceSHA256: audienceSHA, InputSHA256: taskMeetingSHA256([]byte(`{"purpose":"recipient_context"}`)),
		FrozenInput: []byte(`{"purpose":"recipient_context"}`), Timing: timing,
		ParentTaskID: rootTaskID, ParentRunID: rootRunID, AdmissionEvidenceID: modulecore.NewEvidenceID(),
	}
	if invocation.AdmissionEvidenceID == invocation.RegistrationEvidenceID {
		invocation.AdmissionEvidenceID = modulecore.NewEvidenceID()
	}
	task := domaintask.Task{
		TaskID: contextTaskID, Assignee: actorID, Status: domaintask.StatusRunning, ParentTaskID: rootTaskID,
		ReadOnly: true, Timing: &timing, TaskMeetingInvocation: &invocation,
	}
	run := domaintask.Run{
		RunID: contextRunID, TaskID: contextTaskID, TraceID: modulecore.NewTraceID(),
		StartReason: domaintask.RunStartReasonFirst, Assignee: actorID, Status: domaintask.RunStatusRunning, StartedAt: time.Now().UTC(),
	}
	scope := domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: modulecore.NewEvidenceID(), Generation: 3, Epoch: 2}
	body := "canonical inbound group message"
	reference := domaintask.TaskMeetingMessageReference{
		OccurrenceKey: invocation.OccurrenceKey, SessionID: modulecore.NewSessionID(), ThreadID: modulecore.NewThreadID(),
		MessageID: modulecore.NewMessageID(), EventID: modulecore.NewEventID(), BodySHA256: taskMeetingSHA256([]byte(body)),
		AudienceSHA256: audienceSHA, ConfigRevision: invocation.ConfigRevision, ConfigSHA256: invocation.ConfigSHA256,
		AttemptGeneration: scope.Generation, AttemptEpoch: scope.Epoch, ScopeEvidenceID: scope.ScopeEvidenceID,
	}
	return task, run, scope, TaskMeetingReceiveRequest{
		RecipientTask: task, RecipientRun: run, Scope: scope, Reference: reference, Body: body, Audience: audience,
	}
}

func sourceProofs(messages []TaskMeetingSourceMessage) []domaintask.TaskMeetingConsumedMessage {
	proofs := make([]domaintask.TaskMeetingConsumedMessage, len(messages))
	for index := range messages {
		proofs[index] = messages[index].Proof
	}
	return proofs
}
