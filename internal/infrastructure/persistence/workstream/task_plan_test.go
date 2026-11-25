package workstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteTaskPlanSubmitReplayReceiptAndLegacyPrivacy(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "tasks.db")
	store := taskPlanTestStore(t, databasePath)
	insertTaskPlanCompatibilityArtifact(t, store)
	scope := taskPlanTestAgentScope(t, "request-1", "mio", "user-A", tool.DataScopeUser)
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	request := taskPlanTestSubmission(scope, "submit-key", domainworkstream.TaskPlanDataScopeUser)
	// Simulate the historical submission body, which predates creator_kind.
	request.Goal.TaskPlan.CreatorKind = ""

	result, err := store.SubmitTaskPlan(ctx, scope, request, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("SubmitTaskPlan() error = %v", err)
	}
	if result.Replayed || result.Goal.GoalID == "" || result.Goal.WorkstreamID == "" || result.ReceiptRef == "" {
		t.Fatalf("SubmitTaskPlan() result = %#v", result)
	}
	if result.Goal.TaskPlan.CreatorKind != string(scope.ActorKind) {
		t.Fatalf("persisted creator kind = %q, want authenticated scope kind %q", result.Goal.TaskPlan.CreatorKind, scope.ActorKind)
	}
	if !strings.HasPrefix(result.Goal.WorkstreamID, "ws_") || !strings.HasPrefix(result.ReceiptRef, "art_") {
		t.Fatalf("runtime IDs are not canonical: workstream=%q receipt=%q", result.Goal.WorkstreamID, result.ReceiptRef)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() before replay = %v", err)
	}
	store = taskPlanTestStore(t, databasePath)

	replayScope := scope
	replayScope.RequestID = "request-after-restart"
	replayScope.AgentRole = "worker"
	replayScope.Purpose = "resume task submission"
	replayScope.AllowedDataScopes = []string{tool.DataScopeUser, tool.DataScopePublic}
	replayCtx := tool.WithToolExecutionScope(context.Background(), replayScope)
	retry := request
	planWithCurrentCreatorKind := *retry.Goal.TaskPlan
	planWithCurrentCreatorKind.CreatorKind = string(scope.ActorKind)
	retry.Goal.TaskPlan = &planWithCurrentCreatorKind
	retry.Goal.CreatedAt = retry.Goal.CreatedAt.Add(time.Hour)
	plan := *retry.Goal.TaskPlan
	plan.UpdatedAt = plan.UpdatedAt.Add(time.Hour)
	retry.Goal.TaskPlan = &plan
	replayed, err := store.SubmitTaskPlan(replayCtx, replayScope, retry, func(domainworkstream.Goal) error {
		t.Fatal("authorization callback ran during receipt replay")
		return nil
	})
	if err != nil || !replayed.Replayed || replayed.ReceiptRef != result.ReceiptRef || replayed.Goal.GoalID != result.Goal.GoalID {
		t.Fatalf("SubmitTaskPlan() replay = %#v, error = %v", replayed, err)
	}
	changed := retry
	changed.Goal.Title = "different request body"
	if _, err := store.SubmitTaskPlan(replayCtx, replayScope, changed, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("same key with changed body error = %v, want conflict", err)
	}
	second, err := store.SubmitTaskPlan(replayCtx, replayScope, taskPlanTestSubmission(replayScope, "second-key", domainworkstream.TaskPlanDataScopeUser), func(domainworkstream.Goal) error { return nil })
	if err != nil || second.Goal.WorkstreamID != result.Goal.WorkstreamID {
		t.Fatalf("default Workstream reuse = %q, first %q, error %v", second.Goal.WorkstreamID, result.Goal.WorkstreamID, err)
	}

	otherDelegate := taskPlanTestAgentScope(t, "other-request", "mio", "user-B", tool.DataScopeUser)
	if _, found, err := store.TaskPlanReceipt(tool.WithToolExecutionScope(context.Background(), otherDelegate), otherDelegate, "submit", "submit-key"); err != nil || found {
		t.Fatalf("different delegated principal receipt = found %v, error %v", found, err)
	}
	publicOnly := taskPlanTestAgentScope(t, "public-only-recovery", "mio", "user-A", tool.DataScopePublic)
	if _, found, err := store.TaskPlanReceipt(tool.WithToolExecutionScope(context.Background(), publicOnly), publicOnly, "submit", "submit-key"); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) || found {
		t.Fatalf("receipt after losing private scope = found %v, error %v, want not found", found, err)
	}
	if _, err := store.GetTaskPlan(tool.WithToolExecutionScope(context.Background(), otherDelegate), otherDelegate, result.Goal.GoalID); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
		t.Fatalf("cross-user GetTaskPlan() error = %v, want not found", err)
	}

	legacyGoals, err := store.ListGoals(ctx, 100)
	if err != nil || len(legacyGoals) != 0 {
		t.Fatalf("legacy ListGoals() = %#v, error = %v", legacyGoals, err)
	}
	if _, found, err := store.FindGoalByID(ctx, result.Goal.GoalID); err != nil || found {
		t.Fatalf("legacy FindGoalByID() found %v, error %v", found, err)
	}
	legacyWorkstreams, err := store.ListWorkstreams(ctx, 100)
	if err != nil || len(legacyWorkstreams) != 0 {
		t.Fatalf("legacy ListWorkstreams() = %#v, error = %v", legacyWorkstreams, err)
	}
	legacyArtifacts, err := store.ListArtifacts(ctx, 100)
	if err != nil {
		t.Fatalf("legacy ListArtifacts() error = %v", err)
	}
	for _, artifact := range legacyArtifacts {
		if isTaskCenterInternalArtifactType(artifact.Type) {
			t.Fatalf("legacy ListArtifacts() exposed internal artifact type %q", artifact.Type)
		}
	}

	if err := store.SaveGoal(ctx, result.Goal); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic SaveGoal() error = %v, want forbidden", err)
	}
	legacyWorkstream := domainworkstream.Workstream{
		WorkstreamID: result.Goal.WorkstreamID, Name: "overwrite", Status: domainworkstream.StatusActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveWorkstream(ctx, legacyWorkstream); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic SaveWorkstream() error = %v, want forbidden", err)
	}
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID: result.ReceiptRef, WorkstreamID: result.Goal.WorkstreamID, Type: "report", Status: "stored", CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic SaveArtifact() receipt overwrite error = %v, want forbidden", err)
	}
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID: "art_external_task_operation", WorkstreamID: result.Goal.WorkstreamID, Type: "task_operation", Status: "stored", CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic SaveArtifact() reserved type error = %v, want forbidden", err)
	}
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID: "art_external_task_execution_result", WorkstreamID: result.Goal.WorkstreamID, Type: taskExecutionResultArtifactType, Status: "stored", CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic SaveArtifact() private execution result error = %v, want forbidden", err)
	}
	for _, artifactType := range []string{taskMeetingOccurrenceArtifactType, taskMeetingOccurrenceReservationArtifactType} {
		if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
			ArtifactID: "art_external_" + artifactType, WorkstreamID: result.Goal.WorkstreamID, Type: artifactType, Status: "stored", CreatedAt: time.Now().UTC(),
		}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
			t.Fatalf("generic SaveArtifact() reserved %q write error = %v, want forbidden", artifactType, err)
		}
	}
	privateResult := domainworkstream.Artifact{
		ArtifactID: "art_private_execution_result", WorkstreamID: result.Goal.WorkstreamID, Type: taskExecutionResultArtifactType, Status: "stored", CreatedAt: time.Now().UTC(),
	}
	privatePayload, err := json.Marshal(privateResult)
	if err != nil {
		t.Fatalf("marshal private execution result fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, privateResult.ArtifactID, privateResult.WorkstreamID, privateResult.CreatedAt.Format(timeFormatRFC3339Nano), string(privatePayload)); err != nil {
		t.Fatalf("seed private execution result fixture: %v", err)
	}
	artifacts, err := store.ListArtifacts(ctx, 100)
	if err != nil {
		t.Fatalf("ListArtifacts() after private execution result seed: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactID == privateResult.ArtifactID || isTaskCenterInternalArtifactType(artifact.Type) {
			t.Fatalf("legacy ListArtifacts() exposed reserved private artifact: %#v", artifact)
		}
	}
	for _, artifactType := range []string{taskMeetingOccurrenceArtifactType, taskMeetingOccurrenceReservationArtifactType} {
		meetingArtifact := domainworkstream.Artifact{
			ArtifactID: "art_" + artifactType, WorkstreamID: result.Goal.WorkstreamID, Type: artifactType, Status: "stored", CreatedAt: time.Now().UTC(),
		}
		payload, err := json.Marshal(meetingArtifact)
		if err != nil {
			t.Fatalf("marshal reserved meeting fixture: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, meetingArtifact.ArtifactID, meetingArtifact.WorkstreamID, meetingArtifact.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)); err != nil {
			t.Fatalf("seed reserved meeting fixture: %v", err)
		}
	}
	artifacts, err = store.ListArtifacts(ctx, 100)
	if err != nil {
		t.Fatalf("ListArtifacts() after meeting artifact seed: %v", err)
	}
	for _, artifact := range artifacts {
		if isTaskCenterInternalArtifactType(artifact.Type) {
			t.Fatalf("legacy ListArtifacts() exposed reserved meeting artifact: %#v", artifact)
		}
	}
}

func TestGameProjectReceiptRejectsCorruptRegistrationEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, string)
	}{
		{
			name: "corrupt payload",
			mutate: func(t *testing.T, store *SQLiteStore, receiptID string) {
				t.Helper()
				if _, err := store.db.Exec(`UPDATE artifact SET payload = '{' WHERE artifact_id = ?`, receiptID); err != nil {
					t.Fatalf("corrupt receipt payload: %v", err)
				}
			},
		},
		{
			name: "stage keys out of order",
			mutate: func(t *testing.T, store *SQLiteStore, receiptID string) {
				t.Helper()
				mutateGameProjectReceipt(t, store, receiptID, func(receipt *domainworkstream.ProjectPlanOperationReceipt) {
					receipt.Registration.StageKeys[0], receipt.Registration.StageKeys[1] = receipt.Registration.StageKeys[1], receipt.Registration.StageKeys[0]
				})
			},
		},
		{
			name: "duplicate goal id",
			mutate: func(t *testing.T, store *SQLiteStore, receiptID string) {
				t.Helper()
				mutateGameProjectReceipt(t, store, receiptID, func(receipt *domainworkstream.ProjectPlanOperationReceipt) {
					receipt.Registration.GoalIDs[1] = receipt.Registration.GoalIDs[0]
				})
			},
		},
		{
			name: "registration input hash mismatch",
			mutate: func(t *testing.T, store *SQLiteStore, receiptID string) {
				t.Helper()
				mutateGameProjectReceipt(t, store, receiptID, func(receipt *domainworkstream.ProjectPlanOperationReceipt) {
					receipt.Registration.InputSHA256 = strings.Repeat("b", 64)
				})
			},
		},
		{
			name: "artifact row workstream mismatch",
			mutate: func(t *testing.T, store *SQLiteStore, receiptID string) {
				t.Helper()
				if _, err := store.db.Exec(`UPDATE artifact SET workstream_id = ? WHERE artifact_id = ?`, "ws_tampered", receiptID); err != nil {
					t.Fatalf("tamper artifact row workstream: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "game-project.db"))
			insertTaskPlanCompatibilityArtifact(t, store)
			scope := taskPlanTestUserScope(t, "game-project-receipt-integrity")
			ctx := tool.WithToolExecutionScope(context.Background(), scope)
			request := domainworkstream.GameProjectSubmission{Name: "Test game", Description: "Brief", IdempotencyKey: "integrity-key"}
			for _, key := range domainworkstream.GameProjectStageKeys() {
				request.Stages = append(request.Stages, domainworkstream.GameProjectStage{
					Key: key, Title: key, Description: "deliverable", SuccessCriteria: []string{"deliverable exists"}, Verification: []string{"inspect deliverable"},
				})
			}
			created, err := store.SubmitGameProjectPlan(ctx, scope, request)
			if err != nil {
				t.Fatalf("SubmitGameProjectPlan() error = %v", err)
			}
			test.mutate(t, store, created.ReceiptRef)
			if _, found, err := store.GameProjectPlanReceipt(ctx, scope, request.IdempotencyKey); err == nil || found {
				t.Fatalf("GameProjectPlanReceipt() found=%v err=%v; corrupted registration evidence must fail closed", found, err)
			}
		})
	}
}

func TestSQLiteLegacyGameProjectReceiptRecovery(t *testing.T) {
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "legacy-game.db"))
	scope := taskPlanTestUserScope(t, "legacy-game")
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	request := domainworkstream.GameProjectSubmission{Name: "Legacy", IdempotencyKey: "legacy-key"}
	for _, key := range domainworkstream.GameProjectStageKeys() {
		if key != "design" {
			request.Stages = append(request.Stages, domainworkstream.GameProjectStage{Key: key, Title: key, SuccessCriteria: []string{"done"}, Verification: []string{"check"}})
		}
	}
	created, err := store.SubmitGameProjectPlan(ctx, scope, request)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found, err := store.GameProjectPlanReceipt(ctx, scope, request.IdempotencyKey)
	if err != nil || !found || !reflect.DeepEqual(created.Registration, recovered.Registration) {
		t.Fatalf("recovery found=%v err=%v", found, err)
	}
	replayed, err := store.SubmitGameProjectPlan(ctx, scope, request)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(created.Registration, replayed.Registration) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestSQLiteGameProjectRegistrationSerializesConcurrentSameKeySubmissions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "concurrent-game-project.db")
	storeA := taskPlanTestStore(t, databasePath)
	storeB := taskPlanTestStore(t, databasePath)
	scopeA := taskPlanTestUserScope(t, "game-project-concurrent-a")
	scopeB := scopeA
	scopeB.RequestID = "game-project-concurrent-b"
	request := domainworkstream.GameProjectSubmission{Name: "Concurrent game", Description: "Brief", IdempotencyKey: "concurrent-game-key"}
	for _, key := range domainworkstream.GameProjectStageKeys() {
		request.Stages = append(request.Stages, domainworkstream.GameProjectStage{
			Key: key, Title: key, Description: "deliverable", SuccessCriteria: []string{"deliverable exists"}, Verification: []string{"inspect deliverable"},
		})
	}
	start := make(chan struct{})
	type outcome struct {
		result domainworkstream.ProjectPlanMutationResult
		err    error
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for _, attempt := range []struct {
		store *SQLiteStore
		scope tool.ToolExecutionScope
	}{{storeA, scopeA}, {storeB, scopeB}} {
		attempt := attempt
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			ctx := tool.WithToolExecutionScope(context.Background(), attempt.scope)
			result, err := attempt.store.SubmitGameProjectPlan(ctx, attempt.scope, request)
			results <- outcome{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var registration domainworkstream.ProjectPlanRegistration
	var successful, fresh int
	for result := range results {
		if result.err != nil {
			if !errors.Is(result.err, domainworkstream.ErrTaskPlanConflict) {
				t.Fatalf("concurrent submit error = %v, want successful replay or transient SQLite conflict", result.err)
			}
			continue
		}
		successful++
		if !result.result.Replayed {
			fresh++
		}
		if registration.WorkstreamID == "" {
			registration = result.result.Registration
		} else if result.result.Registration.WorkstreamID != registration.WorkstreamID || !reflect.DeepEqual(result.result.Registration.GoalIDs, registration.GoalIDs) {
			t.Fatalf("concurrent submissions returned different registrations: first=%+v next=%+v", registration, result.result.Registration)
		}
	}
	if fresh != 1 || successful == 0 {
		t.Fatalf("concurrent submission successes=%d fresh=%d; want one durable creation and at least one successful response", successful, fresh)
	}
	ctx := tool.WithToolExecutionScope(context.Background(), scopeA)
	recovered, found, err := storeA.GameProjectPlanReceipt(ctx, scopeA, request.IdempotencyKey)
	if err != nil || !found || recovered.Registration.WorkstreamID != registration.WorkstreamID {
		t.Fatalf("receipt recovery=%+v found=%v err=%v", recovered, found, err)
	}
	workstreams, err := storeA.ListWorkstreams(context.Background(), 100)
	if err != nil || len(workstreams) != 1 {
		t.Fatalf("public Workstreams=%d err=%v; want exactly one", len(workstreams), err)
	}
	goals, err := storeA.ListGoals(context.Background(), 100)
	if err != nil || len(goals) != len(request.Stages) {
		t.Fatalf("public Goals=%d err=%v; want exactly one stage bundle", len(goals), err)
	}
}

func insertTaskPlanCompatibilityArtifact(t *testing.T, store *SQLiteStore) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"artifact_id": "art_compatibility_fixture", "workstream_id": "", "artifact_type": "legacy_fixture",
		"title": "legacy fixture", "status": "stored", "payload": json.RawMessage(`{}`),
		"created_at": time.Now().UTC(), "future_field": "preserve forward compatibility",
	})
	if err != nil {
		t.Fatalf("marshal compatibility fixture: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, '', ?, ?)`, "art_compatibility_fixture", time.Now().UTC().Format(timeFormatRFC3339Nano), string(payload)); err != nil {
		t.Fatalf("insert compatibility fixture: %v", err)
	}
}

