package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskOverviewTestToken = "task-overview-owner-token"

type taskOverviewTestItem struct {
	ID                  string                            `json:"id"`
	Title               string                            `json:"title"`
	Assignee            string                            `json:"assignee"`
	SourceID            string                            `json:"source_id"`
	ResourceType        string                            `json:"resource_type"`
	RecordType          string                            `json:"record_type"`
	Reasons             []taskOverviewReason              `json:"reasons"`
	ReasonsIncomplete   bool                              `json:"reasons_incomplete"`
	ObservedState       string                            `json:"observed_state"`
	SourceObservedAt    *time.Time                        `json:"source_observed_at"`
	Children            *[]taskOverviewTestAggregateChild `json:"children"`
	ChildCount          *int                              `json:"child_count"`
	ChildrenTruncated   *bool                             `json:"children_truncated"`
	Status              string                            `json:"status"`
	RecurrenceState     string                            `json:"recurrence_state"`
	RecurrenceFrequency *string                           `json:"recurrence_frequency"`
	IntervalSeconds     *int64                            `json:"interval_seconds"`
	NextRunState        string                            `json:"next_run_state"`
	NextRunAt           *time.Time                        `json:"next_run_at"`
	AllowedAgents       *[]string                         `json:"allowed_agents"`
	TaskID              string                            `json:"task_id"`
}

type taskOverviewTestAggregateChild struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type taskOverviewTestResponse struct {
	View            string                 `json:"view"`
	ObservedAt      time.Time              `json:"observed_at"`
	Availability    string                 `json:"availability"`
	Partial         bool                   `json:"partial"`
	Issues          []taskOverviewIssue    `json:"issues"`
	IssuesTruncated bool                   `json:"issues_truncated"`
	Items           []taskOverviewTestItem `json:"items"`
	NextCursor      string                 `json:"next_cursor"`
}

func TestTaskOverviewDailyProjectsIssuesAndReasonStatesSafely(t *testing.T) {
	unknownCode := "run_receipt_unavailable"
	knownValue := "known-value-must-not-leak"
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{
		Definitions: []taskcenter.DailyDefinition{{
			DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{SourceID: "runtime.jobs", DefinitionID: "job.daily", Label: "Daily job", ResourceType: "background_job"},
			Configured:                taskcenter.DailyAxis{State: taskcenter.DailyConfiguredConfigured},
			Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveUnknown, ReasonCode: "schedule_unavailable"},
			Observed:                  taskcenter.DailyAxis{State: taskcenter.DailyObservedStale, ReasonCode: "snapshot_stale"},
			Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueNone, ReasonCode: "none_must_not_project"}},
			NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueUnknown, ReasonCode: unknownCode},
			LatestEvidence: taskcenter.DailyEvidence{
				LastAttemptAt: taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueUnknown, ReasonCode: "bad reason"},
				LastSuccessAt: taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueKnown, SourceRef: "private.source", ReasonCode: knownValue},
				LastResult:    taskcenter.DailyValue[taskcenter.DailyOutcome]{State: taskcenter.DailyValueUnknown, ReasonCode: "durable_run_receipt_not_projected"},
			},
		}},
		Issues: []taskcenter.DailyIssue{
			{SourceID: "runtime.jobs", OwnerRef: "application.jobs", Code: taskcenter.DailyIssueSourceReported},
			{SourceID: "runtime.jobs", OwnerRef: "private reason", Code: taskcenter.DailyIssueSourceReported},
			{SourceID: "runtime.jobs", Code: taskcenter.DailyIssueCode(strings.Repeat("x", 65))},
		},
		Partial: true, Availability: taskcenter.DailyAvailabilityPartial,
	}}
	handler := NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("daily response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if !got.Partial || got.Availability != "partial" || !got.IssuesTruncated || len(got.Issues) != 1 || got.Issues[0] != (taskOverviewIssue{SourceID: "runtime.jobs", OwnerRef: "application.jobs", Code: "source_reported_partial"}) {
		t.Fatalf("safe issue projection or partial preservation failed: %+v", got)
	}
	if len(got.Items) != 1 || !got.Items[0].ReasonsIncomplete || len(got.Items[0].Reasons) != 4 {
		t.Fatalf("reason projection=%+v", got.Items)
	}
	want := []taskOverviewReason{
		{Aspect: "effective", State: "unknown", Code: "schedule_unavailable"},
		{Aspect: "observed", State: "stale", Code: "snapshot_stale"},
		{Aspect: "next_run", State: "unknown", Code: unknownCode},
		{Aspect: "last_result", State: "unknown", Code: "durable_run_receipt_not_projected"},
	}
	for i := range want {
		if got.Items[0].Reasons[i] != want[i] {
			t.Fatalf("reason[%d]=%+v, want %+v", i, got.Items[0].Reasons[i], want[i])
		}
	}
	for _, forbidden := range []string{"private reason", "bad reason", knownValue, "private.source", "none_must_not_project"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("unsafe or non-uncertain Daily value leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestTaskOverviewDailyReasonInvalidCodeMarksIncomplete(t *testing.T) {
	definition := taskcenter.DailyDefinition{
		Configured: taskcenter.DailyAxis{State: taskcenter.DailyConfiguredUnknown, ReasonCode: "bad reason"},
		Effective:  taskcenter.DailyAxis{State: taskcenter.DailyEffectiveEnabled, ReasonCode: "not_unconfirmed"},
		Observed:   taskcenter.DailyAxis{State: taskcenter.DailyObservedNotObserved, ReasonCode: "run_receipt_unavailable"},
	}
	reasons, incomplete := projectTaskOverviewReasons(definition)
	if !incomplete || len(reasons) != 1 || reasons[0] != (taskOverviewReason{Aspect: "observed", State: "not_observed", Code: "run_receipt_unavailable"}) {
		t.Fatalf("invalid reason projection=%+v incomplete=%t", reasons, incomplete)
	}
}

func TestTaskOverviewProjectionLossPromotesAvailableResultToPartial(t *testing.T) {
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{
		Definitions: []taskcenter.DailyDefinition{{
			DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "job.daily", Label: "Daily job"},
			Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveUnknown, ReasonCode: "bad reason"},
		}},
		Issues:       []taskcenter.DailyIssue{{SourceID: "unsafe source", Code: taskcenter.DailyIssueSourceReported}},
		Availability: taskcenter.DailyAvailabilityAvailable,
	}}
	handler := NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("daily response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if !got.Partial || got.Availability != "partial" || !got.IssuesTruncated || len(got.Issues) != 0 || len(got.Items) != 1 || !got.Items[0].ReasonsIncomplete {
		t.Fatalf("projection loss was not surfaced as partial: %+v", got)
	}
}

