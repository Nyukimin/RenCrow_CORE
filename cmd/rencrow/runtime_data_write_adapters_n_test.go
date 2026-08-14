package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

func TestHobbyGraphOwnerPreferenceCandidateWriteRecallReopenAndCanonicalIsolation(t *testing.T) {
	path := seedRuntimeHobbyGraphOwnerDatabase(t)
	lookup, err := prepareRuntimeMusicCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("prepare hobby graph lookup: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteHobbyGraph(writeRegistry, lookup); err != nil {
		t.Fatalf("register hobby graph write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallHobbyGraph(recallRegistry, lookup); err != nil {
		t.Fatalf("register hobby graph recall: %v", err)
	}
	assertRuntimeDataWriteEContract(t, writeRegistry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "hobby_graph",
		Operation:             "propose_preference_candidate",
		Access:                dataRecallAccessUser,
		RequiredPayloadFields: []string{"signal_type", "target_item_id"},
		OptionalPayloadFields: []string{"note"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	requestID := "hobby-preference-owner-1"
	ctx := runtimeHobbyGraphUserContext(t, requestID, "user-1", "shiro", true)
	payload := map[string]any{
		"target_item_id": " song-1 ",
		"signal_type":    " LIKE ",
		"note":           "  private candidate note  ",
	}
	beforeSignals := runtimeHobbyGraphCanonicalSignalCount(t, path)
	first := runtimeHobbyGraphExecuteWrite(t, worker, ctx, payload)
	if first.IdempotentReplay || first.SchemaVersion != "hobby-preference-candidate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.IdempotencyKey != requestID || first.PolicyRevision != runtimeDataWritePolicyRevision || !strings.HasPrefix(first.AuditRef, runtimeHobbyPreferenceCandidateIDPrefix) {
		t.Fatalf("first hobby preference receipt = %#v", first)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var candidateID, request, userID, actorID, payloadHash, targetID, signalType, note, state, createdAt string
	err = db.QueryRow(`SELECT candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at FROM hobby_agent_preference_candidate WHERE candidate_id = ?`, first.AuditRef).Scan(&candidateID, &request, &userID, &actorID, &payloadHash, &targetID, &signalType, &note, &state, &createdAt)
	if err != nil {
		db.Close()
		t.Fatalf("candidate row: %v", err)
	}
	if candidateID != first.AuditRef || request != requestID || userID != "user-1" || actorID != "shiro" || payloadHash == "" || targetID != "song-1" || signalType != "like" || note != "private candidate note" || state != "candidate" || createdAt == "" {
		db.Close()
		t.Fatalf("candidate binding=%q/%q/%q/%q/%q/%q/%q/%q/%q/%q", candidateID, request, userID, actorID, payloadHash, targetID, signalType, note, state, createdAt)
	}
	if got := runtimeHobbyGraphCanonicalSignalCountDB(t, db); got != beforeSignals {
		db.Close()
		t.Fatalf("canonical preference signals changed: before=%d after=%d", beforeSignals, got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	candidateRecall := runtimeHobbyGraphExecuteRecall(t, worker, ctx, "preference_candidate", first.AuditRef)
	if len(candidateRecall.Records) != 1 || candidateRecall.Records[0]["candidate_id"] != first.AuditRef || candidateRecall.Records[0]["request_id"] != requestID || candidateRecall.Records[0]["user_id"] != "user-1" || candidateRecall.Records[0]["target_item_id"] != "song-1" || candidateRecall.Records[0]["signal_type"] != "like" || candidateRecall.Records[0]["state"] != "candidate" {
		t.Fatalf("candidate exact recall = %#v", candidateRecall)
	}
	requestRecall := runtimeHobbyGraphExecuteRecall(t, worker, ctx, "requests", requestID)
	if len(requestRecall.Records) != 1 || requestRecall.Records[0]["request_id"] != requestID || requestRecall.Records[0]["candidate_id"] != first.AuditRef || requestRecall.Records[0]["payload_hash"] != payloadHash {
		t.Fatalf("request exact recall = %#v", requestRecall)
	}
	otherUser := runtimeHobbyGraphUserContext(t, "hobby-preference-other", "user-2", "shiro", false)
	if result := runtimeHobbyGraphExecuteRecall(t, worker, otherUser, "preference_candidate", first.AuditRef); len(result.Records) != 0 {
		t.Fatalf("cross-user candidate recall leaked = %#v", result)
	}
	if result := runtimeHobbyGraphExecuteRecall(t, worker, otherUser, "requests", requestID); len(result.Records) != 0 {
		t.Fatalf("cross-user request recall leaked = %#v", result)
	}

	replayed := runtimeHobbyGraphExecuteWrite(t, worker, ctx, payload)
	if !replayed.IdempotentReplay || replayed.AuditRef != first.AuditRef {
		t.Fatalf("same-process replay = %#v first=%#v", replayed, first)
	}
	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["signal_type"] = "dislike"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{
		"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": changed,
	}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request payload conflict response=%#v err=%v", response, err)
	}
	if response, err := worker.ExecuteV2(runtimeHobbyGraphUserContext(t, requestID, "user-1", "mio", false), "data.write", map[string]any{
		"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": payload,
	}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request actor conflict response=%#v err=%v", response, err)
	}
	if response, err := worker.ExecuteV2(runtimeHobbyGraphUserContext(t, requestID, "user-2", "shiro", false), "data.write", map[string]any{
		"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": payload,
	}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request user conflict response=%#v err=%v", response, err)
	}

	reopenedLookup, err := prepareRuntimeMusicCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen hobby graph lookup: %v", err)
	}
	reopenedWrite := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteHobbyGraph(reopenedWrite, reopenedLookup); err != nil {
		t.Fatalf("register reopened hobby graph write: %v", err)
	}
	reopenedRecall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallHobbyGraph(reopenedRecall, reopenedLookup); err != nil {
		t.Fatalf("register reopened hobby graph recall: %v", err)
	}
	reopenedWorker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: reopenedWrite, OperationalDataRecall: reopenedRecall, DisableToolHarness: true,
	})
	reopenedReplay := runtimeHobbyGraphExecuteWrite(t, reopenedWorker, ctx, payload)
	if !reopenedReplay.IdempotentReplay || reopenedReplay.AuditRef != first.AuditRef {
		t.Fatalf("reopen replay = %#v first=%#v", reopenedReplay, first)
	}
	if got := runtimeHobbyGraphCanonicalSignalCount(t, path); got != beforeSignals {
		t.Fatalf("canonical preference signals changed after reopen replay: before=%d after=%d", beforeSignals, got)
	}
}

func TestHobbyPreferenceCandidateRejectsUnknownTargetUnsafeFieldsAndBounds(t *testing.T) {
	path := seedRuntimeHobbyGraphOwnerDatabase(t)
	lookup, err := prepareRuntimeMusicCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteHobbyGraph(registry, lookup); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	base := map[string]any{"target_item_id": "song-1", "signal_type": "like"}
	for _, testCase := range []struct {
		name    string
		payload map[string]any
	}{
		{name: "unknown target", payload: map[string]any{"target_item_id": "missing", "signal_type": "like"}},
		{name: "unsafe state", payload: map[string]any{"target_item_id": "song-1", "signal_type": "like", "state": "confirmed"}},
		{name: "unsafe lyrics", payload: map[string]any{"target_item_id": "song-1", "signal_type": "like", "lyrics": "text"}},
		{name: "invalid utf8", payload: map[string]any{"target_item_id": "song-1", "signal_type": "like", "note": string([]byte{0xff})}},
		{name: "overlong target", payload: map[string]any{"target_item_id": strings.Repeat("a", runtimeHobbyPreferenceTargetMaxRunes+1), "signal_type": "like"}},
		{name: "overlong note", payload: map[string]any{"target_item_id": "song-1", "signal_type": "like", "note": strings.Repeat("a", runtimeHobbyPreferenceNoteMaxRunes+1)}},
	} {
		response, err := worker.ExecuteV2(runtimeHobbyGraphUserContext(t, "hobby-reject-"+testCase.name, "user-1", "shiro", false), "data.write", map[string]any{
			"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": testCase.payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("%s response=%#v err=%v", testCase.name, response, err)
		}
	}
	if got := runtimeHobbyGraphCandidateCount(t, path); got != 0 {
		t.Fatalf("rejected payloads inserted candidates: %d", got)
	}
	publicScope := runtimeHobbyGraphPublicContext(t, "hobby-public-write")
	response, err := worker.ExecuteV2(publicScope, "data.write", map[string]any{
		"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": base,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("public write response=%#v err=%v", response, err)
	}
}

func runtimeHobbyGraphExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, payload map[string]any) runtimeDataWriteReceipt {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{
		"store": "hobby_graph", "operation": "propose_preference_candidate", "payload": payload,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("hobby graph data.write response=%#v err=%v", response, err)
	}
	receipt, ok := response.Result.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("hobby graph data.write result type=%T value=%#v", response.Result, response.Result)
	}
	return receipt
}

func runtimeHobbyGraphExecuteRecall(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, operation, query string) runtimeDataRecallResult {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "hobby_graph", "operation": operation, "query": query, "limit": 10,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("hobby graph data.recall response=%#v err=%v", response, err)
	}
	result, ok := response.Result.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("hobby graph data.recall result type=%T value=%#v", response.Result, response.Result)
	}
	return result
}