func mutateGameProjectReceipt(t *testing.T, store *SQLiteStore, receiptID string, mutate func(*domainworkstream.ProjectPlanOperationReceipt)) {
	t.Helper()
	var raw string
	if err := store.db.QueryRow(`SELECT payload FROM artifact WHERE artifact_id = ?`, receiptID).Scan(&raw); err != nil {
		t.Fatalf("read receipt artifact: %v", err)
	}
	var artifact domainworkstream.Artifact
	if err := json.Unmarshal([]byte(raw), &artifact); err != nil {
		t.Fatalf("decode artifact envelope: %v", err)
	}
	var receipt domainworkstream.ProjectPlanOperationReceipt
	if err := json.Unmarshal(artifact.Payload, &receipt); err != nil {
		t.Fatalf("decode receipt payload: %v", err)
	}
	mutate(&receipt)
	payload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("encode mutated receipt: %v", err)
	}
	artifact.Payload = payload
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("encode artifact envelope: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE artifact SET payload = ? WHERE artifact_id = ?`, string(artifactJSON), receiptID); err != nil {
		t.Fatalf("save mutated receipt: %v", err)
	}
}

func TestSQLiteTaskPlanSubmissionRejectsForgedCreatorKind(t *testing.T) {
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "forged-creator.db"))
	scope := taskPlanTestAgentScope(t, "forged-creator", "mio", "user-A", tool.DataScopeUser)
	request := taskPlanTestSubmission(scope, "forged-creator", domainworkstream.TaskPlanDataScopeUser)
	request.Goal.TaskPlan.CreatorKind = string(tool.ActorKindUser)
	if _, err := store.SubmitTaskPlan(tool.WithToolExecutionScope(context.Background(), scope), scope, request, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("SubmitTaskPlan() forged creator kind error = %v, want forbidden", err)
	}
}