func TestTaskOverviewIssueLimitMarksTruncation(t *testing.T) {
	issues := make([]taskcenter.DailyIssue, taskOverviewMaxIssues+1)
	for index := range issues {
		issues[index] = taskcenter.DailyIssue{SourceID: "runtime.jobs", Code: taskcenter.DailyIssueSourceReported}
	}
	got := projectTaskOverviewIssues(issues)
	if len(got) != taskOverviewMaxIssues || !taskOverviewIssuesTruncated(issues) {
		t.Fatalf("issue projection length=%d truncated=%t, want %d and true", len(got), taskOverviewIssuesTruncated(issues), taskOverviewMaxIssues)
	}
}

type taskOverviewDailyReaderStub struct {
	result taskcenter.DailyListResult
	err    error
	query  taskcenter.DailyQuery
	called int
	scope  tool.ToolExecutionScope
	valid  bool
}

func (s *taskOverviewDailyReaderStub) ListDaily(ctx context.Context, query taskcenter.DailyQuery) (taskcenter.DailyListResult, error) {
	s.called++
	s.query = query
	s.scope, s.valid = taskOverviewTrustedUserScope(ctx)
	if !s.valid {
		return taskcenter.DailyListResult{}, taskcenter.ErrForbidden
	}
	return s.result, s.err
}

type taskOverviewListReaderStub struct {
	result taskcenter.ListResult
	err    error
	query  taskcenter.ListQuery
	called int
	scope  tool.ToolExecutionScope
	valid  bool
}

func (s *taskOverviewListReaderStub) List(ctx context.Context, query taskcenter.ListQuery) (taskcenter.ListResult, error) {
	s.called++
	s.query = query
	s.scope, s.valid = taskOverviewTrustedUserScope(ctx)
	if !s.valid {
		return taskcenter.ListResult{}, taskcenter.ErrForbidden
	}
	return s.result, s.err
}

func taskOverviewTrustedUserScope(ctx context.Context) (tool.ToolExecutionScope, bool) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	proof, proofFound := tool.AuthenticatedHTTPUserScopeFromContext(ctx)
	valid := found && proofFound && scope.Validate() == nil && proof.Validate() == nil &&
		scope.ActorKind == tool.ActorKindUser && scope.ActorID == "ren" && scope.AuthenticatedUserID == "ren" &&
		scope.Allows(tool.DataScopePublic) && scope.Allows(tool.DataScopeUser) &&
		proof.ActorKind == tool.ActorKindUser && proof.AuthenticatedUserID == "ren"
	return scope, valid
}

