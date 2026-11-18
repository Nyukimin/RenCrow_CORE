package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type taskCenterHandlerServiceStub struct {
	capabilities taskcenter.Capabilities
	capContext   context.Context
	listResult   taskcenter.ListResult
	item         taskcenter.Item
	mutation     taskcenter.MutationResult
	receiptFound bool
	receiptErr   error
	getErr       error
	listQuery    taskcenter.ListQuery
	listContext  context.Context
	dailyQuery   taskcenter.DailyQuery
	dailyContext context.Context
	dailyResult  taskcenter.DailyListResult
	dailyCalls   int
	submitCalls  int
	submitInput  taskcenter.SubmitInput
	submitCtx    context.Context
	claimCalls   int
	getCalls     int
	acceptCalls  int
	acceptInput  taskcenter.AcceptResultInput
	acceptCtx    context.Context
	resultView   taskcenter.ResultView
	resultCtx    context.Context
	resultGoalID string
}

func (s *taskCenterHandlerServiceStub) Capabilities(ctx context.Context) (taskcenter.Capabilities, error) {
	s.capContext = ctx
	return s.capabilities, nil
}
func (s *taskCenterHandlerServiceStub) List(ctx context.Context, query taskcenter.ListQuery) (taskcenter.ListResult, error) {
	s.listQuery, s.listContext = query, ctx
	return s.listResult, nil
}
func (s *taskCenterHandlerServiceStub) ListDaily(ctx context.Context, query taskcenter.DailyQuery) (taskcenter.DailyListResult, error) {
	s.dailyCalls++
	s.dailyQuery, s.dailyContext = query, ctx
	return s.dailyResult, nil
}
func (s *taskCenterHandlerServiceStub) Get(_ context.Context, _ string) (taskcenter.Item, error) {
	s.getCalls++
	return s.item, s.getErr
}
func (s *taskCenterHandlerServiceStub) Submit(ctx context.Context, input taskcenter.SubmitInput) (taskcenter.MutationResult, error) {
	s.submitCalls++
	s.submitInput, s.submitCtx = input, ctx
	return s.mutation, nil
}
func (s *taskCenterHandlerServiceStub) Claim(context.Context, taskcenter.ClaimInput) (taskcenter.MutationResult, error) {
	s.claimCalls++
	return s.mutation, nil
}
func (s *taskCenterHandlerServiceStub) Receipt(_ context.Context, _, _ string) (taskcenter.MutationResult, bool, error) {
	return s.mutation, s.receiptFound, s.receiptErr
}
func (s *taskCenterHandlerServiceStub) AcceptResult(ctx context.Context, input taskcenter.AcceptResultInput) (taskcenter.MutationResult, error) {
	s.acceptCalls++
	s.acceptInput, s.acceptCtx = input, ctx
	return s.mutation, nil
}
func (s *taskCenterHandlerServiceStub) ReadResult(ctx context.Context, goalID string) (taskcenter.ResultView, error) {
	s.resultCtx, s.resultGoalID = ctx, goalID
	return s.resultView, nil
}

func TestTaskCenterHandlerAuthenticatesUserAndBuildsTrustedScope(t *testing.T) {
	service := &taskCenterHandlerServiceStub{capabilities: taskcenter.Capabilities{
		Kinds:  []taskcenter.KindCapability{{KindID: "movie.catalog.fetch", Label: "Movie fetch", AllowedAgents: []string{"midori"}}},
		Agents: []string{"midori"}, PolicyRevision: strings.Repeat("a", 64), OwnerRef: "workstream", ActorKind: "user",
	}}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/capabilities", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.capContext)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" ||
		scope.AuthenticatedUserID != "local-user" || !scope.Allows(tool.DataScopePublic) || !scope.Allows(tool.DataScopeUser) {
		t.Fatalf("untrusted or incomplete scope: %+v found=%v", scope, found)
	}
	if service.capabilities.ActorKind != "user" {
		t.Fatalf("capabilities actor_kind=%q", service.capabilities.ActorKind)
	}
	var payload taskcenter.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Kinds) != 1 || payload.ActorKind != "user" {
		t.Fatalf("capabilities=%+v", payload)
	}
}