func TestSQLiteTaskPlanClaimIsAtomicAcrossStoreConnections(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "claim.db")
	storeA := taskPlanTestStore(t, databasePath)
	storeB := taskPlanTestStore(t, databasePath)
	userScope := taskPlanTestAgentScope(t, "submit", "mio", "user-A", tool.DataScopePublic)
	userCtx := tool.WithToolExecutionScope(context.Background(), userScope)
	submission := taskPlanTestSubmission(userScope, "public-task", domainworkstream.TaskPlanDataScopePublic)
	created, err := storeA.SubmitTaskPlan(userCtx, userScope, submission, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("SubmitTaskPlan() error = %v", err)
	}

	claimScopeA := taskPlanTestAgentScope(t, "claim-a", "worker-a", "", tool.DataScopePublic)
	claimScopeB := taskPlanTestAgentScope(t, "claim-b", "worker-b", "", tool.DataScopePublic)
	claimRequest := func(key string) domainworkstream.TaskPlanClaim {
		return domainworkstream.TaskPlanClaim{
			GoalID: created.Goal.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: strings.Repeat("a", 64), IdempotencyKey: key,
		}
	}
	start := make(chan struct{})
	type outcome struct {
		result domainworkstream.TaskPlanMutationResult
		err    error
		scope  tool.ToolExecutionScope
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for _, attempt := range []struct {
		store *SQLiteStore
		scope tool.ToolExecutionScope
		key   string
	}{{storeA, claimScopeA, "claim-key-a"}, {storeB, claimScopeB, "claim-key-b"}} {
		attempt := attempt
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			ctx := tool.WithToolExecutionScope(context.Background(), attempt.scope)
			result, err := attempt.store.ClaimTaskPlan(ctx, attempt.scope, claimRequest(attempt.key), func(domainworkstream.Goal) error { return nil })
			results <- outcome{result: result, err: err, scope: attempt.scope}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	winners := 0
	var winnerScope tool.ToolExecutionScope
	for result := range results {
		if result.err == nil {
			winners++
			winnerScope = result.scope
			if result.result.Goal.TaskPlan.Revision != 2 || result.result.Goal.TaskPlan.AssignmentEpoch != 1 {
				t.Fatalf("claim winner revision/epoch = %d/%d", result.result.Goal.TaskPlan.Revision, result.result.Goal.TaskPlan.AssignmentEpoch)
			}
		} else if !errors.Is(result.err, domainworkstream.ErrTaskPlanConflict) {
			t.Fatalf("claim loser error = %v, want conflict", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want exactly one", winners)
	}
	if _, err := storeA.ClaimTaskPlan(tool.WithToolExecutionScope(context.Background(), claimScopeA), claimScopeA, claimRequest("stale-key"), func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("stale revision claim error = %v, want conflict", err)
	}
	winnerCtx := tool.WithToolExecutionScope(context.Background(), winnerScope)
	claimed, err := storeA.GetTaskPlan(winnerCtx, winnerScope, created.Goal.GoalID)
	if err != nil {
		t.Fatalf("GetTaskPlan() after claim error = %v", err)
	}
	if _, err := storeA.ReleaseTaskPlan(winnerCtx, winnerScope, domainworkstream.TaskPlanRelease{
		GoalID: claimed.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: strings.Repeat("a", 64), IdempotencyKey: "stale-release",
	}, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("stale revision release error = %v, want conflict", err)
	}
	released, err := storeA.ReleaseTaskPlan(winnerCtx, winnerScope, domainworkstream.TaskPlanRelease{
		GoalID: claimed.GoalID, ExpectedRevision: claimed.TaskPlan.Revision, ExpectedPolicyRevision: strings.Repeat("a", 64), IdempotencyKey: "release-key",
	}, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("ReleaseTaskPlan() error = %v", err)
	}
	if released.Goal.TaskPlan.Revision != 3 || released.Goal.TaskPlan.AssignmentEpoch != 2 || released.Goal.TaskPlan.Assignee != domainworkstream.TaskPlanAssigneeUnassigned {
		t.Fatalf("release revision/epoch/assignee = %d/%d/%q", released.Goal.TaskPlan.Revision, released.Goal.TaskPlan.AssignmentEpoch, released.Goal.TaskPlan.Assignee)
	}
}

func TestSQLiteTaskPlanHandoffCASReplayAndUncertainRecovery(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "handoff.db")
	storeA := taskPlanTestStore(t, databasePath)
	storeB := taskPlanTestStore(t, databasePath)
	scope := taskPlanTestAgentScope(t, "handoff-owner", "mio", "", tool.DataScopePublic)
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	submission := taskPlanTestSubmission(scope, "handoff-submit", domainworkstream.TaskPlanDataScopePublic)
	plan := submission.Goal.TaskPlan
	plan.KindID = domainworkstream.TaskPlanKindMovieCatalogLookup
	plan.KindInput = json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
	plan.KindInputVersion = 1
	created, err := storeA.SubmitTaskPlan(ctx, scope, submission, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("SubmitTaskPlan() error = %v", err)
	}
	claimed, err := storeA.ClaimTaskPlan(ctx, scope, domainworkstream.TaskPlanClaim{
		GoalID: created.Goal.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: strings.Repeat("a", 64), IdempotencyKey: "handoff-claim",
	}, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("ClaimTaskPlan() error = %v", err)
	}
	kindInput := append(json.RawMessage(nil), claimed.Goal.TaskPlan.KindInput...)
	kindInputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatalf("TaskCenterKindInputSHA256() error = %v", err)
	}
	timingDigest, err := domaintask.TaskTimingSHA256(claimed.Goal.TaskPlan.Timing)
	if err != nil {
		t.Fatalf("TaskTimingSHA256() error = %v", err)
	}
	draftBytes, err := json.Marshal(domaintask.Task{
		Title: claimed.Goal.Title, OwnerID: scope.ActorID, Assignee: scope.ActorID, ReadOnly: true,
		WorkstreamID: modulecore.WorkstreamID(claimed.Goal.WorkstreamID), GoalID: modulecore.GoalID(claimed.Goal.GoalID),
		Timing: &claimed.Goal.TaskPlan.Timing,
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: claimed.Goal.TaskPlan.KindID, KindInputVersion: claimed.Goal.TaskPlan.KindInputVersion,
			KindInputSHA256: kindInputDigest, SourceGoalID: modulecore.GoalID(claimed.Goal.GoalID),
			PreparedRevision: claimed.Goal.TaskPlan.Revision + 1, AssignmentEpoch: claimed.Goal.TaskPlan.AssignmentEpoch,
			TimingSHA256: timingDigest,
		},
	})
	if err != nil {
		t.Fatalf("marshal frozen Task draft: %v", err)
	}
	sharedBytes, err := json.Marshal(domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("marshal frozen SharedRoleContext: %v", err)
	}
	handoffKey := modulecore.NewRequestID()
	frozen := domainworkstream.TaskPlanHandoffInput{
		Version: 1, Revision: claimed.Goal.TaskPlan.Revision + 1, AssignmentEpoch: claimed.Goal.TaskPlan.AssignmentEpoch,
		KindID: claimed.Goal.TaskPlan.KindID, KindInputVersion: claimed.Goal.TaskPlan.KindInputVersion,
		TaskDraft: draftBytes, SharedContext: sharedBytes, KindInput: append(json.RawMessage(nil), claimed.Goal.TaskPlan.KindInput...),
	}
	frozen.InputHash = domainworkstream.ComputeTaskPlanHandoffInputHash(frozen.TaskDraft, frozen.SharedContext, frozen.KindInput)
	prepare := domainworkstream.TaskPlanHandoffPrepare{
		GoalID: claimed.Goal.GoalID, ExpectedRevision: claimed.Goal.TaskPlan.Revision,
		ExpectedAssignmentEpoch: claimed.Goal.TaskPlan.AssignmentEpoch, ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision,
		IdempotencyKey: "handoff-prepare", HandoffKey: handoffKey, Input: frozen,
	}
	prepared, err := storeA.PrepareTaskPlanHandoff(ctx, scope, prepare, func(domainworkstream.Goal) error { return nil })
	if err != nil || prepared.Goal.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffPrepared || prepared.Goal.TaskPlan.HandoffInput.Revision != 3 {
		t.Fatalf("PrepareTaskPlanHandoff() = %#v, error = %v", prepared.Goal.TaskPlan, err)
	}
	replayed, err := storeB.PrepareTaskPlanHandoff(ctx, scope, prepare, func(domainworkstream.Goal) error {
		t.Fatal("authorization ran during prepared receipt replay")
		return nil
	})
	if err != nil || !replayed.Replayed || replayed.Goal.TaskPlan.HandoffInput.InputHash != frozen.InputHash {
		t.Fatalf("PrepareTaskPlanHandoff() replay = %#v, error = %v", replayed.Goal.TaskPlan, err)
	}
	changed := prepare
	changed.IdempotencyKey = "handoff-prepare"
	changed.Input.SharedContext = append(append([]byte(nil), frozen.SharedContext...), ' ')
	changed.Input.InputHash = domainworkstream.ComputeTaskPlanHandoffInputHash(changed.Input.TaskDraft, changed.Input.SharedContext, changed.Input.KindInput)
	if _, err := storeB.PrepareTaskPlanHandoff(ctx, scope, changed, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("same prepare key with changed frozen bytes error = %v, want conflict", err)
	}

	staleEpoch := domainworkstream.TaskPlanHandoffTransition{
		GoalID: claimed.Goal.GoalID, ExpectedRevision: 3, ExpectedAssignmentEpoch: 2,
		ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision, IdempotencyKey: "handoff-stale-epoch", HandoffKey: handoffKey,
	}
	if _, err := storeA.MarkTaskPlanHandoffUnknown(ctx, scope, staleEpoch, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("stale assignment epoch error = %v, want conflict", err)
	}
	otherActor := taskPlanTestAgentScope(t, "foreign-handoff", "other", "", tool.DataScopePublic)
	if _, err := storeA.MarkTaskPlanHandoffUnknown(tool.WithToolExecutionScope(context.Background(), otherActor), otherActor, domainworkstream.TaskPlanHandoffTransition{
		GoalID: claimed.Goal.GoalID, ExpectedRevision: 3, ExpectedAssignmentEpoch: 1,
		ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision, IdempotencyKey: "handoff-foreign", HandoffKey: handoffKey,
	}, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("foreign assignee reconciliation error = %v, want forbidden", err)
	}

	start := make(chan struct{})
	type transitionResult struct {
		key    string
		result domainworkstream.TaskPlanMutationResult
		err    error
	}
	results := make(chan transitionResult, 2)
	var group sync.WaitGroup
	for _, attempt := range []struct {
		store *SQLiteStore
		key   string
	}{{storeA, "handoff-unknown-a"}, {storeB, "handoff-unknown-b"}} {
		attempt := attempt
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			request := domainworkstream.TaskPlanHandoffTransition{
				GoalID: claimed.Goal.GoalID, ExpectedRevision: 3, ExpectedAssignmentEpoch: 1,
				ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision, IdempotencyKey: attempt.key, HandoffKey: handoffKey,
			}
			result, err := attempt.store.MarkTaskPlanHandoffUnknown(ctx, scope, request, func(domainworkstream.Goal) error { return nil })
			results <- transitionResult{key: attempt.key, result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	winners := 0
	winningKey := ""
	for result := range results {
		if result.err == nil {
			winners++
			winningKey = result.key
		} else if !errors.Is(result.err, domainworkstream.ErrTaskPlanConflict) {
			t.Fatalf("concurrent uncertain outcome loser error = %v, want conflict", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent uncertain outcome winners = %d, want 1", winners)
	}
	unknownRequest := domainworkstream.TaskPlanHandoffTransition{
		GoalID: claimed.Goal.GoalID, ExpectedRevision: 3, ExpectedAssignmentEpoch: 1,
		ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision, IdempotencyKey: winningKey, HandoffKey: handoffKey,
	}
	unknownReplay, err := storeA.MarkTaskPlanHandoffUnknown(ctx, scope, unknownRequest, func(domainworkstream.Goal) error {
		t.Fatal("authorization ran during uncertain outcome receipt replay")
		return nil
	})
	if err != nil || !unknownReplay.Replayed || unknownReplay.Goal.TaskPlan.HandoffInput.Revision != 3 {
		t.Fatalf("uncertain outcome receipt replay = %#v, error = %v", unknownReplay.Goal.TaskPlan, err)
	}
	taskID := modulecore.NewTaskID()
	link := domainworkstream.TaskPlanHandoffTransition{
		GoalID: claimed.Goal.GoalID, ExpectedRevision: 4, ExpectedAssignmentEpoch: 1,
		ExpectedPolicyRevision: claimed.Goal.TaskPlan.PolicyRevision, IdempotencyKey: "handoff-link", HandoffKey: handoffKey,
		TaskID: taskID, RunID: modulecore.NewRunID(),
	}
	linked, err := storeB.LinkTaskPlanHandoff(ctx, scope, link, func(domainworkstream.Goal) error { return nil })
	if err != nil || linked.Goal.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffLinked || linked.Goal.TaskPlan.TaskID != taskID || linked.Goal.TaskPlan.RunID != link.RunID || linked.Goal.TaskPlan.HandoffInput.Revision != 3 {
		t.Fatalf("LinkTaskPlanHandoff() = %#v, error = %v", linked.Goal.TaskPlan, err)
	}
	linkReplay, err := storeA.LinkTaskPlanHandoff(ctx, scope, link, func(domainworkstream.Goal) error {
		t.Fatal("authorization ran during linked receipt replay")
		return nil
	})
	if err != nil || !linkReplay.Replayed {
		t.Fatalf("LinkTaskPlanHandoff() replay = %#v, error = %v", linkReplay, err)
	}
	newerGoal := linked.Goal
	newerPlan := *newerGoal.TaskPlan
	newerPlan.Revision++
	newerPlan.UpdatedAt = time.Now().UTC()
	newerGoal.TaskPlan = &newerPlan
	newerPayload, err := json.Marshal(newerGoal)
	if err != nil {
		t.Fatalf("marshal later Goal revision: %v", err)
	}
	if _, err := storeA.db.ExecContext(ctx, `UPDATE workstream_goal SET payload = ? WHERE goal_id = ?`, string(newerPayload), newerGoal.GoalID); err != nil {
		t.Fatalf("advance current Goal revision for receipt replay fixture: %v", err)
	}
	laterRevisionReplay, err := storeB.LinkTaskPlanHandoff(ctx, scope, link, func(domainworkstream.Goal) error {
		t.Fatal("authorization ran during Link receipt replay after later Goal revision")
		return nil
	})
	if err != nil || !laterRevisionReplay.Replayed || laterRevisionReplay.Goal.TaskPlan.Revision != linked.Goal.TaskPlan.Revision {
		t.Fatalf("Link receipt replay after later Goal revision = %#v, error = %v", laterRevisionReplay.Goal.TaskPlan, err)
	}
	laterReadReceipt, found, err := storeB.TaskPlanReceipt(ctx, scope, "handoff_link", link.IdempotencyKey)
	if err != nil || !found || !laterReadReceipt.Replayed || laterReadReceipt.Goal.TaskPlan.Revision != linked.Goal.TaskPlan.Revision {
		t.Fatalf("read-only Link receipt after later Goal revision = %#v, found %v, error %v", laterReadReceipt.Goal.TaskPlan, found, err)
	}
	changedLink := link
	changedLink.RunID = modulecore.NewRunID()
	if _, err := storeA.LinkTaskPlanHandoff(ctx, scope, changedLink, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("same link key with changed RunID error = %v, want conflict", err)
	}
	changedScopeGoal := newerGoal
	changedScopePlan := *newerGoal.TaskPlan
	changedScopePlan.DataScope, changedScopePlan.UserID = domainworkstream.TaskPlanDataScopeUser, "user-B"
	changedScopeGoal.TaskPlan = &changedScopePlan
	changedScopePayload, err := json.Marshal(changedScopeGoal)
	if err != nil {
		t.Fatalf("marshal changed current Goal scope: %v", err)
	}
	if _, err := storeA.db.ExecContext(ctx, `UPDATE workstream_goal SET payload = ? WHERE goal_id = ?`, string(changedScopePayload), changedScopeGoal.GoalID); err != nil {
		t.Fatalf("change current Goal scope for receipt fixture: %v", err)
	}
	if _, found, err := storeA.TaskPlanReceipt(ctx, scope, "handoff_link", link.IdempotencyKey); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) || found {
		t.Fatalf("Link receipt after current Goal scope changed = found %v, error %v, want not found", found, err)
	}
}

func TestSQLiteLegacyTaskPlanHandoffRemainsReadableButCannotBeRepreparedOrReleased(t *testing.T) {
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "legacy-handoff.db"))
	scope := taskPlanTestAgentScope(t, "legacy-owner", "mio", "", tool.DataScopePublic)
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	created, err := store.SubmitTaskPlan(ctx, scope, taskPlanTestSubmission(scope, "legacy-submit", domainworkstream.TaskPlanDataScopePublic), func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("SubmitTaskPlan() error = %v", err)
	}
	claimed, err := store.ClaimTaskPlan(ctx, scope, domainworkstream.TaskPlanClaim{
		GoalID: created.Goal.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: strings.Repeat("a", 64), IdempotencyKey: "legacy-claim",
	}, func(domainworkstream.Goal) error { return nil })
	if err != nil {
		t.Fatalf("ClaimTaskPlan() error = %v", err)
	}
	legacy := claimed.Goal
	legacy.TaskPlan.HandoffState = domainworkstream.TaskPlanHandoffPrepared
	legacy.TaskPlan.HandoffKey = "retired-handoff-key"
	legacy.TaskPlan.HandoffInput = nil
	persistTaskPlanFixture(t, store, legacy)
	readback, err := store.GetTaskPlan(ctx, scope, legacy.GoalID)
	if err != nil || readback.TaskPlan.HandoffInput != nil || readback.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffPrepared {
		t.Fatalf("legacy prepared Goal readback = %#v, error = %v", readback.TaskPlan, err)
	}
	if _, err := store.ReleaseTaskPlan(ctx, scope, domainworkstream.TaskPlanRelease{
		GoalID: legacy.GoalID, ExpectedRevision: legacy.TaskPlan.Revision,
		ExpectedPolicyRevision: legacy.TaskPlan.PolicyRevision, IdempotencyKey: "legacy-release",
	}, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("legacy handoff release error = %v, want unavailable", err)
	}
	draft, err := json.Marshal(domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: legacy.Title, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
		WorkstreamID: modulecore.WorkstreamID(legacy.WorkstreamID), GoalID: modulecore.GoalID(legacy.GoalID),
	})
	if err != nil {
		t.Fatalf("marshal legacy Task draft: %v", err)
	}
	shared, err := json.Marshal(domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("marshal legacy shared context: %v", err)
	}
	kindInput := json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
	frozen := domainworkstream.TaskPlanHandoffInput{
		Version: 1, Revision: legacy.TaskPlan.Revision + 1, AssignmentEpoch: legacy.TaskPlan.AssignmentEpoch,
		KindID: legacy.TaskPlan.KindID, KindInputVersion: 1, TaskDraft: draft, SharedContext: shared, KindInput: kindInput,
	}
	frozen.InputHash = domainworkstream.ComputeTaskPlanHandoffInputHash(draft, shared, kindInput)
	if _, err := store.PrepareTaskPlanHandoff(ctx, scope, domainworkstream.TaskPlanHandoffPrepare{
		GoalID: legacy.GoalID, ExpectedRevision: legacy.TaskPlan.Revision,
		ExpectedAssignmentEpoch: legacy.TaskPlan.AssignmentEpoch, ExpectedPolicyRevision: legacy.TaskPlan.PolicyRevision,
		IdempotencyKey: "legacy-reprepare", HandoffKey: modulecore.NewRequestID(), Input: frozen,
	}, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("legacy handoff re-prepare error = %v, want conflict", err)
	}
}