func taskOverviewRequest(handler http.Handler, method, target string, bearer, profile bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:18790"+target, nil)
	request.RemoteAddr = "127.0.0.1:49152"
	if bearer {
		request.Header.Set("Authorization", "Bearer "+taskOverviewTestToken)
	}
	if profile {
		request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
		request.Header.Set("X-RenCrow-Interaction-Profile", "board-studio-readonly")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func taskOverviewDecodeResponse(t *testing.T, response *httptest.ResponseRecorder) taskOverviewTestResponse {
	t.Helper()
	var decoded taskOverviewTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
	return decoded
}

func TestTaskOverviewDailyProjectsOwnerStatesAndIncludesUnassignedDefinitions(t *testing.T) {
	observedAt := time.Date(2026, 11, 23, 1, 2, 3, 0, time.UTC)
	nextAt := observedAt.Add(time.Hour)
	sourceObservedAt := observedAt.Add(-time.Minute)
	childCount := 80
	secondScheduleID := string(modulecore.NewScheduleID())
	weekly := modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceWeekly, Times: []string{"09:30"}, Weekdays: []time.Weekday{time.Monday}}
	none := modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{
		Definitions: []taskcenter.DailyDefinition{
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{SourceID: "runtime.heartbeat", DefinitionID: "heartbeat.gmail_intake", Label: "Daily\nKnown", ResourceType: "heartbeat_child_routine", Assignee: "mio"},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveEnabled},
				Observed:                  taskcenter.DailyAxis{State: taskcenter.DailyObservedAvailable},
				SourceObservedAt:          taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueKnown, Value: &sourceObservedAt},
				Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueKnown, Value: &weekly}},
				NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueKnown, Value: &nextAt},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "daily-unknown", Label: "Unknown timing", Assignee: "shiro"},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveBlocked},
				Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueUnknown}},
				NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueUnknown},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "daily-none", Label: "No recurrence", Assignee: "ren"},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveDisabled},
				Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueKnown, Value: &none}},
				NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueNone},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "daily-owner-none", Label: "Owner reports none", Assignee: "aka"},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveDisabled},
				Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueNone}},
				NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueNone},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: "daily-unsupported", Label: "Unsupported timing", Assignee: "gin"},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveUnknown},
				Timing:                    taskcenter.DailyTimingProjection{Recurrence: taskcenter.DailyValue[modulecore.TaskRecurrence]{State: taskcenter.DailyValueUnsupported}},
				NextRunAt:                 taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueUnsupported},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{SourceID: "runtime.heartbeat", DefinitionID: "heartbeat.core", Label: "Heartbeat", ResourceType: "heartbeat_schedule", Assignee: ""},
				Effective:                 taskcenter.DailyAxis{State: taskcenter.DailyEffectiveEnabled},
				Observed:                  taskcenter.DailyAxis{State: taskcenter.DailyObservedAvailable},
			},
			{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{SourceID: "runtime.workstream-heartbeat", DefinitionID: "workstream.heartbeat_schedules", Label: "Workstream heartbeat schedules", OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule", DataScope: tool.DataScopePublic},
				Observed:                  taskcenter.DailyAxis{State: taskcenter.DailyObservedAvailable},
				SourceObservedAt:          taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueKnown, Value: &sourceObservedAt},
				NativeRefs: []taskcenter.DailyResourceRef{
					{SourceID: "runtime.workstream-heartbeat", OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule", Reference: "workstream.heartbeat_schedule", NativeID: "sch_c0d4aec2-01bf-5cd4-9408-2ee31b178c84", DataScope: tool.DataScopePublic, DisplayName: "Schedule A"},
					{SourceID: "runtime.workstream-heartbeat", OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule", Reference: "workstream.heartbeat_schedule", NativeID: secondScheduleID, DataScope: tool.DataScopePublic, DisplayName: "Schedule B"},
					{SourceID: "runtime.workstream-heartbeat", OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule", Reference: "private-reference-marker", NativeID: "sch_8e71468a-36c7-45bb-bfc3-9d50b5484f2b", DataScope: tool.DataScopePublic, DisplayName: "Must not leak"},
					{SourceID: "runtime.workstream-heartbeat", OwnerRef: "other.owner", ResourceType: "workstream_heartbeat_schedule", Reference: "workstream.heartbeat_schedule", NativeID: "sch_55c7eab6-1ab1-4d31-89b2-973fe77543bc", DataScope: tool.DataScopePublic, DisplayName: "Must not leak"},
					{SourceID: "runtime.workstream-heartbeat", OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule", Reference: "workstream.heartbeat_schedule", NativeID: "sch_99f8a312-c1d8-4953-98a5-f297b4bd058b", DataScope: tool.DataScopeUser, UserID: "private-user", DisplayName: "Private schedule"},
				},
				NativeRefCount:      taskcenter.DailyValue[int]{State: taskcenter.DailyValueKnown, Value: &childCount},
				NativeRefsTruncated: true,
			},
		},
		NextCursor: "daily-owner-cursor", Partial: true, Availability: taskcenter.DailyAvailabilityPartial, ObservedAt: observedAt,
	}}
	handler := NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily&limit=25&cursor=opaque-daily", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("daily response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if got.View != "daily" || !got.ObservedAt.Equal(observedAt) || got.Availability != "partial" || !got.Partial || got.NextCursor != "daily-owner-cursor" {
		t.Fatalf("daily metadata=%+v", got)
	}
	if daily.called != 1 || daily.query.Limit != 25 || daily.query.Cursor != "opaque-daily" || !daily.valid || daily.scope.AuthenticatedUserID != "ren" {
		t.Fatalf("daily owner call query=%+v scope=%+v valid=%v calls=%d", daily.query, daily.scope, daily.valid, daily.called)
	}
	if len(got.Items) != 7 {
		t.Fatalf("daily items=%+v; owner definitions must remain visible", got.Items)
	}
	if got.Items[0].ID != "heartbeat.gmail_intake" || got.Items[0].Title != "Daily Known" || got.Items[0].Assignee != "mio" || got.Items[0].Status != "enabled" || got.Items[0].SourceID != "runtime.heartbeat" || got.Items[0].ResourceType != "heartbeat_child_routine" || got.Items[0].RecordType != "job" || got.Items[0].ObservedState != "available" || got.Items[0].SourceObservedAt == nil || !got.Items[0].SourceObservedAt.Equal(sourceObservedAt) || got.Items[0].RecurrenceState != "known" || got.Items[0].RecurrenceFrequency == nil || *got.Items[0].RecurrenceFrequency != "weekly" || got.Items[0].NextRunState != "known" || got.Items[0].NextRunAt == nil || !got.Items[0].NextRunAt.Equal(nextAt) {
		t.Fatalf("known daily item=%+v", got.Items[0])
	}
	if got.Items[1].ID != "daily-unknown" || got.Items[1].RecurrenceState != "unknown" || got.Items[1].RecurrenceFrequency != nil || got.Items[1].NextRunState != "unknown" || got.Items[1].NextRunAt != nil {
		t.Fatalf("unknown daily item=%+v", got.Items[1])
	}
	if got.Items[2].ID != "daily-none" || got.Items[2].RecurrenceState != "none" || got.Items[2].RecurrenceFrequency == nil || *got.Items[2].RecurrenceFrequency != "none" || got.Items[2].NextRunState != "none" || got.Items[2].NextRunAt != nil {
		t.Fatalf("explicit-none daily item=%+v", got.Items[2])
	}
	if got.Items[3].ID != "daily-owner-none" || got.Items[3].RecurrenceState != "none" || got.Items[3].RecurrenceFrequency != nil || got.Items[3].NextRunState != "none" {
		t.Fatalf("owner-none daily item=%+v", got.Items[3])
	}
	if got.Items[4].ID != "daily-unsupported" || got.Items[4].RecurrenceState != "unsupported" || got.Items[4].RecurrenceFrequency != nil || got.Items[4].NextRunState != "unsupported" {
		t.Fatalf("unsupported daily item=%+v", got.Items[4])
	}
	if got.Items[5].ID != "heartbeat.core" || got.Items[5].Assignee != "" || got.Items[5].RecordType != "execution_mechanism" || got.Items[5].ObservedState != "available" {
		t.Fatalf("unassigned heartbeat mechanism=%+v", got.Items[5])
	}
	aggregate := got.Items[6]
	if aggregate.ID != "workstream.heartbeat_schedules" || aggregate.RecordType != "aggregate" || aggregate.Children == nil || len(*aggregate.Children) != 2 || aggregate.ChildCount == nil || *aggregate.ChildCount != 80 || aggregate.ChildrenTruncated == nil || !*aggregate.ChildrenTruncated {
		t.Fatalf("workstream aggregate=%+v", aggregate)
	}
	if (*aggregate.Children)[0].ID != "sch_c0d4aec2-01bf-5cd4-9408-2ee31b178c84" || (*aggregate.Children)[0].DisplayName != "Schedule A" || (*aggregate.Children)[1].ID != secondScheduleID {
		t.Fatalf("workstream child summaries=%+v", *aggregate.Children)
	}
	for _, forbidden := range []string{"private-reference-marker", "other.owner", "sch_55c7eab6", "private-user", "Private schedule", "native_id", "reference", "data_scope"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("daily response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestTaskOverviewDailyPreservesAllThirtyTwoDefinitionsAndFutureDefinitions(t *testing.T) {
	definitions := make([]taskcenter.DailyDefinition, 0, 33)
	for index := 0; index < 32; index++ {
		definitions = append(definitions, taskcenter.DailyDefinition{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{
			SourceID: "runtime.daily-test", DefinitionID: fmt.Sprintf("daily.defined_%02d", index), ResourceType: "future_daily_resource",
		}})
	}
	definitions = append(definitions, taskcenter.DailyDefinition{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{
		SourceID: "runtime.future", DefinitionID: "daily.future_definition", ResourceType: "future_daily_resource",
	}})
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{
		Definitions: definitions, Availability: taskcenter.DailyAvailabilityAvailable,
	}}
	handler := NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("daily response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if len(got.Items) != 33 {
		t.Fatalf("daily rows=%d; want 32 current and one future definition", len(got.Items))
	}
	for index := 0; index < 32; index++ {
		if got.Items[index].ID != fmt.Sprintf("daily.defined_%02d", index) || got.Items[index].Assignee != "" || got.Items[index].RecordType != "unknown" {
			t.Fatalf("daily definition %d was filtered or misclassified: %+v", index, got.Items[index])
		}
	}
	if got.Items[32].ID != "daily.future_definition" || got.Items[32].RecordType != "unknown" {
		t.Fatalf("future daily definition was filtered or misclassified: %+v", got.Items[32])
	}
}

func TestTaskOverviewDailyRecordTypeUsesExactDefinitionIdentityAndResourceType(t *testing.T) {
	cases := []struct {
		definitionID string
		resourceType string
		want         string
	}{
		{definitionID: "heartbeat.core", resourceType: "heartbeat_schedule", want: taskOverviewRecordTypeExecutionMechanism},
		{definitionID: "workstream.heartbeat_schedules", resourceType: "workstream_heartbeat_schedule", want: taskOverviewRecordTypeAggregate},
		{definitionID: "heartbeat.gmail_intake", resourceType: "heartbeat_child_routine", want: taskOverviewRecordTypeJob},
		{definitionID: "heartbeat.x_bookmarks", resourceType: "heartbeat_child_routine", want: taskOverviewRecordTypeJob},
		{definitionID: "knowledge_memory.daily_intake", resourceType: "daily_intake_rule_set", want: taskOverviewRecordTypeJob},
		{definitionID: "memory.lifecycle_maintenance", resourceType: "maintenance_runner", want: taskOverviewRecordTypeJob},
		{definitionID: "meeting.daily_report", resourceType: "meeting_definition", want: taskOverviewRecordTypeJob},
		{definitionID: "movie_catalog.backfill", resourceType: "background_job", want: taskOverviewRecordTypeJob},
		{definitionID: "person_related.collection", resourceType: "person_catalog_collection_worker", want: taskOverviewRecordTypeJob},
		{definitionID: "source_registry.feed_collection.aka", resourceType: "background_job", want: taskOverviewRecordTypeJob},
		{definitionID: "source_registry.feed_collection.kin", resourceType: "background_job", want: taskOverviewRecordTypeJob},
		{definitionID: "idlechat.daily_seed_collection", resourceType: "daily_seed_snapshot", want: taskOverviewRecordTypeJob},
		{definitionID: "newsdb.daily_items", resourceType: "news_item_set", want: taskOverviewRecordTypeInformation},
		{definitionID: "newsdb.daily_items", resourceType: "background_job", want: taskOverviewRecordTypeUnknown},
		{definitionID: "heartbeat.gmail_intake", resourceType: "background_job", want: taskOverviewRecordTypeUnknown},
		{definitionID: "person_related.summary", resourceType: "person_catalog_summary_worker", want: taskOverviewRecordTypeJob},
		{definitionID: "person_related.identity", resourceType: "person_identity_worker", want: taskOverviewRecordTypeJob},
		{definitionID: "memory.profile_promotion", resourceType: "background_worker", want: taskOverviewRecordTypeJob},
		{definitionID: "backup.external_runner", resourceType: "external_backup_configuration", want: taskOverviewRecordTypeInformation},
		{definitionID: "heartbeat.workspace_file", resourceType: "workspace_instruction_source", want: taskOverviewRecordTypeInformation},
		{definitionID: "ops.idlechat_watchdog", resourceType: "background_job", want: taskOverviewRecordTypeUnknown},
		{definitionID: "future.daily_job", resourceType: "background_job", want: taskOverviewRecordTypeUnknown},
	}
	for _, test := range cases {
		t.Run(test.definitionID+"/"+test.resourceType, func(t *testing.T) {
			definition := taskcenter.DailyDefinition{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{
				DefinitionID: test.definitionID, ResourceType: test.resourceType,
			}}
			if got := taskOverviewDailyRecordType(definition); got != test.want {
				t.Fatalf("record type=%q, want %q", got, test.want)
			}
		})
	}
}

func TestTaskOverviewClassifiesCurrentDailyOwnersByExactIdentity(t *testing.T) {
	cases := []struct{ id, resource, want string }{
		{"backup.external_runner", "external_backup_configuration", taskOverviewRecordTypeInformation},
		{"heartbeat.atlas_maturation", "heartbeat_child_routine", taskOverviewRecordTypeJob},
		{"heartbeat.backlog_intake", "heartbeat_child_routine", taskOverviewRecordTypeJob},
		{"heartbeat.core", "heartbeat_schedule", taskOverviewRecordTypeExecutionMechanism},
		{"heartbeat.economic_discovery", "heartbeat_child_routine", taskOverviewRecordTypeJob},
		{"heartbeat.gmail_intake", "heartbeat_child_routine", taskOverviewRecordTypeJob},
		{"heartbeat.workspace_file", "workspace_instruction_source", taskOverviewRecordTypeInformation},
		{"heartbeat.x_bookmarks", "heartbeat_child_routine", taskOverviewRecordTypeJob},
		{"idlechat.daily_seed_collection", "daily_seed_snapshot", taskOverviewRecordTypeJob},
		{"idlechat.night_story_stock", "story_episode_stock", taskOverviewRecordTypeJob},
		{"idlechat.topic_stock", "topic_stock_snapshot", taskOverviewRecordTypeJob},
		{"idlechat.watchdog", "watchdog_snapshot", taskOverviewRecordTypeInformation},
		{"knowledge_memory.daily_intake", "daily_intake_rule_set", taskOverviewRecordTypeJob},
		{"logging.size_rotation", "size_triggered_log_rotation", taskOverviewRecordTypeInformation},
		{"maintenance.action_store_rotation", "write_triggered_utc_segment_rotation", taskOverviewRecordTypeInformation},
		{"maintenance.task_store_rotation", "write_triggered_utc_segment_rotation", taskOverviewRecordTypeInformation},
		{"meeting.daily_report", "meeting_definition", taskOverviewRecordTypeJob},
		{"memory.archive_parquet_export", "unwired_export_helper", taskOverviewRecordTypeInformation},
		{"memory.lifecycle_maintenance", "maintenance_runner", taskOverviewRecordTypeJob},
		{"memory.profile_promotion", "background_worker", taskOverviewRecordTypeJob},
		{"movie_catalog.backfill", "background_job", taskOverviewRecordTypeJob},
		{"newsbrief.daily_morning", "daily_news_brief_read_projection", taskOverviewRecordTypeInformation},
		{"newsdb.daily_items", "news_item_set", taskOverviewRecordTypeInformation},
		{"ops.resilience_status", "manual_incident_status_cli", taskOverviewRecordTypeInformation},
		{"person_related.collection", "person_catalog_collection_worker", taskOverviewRecordTypeJob},
		{"person_related.identity", "person_identity_worker", taskOverviewRecordTypeJob},
		{"person_related.summary", "person_catalog_summary_worker", taskOverviewRecordTypeJob},
		{"revenue.daily_report", "revenue_daily_report_history", taskOverviewRecordTypeInformation},
		{"source_registry.feed_collection.aka", "background_job", taskOverviewRecordTypeJob},
		{"source_registry.feed_collection.kin", "background_job", taskOverviewRecordTypeJob},
		{"tts.pronunciation_check", "scheduler_schedule", taskOverviewRecordTypeJob},
		{"workstream.heartbeat_schedules", "workstream_heartbeat_schedule", taskOverviewRecordTypeAggregate},
	}
	for _, test := range cases {
		t.Run(test.id, func(t *testing.T) {
			definition := taskcenter.DailyDefinition{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{DefinitionID: test.id, ResourceType: test.resource}}
			if got := taskOverviewDailyRecordType(definition); got != test.want {
				t.Fatalf("record type for %s/%s = %q, want %q", test.id, test.resource, got, test.want)
			}
		})
	}
}

func TestTaskOverviewAggregateMarksFilteredOrUncountedChildrenTruncated(t *testing.T) {
	validRef := taskcenter.DailyResourceRef{
		SourceID: "runtime.workstream-heartbeat", OwnerRef: "workstream.heartbeat_schedules",
		ResourceType: "workstream_heartbeat_schedule", Reference: "workstream.heartbeat_schedule",
		NativeID: "sch_c0d4aec2-01bf-5cd4-9408-2ee31b178c84", DataScope: tool.DataScopePublic,
	}
	filteredRef := validRef
	filteredRef.Reference = "unrecognized.reference"
	childCount := 2
	cases := []struct {
		name      string
		refs      []taskcenter.DailyResourceRef
		count     taskcenter.DailyValue[int]
		wantCount *int
	}{
		{
			name:  "filtered reference with unknown owner count",
			refs:  []taskcenter.DailyResourceRef{validRef, filteredRef},
			count: taskcenter.DailyValue[int]{State: taskcenter.DailyValueUnknown},
		},
		{
			name:      "known total exceeds visible children",
			refs:      []taskcenter.DailyResourceRef{validRef},
			count:     taskcenter.DailyValue[int]{State: taskcenter.DailyValueKnown, Value: &childCount},
			wantCount: &childCount,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := taskcenter.DailyDefinition{
				DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{
					SourceID: "runtime.workstream-heartbeat", DefinitionID: "workstream.heartbeat_schedules",
					OwnerRef: "workstream.heartbeat_schedules", ResourceType: "workstream_heartbeat_schedule",
				},
				Observed:            taskcenter.DailyAxis{State: taskcenter.DailyObservedAvailable},
				NativeRefs:          test.refs,
				NativeRefCount:      test.count,
				NativeRefsTruncated: false,
			}
			got := projectDailyTaskOverviewItem(definition)
			if got.Children == nil || len(*got.Children) != 1 || got.ChildrenTruncated == nil || !*got.ChildrenTruncated {
				t.Fatalf("incomplete aggregate children were not marked truncated: %+v", got)
			}
			if test.wantCount == nil {
				if got.ChildCount != nil {
					t.Fatalf("unknown owner count was inferred: got %d", *got.ChildCount)
				}
			} else if got.ChildCount == nil || *got.ChildCount != *test.wantCount {
				t.Fatalf("owner child count=%v, want %d", got.ChildCount, *test.wantCount)
			}
		})
	}
}