func TestTaskCenterHandlerRequiresLoopbackBearerAndProfile(t *testing.T) {
	handler := NewTaskCenterHandler(&taskCenterHandlerServiceStub{}, "local-user", []byte("owner-token"), "")
	for _, test := range []struct {
		name    string
		remote  string
		token   string
		client  string
		profile string
		status  int
	}{
		{name: "remote", remote: "192.0.2.1:1234", token: "owner-token", client: "RenCrow_Viewer", profile: "task-center", status: http.StatusNotFound},
		{name: "missing bearer", remote: "127.0.0.1:1234", client: "RenCrow_Viewer", profile: "task-center", status: http.StatusUnauthorized},
		{name: "wrong profile", remote: "127.0.0.1:1234", token: "owner-token", client: "RenCrow_Viewer", profile: "core-viewer", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/task-center/capabilities", nil)
			request.RemoteAddr = test.remote
			request.Host = "127.0.0.1:8080"
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			request.Header.Set("X-RenCrow-Client", test.client)
			request.Header.Set("X-RenCrow-Interaction-Profile", test.profile)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
	missingConfig := NewTaskCenterHandler(&taskCenterHandlerServiceStub{}, "local-user", nil, "")
	response := serveTaskCenterRequest(missingConfig, http.MethodGet, "/api/v1/task-center/capabilities", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing user credential status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskCenterHandlerRejectsSpoofedActorBodyAndUserClaim(t *testing.T) {
	service := &taskCenterHandlerServiceStub{}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	body := `{"kind_id":"movie.catalog.fetch","title":"title","success_criteria":["done"],"verification":["check"],"idempotency_key":"key","expected_policy_revision":"revision","actor_id":"midori"}`
	response := serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/requests", body, "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusBadRequest || service.submitCalls != 0 {
		t.Fatalf("spoofed actor body status=%d submits=%d body=%s", response.Code, service.submitCalls, response.Body.String())
	}
	claimBody := `{"goal_id":"goal-1","expected_revision":1,"expected_policy_revision":"revision","idempotency_key":"claim-key"}`
	response = serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/claims", claimBody, "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusForbidden || service.claimCalls != 0 {
		t.Fatalf("user claim status=%d claimCalls=%d body=%s", response.Code, service.claimCalls, response.Body.String())
	}
}

func TestTaskCenterResultAcceptanceIsCreatorScopedViewerPOST(t *testing.T) {
	service := &taskCenterHandlerServiceStub{}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	body := `{"goal_id":"goal-1","expected_revision":7,"expected_assignment_epoch":2,"idempotency_key":"accept-key"}`
	response := serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/result/accept", body, "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusCreated || service.acceptCalls != 1 || service.acceptInput.GoalID != "goal-1" || service.acceptInput.ExpectedRevision != 7 || service.acceptInput.ExpectedAssignmentEpoch != 2 || service.acceptInput.IdempotencyKey != "accept-key" {
		t.Fatalf("accept response=%d %s calls=%d input=%+v", response.Code, response.Body.String(), service.acceptCalls, service.acceptInput)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.acceptCtx)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" || scope.AuthenticatedUserID != "local-user" {
		t.Fatalf("result acceptance did not receive authenticated creator scope: %+v found=%v", scope, found)
	}
	wrongProfile := serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/result/accept", body, "owner-token", "RenCrow_Viewer", "general")
	if wrongProfile.Code != http.StatusForbidden || service.acceptCalls != 1 {
		t.Fatalf("wrong profile acceptance response=%d calls=%d", wrongProfile.Code, service.acceptCalls)
	}
}

func TestTaskCenterResultReadUsesOnlyGoalIDAndTrustedUserScope(t *testing.T) {
	service := &taskCenterHandlerServiceStub{resultView: taskcenter.ResultView{
		GoalID: "goal-1", Revision: 9, AssignmentEpoch: 2, ResultArtifactID: "task_execution_result:tsk_example:run_example",
		ResultSHA256: strings.Repeat("a", 64), ExecutionVerification: "verification_pending", Result: json.RawMessage(`{"matches":[]}`),
	}}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/result?goal_id=goal-1", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK || service.resultGoalID != "goal-1" {
		t.Fatalf("result response=%d %s goal=%q", response.Code, response.Body.String(), service.resultGoalID)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.resultCtx)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" {
		t.Fatalf("result read did not receive trusted Viewer scope: %+v found=%v", scope, found)
	}
	if strings.Contains(response.Body.String(), "artifact_path") || strings.Contains(response.Body.String(), "file://") {
		t.Fatalf("result read exposed a path or URL: %s", response.Body.String())
	}
}

func TestTaskCenterHandlerRejectsMalformedJSONAndDuplicateQuery(t *testing.T) {
	service := &taskCenterHandlerServiceStub{}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	for _, body := range []string{"[]", "null", "{} {}", "{\"actor_id\":\"midori\"}"} {
		response := serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/requests", body, "owner-token", "RenCrow_Viewer", "task-center")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	if service.submitCalls != 0 {
		t.Fatalf("malformed request reached Submit %d times", service.submitCalls)
	}
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/items?limit=10&limit=20", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskCenterHandlerSubmitUsesTrustedUserScopeAndReturnsMutationEnvelope(t *testing.T) {
	service := &taskCenterHandlerServiceStub{mutation: taskcenter.MutationResult{Item: taskcenter.Item{GoalID: "goal-1"}}}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	body := `{"kind_id":"movie.catalog.lookup","title":"lookup request","success_criteria":["record result"],"verification":["check result"],"idempotency_key":"submit-key","expected_policy_revision":"` + strings.Repeat("a", 64) + `","timing":{"timezone":"","scheduled_start_at":null,"not_before":null,"allowed_windows":[],"deadline_at":null,"recurrence":null,"estimated_duration_seconds":null}}`
	response := serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/requests", body, "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusCreated || service.submitCalls != 1 || service.submitInput.KindID != "movie.catalog.lookup" {
		t.Fatalf("status=%d submits=%d input=%+v response=%s", response.Code, service.submitCalls, service.submitInput, response.Body.String())
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.submitCtx)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" || service.submitInput.Assignee != "" {
		t.Fatalf("Submit received scope=%+v found=%v input=%+v", scope, found, service.submitInput)
	}
	service.mutation.Replayed = true
	response = serveTaskCenterRequest(handler, http.MethodPost, "/api/v1/task-center/requests", body, "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK {
		t.Fatalf("replayed mutation status=%d response=%s", response.Code, response.Body.String())
	}
}