func TestSQLiteTaskPlanStatusControlsClaimAndTerminalRelease(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "status-contract.db")
	store := taskPlanTestStore(t, databasePath)
	ownerScope := taskPlanTestUserScope(t, "status-owner")
	ownerCtx := tool.WithToolExecutionScope(context.Background(), ownerScope)
	agentScope := taskPlanTestAgentScope(t, "status-claimer", "mio", "user-A", tool.DataScopePublic)
	agentCtx := tool.WithToolExecutionScope(context.Background(), agentScope)
	completedAt := time.Date(2026, time.November, 16, 23, 0, 0, 0, time.UTC)

	statuses := []string{
		domainworkstream.StatusWaiting,
		domainworkstream.StatusDraft,
		domainworkstream.StatusActive,
		domainworkstream.StatusPaused,
		domainworkstream.StatusCompleted,
		domainworkstream.StatusArchived,
		"unknown",
	}
	goals := make(map[string]domainworkstream.Goal, len(statuses))
	for _, status := range statuses {
		request := taskPlanTestSubmission(ownerScope, "status-seed-"+status, domainworkstream.TaskPlanDataScopePublic)
		created, err := store.SubmitTaskPlan(ownerCtx, ownerScope, request, func(domainworkstream.Goal) error { return nil })
		if err != nil {
			t.Fatalf("SubmitTaskPlan(%q) error = %v", status, err)
		}
		fixture := created.Goal
		fixture.Status = status
		if status == domainworkstream.StatusWaiting || status == domainworkstream.StatusCompleted || status == domainworkstream.StatusArchived {
			fixture.CompletedAt = completedAt
		}
		persistTaskPlanFixture(t, store, fixture)
		goals[status] = fixture
	}

	claimable, _, _, err := store.ListTaskPlans(agentCtx, agentScope, domainworkstream.TaskPlanQuery{
		View: "claimable", Assignee: domainworkstream.TaskPlanAssigneeUnassigned, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListTaskPlans(claimable) error = %v", err)
	}
	if len(claimable) != 1 || claimable[0].Status != domainworkstream.StatusWaiting {
		t.Fatalf("native claimable statuses = %#v, want only waiting", claimable)
	}

	for _, status := range statuses {
		fixture := goals[status]
		t.Run("claim_"+status, func(t *testing.T) {
			callbackCalled := false
			result, err := store.ClaimTaskPlan(agentCtx, agentScope, domainworkstream.TaskPlanClaim{
				GoalID: fixture.GoalID, ExpectedRevision: fixture.TaskPlan.Revision,
				ExpectedPolicyRevision: fixture.TaskPlan.PolicyRevision, IdempotencyKey: "claim-status-" + status,
			}, func(domainworkstream.Goal) error {
				callbackCalled = true
				return nil
			})
			if status == domainworkstream.StatusWaiting {
				if err != nil || !callbackCalled || result.Goal.TaskPlan.Assignee != agentScope.ActorID ||
					result.Goal.Status != fixture.Status || !result.Goal.CompletedAt.Equal(fixture.CompletedAt) {
					t.Fatalf("Claim(waiting) = %#v, callback=%v, error=%v", result, callbackCalled, err)
				}
				return
			}
			if !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
				t.Fatalf("Claim(%q) error = %v, want conflict", status, err)
			}
			if callbackCalled {
				t.Fatalf("Claim(%q) called authorization before status admission", status)
			}
			after, err := store.GetTaskPlan(ownerCtx, ownerScope, fixture.GoalID)
			if err != nil {
				t.Fatalf("GetTaskPlan(%q) after claim rejection: %v", status, err)
			}
			if !sameTaskPlanGoalJSON(fixture, after) {
				beforeJSON, _ := json.Marshal(fixture)
				afterJSON, _ := json.Marshal(after)
				t.Fatalf("Claim(%q) changed Goal:\nbefore=%s\nafter=%s", status, beforeJSON, afterJSON)
			}
			if _, found, err := store.TaskPlanReceipt(ownerCtx, ownerScope, "claim", "claim-status-"+status); err != nil || found {
				t.Fatalf("Claim(%q) receipt found=%v error=%v, want no receipt", status, found, err)
			}
		})
	}
}