func TestTaskOverviewInstantPreservesOwnerPolicyRecurrenceAndPagination(t *testing.T) {
	startAt := time.Date(2026, 11, 24, 3, 4, 5, 0, time.UTC)
	dailyRecurrence := &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00"}}
	intervalSeconds := int64(900)
	intervalRecurrence := &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceInterval, IntervalSeconds: &intervalSeconds}
	noneRecurrence := &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}
	list := &taskOverviewListReaderStub{result: taskcenter.ListResult{
		Items: []taskcenter.Item{
			{GoalID: "goal-recurring", Title: "Repeat\tjob", Assignee: "mio", Status: "waiting", TimingStatus: "known", TimingSource: "workstream.goal.task_plan", Timing: modulecore.TaskTiming{Recurrence: dailyRecurrence}, TaskID: "task-canonical-1", AllowedAgents: []string{"mio", "shiro"}},
			{GoalID: "goal-single", Title: "One run", Assignee: "ren", Status: "waiting", TimingStatus: "known", TimingSource: "workstream.goal.task_plan", Timing: modulecore.TaskTiming{Recurrence: noneRecurrence, ScheduledStartAt: &startAt}, AllowedAgents: []string{}},
			{GoalID: "goal-unknown", Title: "Timing not known", Assignee: "ren", Status: "waiting", TimingStatus: "unknown", TimingSource: "legacy", Timing: modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: ""}}},
			{GoalID: "goal-unspecified", Title: "No recurrence field", Assignee: "shiro", Status: "waiting", TimingStatus: "known", TimingSource: "workstream.goal.task_plan", Timing: modulecore.TaskTiming{}, AllowedAgents: []string{"shiro"}, TaskID: "task-canonical-2"},
			{GoalID: "goal-interval", Title: "Interval recurrence", Assignee: "mio", Status: "waiting", TimingStatus: "known", TimingSource: "workstream.goal.task_plan", Timing: modulecore.TaskTiming{Recurrence: intervalRecurrence}},
		},
		NextCursor: "managed-owner-cursor", Partial: true,
	}}
	handler := NewTaskOverviewHandler(nil, list, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=instant&limit=100&cursor=opaque-instant", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("instant response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if got.View != "instant" || got.ObservedAt.IsZero() || got.Availability != "partial" || !got.Partial || got.NextCursor != "managed-owner-cursor" {
		t.Fatalf("instant metadata=%+v", got)
	}
	if strings.Contains(response.Body.String(), `"record_type"`) || strings.Contains(response.Body.String(), `"source_id"`) || strings.Contains(response.Body.String(), `"children"`) {
		t.Fatalf("daily-only metadata changed the instant item contract: %s", response.Body.String())
	}
	if list.called != 1 || list.query.View != taskcenter.ViewRemaining || list.query.Limit != 100 || list.query.Cursor != "opaque-instant" || !list.valid || list.scope.AuthenticatedUserID != "ren" {
		t.Fatalf("managed owner call query=%+v scope=%+v valid=%v calls=%d", list.query, list.scope, list.valid, list.called)
	}
	if len(got.Items) != 5 {
		t.Fatalf("instant items=%+v", got.Items)
	}
	if got.Items[0].ID != "goal-recurring" || got.Items[0].Title != "Repeat job" || got.Items[0].RecurrenceState != "known" || got.Items[0].RecurrenceFrequency == nil || *got.Items[0].RecurrenceFrequency != "daily" || got.Items[0].NextRunState != "unsupported" || got.Items[0].TaskID != "task-canonical-1" || got.Items[0].AllowedAgents == nil || len(*got.Items[0].AllowedAgents) != 2 {
		t.Fatalf("recurring managed item=%+v", got.Items[0])
	}
	if got.Items[1].ID != "goal-single" || got.Items[1].RecurrenceState != "none" || got.Items[1].RecurrenceFrequency == nil || *got.Items[1].RecurrenceFrequency != "none" || got.Items[1].NextRunState != "known" || got.Items[1].NextRunAt == nil || !got.Items[1].NextRunAt.Equal(startAt) || got.Items[1].AllowedAgents == nil || len(*got.Items[1].AllowedAgents) != 0 {
		t.Fatalf("explicit single managed item=%+v", got.Items[1])
	}
	if got.Items[2].ID != "goal-unknown" || got.Items[2].RecurrenceState != "unknown" || got.Items[2].RecurrenceFrequency != nil || got.Items[2].NextRunState != "unknown" || got.Items[2].NextRunAt != nil {
		t.Fatalf("unknown managed item=%+v", got.Items[2])
	}
	if got.Items[3].ID != "goal-unspecified" || got.Items[3].RecurrenceState != "none" || got.Items[3].RecurrenceFrequency != nil || got.Items[3].TaskID != "task-canonical-2" {
		t.Fatalf("current managed default item=%+v", got.Items[3])
	}
	if got.Items[4].ID != "goal-interval" || got.Items[4].RecurrenceState != "known" || got.Items[4].RecurrenceFrequency == nil || *got.Items[4].RecurrenceFrequency != "interval" || got.Items[4].IntervalSeconds == nil || *got.Items[4].IntervalSeconds != intervalSeconds {
		t.Fatalf("interval managed item=%+v", got.Items[4])
	}
}

