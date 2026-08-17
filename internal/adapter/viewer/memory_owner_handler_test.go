package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type memoryOwnerStoreStub struct {
	items                  []domainmemory.UserMemory
	listUserID             string
	listScope              domaintool.ToolExecutionScope
	proposeScope           domaintool.ToolExecutionScope
	findID                 string
	proposeID              string
	proposeUserID          string
	proposeActor           string
	proposeType            string
	proposeText            string
	proposeReason          string
	transitionOp           string
	transitionID           string
	transitionNew          string
	transitionWhy          string
	archiveScope           domaintool.ToolExecutionScope
	archiveUserID          string
	archiveActor           string
	archiveID              string
	archiveReason          string
	archiveErr             error
	lifecyclePlanRequestID string
	lifecyclePlanOwnerID   string
	lifecyclePlanActorID   string
	lifecyclePlanScope     domaintool.ToolExecutionScope
	lifecyclePlanErr       error
	lifecycleRunRequestID  string
	lifecycleRunOwnerID    string
	lifecycleRunActorID    string
	lifecycleRunPlanID     string
	lifecycleRunReason     string
	lifecycleRunApply      bool
	lifecycleRunScope      domaintool.ToolExecutionScope
	lifecycleRunErr        error
	requestID              string
	parquetExportCalls     int
	parquetExportRequestID string
	parquetExportUserID    string
	parquetExportActorID   string
	parquetExportScope     domaintool.ToolExecutionScope
	parquetExportErr       error
	parquetVerifyCalls     int
	parquetVerifyRequestID string
	parquetVerifyUserID    string
	parquetVerifyActorID   string
	parquetVerifyTargetID  string
	parquetVerifyScope     domaintool.ToolExecutionScope
	parquetVerifyErr       error
	knowledgeBackfillCalls int
	knowledgeBackfillReqID string
	knowledgeBackfillOwner string
	knowledgeBackfillActor string
	knowledgeBackfillApply bool
	knowledgeBackfillScope domaintool.ToolExecutionScope
	knowledgeBackfillErr   error
}