func TestSQLiteTaskPlanReleaseRejectsTerminalGoalsAndPreservesHistory(t *testing.T) {
	for _, status := range []string{domainworkstream.StatusCompleted, domainworkstream.StatusArchived} {
		t.Run(status, func(t *testing.T) {
			store := taskPlanTestStore(t, filepath.Join(t.TempDir(), status+".db"))
			scope := taskPlanTestUserScope(t, "release-owner-"+status)
			ctx := tool.WithToolExecutionScope(context.Background(), scope)
			agentScope := taskPlanTestAgentScope(t, "release-claimable-"+status, "mio", "user-A", tool.DataScopePublic)
			agentCtx := tool.WithToolExecutionScope(context.Background(), agentScope)
			submission := taskPlanTestSubmission(scope, "release-seed-"+status, domainworkstream.TaskPlanDataScopePublic)
			created, err := store.SubmitTaskPlan(ctx, scope, submission, func(domainworkstream.Goal) error { return nil })
			if err != nil {
				t.Fatalf("SubmitTaskPlan() error = %v", err)
			}
			historyRequest := domainworkstream.TaskPlanRelease{
				GoalID: created.Goal.GoalID, ExpectedRevision: 1,
				ExpectedPolicyRevision: created.Goal.TaskPlan.PolicyRevision, IdempotencyKey: "release-history-" + status,
			}
			history, err := store.ReleaseTaskPlan(ctx, scope, historyRequest, func(domainworkstream.Goal) error { return nil })
			if err != nil || history.Replayed {
				t.Fatalf("Release(initial open Goal) = %#v, error=%v", history, err)
			}

			terminal := history.Goal
			terminal.Status = status
			terminal.CompletedAt = time.Date(2026, time.November, 16, 23, 0, 0, 0, time.UTC)
			persistTaskPlanFixture(t, store, terminal)
			before, err := store.GetTaskPlan(ctx, scope, terminal.GoalID)
			if err != nil {
				t.Fatalf("GetTaskPlan(terminal fixture) error = %v", err)
			}

			replayed, err := store.ReleaseTaskPlan(ctx, scope, historyRequest, func(domainworkstream.Goal) error {
				t.Fatal("historical receipt replay ran the authorization callback")
				return nil
			})
			if err != nil || !replayed.Replayed || !sameTaskPlanGoalJSON(replayed.Goal, history.Goal) {
				t.Fatalf("Release(historical receipt) = %#v, error=%v", replayed, err)
			}

			withoutAuthorizer := domainworkstream.TaskPlanRelease{
				GoalID: before.GoalID, ExpectedRevision: before.TaskPlan.Revision,
				ExpectedPolicyRevision: before.TaskPlan.PolicyRevision, IdempotencyKey: "release-no-authorizer-" + status,
			}
			if _, err := store.ReleaseTaskPlan(ctx, scope, withoutAuthorizer, nil); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
				t.Fatalf("Release(terminal, nil authorizer) error = %v, want forbidden", err)
			}

			terminalRequest := withoutAuthorizer
			terminalRequest.IdempotencyKey = "release-terminal-" + status
			authorizerCalled := false
			if _, err := store.ReleaseTaskPlan(ctx, scope, terminalRequest, func(domainworkstream.Goal) error {
				authorizerCalled = true
				return nil
			}); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
				t.Fatalf("Release(%q) error = %v, want conflict", status, err)
			}
			if authorizerCalled {
				t.Fatalf("Release(%q) called authorization before terminal-state rejection", status)
			}
			after, err := store.GetTaskPlan(ctx, scope, before.GoalID)
			if err != nil {
				t.Fatalf("GetTaskPlan(%q) after release rejection: %v", status, err)
			}
			if !sameTaskPlanGoalJSON(before, after) {
				beforeJSON, _ := json.Marshal(before)
				afterJSON, _ := json.Marshal(after)
				t.Fatalf("Release(%q) changed Goal:\nbefore=%s\nafter=%s", status, beforeJSON, afterJSON)
			}
			for _, key := range []string{withoutAuthorizer.IdempotencyKey, terminalRequest.IdempotencyKey} {
				if _, found, err := store.TaskPlanReceipt(ctx, scope, "release", key); err != nil || found {
					t.Fatalf("Release(%q) receipt %q found=%v error=%v, want absent", status, key, found, err)
				}
			}
			claimable, _, _, err := store.ListTaskPlans(agentCtx, agentScope, domainworkstream.TaskPlanQuery{
				View: "claimable", Assignee: domainworkstream.TaskPlanAssigneeUnassigned, Limit: 10,
			})
			if err != nil || len(claimable) != 0 {
				t.Fatalf("ListTaskPlans(claimable,%q) = %#v, error=%v, want empty", status, claimable, err)
			}
		})
	}
}