func TestTaskOverviewOwnersRemainIndependentAndErrorsAreUnavailable(t *testing.T) {
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{Availability: taskcenter.DailyAvailabilityAvailable, Definitions: []taskcenter.DailyDefinition{}}}
	list := &taskOverviewListReaderStub{result: taskcenter.ListResult{Items: []taskcenter.Item{}}}
	handler := NewTaskOverviewHandler(daily, list, "ren", []byte(taskOverviewTestToken))
	if response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily", true, true); response.Code != http.StatusOK {
		t.Fatalf("daily should work independently: %d %s", response.Code, response.Body.String())
	}
	if response := taskOverviewRequest(NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken)), http.MethodGet, "/viewer/board/task-overview?view=instant", true, true); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "task_overview_unavailable") {
		t.Fatalf("missing managed owner response=%d body=%s", response.Code, response.Body.String())
	}
	if response := taskOverviewRequest(NewTaskOverviewHandler(nil, list, "ren", []byte(taskOverviewTestToken)), http.MethodGet, "/viewer/board/task-overview?view=instant", true, true); response.Code != http.StatusOK {
		t.Fatalf("instant should work independently: %d %s", response.Code, response.Body.String())
	}
	if response := taskOverviewRequest(NewTaskOverviewHandler(nil, list, "ren", []byte(taskOverviewTestToken)), http.MethodGet, "/viewer/board/task-overview?view=daily", true, true); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "task_overview_unavailable") {
		t.Fatalf("missing daily owner response=%d body=%s", response.Code, response.Body.String())
	}
	daily.err = errors.New("daily source read failed")
	if response := taskOverviewRequest(NewTaskOverviewHandler(daily, list, "ren", []byte(taskOverviewTestToken)), http.MethodGet, "/viewer/board/task-overview?view=daily", true, true); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("daily owner error response=%d body=%s", response.Code, response.Body.String())
	}
	list.err = errors.New("managed source read failed")
	if response := taskOverviewRequest(NewTaskOverviewHandler(daily, list, "ren", []byte(taskOverviewTestToken)), http.MethodGet, "/viewer/board/task-overview?view=instant", true, true); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("managed owner error response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskOverviewRejectsUntrustedRequestsBeforeReadingOwners(t *testing.T) {
	daily := &taskOverviewDailyReaderStub{}
	list := &taskOverviewListReaderStub{}
	handler := NewTaskOverviewHandler(daily, list, "ren", []byte(taskOverviewTestToken))
	cases := []struct {
		name       string
		method     string
		path       string
		remoteAddr string
		bearer     bool
		profile    bool
		want       int
	}{
		{name: "missing bearer", method: http.MethodGet, path: "/viewer/board/task-overview?view=daily", bearer: false, profile: true, want: http.StatusUnauthorized},
		{name: "missing board profile", method: http.MethodGet, path: "/viewer/board/task-overview?view=daily", bearer: true, profile: false, want: http.StatusForbidden},
		{name: "wrong method", method: http.MethodPost, path: "/viewer/board/task-overview?view=daily", bearer: true, profile: true, want: http.StatusMethodNotAllowed},
		{name: "non loopback", method: http.MethodGet, path: "/viewer/board/task-overview?view=daily", remoteAddr: "192.0.2.10:49152", bearer: true, profile: true, want: http.StatusNotFound},
		{name: "wrong path", method: http.MethodGet, path: "/viewer/board/task-overview-extra?view=daily", bearer: true, profile: true, want: http.StatusNotFound},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://127.0.0.1:18790"+test.path, nil)
			request.RemoteAddr = test.remoteAddr
			if request.RemoteAddr == "" {
				request.RemoteAddr = "127.0.0.1:49152"
			}
			if test.bearer {
				request.Header.Set("Authorization", "Bearer "+taskOverviewTestToken)
			}
			if test.profile {
				request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
				request.Header.Set("X-RenCrow-Interaction-Profile", "board-studio-readonly")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("response=%d body=%s; want %d", recorder.Code, recorder.Body.String(), test.want)
			}
		})
	}
	if daily.called != 0 || list.called != 0 {
		t.Fatalf("untrusted requests reached owners: daily=%d instant=%d", daily.called, list.called)
	}
}