func (s *memoryOwnerStoreStub) BackfillKnowledgeCommonRaw(ctx context.Context, requestID, ownerID, actorID string, apply bool) (domainmemory.KnowledgeCommonRawBackfillResult, error) {
	s.knowledgeBackfillCalls++
	s.knowledgeBackfillReqID, s.knowledgeBackfillOwner, s.knowledgeBackfillActor = requestID, ownerID, actorID
	s.knowledgeBackfillApply = apply
	s.knowledgeBackfillScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	if s.knowledgeBackfillErr != nil {
		return domainmemory.KnowledgeCommonRawBackfillResult{}, s.knowledgeBackfillErr
	}
	status := domainmemory.CommonRawStateBlocked
	if apply {
		status = domainmemory.CommonRawStateCompleted
	}
	return domainmemory.KnowledgeCommonRawBackfillResult{
		Validated: 2, ItemCount: 2, Coverage: 2, Ready: apply,
		RawImported: 2, Linked: 2, Status: status, ManifestID: "knowledge-manifest",
		RawRecordIDs: []string{"raw-one", "raw-two"},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerListUserMemories(ctx context.Context, userID, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	s.listUserID = userID
	s.listScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return append([]domainmemory.UserMemory(nil), s.items...), nil
}

func (s *memoryOwnerStoreStub) OwnerFindUserMemory(_ context.Context, _, id string) (domainmemory.UserMemory, error) {
	s.findID = id
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return domainmemory.UserMemory{}, domainmemory.ErrUserMemoryOwnerNotFound
}

func (s *memoryOwnerStoreStub) OwnerProposeUserMemory(ctx context.Context, requestID, userID, actorID, memoryType, statement, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	s.requestID, s.proposeID, s.proposeUserID, s.proposeActor = requestID, requestID, userID, actorID
	s.proposeType, s.proposeText, s.proposeReason = memoryType, statement, reason
	s.proposeScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return domainmemory.UserMemoryOwnerResult{
		Item:    domainmemory.UserMemoryOwnerView{ID: "owner-proposed", Type: memoryType, Statement: statement, State: domainmemory.MemoryStateCandidate, Active: true},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: domainmemory.UserMemoryOwnerOperationPropose, Status: "completed", OwnerRoute: "conversation_l1/user_memory/propose", PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, InputCount: 1, OutputCount: 1, Warnings: []string{}, AuditReference: "owner-proposed", CompletedAt: time.Now().UTC()},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerTransitionUserMemory(_ context.Context, requestID, userID, actorID, id, operation, replacementID, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	s.requestID, s.proposeUserID, s.proposeActor = requestID, userID, actorID
	s.transitionOp, s.transitionID, s.transitionNew, s.transitionWhy = operation, id, replacementID, reason
	return domainmemory.UserMemoryOwnerResult{
		Item:    domainmemory.UserMemoryOwnerView{ID: id, Type: domainmemory.UserMemoryTypePreference, Statement: "owner item", State: domainmemory.MemoryStateConfirmed, Active: true},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: operation, Status: "completed", OwnerRoute: "conversation_l1/user_memory/" + operation, PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, InputCount: 1, OutputCount: 1, Warnings: []string{}, AuditReference: id, CompletedAt: time.Now().UTC()},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerArchiveUserMemory(ctx context.Context, requestID, userID, actorID, id, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	if s.archiveErr != nil {
		return domainmemory.UserMemoryOwnerResult{}, s.archiveErr
	}
	s.requestID, s.archiveUserID, s.archiveActor = requestID, userID, actorID
	s.archiveID, s.archiveReason = id, reason
	s.archiveScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return domainmemory.UserMemoryOwnerResult{
		Item:    domainmemory.UserMemoryOwnerView{ID: id, Type: domainmemory.UserMemoryTypePreference, Statement: "owner item", State: domainmemory.MemoryStateConfirmed, Active: true},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: "archive", Status: "completed", OwnerRoute: "conversation_archive/user_memory/archive", PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, InputCount: 1, OutputCount: 1, Warnings: []string{}, AuditReference: id, CompletedAt: time.Now().UTC()},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerRecallUserMemories(_ context.Context, requestID, ownerID, query string, limit int) (domainmemory.UserMemoryOwnerRecallResult, error) {
	now := time.Now().UTC()
	return domainmemory.UserMemoryOwnerRecallResult{
		Items:   []domainmemory.UserMemoryOwnerRecallItem{},
		Trace:   domainmemory.UserMemoryRecallTrace{ID: "trace:owner:test", Status: "completed", QueryTextRedacted: query, CreatedAt: now},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: domainmemory.UserMemoryOwnerOperationRecall, Status: "completed", OwnerRoute: "conversation_l1/user_memory/recall", PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, Warnings: []string{}, AuditReference: "trace:owner:test", CompletedAt: now},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerListRecallTraces(_ context.Context, _ string, _ int) ([]domainmemory.UserMemoryTraceSummary, error) {
	return []domainmemory.UserMemoryTraceSummary{}, nil
}

func (s *memoryOwnerStoreStub) OwnerFindRecallTrace(_ context.Context, _ string, id string) (domainmemory.UserMemoryTraceDetail, error) {
	return domainmemory.UserMemoryTraceDetail{UserMemoryTraceSummary: domainmemory.UserMemoryTraceSummary{ID: id, Status: "completed"}, QueryTextRedacted: "trace", Items: []domainmemory.UserMemoryTraceItem{}}, nil
}

func (s *memoryOwnerStoreStub) OwnerPlanUserMemoryLifecycle(ctx context.Context, requestID, ownerID, actorID string) (domainmemory.UserMemoryLifecyclePlanResponse, error) {
	if s.lifecyclePlanErr != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, s.lifecyclePlanErr
	}
	s.lifecyclePlanRequestID, s.lifecyclePlanOwnerID, s.lifecyclePlanActorID = requestID, ownerID, actorID
	s.lifecyclePlanScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	now := time.Now().UTC()
	return domainmemory.UserMemoryLifecyclePlanResponse{
		PlanRequestID: requestID, Status: "planned", PolicyRevision: domainmemory.UserMemoryLifecyclePolicyRevision,
		CohortHash: "cohort", EvaluationAt: now, ExpiresAt: now.Add(15 * time.Minute), Actions: []domainmemory.UserMemoryLifecycleAction{},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: domainmemory.UserMemoryLifecycleOperationPlan, Status: "completed", OwnerRoute: "conversation_l1/user_memory/lifecycle/plan", PolicyRevision: domainmemory.UserMemoryLifecyclePolicyRevision, IdempotencyKey: requestID, InputCount: 0, OutputCount: 0, Warnings: []string{}, AuditReference: requestID, CompletedAt: now},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerRunUserMemoryLifecycle(ctx context.Context, requestID, ownerID, actorID, planRequestID, reason string, apply bool) (domainmemory.UserMemoryLifecycleRunResponse, error) {
	if s.lifecycleRunErr != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, s.lifecycleRunErr
	}
	s.lifecycleRunRequestID, s.lifecycleRunOwnerID, s.lifecycleRunActorID = requestID, ownerID, actorID
	s.lifecycleRunPlanID, s.lifecycleRunReason, s.lifecycleRunApply = planRequestID, reason, apply
	s.lifecycleRunScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	now := time.Now().UTC()
	return domainmemory.UserMemoryLifecycleRunResponse{
		PlanRequestID: planRequestID, Status: "completed", Actions: []domainmemory.UserMemoryLifecycleAction{},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: domainmemory.UserMemoryLifecycleOperationRun, Status: "completed", OwnerRoute: "conversation_l1/user_memory/lifecycle/run", PolicyRevision: domainmemory.UserMemoryLifecyclePolicyRevision, IdempotencyKey: requestID, InputCount: 0, OutputCount: 0, Warnings: []string{}, AuditReference: planRequestID, CompletedAt: now},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerExportConversationArchiveParquet(ctx context.Context, requestID, ownerID, actorID string) (domainmemory.ConversationArchiveParquetExportResult, error) {
	s.parquetExportCalls++
	s.parquetExportRequestID, s.parquetExportUserID, s.parquetExportActorID = requestID, ownerID, actorID
	s.parquetExportScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	if s.parquetExportErr != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, s.parquetExportErr
	}
	now := time.Now().UTC()
	return domainmemory.ConversationArchiveParquetExportResult{
		ExportID:        requestID,
		CreatedAt:       now,
		RunRelPath:      "runs/" + requestID,
		ManifestRelPath: "runs/" + requestID + "/manifest.json",
		Files:           []domainmemory.ConversationArchiveParquetFile{{RelativePath: "runs/" + requestID + "/archive.parquet", RowCount: 0, Bytes: 1, SHA256: strings.Repeat("a", 64)}},
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID: requestID, Operation: domainmemory.UserMemoryOwnerOperationParquetExport,
			Status: "completed", OwnerRoute: "conversation_archive/parquet/export",
			PolicyRevision: domainmemory.ConversationArchiveParquetPolicyRevision,
			IdempotencyKey: requestID, Warnings: []string{}, CompletedAt: now,
		},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerVerifyConversationArchiveParquet(ctx context.Context, requestID, ownerID, actorID, targetExportRequestID string) (domainmemory.ConversationArchiveParquetVerifyResult, error) {
	s.parquetVerifyCalls++
	s.parquetVerifyRequestID, s.parquetVerifyUserID, s.parquetVerifyActorID, s.parquetVerifyTargetID = requestID, ownerID, actorID, targetExportRequestID
	s.parquetVerifyScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	if s.parquetVerifyErr != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, s.parquetVerifyErr
	}
	now := time.Now().UTC()
	return domainmemory.ConversationArchiveParquetVerifyResult{
		ExportID:        targetExportRequestID,
		CreatedAt:       now,
		RunRelPath:      "runs/" + targetExportRequestID,
		ManifestRelPath: "runs/" + targetExportRequestID + "/manifest.json",
		Files:           []domainmemory.ConversationArchiveParquetFile{{RelativePath: "runs/" + targetExportRequestID + "/archive.parquet", RowCount: 0, Bytes: 1, SHA256: strings.Repeat("b", 64)}},
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID: requestID, Operation: domainmemory.UserMemoryOwnerOperationParquetVerify,
			Status: "completed", OwnerRoute: "conversation_archive/parquet/verify",
			PolicyRevision: domainmemory.ConversationArchiveParquetPolicyRevision,
			IdempotencyKey: requestID, AuditReference: targetExportRequestID,
			Warnings: []string{}, CompletedAt: now,
		},
	}, nil
}

func TestMemoryOwnerHandlerAuthenticatesLoopbackAndUsesConfiguredUserScope(t *testing.T) {
	store := &memoryOwnerStoreStub{items: []domainmemory.UserMemory{{ID: "mem-1", UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference, Statement: "short answers", State: domainmemory.MemoryStateCandidate, Active: true}}}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user?limit=3", nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	rec := httptest.NewRecorder()
	h(rewritableResponseWriter{ResponseRecorder: rec}, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.listUserID != "ren" || store.listScope.AuthenticatedUserID != "ren" || !store.listScope.Allows(domaintool.DataScopeUser) || store.listScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		t.Fatalf("owner scope/user not propagated: user=%q scope=%+v", store.listUserID, store.listScope)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if receipt, ok := body["receipt"].(map[string]any); !ok || receipt["operation"] != domainmemory.UserMemoryOwnerOperationList || receipt["status"] != "completed" {
		t.Fatalf("missing list receipt: %#v", body)
	}
	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{"namespace", "user_id", "source", "meta", "sql", "path"} {
		if strings.Contains(string(raw), "\""+forbidden+"\"") {
			t.Fatalf("safe owner response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestMemoryOwnerHandlerRejectsRemoteAndProfileOrBearerMismatch(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	tests := []struct {
		name    string
		remote  string
		token   string
		client  string
		profile string
		want    int
	}{
		{name: "remote", remote: "192.0.2.5:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_CMD", profile: "cmd-diagnostics", want: http.StatusNotFound},
		{name: "bearer", remote: "127.0.0.1:18790", token: "wrong-token", client: "RenCrow_CMD", profile: "cmd-diagnostics", want: http.StatusUnauthorized},
		{name: "profile", remote: "127.0.0.1:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_CMD", profile: "cmd-control", want: http.StatusForbidden},
		{name: "client", remote: "127.0.0.1:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_PORTAL", profile: "cmd-diagnostics", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user", nil)
			req.RemoteAddr = tt.remote
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.Header.Set("X-RenCrow-Client", tt.client)
			req.Header.Set("X-RenCrow-Interaction-Profile", tt.profile)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestMemoryOwnerHandlerRejectsCallerOwnedQueryParameters(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user?user_id=other", nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMemoryOwnerHandlerProposeAndTransitionAreStrictAndReceiptBound(t *testing.T) {
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	newRequest := func(method, path, body, profile string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	propose := httptest.NewRecorder()
	h(propose, newRequest(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"operator confirmed"}`, "cmd-control"))
	if propose.Code != http.StatusOK || store.proposeUserID != "ren" || store.proposeActor != "ren" || store.proposeType != "preference" || store.proposeText != "short answers" || store.proposeReason != "operator confirmed" {
		t.Fatalf("propose status/body=%d/%s store=%+v", propose.Code, propose.Body.String(), store)
	}
	if store.requestID == "" {
		t.Fatal("CORE must generate a request ID")
	}
	if store.proposeScope.RequestID != store.requestID {
		t.Fatalf("store request id=%q scope request id=%q", store.requestID, store.proposeScope.RequestID)
	}
	var proposeBody map[string]any
	if err := json.Unmarshal(propose.Body.Bytes(), &proposeBody); err != nil {
		t.Fatalf("decode propose: %v", err)
	}
	if receipt, ok := proposeBody["receipt"].(map[string]any); !ok || receipt["status"] != "completed" || receipt["idempotency_key"] != store.requestID {
		t.Fatalf("invalid propose receipt: %#v", proposeBody)
	}

	bad := httptest.NewRecorder()
	h(bad, newRequest(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"ok","request_id":"caller-controlled"}`, "cmd-control"))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown request_id status=%d body=%s", bad.Code, bad.Body.String())
	}

	transition := httptest.NewRecorder()
	h(transition, newRequest(http.MethodPost, "/v1/memory/user/mem-1/confirm", `{"reason":"reviewed"}`, "cmd-control"))
	if transition.Code != http.StatusOK || store.transitionOp != domainmemory.UserMemoryOwnerOperationConfirm || store.transitionID != "mem-1" || store.transitionWhy != "reviewed" {
		t.Fatalf("transition status/body=%d/%s store=%+v", transition.Code, transition.Body.String(), store)
	}
}

func TestMemoryOwnerHandlerUnescapesSlashMemoryIDs(t *testing.T) {
	const id = "user-memory-candidate/sha256:abc"
	store := &memoryOwnerStoreStub{items: []domainmemory.UserMemory{{ID: id, UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference, Statement: "short answers", State: domainmemory.MemoryStateCandidate, Active: true}}}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	escaped := url.PathEscape(id)
	show := request(http.MethodGet, "/v1/memory/user/"+escaped, "")
	show.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	showRec := httptest.NewRecorder()
	h(showRec, show)
	if showRec.Code != http.StatusOK || store.findID != id {
		t.Fatalf("show status=%d body=%s id=%q", showRec.Code, showRec.Body.String(), store.findID)
	}
	var showBody map[string]any
	if err := json.Unmarshal(showRec.Body.Bytes(), &showBody); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if receipt, ok := showBody["receipt"].(map[string]any); !ok || receipt["operation"] != domainmemory.UserMemoryOwnerOperationShow || receipt["status"] != "completed" {
		t.Fatalf("missing show receipt: %#v", showBody)
	}
	confirm := httptest.NewRecorder()
	h(confirm, request(http.MethodPost, "/v1/memory/user/"+escaped+"/confirm", `{"reason":"reviewed"}`))
	if confirm.Code != http.StatusOK || store.transitionID != id || store.transitionOp != domainmemory.UserMemoryOwnerOperationConfirm {
		t.Fatalf("confirm status=%d body=%s id=%q operation=%q", confirm.Code, confirm.Body.String(), store.transitionID, store.transitionOp)
	}
}

func TestMemoryOwnerHandlerRealStoreUsesEscapedIDsForEveryMutation(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "owner-e2e.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	request := func(method, path, body, profile string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	write := func(method, path, body string) domainmemory.UserMemoryOwnerResult {
		rec := httptest.NewRecorder()
		h(rec, request(method, path, body, "cmd-control"))
		if rec.Code != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var result domainmemory.UserMemoryOwnerResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return result
	}
	first := write(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"operator confirmed"}`)
	firstID := first.Item.ID
	if !strings.Contains(firstID, "/") {
		t.Fatalf("candidate ID=%q must exercise escaped path segment", firstID)
	}
	show := httptest.NewRecorder()
	h(show, request(http.MethodGet, "/v1/memory/user/"+url.PathEscape(firstID), "", "cmd-diagnostics"))
	if show.Code != http.StatusOK {
		t.Fatalf("show status=%d body=%s", show.Code, show.Body.String())
	}
	confirmed := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/confirm", `{"reason":"reviewed"}`)
	if confirmed.Item.State != domainmemory.MemoryStateConfirmed {
		t.Fatalf("confirmed=%+v", confirmed.Item)
	}
	pinned := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/pin", `{"reason":"keep fixed"}`)
	if pinned.Item.State != domainmemory.MemoryStatePinned {
		t.Fatalf("pinned=%+v", pinned.Item)
	}
	second := write(http.MethodPost, "/v1/memory/user/propose", `{"type":"project","statement":"owner project","reason":"operator confirmed"}`)
	superseded := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/supersede", `{"replacement_id":"`+second.Item.ID+`","reason":"newer item"}`)
	if superseded.Item.Active || superseded.Item.SupersededBy != second.Item.ID {
		t.Fatalf("superseded=%+v", superseded.Item)
	}
	forgotten := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(second.Item.ID)+"/forget", `{"reason":"remove item"}`)
	if forgotten.Item.Active {
		t.Fatalf("forgotten=%+v", forgotten.Item)
	}
}

func memoryOwnerContractRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	return req
}

func TestMemoryOwnerRecallAndTraceRoutesUseTypedOwnerContract(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	cases := []struct {
		name      string
		path      string
		operation string
	}{
		{name: "recall", path: "/v1/memory/user/recall?query=blue&limit=12", operation: "recall"},
		{name: "trace list", path: "/v1/memory/user/traces?limit=20", operation: "trace_list"},
		{name: "trace show", path: "/v1/memory/user/traces/trace%2Fone", operation: "trace_show"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerContractRequest(http.MethodGet, tc.path))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want typed owner success", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			receipt, ok := body["receipt"].(map[string]any)
			if !ok || receipt["operation"] != tc.operation || receipt["status"] != "completed" {
				t.Fatalf("receipt=%#v, want operation=%q completed", body["receipt"], tc.operation)
			}
		})
	}
}

func TestMemoryOwnerRecallTraceRejectsInvalidBoundsAndQueries(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	cases := []struct {
		name string
		path string
	}{
		{name: "recall query missing", path: "/v1/memory/user/recall?limit=12"},
		{name: "recall query blank", path: "/v1/memory/user/recall?query=&limit=12"},
		{name: "recall query too long", path: "/v1/memory/user/recall?query=" + strings.Repeat("あ", 513)},
		{name: "recall duplicate query", path: "/v1/memory/user/recall?query=blue&query=green"},
		{name: "recall unknown query", path: "/v1/memory/user/recall?query=blue&session_id=caller"},
		{name: "recall limit below range", path: "/v1/memory/user/recall?query=blue&limit=0"},
		{name: "recall limit above range", path: "/v1/memory/user/recall?query=blue&limit=51"},
		{name: "trace list limit below range", path: "/v1/memory/user/traces?limit=0"},
		{name: "trace list limit above range", path: "/v1/memory/user/traces?limit=101"},
		{name: "trace list duplicate limit", path: "/v1/memory/user/traces?limit=20&limit=21"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerContractRequest(http.MethodGet, tc.path))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400 rejected", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMemoryOwnerRecallTraceFailureSemanticsRemainTyped(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		request    *http.Request
		wantStatus int
		wantCode   string
		wantState  string
	}{
		{
			name:       "missing bearer",
			handler:    NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123")),
			request:    httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user/recall?query=blue", nil),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
			wantState:  "rejected",
		},
		{
			name:       "owner store unavailable",
			handler:    NewMemoryOwnerHandler(nil, "ren", []byte("owner-token-012345678901234567890123")),
			request:    memoryOwnerContractRequest(http.MethodGet, "/v1/memory/user/recall?query=blue"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "store_unavailable",
			wantState:  "blocked",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.RemoteAddr = "127.0.0.1:18790"
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, tc.request)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body["status"] != tc.wantState {
				t.Errorf("status field=%v, want %q", body["status"], tc.wantState)
			}
			errorBody, _ := body["error"].(map[string]any)
			if errorBody["code"] != tc.wantCode {
				t.Errorf("error code=%v, want %q", errorBody["code"], tc.wantCode)
			}
		})
	}
}

func memoryOwnerParquetRequest(method, path, body, profile string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", profile)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestMemoryOwnerParquetExportAcceptsOnlyEmptyPayloadAndBindsTrustedScope(t *testing.T) {
	for _, body := range []string{"", "{}", " \n { } \n"} {
		t.Run(strings.ReplaceAll(body, " ", "space"), func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerParquetRequest(http.MethodPost, "/viewer/memory/export/parquet", body, "cmd-control"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want typed export result", rec.Code, rec.Body.String())
			}
			if store.parquetExportCalls != 1 {
				t.Fatalf("export calls=%d, want exactly one", store.parquetExportCalls)
			}
			if store.parquetExportRequestID == "" || store.parquetExportRequestID != store.parquetExportScope.RequestID {
				t.Fatalf("request id=%q scope=%+v, want server-generated matching scope", store.parquetExportRequestID, store.parquetExportScope)
			}
			if store.parquetExportUserID != "ren" || store.parquetExportActorID != "ren" {
				t.Fatalf("owner=%q actor=%q, want configured ren", store.parquetExportUserID, store.parquetExportActorID)
			}
			if store.parquetExportScope.ActorKind != domaintool.ActorKindUser ||
				store.parquetExportScope.AuthenticatedUserID != "ren" ||
				!store.parquetExportScope.Allows(domaintool.DataScopeUser) ||
				store.parquetExportScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
				t.Fatalf("unexpected owner scope: %+v", store.parquetExportScope)
			}
			var result domainmemory.ConversationArchiveParquetExportResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode export result: %v", err)
			}
			if result.Receipt.Operation != domainmemory.UserMemoryOwnerOperationParquetExport || result.Receipt.Status != "completed" {
				t.Fatalf("receipt=%+v, want completed parquet export", result.Receipt)
			}
			if strings.Contains(result.RunRelPath, string(filepath.Separator)+"tmp") || strings.Contains(rec.Body.String(), "cold_export_dir") {
				t.Fatalf("response leaked an absolute/private output root: %s", rec.Body.String())
			}
		})
	}
}

func TestMemoryOwnerParquetVerifySeparatesCurrentRequestIDFromTarget(t *testing.T) {
	const targetID = "export-request-42"
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	rec := httptest.NewRecorder()
	path := "/viewer/memory/export/" + url.PathEscape(targetID)
	h(rec, memoryOwnerParquetRequest(http.MethodGet, path, "", "cmd-diagnostics"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want typed verify result", rec.Code, rec.Body.String())
	}
	if store.parquetVerifyCalls != 1 {
		t.Fatalf("verify calls=%d, want exactly one", store.parquetVerifyCalls)
	}
	if store.parquetVerifyTargetID != targetID {
		t.Fatalf("target id=%q, want %q", store.parquetVerifyTargetID, targetID)
	}
	if store.parquetVerifyRequestID == "" || store.parquetVerifyRequestID == targetID || store.parquetVerifyRequestID != store.parquetVerifyScope.RequestID {
		t.Fatalf("current request id=%q scope=%+v, want new id distinct from target", store.parquetVerifyRequestID, store.parquetVerifyScope)
	}
	if store.parquetVerifyUserID != "ren" || store.parquetVerifyActorID != "ren" {
		t.Fatalf("owner=%q actor=%q, want configured ren", store.parquetVerifyUserID, store.parquetVerifyActorID)
	}
	var result domainmemory.ConversationArchiveParquetVerifyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode verify result: %v", err)
	}
	if result.ExportID != targetID || result.Receipt.Operation != domainmemory.UserMemoryOwnerOperationParquetVerify || result.Receipt.AuditReference != targetID {
		t.Fatalf("verify result=%+v, want target-bound typed receipt", result)
	}
}

func TestMemoryOwnerParquetRoutesRejectCallerPayloadQueryAndInvalidPaths(t *testing.T) {
	postCases := []struct {
		name string
		path string
		body string
	}{
		{name: "caller path body", path: "/viewer/memory/export/parquet", body: `{"path":"/tmp/escape"}`},
		{name: "null body", path: "/viewer/memory/export/parquet", body: "null"},
		{name: "array body", path: "/viewer/memory/export/parquet", body: "[]"},
		{name: "trailing json", path: "/viewer/memory/export/parquet", body: "{} {}"},
		{name: "caller query", path: "/viewer/memory/export/parquet?request_id=caller", body: "{}"},
		{name: "caller path query", path: "/viewer/memory/export/parquet?path=%2Ftmp%2Fescape", body: "{}"},
		{name: "trailing slash", path: "/viewer/memory/export/parquet/", body: "{}"},
	}
	for _, tc := range postCases {
		t.Run("POST "+tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerParquetRequest(http.MethodPost, tc.path, tc.body, "cmd-control"))
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 400 or 404 rejection", rec.Code, rec.Body.String())
			}
			if store.parquetExportCalls != 0 {
				t.Fatalf("export calls=%d, want no store call", store.parquetExportCalls)
			}
		})
	}

	getCases := []struct {
		name string
		path string
	}{
		{name: "empty target", path: "/viewer/memory/export/"},
		{name: "trailing slash", path: "/viewer/memory/export/export-1/"},
		{name: "additional segment", path: "/viewer/memory/export/export-1/extra"},
		{name: "encoded slash", path: "/viewer/memory/export/export%2F1"},
		{name: "encoded backslash", path: "/viewer/memory/export/export%5C1"},
		{name: "caller query", path: "/viewer/memory/export/export-1?request_id=caller"},
	}
	for _, tc := range getCases {
		t.Run("GET "+tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerParquetRequest(http.MethodGet, tc.path, "", "cmd-diagnostics"))
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 400 or 404 rejection", rec.Code, rec.Body.String())
			}
			if store.parquetVerifyCalls != 0 {
				t.Fatalf("verify calls=%d, want no store call", store.parquetVerifyCalls)
			}
		})
	}
}

func TestMemoryOwnerParquetTargetIDParserRejectsUnsafeEscapedSegments(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		wantID string
		wantOK bool
	}{
		{name: "safe", path: "/viewer/memory/export/export-1", wantID: "export-1", wantOK: true},
		{name: "encoded slash", path: "/viewer/memory/export/export%2F1"},
		{name: "encoded backslash", path: "/viewer/memory/export/export%5C1"},
		{name: "malformed escape", path: "/viewer/memory/export/export%ZZ"},
		{name: "additional raw segment", path: "/viewer/memory/export/export-1/extra"},
		{name: "trailing slash", path: "/viewer/memory/export/export-1/"},
		{name: "empty", path: "/viewer/memory/export/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := memoryOwnerParquetTargetID(tt.path)
			if ok != tt.wantOK || got != tt.wantID {
				t.Fatalf("memoryOwnerParquetTargetID(%q)=(%q,%v), want (%q,%v)", tt.path, got, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestMemoryOwnerParquetStoreErrorsUseExistingMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid", err: domainmemory.ErrUserMemoryOwnerInvalid, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "not found", err: domainmemory.ErrUserMemoryOwnerNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "forbidden", err: domainmemory.ErrUserMemoryOwnerForbidden, wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "conflict", err: domainmemory.ErrUserMemoryOwnerConflict, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "unavailable", err: domainmemory.ErrUserMemoryOwnerUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "store_unavailable"},
		{name: "unknown", err: errors.New("storage failure"), wantStatus: http.StatusInternalServerError, wantCode: "storage_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{parquetExportErr: tc.err}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerParquetRequest(http.MethodPost, "/viewer/memory/export/parquet", "{}", "cmd-control"))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			errorBody, _ := body["error"].(map[string]any)
			if errorBody["code"] != tc.wantCode {
				t.Fatalf("error code=%v, want %q", errorBody["code"], tc.wantCode)
			}
		})
	}
}

// rewritableResponseWriter keeps the test compatible with handlers that set
// response headers before writing JSON.
type rewritableResponseWriter struct{ *httptest.ResponseRecorder }