func TestSQLiteTaskPlanReleasePreservesOpenGoalLifecycle(t *testing.T) {
	statuses := []string{domainworkstream.StatusDraft, domainworkstream.StatusActive, domainworkstream.StatusPaused, domainworkstream.StatusWaiting}
	completedAt := time.Date(2026, time.November, 16, 23, 0, 0, 0, time.UTC)
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			store := taskPlanTestStore(t, filepath.Join(t.TempDir(), status+".db"))
			scope := taskPlanTestUserScope(t, "release-open-owner-"+status)
			ctx := tool.WithToolExecutionScope(context.Background(), scope)
			agentScope := taskPlanTestAgentScope(t, "claimable-open-"+status, "mio", "user-A", tool.DataScopePublic)
			agentCtx := tool.WithToolExecutionScope(context.Background(), agentScope)

			submission := taskPlanTestSubmission(scope, "release-open-seed-"+status, domainworkstream.TaskPlanDataScopePublic)
			submission.Goal.TaskPlan.Assignee = "mio"
			created, err := store.SubmitTaskPlan(ctx, scope, submission, func(domainworkstream.Goal) error { return nil })
			if err != nil {
				t.Fatalf("SubmitTaskPlan() error = %v", err)
			}
			fixture := created.Goal
			fixture.Status = status
			fixture.CompletedAt = completedAt
			persistTaskPlanFixture(t, store, fixture)
			before, err := store.GetTaskPlan(ctx, scope, fixture.GoalID)
			if err != nil {
				t.Fatalf("GetTaskPlan(before release) error = %v", err)
			}

			request := domainworkstream.TaskPlanRelease{
				GoalID: before.GoalID, ExpectedRevision: before.TaskPlan.Revision,
				ExpectedPolicyRevision: before.TaskPlan.PolicyRevision, IdempotencyKey: "release-open-" + status,
			}
			result, err := store.ReleaseTaskPlan(ctx, scope, request, func(domainworkstream.Goal) error { return nil })
			if err != nil || result.Replayed {
				t.Fatalf("Release(%q) = %#v, error = %v", status, result, err)
			}
			after, err := store.GetTaskPlan(ctx, scope, before.GoalID)
			if err != nil {
				t.Fatalf("GetTaskPlan(after release) error = %v", err)
			}
			expected := before
			plan := *before.TaskPlan
			plan.Assignee = domainworkstream.TaskPlanAssigneeUnassigned
			plan.Revision++
			plan.AssignmentEpoch++
			plan.PolicyRevision = request.ExpectedPolicyRevision
			plan.UpdatedAt = after.TaskPlan.UpdatedAt
			expected.TaskPlan = &plan
			if !sameTaskPlanGoalJSON(expected, after) {
				beforeJSON, _ := json.Marshal(expected)
				afterJSON, _ := json.Marshal(after)
				t.Fatalf("Release(%q) changed more than the assignment state:\nwant=%s\ngot=%s", status, beforeJSON, afterJSON)
			}
			if result.Goal.Status != status || !result.Goal.CompletedAt.Equal(completedAt) {
				t.Fatalf("Release(%q) changed lifecycle status/completed_at: status=%q completed_at=%v", status, result.Goal.Status, result.Goal.CompletedAt)
			}

			claimable, _, _, err := store.ListTaskPlans(agentCtx, agentScope, domainworkstream.TaskPlanQuery{
				View: "claimable", Assignee: domainworkstream.TaskPlanAssigneeUnassigned, Limit: 10,
			})
			if err != nil {
				t.Fatalf("ListTaskPlans(claimable) error = %v", err)
			}
			if status == domainworkstream.StatusWaiting {
				if len(claimable) != 1 || claimable[0].GoalID != before.GoalID {
					t.Fatalf("waiting Goal after release claimable = %#v, want released task", claimable)
				}
			} else if len(claimable) != 0 {
				t.Fatalf("%q Goal after release entered claimable: %#v", status, claimable)
			}
		})
	}
}