func TestTaskOverviewKnownTimingWithoutRecurrenceIsSingleOnlyForCurrentManagedOwner(t *testing.T) {
	list := &taskOverviewListReaderStub{result: taskcenter.ListResult{Items: []taskcenter.Item{
		{GoalID: "current-unspecified", TimingStatus: "known", TimingSource: "workstream.goal.task_plan"},
		{GoalID: "unknown-source", TimingStatus: "known", TimingSource: "legacy"},
		{GoalID: "unknown-state", TimingStatus: "unknown", TimingSource: "workstream.goal.task_plan"},
	}}}
	handler := NewTaskOverviewHandler(nil, list, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=instant", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("instant response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if got.Items[0].RecurrenceState != "none" || got.Items[1].RecurrenceState != "unknown" || got.Items[2].RecurrenceState != "unknown" {
		t.Fatalf("recurrence states=%+v", got.Items)
	}
}

func TestTaskOverviewDoesNotInventFrequencyForUnknownOrUnsupportedTiming(t *testing.T) {
	gotUnknown := projectManagedTaskOverviewItem(taskcenter.Item{
		GoalID: "unknown-state", TimingStatus: "unexpected", TimingSource: "workstream.goal.task_plan",
		Timing: modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily}},
	})
	if gotUnknown.RecurrenceState != "unknown" || gotUnknown.RecurrenceFrequency != nil || gotUnknown.NextRunState != "unknown" {
		t.Fatalf("invalid timing status was exposed: %+v", gotUnknown)
	}

	gotUnsupported := projectManagedTaskOverviewItem(taskcenter.Item{
		GoalID: "unsupported-frequency", TimingStatus: "known", TimingSource: "workstream.goal.task_plan",
		Timing: modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: "yearly"}},
	})
	if gotUnsupported.RecurrenceState != "unsupported" || gotUnsupported.RecurrenceFrequency != nil || gotUnsupported.NextRunState != "unsupported" {
		t.Fatalf("unsupported recurrence frequency was exposed: %+v", gotUnsupported)
	}

	gotUnconfirmed := projectManagedTaskOverviewItem(taskcenter.Item{
		GoalID: "unconfirmed-timing", TimingStatus: "unconfirmed", TimingSource: "workstream.goal.task_plan",
		Timing: modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily}},
	})
	if gotUnconfirmed.RecurrenceState != "unknown" || gotUnconfirmed.RecurrenceFrequency != nil || gotUnconfirmed.NextRunState != "unknown" {
		t.Fatalf("unconfirmed timing was not normalized to unknown: %+v", gotUnconfirmed)
	}

	gotUnavailable := projectManagedTaskOverviewItem(taskcenter.Item{
		GoalID: "unavailable-timing", TimingStatus: "unavailable", TimingSource: "workstream.goal.task_plan",
		Timing: modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily}},
	})
	if gotUnavailable.RecurrenceState != "unavailable" || gotUnavailable.RecurrenceFrequency != nil || gotUnavailable.NextRunState != "unavailable" {
		t.Fatalf("unavailable timing was collapsed into another state: %+v", gotUnavailable)
	}
}