func TestTaskCenterClaimReceiptIsReadOnly(t *testing.T) {
	service := &taskCenterHandlerServiceStub{
		receiptFound: true,
		mutation:     taskcenter.MutationResult{Item: taskcenter.Item{GoalID: "goal-1"}, Replayed: true, ReceiptRef: "receipt-1"},
	}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/claim?goal_id=goal-1&idempotency_key=claim-key", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK || service.claimCalls != 0 {
		t.Fatalf("receipt read status=%d claim calls=%d body=%s", response.Code, service.claimCalls, response.Body.String())
	}
	response = serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/claim?goal_id=goal-2&idempotency_key=claim-key", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusNotFound || service.claimCalls != 0 {
		t.Fatalf("mismatched receipt status=%d claim calls=%d body=%s", response.Code, service.claimCalls, response.Body.String())
	}
}

func TestTaskCenterHandlerUsesScopeWideDefaultAndRejectsUnknownQuery(t *testing.T) {
	service := &taskCenterHandlerServiceStub{listResult: taskcenter.ListResult{Items: []taskcenter.Item{}, PolicyRevision: strings.Repeat("b", 64)}}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/items?view=remaining&limit=10", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.listQuery.Assignee != "" {
		t.Fatalf("default assignee = %q, want scope-wide empty filter", service.listQuery.Assignee)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.listContext)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" || scope.AuthenticatedUserID != "local-user" ||
		!scope.Allows(tool.DataScopePublic) || !scope.Allows(tool.DataScopeUser) {
		t.Fatalf("untrusted or incomplete scope: %+v found=%v", scope, found)
	}
	response = serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/items?view=remaining&unknown=true", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d body=%s", response.Code, response.Body.String())
	}
	response = serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/items?assignee=all", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("all literal status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskCenterHandlerDailyViewUsesTypedReaderAndTrustedScope(t *testing.T) {
	service := &taskCenterHandlerServiceStub{dailyResult: taskcenter.DailyListResult{
		Definitions: []taskcenter.DailyDefinition{{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "heartbeat.daily"}}},
		Partial:     true,
	}}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet,
		"/api/v1/task-center/items?view=daily&kind_id=heartbeat.daily&limit=20&cursor=page-2",
		"", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.dailyCalls != 1 || service.dailyQuery.KindID != "heartbeat.daily" || service.dailyQuery.Limit != 20 || service.dailyQuery.Cursor != "page-2" {
		t.Fatalf("daily calls=%d query=%+v", service.dailyCalls, service.dailyQuery)
	}
	if service.listContext != nil {
		t.Fatalf("Daily view was sent to managed List: query=%+v", service.listQuery)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.dailyContext)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.ActorID != "local-user" || scope.AuthenticatedUserID != "local-user" ||
		!scope.Allows(tool.DataScopePublic) || !scope.Allows(tool.DataScopeUser) {
		t.Fatalf("Daily reader received untrusted or incomplete scope: %+v found=%v", scope, found)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["definitions"]; !ok {
		t.Fatalf("Daily response omitted typed definitions envelope: %s", response.Body.String())
	}
	if _, ok := payload["items"]; ok {
		t.Fatalf("Daily response was projected as managed Goal items: %s", response.Body.String())
	}
	var partial bool
	if err := json.Unmarshal(payload["partial"], &partial); err != nil || !partial {
		t.Fatalf("Daily partial status was not preserved: value=%v err=%v body=%s", partial, err, response.Body.String())
	}
}