func persistTaskPlanFixture(t *testing.T, store *SQLiteStore, goal domainworkstream.Goal) {
	t.Helper()
	payload, err := json.Marshal(goal)
	if err != nil {
		t.Fatalf("marshal Goal fixture: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE workstream_goal SET payload = ? WHERE goal_id = ?`, string(payload), goal.GoalID); err != nil {
		t.Fatalf("persist Goal fixture: %v", err)
	}
}

func sameTaskPlanGoalJSON(left, right domainworkstream.Goal) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func TestSQLiteTaskPlanOwnerRequiresTrustedContext(t *testing.T) {
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "trusted.db"))
	scope := taskPlanTestAgentScope(t, "request", "mio", "user-A", tool.DataScopeUser)
	request := taskPlanTestSubmission(scope, "key", domainworkstream.TaskPlanDataScopeUser)
	authorize := func(domainworkstream.Goal) error { return nil }
	if _, err := store.SubmitTaskPlan(context.Background(), scope, request, authorize); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("missing context scope error = %v, want forbidden", err)
	}
	otherScope := scope
	otherScope.AuthenticatedUserID = "user-B"
	if _, err := store.SubmitTaskPlan(tool.WithToolExecutionScope(context.Background(), scope), otherScope, request, authorize); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("mismatched owner argument error = %v, want forbidden", err)
	}
}