func TestTaskOverviewRetainsCursorAndUnassignedRows(t *testing.T) {
	daily := &taskOverviewDailyReaderStub{result: taskcenter.DailyListResult{
		Definitions: []taskcenter.DailyDefinition{{DailyDefinitionDescriptor: taskcenter.DailyDefinitionDescriptor{
			DefinitionID: "unassigned-system-job", Label: "System job", Assignee: "",
		}}},
		NextCursor: "owner-next-page", Partial: true, Availability: taskcenter.DailyAvailabilityPartial,
	}}
	handler := NewTaskOverviewHandler(daily, nil, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=daily", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("daily response=%d body=%s", response.Code, response.Body.String())
	}
	got := taskOverviewDecodeResponse(t, response)
	if len(got.Items) != 1 || got.Items[0].ID != "unassigned-system-job" || got.Items[0].Assignee != "" || got.NextCursor != "owner-next-page" || !got.Partial || got.Availability != "partial" {
		t.Fatalf("daily page lost unassigned definition or owner pagination state: %+v", got)
	}
	if daily.query.Limit != 50 || daily.query.Cursor != "" {
		t.Fatalf("default daily query=%+v; want default limit 50 and no cursor", daily.query)
	}
}

func TestTaskOverviewDoesNotExposeTaskCenterInternalFields(t *testing.T) {
	list := &taskOverviewListReaderStub{result: taskcenter.ListResult{Items: []taskcenter.Item{{
		GoalID: "goal-safe", Title: "safe", Description: "private description", KindInput: json.RawMessage(`{"secret":"value"}`),
		Assignee: "mio", Status: "waiting", TimingStatus: "known", TimingSource: "workstream.goal.task_plan",
		TaskID: "task-safe", RunID: "run-internal", ResultArtifactID: "artifact-internal", AllowedAgents: []string{"mio"},
	}}}}
	handler := NewTaskOverviewHandler(nil, list, "ren", []byte(taskOverviewTestToken))
	response := taskOverviewRequest(handler, http.MethodGet, "/viewer/board/task-overview?view=instant", true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("instant response=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"private description", "secret", "run-internal", "artifact-internal", "kind_input", "description"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestTaskOverviewTitleUsesExistingTaskActivitySafeProjection(t *testing.T) {
	got := domaintask.TaskActivityInventorySummary(domaintask.Task{Title: "safe\n title"}).Title
	if got != "safe  title" {
		t.Fatalf("sanitizer fixture=%q", got)
	}
}