func runtimeHobbyGraphUserContext(t *testing.T, requestID, userID, actorID string, public bool) context.Context {
	t.Helper()
	scopes := []string{domaintool.DataScopeUser}
	if public {
		scopes = append(scopes, domaintool.DataScopePublic)
	}
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AuthenticatedUserID: userID, AllowedDataScopes: scopes,
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("hobby graph user scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeHobbyGraphPublicContext(t *testing.T, requestID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "mio",
		AllowedDataScopes:    []string{domaintool.DataScopePublic},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("hobby graph public scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func seedRuntimeHobbyGraphOwnerDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open hobby graph seed: %v", err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY,category TEXT,item_type TEXT,title TEXT,normalized_title TEXT,subtitle TEXT,canonical_source TEXT,canonical_url TEXT,metadata_json TEXT)`,
		`CREATE TABLE hobby_relations(relation_id TEXT PRIMARY KEY,from_item_id TEXT,to_item_id TEXT,relation_type TEXT,source TEXT,evidence_url TEXT)`,
		`CREATE TABLE hobby_interactions(interaction_id TEXT PRIMARY KEY,item_id TEXT,note TEXT)`,
		`CREATE TABLE hobby_preference_signals(signal_id TEXT PRIMARY KEY,target_item_id TEXT,evidence_json TEXT)`,
		`CREATE TABLE hobby_music_lyrics(lyrics_id TEXT PRIMARY KEY,song_item_id TEXT,source TEXT,source_record_id TEXT,canonical_url TEXT,language TEXT,rights_status TEXT,license_reference TEXT,storage_mode TEXT,lyrics_text TEXT,content_sha256 TEXT,fetched_at TEXT,updated_at TEXT)`,
		`CREATE TABLE hobby_music_syntax_features(feature_id TEXT PRIMARY KEY,song_item_id TEXT,lyrics_source TEXT,language TEXT,analyzer TEXT,analyzer_version TEXT,feature_schema TEXT,token_count INTEGER,line_count INTEGER,vocabulary_size INTEGER,features_json TEXT,source_content_sha256 TEXT,non_reconstructable INTEGER,generated_at TEXT)`,
		`INSERT INTO hobby_items(item_id,category,item_type,title,normalized_title,subtitle,canonical_source,canonical_url,metadata_json) VALUES ('artist-1','music','artist','Artist One','artist one','','seed','https://example.test/artist-1','{}'),('song-1','music','song','Blue Bird','blue bird','','seed','https://example.test/song-1','{}'),('song-2','music','song','Other Song','other song','','seed','https://example.test/song-2','{}')`,
		`INSERT INTO hobby_relations(relation_id,from_item_id,to_item_id,relation_type,source,evidence_url) VALUES ('relation-1','artist-1','song-1','performed','seed','https://example.test/relation-1')`,
		`INSERT INTO hobby_interactions(interaction_id,item_id,note) VALUES ('interaction-1','song-1','canonical interaction')`,
		`INSERT INTO hobby_preference_signals(signal_id,target_item_id,evidence_json) VALUES ('signal-1','song-1','canonical signal')`,
		`INSERT INTO hobby_music_lyrics(lyrics_id,song_item_id,source,source_record_id,canonical_url,language,rights_status,license_reference,storage_mode,lyrics_text,content_sha256,fetched_at,updated_at) VALUES ('lyrics-1','song-1','reference','record-1','https://example.test/lyrics-1','ja','unknown','','hash_only',NULL,'hash-1','2026-08-14T00:00:00Z','2026-08-14T00:00:00Z')`,
		`INSERT INTO hobby_music_syntax_features(feature_id,song_item_id,lyrics_source,language,analyzer,analyzer_version,feature_schema,token_count,line_count,vocabulary_size,features_json,source_content_sha256,non_reconstructable,generated_at) VALUES ('feature-1','song-1','reference','ja','syntax','1','rencrow.music.syntax.v1',2,1,2,'{}','hash-1',1,'2026-08-14T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed hobby graph statement failed: %v", err)
		}
	}
	return path
}

func runtimeHobbyGraphCanonicalSignalCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open canonical signal database: %v", err)
	}
	defer db.Close()
	return runtimeHobbyGraphCanonicalSignalCountDB(t, db)
}

func runtimeHobbyGraphCanonicalSignalCountDB(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_preference_signals`).Scan(&count); err != nil {
		t.Fatalf("count canonical preference signals: %v", err)
	}
	return count
}

func runtimeHobbyGraphCandidateCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open candidate database: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_agent_preference_candidate`).Scan(&count); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0
		}
		t.Fatalf("count hobby preference candidates: %v", err)
	}
	return count
}