func TestTaskCenterHandlerDailyQueryIsStrictAndUsesDefaultLimit(t *testing.T) {
	service := &taskCenterHandlerServiceStub{}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/items?view=daily", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusOK || service.dailyCalls != 1 || service.dailyQuery.Limit != 50 {
		t.Fatalf("default daily query status=%d calls=%d query=%+v body=%s", response.Code, service.dailyCalls, service.dailyQuery, response.Body.String())
	}
	for _, target := range []string{
		"/api/v1/task-center/items?view=daily&limit=101",
		"/api/v1/task-center/items?view=daily&assignee=unassigned",
		"/api/v1/task-center/items?view=daily&unknown=true",
		"/api/v1/task-center/items?view=daily&kind_id=one&kind_id=two",
		"/api/v1/task-center/items?view=remaining&kind_id=heartbeat.daily",
	} {
		response = serveTaskCenterRequest(handler, http.MethodGet, target, "", "owner-token", "RenCrow_Viewer", "task-center")
		if response.Code != http.StatusBadRequest {
			t.Errorf("target=%q status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	if service.dailyCalls != 1 {
		t.Fatalf("invalid Daily queries reached reader: calls=%d", service.dailyCalls)
	}
}

func TestTaskCenterHandlerHidesCrossScopeNotFoundErrors(t *testing.T) {
	service := &taskCenterHandlerServiceStub{getErr: taskcenter.ErrNotFound}
	handler := NewTaskCenterHandler(service, "local-user", []byte("owner-token"), "")
	response := serveTaskCenterRequest(handler, http.MethodGet, "/api/v1/task-center/item?goal_id=goal-1", "", "owner-token", "RenCrow_Viewer", "task-center")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "not found") {
		t.Fatalf("handler returned a prose detail instead of a safe code: %s", response.Body.String())
	}
}

func serveTaskCenterRequest(handler http.Handler, method, target, body, token, client, profile string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:8080"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("X-RenCrow-Client", client)
	request.Header.Set("X-RenCrow-Interaction-Profile", profile)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
