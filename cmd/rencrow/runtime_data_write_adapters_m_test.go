package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

func TestMovieCatalogOwnerPreferenceCandidateWriteRecallAndReopen(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	lookup, err := prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("prepare movie catalog lookup: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(writeRegistry, lookup); err != nil {
		t.Fatalf("register movie catalog write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallMovieCatalog(recallRegistry, lookup); err != nil {
		t.Fatalf("register movie catalog recall: %v", err)
	}
	assertRuntimeDataWriteEContract(t, writeRegistry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "movie_catalog",
		Operation:             "propose_preference_candidate",
		Access:                dataRecallAccessUser,
		RequiredPayloadFields: []string{"target_id", "target_kind"},
		OptionalPayloadFields: []string{"familiarity", "note", "sentiment"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	requestID := "movie-preference-owner-1"
	ctx := runtimeMovieCatalogUserContext(t, requestID, "user-1", "shiro", true)
	payload := map[string]any{
		"target_kind": " movie ", "target_id": " m1 ", "familiarity": " known ",
		"sentiment": " like ", "note": "  candidate note  ",
	}
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "movie_catalog", "propose_preference_candidate", payload)
	if first.IdempotentReplay || first.SchemaVersion != "movie-preference-candidate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.IdempotencyKey != requestID || first.PolicyRevision != runtimeDataWritePolicyRevision || !strings.HasPrefix(first.AuditRef, "movie-preference-candidate/sha256:") {
		t.Fatalf("first movie preference receipt=%#v", first)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var targetKind, targetID, familiarity, sentiment, note, state, userID, actorID string
	if err := db.QueryRow(`SELECT target_kind,target_id,familiarity,sentiment,note,state,user_id,actor_id FROM movie_preference_candidate WHERE id = ?`, first.AuditRef).Scan(&targetKind, &targetID, &familiarity, &sentiment, &note, &state, &userID, &actorID); err != nil {
		db.Close()
		t.Fatalf("candidate row: %v", err)
	}
	if targetKind != "movie" || targetID != "m1" || familiarity != "known" || sentiment != "like" || note != "candidate note" || state != "candidate" || userID != "user-1" || actorID != "shiro" {
		db.Close()
		t.Fatalf("candidate binding=%q/%q/%q/%q/%q state=%q user=%q actor=%q", targetKind, targetID, familiarity, sentiment, note, state, userID, actorID)
	}
	var selectionTableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='selection_state'`).Scan(&selectionTableCount); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if selectionTableCount != 0 {
		db.Close()
		t.Fatal("movie preference candidate must not create or mutate canonical selection_state")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	candidateRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "movie_catalog", "preference_candidate", first.AuditRef)
	if len(candidateRecall.Records) != 1 || candidateRecall.Records[0]["candidate_id"] != first.AuditRef || candidateRecall.Records[0]["target_kind"] != "movie" || candidateRecall.Records[0]["target_id"] != "m1" || candidateRecall.Records[0]["state"] != "candidate" {
		t.Fatalf("candidate recall=%#v", candidateRecall)
	}
	if _, leaked := candidateRecall.Records[0]["user_id"]; leaked {
		t.Fatal("candidate recall must not expose owner identity")
	}
	requestRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "movie_catalog", "requests", requestID)
	if len(requestRecall.Records) != 1 || requestRecall.Records[0]["request_id"] != requestID || requestRecall.Records[0]["candidate_id"] != first.AuditRef {
		t.Fatalf("request recall=%#v", requestRecall)
	}

	publicCtx := runtimeMovieCatalogUserContext(t, "movie-public-recall-1", "user-1", "shiro", true)
	publicMovies := runtimeDataWriteOwnerExecuteRecall(t, worker, publicCtx, "movie_catalog", "movies", "Heat")
	if len(publicMovies.Records) != 1 || publicMovies.Records[0]["movie_id"] != "m1" || publicMovies.Records[0]["title"] != "Heat" {
		t.Fatalf("public movie recall=%#v", publicMovies)
	}
	publicPeople := runtimeDataWriteOwnerExecuteRecall(t, worker, publicCtx, "movie_catalog", "people", "Al Pacino")
	if len(publicPeople.Records) != 1 || publicPeople.Records[0]["person_id"] != "p1" || publicPeople.Records[0]["name"] != "Al Pacino" {
		t.Fatalf("public people recall=%#v", publicPeople)
	}

	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "movie_catalog", "propose_preference_candidate", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("same-process replay=%#v first=%#v", second, first)
	}

	lookup, err = prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen movie catalog lookup: %v", err)
	}
	reopenedWrite := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(reopenedWrite, lookup); err != nil {
		t.Fatalf("register reopened movie catalog write: %v", err)
	}
	reopenedWorker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: reopenedWrite, DisableToolHarness: true})
	replayed := runtimeDataWriteOwnerExecuteWrite(t, reopenedWorker, ctx, "movie_catalog", "propose_preference_candidate", payload)
	if !replayed.IdempotentReplay || replayed.AuditRef != first.AuditRef {
		t.Fatalf("reopen replay=%#v first=%#v", replayed, first)
	}
}

func TestMovieCatalogOwnerPreferenceCandidateRejectsUnknownUnsafeAndConflictingRequests(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	lookup, err := prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(writeRegistry, lookup); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, DisableToolHarness: true})
	base := map[string]any{"target_kind": "movie", "target_id": "m1", "familiarity": "known"}
	for _, test := range []struct {
		name    string
		payload map[string]any
	}{
		{name: "unknown target", payload: map[string]any{"target_kind": "movie", "target_id": "missing", "familiarity": "known"}},
		{name: "unknown kind", payload: map[string]any{"target_kind": "music", "target_id": "m1", "familiarity": "known"}},
		{name: "empty dimensions", payload: map[string]any{"target_kind": "movie", "target_id": "m1"}},
		{name: "unsafe field", payload: map[string]any{"target_kind": "movie", "target_id": "m1", "familiarity": "known", "state": "confirmed"}},
		{name: "invalid utf8", payload: map[string]any{"target_kind": "movie", "target_id": "m1", "familiarity": "known", "note": string([]byte{0xff})}},
	} {
		response, err := worker.ExecuteV2(runtimeMovieCatalogUserContext(t, "movie-reject-"+test.name, "user-1", "shiro", false), "data.write", map[string]any{
			"store": "movie_catalog", "operation": "propose_preference_candidate", "payload": test.payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("%s response=%#v err=%v", test.name, response, err)
		}
	}

	requestID := "movie-preference-conflict-1"
	ctx := runtimeMovieCatalogUserContext(t, requestID, "user-1", "shiro", false)
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "movie_catalog", "propose_preference_candidate", base)
	changed := runtimeDataWriteOwnerClonePayload(base)
	changed["sentiment"] = "dislike"
	for _, conflictCtx := range []context.Context{
		ctx,
		runtimeMovieCatalogUserContext(t, requestID, "user-1", "mio", false),
		runtimeMovieCatalogUserContext(t, requestID, "user-2", "shiro", false),
	} {
		payload := base
		if conflictCtx == ctx {
			payload = changed
		}
		response, err := worker.ExecuteV2(conflictCtx, "data.write", map[string]any{"store": "movie_catalog", "operation": "propose_preference_candidate", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("conflict response=%#v err=%v", response, err)
		}
	}
	if first.AuditRef == "" {
		t.Fatal("first candidate receipt is empty")
	}
}

func TestMovieCatalogOwnerPreferenceCandidateRequiresUserScope(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	lookup, err := prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(writeRegistry, lookup); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "movie-no-user-1", false), "data.write", map[string]any{
		"store": "movie_catalog", "operation": "propose_preference_candidate", "payload": map[string]any{"target_kind": "movie", "target_id": "m1", "familiarity": "known"},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("missing user scope response=%#v err=%v", response, err)
	}
}

func runtimeMovieCatalogUserContext(t *testing.T, requestID, userID, actorID string, includePublic bool) context.Context {
	t.Helper()
	scopes := []string{domaintool.DataScopeUser}
	if includePublic {
		scopes = append(scopes, domaintool.DataScopePublic)
	}
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AuthenticatedUserID: userID, AllowedDataScopes: scopes,
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func TestMovieCatalogOwnerPreferenceCandidateSchemaIsPrivateToCanonicalLookup(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	lookup, err := prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(writeRegistry, lookup); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, DisableToolHarness: true})
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, runtimeMovieCatalogUserContext(t, "movie-people-candidate-1", "user-1", "mio", false), "movie_catalog", "propose_preference_candidate", map[string]any{
		"target_kind": "person", "target_id": "p1", "sentiment": "like",
	})
	value, err := lookup.Lookup(context.Background(), "person", "Al Pacino", "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(moviecatalogapp.LookupResult)
	if !ok {
		t.Fatalf("canonical lookup result type=%T", value)
	}
	if len(result.People) != 1 || result.People[0].PersonID != "p1" {
		t.Fatalf("canonical lookup result=%#v", result)
	}
	if strings.Contains(result.People[0].PersonID, first.AuditRef) {
		t.Fatal("candidate id leaked into canonical lookup")
	}
}