func TestJSONLTaskCenterIsUnsupportedAndManagedRecordsStayPrivate(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	scope := taskPlanTestAgentScope(t, "request", "mio", "user-A", tool.DataScopeUser)
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	if _, err := store.SubmitTaskPlan(ctx, scope, domainworkstream.TaskPlanSubmission{}, func(domainworkstream.Goal) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanUnsupported) {
		t.Fatalf("JSONL SubmitTaskPlan() error = %v, want unsupported", err)
	}
	if _, _, _, err := store.ListTaskPlans(ctx, scope, domainworkstream.TaskPlanQuery{}); !errors.Is(err, domainworkstream.ErrTaskPlanUnsupported) {
		t.Fatalf("JSONL ListTaskPlans() error = %v, want unsupported", err)
	}

	goal := taskPlanTestManagedGoal()
	legacyGoal := goal
	legacyGoal.TaskPlan = nil
	legacyGoal.Title = "legacy version"
	if err := appendJSONL(store.goalPath, legacyGoal); err != nil {
		t.Fatalf("append legacy Goal fixture: %v", err)
	}
	legacyDuplicate := legacyGoal
	legacyDuplicate.GoalID = "goal_legacy_duplicate"
	legacyDuplicate.Title = "legacy duplicate older"
	if err := appendJSONL(store.goalPath, legacyDuplicate); err != nil {
		t.Fatalf("append older legacy Goal version: %v", err)
	}
	legacyDuplicate.Title = "legacy duplicate newer"
	if err := appendJSONL(store.goalPath, legacyDuplicate); err != nil {
		t.Fatalf("append newer legacy Goal version: %v", err)
	}
	if err := appendJSONL(store.goalPath, goal); err != nil {
		t.Fatalf("append managed Goal fixture: %v", err)
	}
	workstream := domainworkstream.Workstream{
		WorkstreamID: goal.WorkstreamID, Name: "Task", Status: domainworkstream.StatusActive,
		CreatedAt: goal.CreatedAt, TaskCenterScope: &domainworkstream.TaskCenterWorkstreamScope{DataScope: domainworkstream.TaskPlanDataScopeUser, UserID: "user-A"},
	}
	if err := appendJSONL(store.workstreamPath, workstream); err != nil {
		t.Fatalf("append managed Workstream fixture: %v", err)
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: "art_managed_receipt", WorkstreamID: goal.WorkstreamID, Type: taskPlanOperationArtifactType,
		Status: "stored", CreatedAt: goal.CreatedAt, Payload: []byte(`{"internal":true}`),
	}
	if err := appendJSONL(store.artifactPath, artifact); err != nil {
		t.Fatalf("append internal Artifact fixture: %v", err)
	}
	for _, artifactType := range []string{taskMeetingOccurrenceArtifactType, taskMeetingOccurrenceReservationArtifactType} {
		meetingArtifact := domainworkstream.Artifact{
			ArtifactID: "art_" + artifactType, WorkstreamID: goal.WorkstreamID, Type: artifactType,
			Status: "stored", CreatedAt: goal.CreatedAt,
		}
		if err := appendJSONL(store.artifactPath, meetingArtifact); err != nil {
			t.Fatalf("append reserved meeting Artifact fixture: %v", err)
		}
	}

	goals, err := store.ListGoals(ctx, 50)
	if err != nil || len(goals) != 2 || goals[0].Title != "legacy duplicate newer" || goals[1].Title != "legacy duplicate older" {
		t.Fatalf("JSONL ListGoals() = %#v, error = %v, want legacy append history in reverse order", goals, err)
	}
	if _, found, err := store.FindGoalByID(ctx, goal.GoalID); err != nil || found {
		t.Fatalf("JSONL FindGoalByID() found %v, error %v", found, err)
	}
	workstreams, err := store.ListWorkstreams(ctx, 50)
	if err != nil || len(workstreams) != 0 {
		t.Fatalf("JSONL ListWorkstreams() = %#v, error = %v", workstreams, err)
	}
	artifacts, err := store.ListArtifacts(ctx, 50)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("JSONL ListArtifacts() = %#v, error = %v", artifacts, err)
	}

	if err := store.SaveGoal(ctx, legacyGoal); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("JSONL generic SaveGoal() overwrite error = %v, want forbidden", err)
	}
	workstream.TaskCenterScope = nil
	if err := store.SaveWorkstream(ctx, workstream); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("JSONL generic SaveWorkstream() overwrite error = %v, want forbidden", err)
	}
	artifact.Type = "report"
	if err := store.SaveArtifact(ctx, artifact); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("JSONL generic SaveArtifact() overwrite error = %v, want forbidden", err)
	}
	for _, artifactType := range []string{taskMeetingOccurrenceArtifactType, taskMeetingOccurrenceReservationArtifactType} {
		artifact.Type = artifactType
		if err := store.SaveArtifact(ctx, artifact); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
			t.Fatalf("JSONL generic SaveArtifact() reserved %q error = %v, want forbidden", artifactType, err)
		}
	}
}

func taskPlanTestStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func taskPlanTestAgentScope(t *testing.T, requestID, actorID, userID, dataScope string) tool.ToolExecutionScope {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(requestID, tool.ActorKindAgent, actorID, userID, []string{dataScope}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	return scope
}

func taskPlanTestUserScope(t *testing.T, requestID string) tool.ToolExecutionScope {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		requestID, tool.ActorKindUser, "user-A", "user-A", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(user) error = %v", err)
	}
	return scope
}

func taskPlanTestSubmission(scope tool.ToolExecutionScope, key, dataScope string) domainworkstream.TaskPlanSubmission {
	now := time.Now().UTC().Truncate(time.Second)
	userID := ""
	if dataScope == domainworkstream.TaskPlanDataScopeUser {
		userID = scope.AuthenticatedUserID
	}
	return domainworkstream.TaskPlanSubmission{
		IdempotencyKey: key,
		Goal: domainworkstream.Goal{
			Title: "Task title", Description: "Task description", SuccessCriteria: []string{"criterion"},
			Verification: []string{"verification"}, Status: domainworkstream.StatusWaiting, CreatedAt: now,
			TaskPlan: &domainworkstream.TaskPlan{
				DataScope: dataScope, UserID: userID, CreatorKind: string(scope.ActorKind), CreatedBy: scope.ActorID,
				Assignee: domainworkstream.TaskPlanAssigneeUnassigned, KindID: "research", Revision: 1,
				PolicyRevision: strings.Repeat("a", 64), HandoffState: domainworkstream.TaskPlanHandoffNone, UpdatedAt: now,
			},
		},
	}
}

func taskPlanTestManagedGoal() domainworkstream.Goal {
	now := time.Now().UTC().Truncate(time.Second)
	return domainworkstream.Goal{
		GoalID: "goal_managed", WorkstreamID: "ws_managed", Title: "Managed task", Description: "description",
		SuccessCriteria: []string{"criterion"}, Verification: []string{"verification"},
		Status: domainworkstream.StatusWaiting, CreatedAt: now,
		TaskPlan: &domainworkstream.TaskPlan{
			DataScope: domainworkstream.TaskPlanDataScopeUser, UserID: "user-A", CreatedBy: "mio",
			Assignee: domainworkstream.TaskPlanAssigneeUnassigned, KindID: "research", Revision: 1,
			PolicyRevision: strings.Repeat("a", 64), HandoffState: domainworkstream.TaskPlanHandoffNone, UpdatedAt: now,
		},
	}
}
