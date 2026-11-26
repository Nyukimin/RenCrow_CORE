package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskOverviewPath      = "/viewer/board/task-overview"
	taskOverviewMaxLimit  = 100
	taskOverviewMaxQuery  = 4096
	taskOverviewMaxCursor = 2048
)

type taskOverviewDailyReader interface {
	ListDaily(context.Context, taskcenter.DailyQuery) (taskcenter.DailyListResult, error)
}

type taskOverviewTaskReader interface {
	List(context.Context, taskcenter.ListQuery) (taskcenter.ListResult, error)
}

type taskOverviewHandler struct {
	daily   taskOverviewDailyReader
	instant taskOverviewTaskReader
	userID  string
	token   []byte
}

type taskOverviewQuery struct {
	view   string
	limit  int
	cursor string
}

type taskOverviewResponse struct {
	View            string              `json:"view"`
	ObservedAt      time.Time           `json:"observed_at"`
	Availability    string              `json:"availability"`
	Partial         bool                `json:"partial"`
	Issues          []taskOverviewIssue `json:"issues,omitempty"`
	IssuesTruncated bool                `json:"issues_truncated,omitempty"`
	Items           []taskOverviewItem  `json:"items"`
	NextCursor      string              `json:"next_cursor,omitempty"`
}

type taskOverviewIssue struct {
	SourceID string `json:"source_id"`
	OwnerRef string `json:"owner_ref,omitempty"`
	Code     string `json:"code"`
}

type taskOverviewReason struct {
	Aspect string `json:"aspect"`
	State  string `json:"state"`
	Code   string `json:"code"`
}

type taskOverviewItem struct {
	ID                  string                        `json:"id"`
	Title               string                        `json:"title"`
	Assignee            string                        `json:"assignee"`
	SourceID            string                        `json:"source_id,omitempty"`
	ResourceType        string                        `json:"resource_type,omitempty"`
	RecordType          string                        `json:"record_type,omitempty"`
	Reasons             []taskOverviewReason          `json:"reasons,omitempty"`
	ReasonsIncomplete   bool                          `json:"reasons_incomplete,omitempty"`
	ObservedState       string                        `json:"observed_state,omitempty"`
	SourceObservedAt    *time.Time                    `json:"source_observed_at,omitempty"`
	Children            *[]taskOverviewAggregateChild `json:"children,omitempty"`
	ChildCount          *int                          `json:"child_count,omitempty"`
	ChildrenTruncated   *bool                         `json:"children_truncated,omitempty"`
	Status              string                        `json:"status"`
	RecurrenceState     string                        `json:"recurrence_state"`
	RecurrenceFrequency *string                       `json:"recurrence_frequency,omitempty"`
	IntervalSeconds     *int64                        `json:"interval_seconds,omitempty"`
	NextRunState        string                        `json:"next_run_state"`
	NextRunAt           *time.Time                    `json:"next_run_at,omitempty"`
	LastAttemptAt       *time.Time                    `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time                    `json:"last_success_at,omitempty"`
	LastResult          *string                       `json:"last_result,omitempty"`
	AllowedAgents       *[]string                     `json:"allowed_agents,omitempty"`
	TaskID              string                        `json:"task_id,omitempty"`
}

type taskOverviewAggregateChild struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// NewTaskOverviewHandler creates the authenticated, read-only BOARD summary
// endpoint. Daily and managed reads remain separate owner calls.
func NewTaskOverviewHandler(daily taskOverviewDailyReader, instant taskOverviewTaskReader, userID string, token []byte) http.HandlerFunc {
	return (&taskOverviewHandler{
		daily: daily, instant: instant, userID: strings.TrimSpace(userID), token: append([]byte(nil), token...),
	}).ServeHTTP
}

func (h *taskOverviewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r == nil || r.URL == nil || !security.IsDirectLoopbackRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeMemoryOwnerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.Path != taskOverviewPath {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if h == nil || h.userID == "" || len(h.token) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_auth_unavailable")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !boardStudioOwnerProfileAllowed(r) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	query, err := parseTaskOverviewQuery(r.URL)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, err := taskCenterUserContext(r.Context(), h.userID)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_auth_unavailable")
		return
	}

	if query.view == "daily" {
		if h.daily == nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_unavailable")
			return
		}
		result, err := h.daily.ListDaily(ctx, taskcenter.DailyQuery{Limit: query.limit, Cursor: query.cursor})
		if err != nil || !validTaskOverviewAvailability(result.Availability) {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_unavailable")
			return
		}
		items := make([]taskOverviewItem, 0, len(result.Definitions))
		reasonsIncomplete := false
		for _, definition := range result.Definitions {
			item := projectDailyTaskOverviewItem(definition)
			reasonsIncomplete = reasonsIncomplete || item.ReasonsIncomplete
			items = append(items, item)
		}
		issuesTruncated := taskOverviewIssuesTruncated(result.Issues)
		projectionIncomplete := issuesTruncated || reasonsIncomplete
		availability := result.Availability
		if projectionIncomplete && availability == taskcenter.DailyAvailabilityAvailable {
			availability = taskcenter.DailyAvailabilityPartial
		}
		writeJSON(w, http.StatusOK, taskOverviewResponse{
			View: query.view, ObservedAt: result.ObservedAt, Availability: string(availability),
			Partial: result.Partial || availability != taskcenter.DailyAvailabilityAvailable || projectionIncomplete, Issues: projectTaskOverviewIssues(result.Issues),
			IssuesTruncated: issuesTruncated, Items: items, NextCursor: result.NextCursor,
		})
		return
	}

	if h.instant == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_unavailable")
		return
	}
	result, err := h.instant.List(ctx, taskcenter.ListQuery{View: taskcenter.ViewRemaining, Limit: query.limit, Cursor: query.cursor})
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_overview_unavailable")
		return
	}
	items := make([]taskOverviewItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, projectManagedTaskOverviewItem(item))
	}
	availability := string(taskcenter.DailyAvailabilityAvailable)
	if result.Partial {
		availability = string(taskcenter.DailyAvailabilityPartial)
	}
	writeJSON(w, http.StatusOK, taskOverviewResponse{
		View: query.view, ObservedAt: time.Now().UTC(), Availability: availability,
		Partial: result.Partial, Items: items, NextCursor: result.NextCursor,
	})
}

func validTaskOverviewAvailability(value taskcenter.DailyAvailability) bool {
	switch value {
	case taskcenter.DailyAvailabilityAvailable, taskcenter.DailyAvailabilityPartial, taskcenter.DailyAvailabilityUnavailable:
		return true
	default:
		return false
	}
}

func parseTaskOverviewQuery(requestURL *url.URL) (taskOverviewQuery, error) {
	if requestURL == nil {
		return taskOverviewQuery{}, errors.New("invalid query")
	}
	values, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil || len(requestURL.RawQuery) > taskOverviewMaxQuery {
		return taskOverviewQuery{}, errors.New("invalid query")
	}
	values, ok := taskCenterQueryValues(values, map[string]bool{"view": true, "limit": true, "cursor": true})
	if !ok {
		return taskOverviewQuery{}, errors.New("invalid query")
	}
	query := taskOverviewQuery{view: values.Get("view"), limit: 50, cursor: values.Get("cursor")}
	if query.view != "daily" && query.view != "instant" {
		return taskOverviewQuery{}, errors.New("invalid query")
	}
	if len(query.cursor) > taskOverviewMaxCursor || strings.TrimSpace(query.cursor) != query.cursor {
		return taskOverviewQuery{}, errors.New("invalid query")
	}
	if value, present := values["limit"]; present {
		if len(value[0]) > 3 {
			return taskOverviewQuery{}, errors.New("invalid query")
		}
		limit, err := strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > taskOverviewMaxLimit {
			return taskOverviewQuery{}, errors.New("invalid query")
		}
		query.limit = limit
	}
	return query, nil
}

func projectDailyTaskOverviewItem(definition taskcenter.DailyDefinition) taskOverviewItem {
	item := taskOverviewItem{
		ID: definition.DefinitionID, Title: safeTaskOverviewTitle(definition.Label), Assignee: safeTaskOverviewAssignee(definition.Assignee),
		SourceID: definition.SourceID, ResourceType: definition.ResourceType,
		RecordType: taskOverviewDailyRecordType(definition), ObservedState: taskOverviewObservedState(definition.Observed.State),
		Status: string(definition.Effective.State), RecurrenceState: string(definition.Timing.Recurrence.State),
		NextRunState: string(definition.NextRunAt.State),
	}
	item.Reasons, item.ReasonsIncomplete = projectTaskOverviewReasons(definition)
	if definition.SourceObservedAt.Known() && !definition.SourceObservedAt.Value.IsZero() {
		observedAt := definition.SourceObservedAt.Value.UTC()
		item.SourceObservedAt = &observedAt
	}
	if item.RecordType == taskOverviewRecordTypeAggregate {
		children := projectTaskOverviewWorkstreamChildren(definition)
		item.Children = &children
		truncated := definition.NativeRefsTruncated || len(children) < len(definition.NativeRefs)
		if definition.NativeRefCount.Known() {
			count := *definition.NativeRefCount.Value
			if count >= 0 {
				item.ChildCount = &count
				if count > len(children) {
					truncated = true
				}
			}
		}
		item.ChildrenTruncated = &truncated
	}
	if definition.Timing.Recurrence.Known() {
		frequency := definition.Timing.Recurrence.Value.Frequency
		if err := definition.Timing.Recurrence.Value.Validate(); err != nil {
			item.RecurrenceState = string(taskcenter.DailyValueUnsupported)
		} else {
			switch frequency {
			case modulecore.TaskRecurrenceNone:
				item.RecurrenceState = string(taskcenter.DailyValueNone)
				item.RecurrenceFrequency = &frequency
			case modulecore.TaskRecurrenceDaily, modulecore.TaskRecurrenceWeekly, modulecore.TaskRecurrenceInterval:
				item.RecurrenceState = string(taskcenter.DailyValueKnown)
				item.RecurrenceFrequency = &frequency
			default:
				item.RecurrenceState = string(taskcenter.DailyValueUnsupported)
			}
			if frequency == modulecore.TaskRecurrenceInterval && definition.Timing.Recurrence.Value.IntervalSeconds != nil {
				seconds := *definition.Timing.Recurrence.Value.IntervalSeconds
				item.IntervalSeconds = &seconds
			}
		}
	} else if definition.Timing.Recurrence.State == taskcenter.DailyValueKnown {
		item.RecurrenceState = string(taskcenter.DailyValueUnknown)
	}
	if definition.NextRunAt.Known() {
		nextRunAt := *definition.NextRunAt.Value
		item.NextRunAt = &nextRunAt
	}
	if definition.LatestEvidence.LastAttemptAt.Known() && !definition.LatestEvidence.LastAttemptAt.Value.IsZero() {
		at := definition.LatestEvidence.LastAttemptAt.Value.UTC()
		item.LastAttemptAt = &at
	}
	if definition.LatestEvidence.LastSuccessAt.Known() && !definition.LatestEvidence.LastSuccessAt.Value.IsZero() {
		at := definition.LatestEvidence.LastSuccessAt.Value.UTC()
		item.LastSuccessAt = &at
	}
	if definition.LatestEvidence.LastResult.Known() {
		switch result := *definition.LatestEvidence.LastResult.Value; result {
		case taskcenter.DailyOutcomeSuccess, taskcenter.DailyOutcomeFailure, taskcenter.DailyOutcomePartial,
			taskcenter.DailyOutcomeRunning, taskcenter.DailyOutcomeMissed, taskcenter.DailyOutcomeUnknown:
			value := string(result)
			item.LastResult = &value
		}
	}
	return item
}

const (
	taskOverviewRecordTypeJob                = "job"
	taskOverviewRecordTypeExecutionMechanism = "execution_mechanism"
	taskOverviewRecordTypeAggregate          = "aggregate"
	taskOverviewRecordTypeInformation        = "information"
	taskOverviewRecordTypeUnknown            = "unknown"

	taskOverviewWorkstreamSourceID       = "runtime.workstream-heartbeat"
	taskOverviewWorkstreamDefinitionID   = "workstream.heartbeat_schedules"
	taskOverviewWorkstreamOwnerRef       = "workstream.heartbeat_schedules"
	taskOverviewWorkstreamResourceType   = "workstream_heartbeat_schedule"
	taskOverviewWorkstreamChildReference = "workstream.heartbeat_schedule"
)

type taskOverviewDailyIdentity struct {
	definitionID string
	resourceType string
}

func taskOverviewDailyRecordType(definition taskcenter.DailyDefinition) string {
	identity := taskOverviewDailyIdentity{definitionID: definition.DefinitionID, resourceType: definition.ResourceType}
	switch identity {
	case taskOverviewDailyIdentity{definitionID: "heartbeat.core", resourceType: "heartbeat_schedule"}:
		return taskOverviewRecordTypeExecutionMechanism
	case taskOverviewDailyIdentity{definitionID: taskOverviewWorkstreamDefinitionID, resourceType: taskOverviewWorkstreamResourceType}:
		return taskOverviewRecordTypeAggregate
	case taskOverviewDailyIdentity{definitionID: "heartbeat.gmail_intake", resourceType: "heartbeat_child_routine"},
		taskOverviewDailyIdentity{definitionID: "heartbeat.atlas_maturation", resourceType: "heartbeat_child_routine"},
		taskOverviewDailyIdentity{definitionID: "heartbeat.backlog_intake", resourceType: "heartbeat_child_routine"},
		taskOverviewDailyIdentity{definitionID: "heartbeat.economic_discovery", resourceType: "heartbeat_child_routine"},
		taskOverviewDailyIdentity{definitionID: "heartbeat.x_bookmarks", resourceType: "heartbeat_child_routine"},
		taskOverviewDailyIdentity{definitionID: "idlechat.daily_seed_collection", resourceType: "daily_seed_snapshot"},
		taskOverviewDailyIdentity{definitionID: "idlechat.night_story_stock", resourceType: "story_episode_stock"},
		taskOverviewDailyIdentity{definitionID: "idlechat.topic_stock", resourceType: "topic_stock_snapshot"},
		taskOverviewDailyIdentity{definitionID: "knowledge_memory.daily_intake", resourceType: "daily_intake_rule_set"},
		taskOverviewDailyIdentity{definitionID: "memory.lifecycle_maintenance", resourceType: "maintenance_runner"},
		taskOverviewDailyIdentity{definitionID: "meeting.daily_report", resourceType: "meeting_definition"},
		taskOverviewDailyIdentity{definitionID: "movie_catalog.backfill", resourceType: "background_job"},
		taskOverviewDailyIdentity{definitionID: "person_related.collection", resourceType: "person_catalog_collection_worker"},
		taskOverviewDailyIdentity{definitionID: "person_related.identity", resourceType: "person_identity_worker"},
		taskOverviewDailyIdentity{definitionID: "person_related.summary", resourceType: "person_catalog_summary_worker"},
		taskOverviewDailyIdentity{definitionID: "memory.profile_promotion", resourceType: "background_worker"},
		taskOverviewDailyIdentity{definitionID: "tts.pronunciation_check", resourceType: "scheduler_schedule"},
		taskOverviewDailyIdentity{definitionID: "source_registry.feed_collection.aka", resourceType: "background_job"},
		taskOverviewDailyIdentity{definitionID: "source_registry.feed_collection.kin", resourceType: "background_job"}:
		return taskOverviewRecordTypeJob
	case taskOverviewDailyIdentity{definitionID: "backup.external_runner", resourceType: "external_backup_configuration"},
		taskOverviewDailyIdentity{definitionID: "heartbeat.workspace_file", resourceType: "workspace_instruction_source"},
		taskOverviewDailyIdentity{definitionID: "idlechat.watchdog", resourceType: "watchdog_snapshot"},
		taskOverviewDailyIdentity{definitionID: "logging.size_rotation", resourceType: "size_triggered_log_rotation"},
		taskOverviewDailyIdentity{definitionID: "maintenance.action_store_rotation", resourceType: "write_triggered_utc_segment_rotation"},
		taskOverviewDailyIdentity{definitionID: "maintenance.task_store_rotation", resourceType: "write_triggered_utc_segment_rotation"},
		taskOverviewDailyIdentity{definitionID: "memory.archive_parquet_export", resourceType: "unwired_export_helper"},
		taskOverviewDailyIdentity{definitionID: "newsbrief.daily_morning", resourceType: "daily_news_brief_read_projection"},
		taskOverviewDailyIdentity{definitionID: "newsdb.daily_items", resourceType: "news_item_set"},
		taskOverviewDailyIdentity{definitionID: "ops.resilience_status", resourceType: "manual_incident_status_cli"},
		taskOverviewDailyIdentity{definitionID: "revenue.daily_report", resourceType: "revenue_daily_report_history"}:
		return taskOverviewRecordTypeInformation
	default:
		return taskOverviewRecordTypeUnknown
	}
}

const taskOverviewMaxIssues = 100

func taskOverviewIssuesTruncated(issues []taskcenter.DailyIssue) bool {
	if len(issues) > taskOverviewMaxIssues {
		return true
	}
	for _, issue := range issues {
		if !taskOverviewSafeIdentifier(issue.SourceID) || !taskOverviewIssueCode(string(issue.Code)) ||
			(issue.OwnerRef != "" && !taskOverviewSafeOwnerRef(issue.OwnerRef)) {
			return true
		}
	}
	return false
}

func projectTaskOverviewIssues(issues []taskcenter.DailyIssue) []taskOverviewIssue {
	if len(issues) == 0 {
		return nil
	}
	projected := make([]taskOverviewIssue, 0, min(len(issues), taskOverviewMaxIssues))
	for index, issue := range issues {
		if index == taskOverviewMaxIssues {
			break
		}
		if !taskOverviewSafeIdentifier(issue.SourceID) || !taskOverviewIssueCode(string(issue.Code)) ||
			(issue.OwnerRef != "" && !taskOverviewSafeOwnerRef(issue.OwnerRef)) {
			continue
		}
		projected = append(projected, taskOverviewIssue{SourceID: issue.SourceID, OwnerRef: issue.OwnerRef, Code: string(issue.Code)})
	}
	return projected
}

func projectTaskOverviewReasons(definition taskcenter.DailyDefinition) ([]taskOverviewReason, bool) {
	type candidate struct{ aspect, state, code string }
	candidates := []candidate{
		{"configured", string(definition.Configured.State), definition.Configured.ReasonCode},
		{"effective", string(definition.Effective.State), definition.Effective.ReasonCode},
		{"observed", string(definition.Observed.State), definition.Observed.ReasonCode},
		{"recurrence", string(definition.Timing.Recurrence.State), definition.Timing.Recurrence.ReasonCode},
		{"next_run", string(definition.NextRunAt.State), definition.NextRunAt.ReasonCode},
		{"last_attempt", string(definition.LatestEvidence.LastAttemptAt.State), definition.LatestEvidence.LastAttemptAt.ReasonCode},
		{"last_success", string(definition.LatestEvidence.LastSuccessAt.State), definition.LatestEvidence.LastSuccessAt.ReasonCode},
		{"last_result", string(definition.LatestEvidence.LastResult.State), definition.LatestEvidence.LastResult.ReasonCode},
	}
	const maxReasons = 8
	result := make([]taskOverviewReason, 0, len(candidates))
	incomplete := false
	for _, candidate := range candidates {
		if !taskOverviewReasonState(candidate.state) {
			if candidate.code != "" && !taskOverviewNonReasonState(candidate.state) {
				incomplete = true
			}
			continue
		}
		if !taskOverviewSafeCode(candidate.code) {
			incomplete = true
			continue
		}
		if len(result) == maxReasons {
			incomplete = true
			continue
		}
		result = append(result, taskOverviewReason{Aspect: candidate.aspect, State: candidate.state, Code: candidate.code})
	}
	return result, incomplete
}

func taskOverviewNonReasonState(value string) bool {
	switch value {
	case "", string(taskcenter.DailyValueKnown), string(taskcenter.DailyValueNone),
		string(taskcenter.DailyImplementationImplemented), string(taskcenter.DailyImplementationUnimplemented),
		string(taskcenter.DailyConfiguredConfigured), string(taskcenter.DailyConfiguredUnconfigured),
		string(taskcenter.DailyConfiguredDisabled), string(taskcenter.DailyEffectiveEnabled),
		string(taskcenter.DailyObservedAvailable):
		return true
	default:
		return false
	}
}

func taskOverviewReasonState(value string) bool {
	switch value {
	case "unknown", "not_observed", "unavailable", "unsupported", "stale":
		return true
	default:
		return false
	}
}

func taskOverviewSafeCode(value string) bool {
	if len(value) == 0 || len(value) > 64 || value != strings.ToLower(value) {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && strings.ContainsRune("._-", r)) {
			continue
		}
		return false
	}
	return true
}

func taskOverviewIssueCode(value string) bool {
	switch taskcenter.DailyIssueCode(value) {
	case taskcenter.DailyIssueSourceUnavailable, taskcenter.DailyIssueInvalidProjection,
		taskcenter.DailyIssueObservationMissing, taskcenter.DailyIssueSourceReported,
		taskcenter.DailyIssueKindFilterUnavailable:
		return taskOverviewSafeCode(value)
	default:
		return false
	}
}

func taskOverviewSafeIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && strings.ContainsRune("._-", r)) {
			continue
		}
		return false
	}
	return true
}

func taskOverviewSafeOwnerRef(value string) bool {
	if len(value) == 0 || len(value) > 512 || value != strings.TrimSpace(value) ||
		strings.Contains(value, "..") || strings.Contains(value, "://") || strings.ContainsAny(value, "\\?#") ||
		strings.HasPrefix(value, "/") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:/-", r) {
			continue
		}
		return false
	}
	return true
}

func taskOverviewObservedState(state taskcenter.DailyAxisState) string {
	switch state {
	case taskcenter.DailyObservedAvailable,
		taskcenter.DailyObservedUnavailable,
		taskcenter.DailyObservedStale,
		taskcenter.DailyObservedNotObserved,
		taskcenter.DailyObservedUnsupported,
		taskcenter.DailyObservedUnknown:
		return string(state)
	default:
		return string(taskcenter.DailyObservedUnknown)
	}
}

func projectTaskOverviewWorkstreamChildren(definition taskcenter.DailyDefinition) []taskOverviewAggregateChild {
	children := make([]taskOverviewAggregateChild, 0, len(definition.NativeRefs))
	if definition.SourceID != taskOverviewWorkstreamSourceID || definition.DefinitionID != taskOverviewWorkstreamDefinitionID ||
		definition.OwnerRef != taskOverviewWorkstreamOwnerRef || definition.ResourceType != taskOverviewWorkstreamResourceType {
		return children
	}
	for _, ref := range definition.NativeRefs {
		if ref.SourceID != taskOverviewWorkstreamSourceID || ref.OwnerRef != taskOverviewWorkstreamOwnerRef ||
			ref.ResourceType != taskOverviewWorkstreamResourceType || ref.Reference != taskOverviewWorkstreamChildReference ||
			ref.DataScope != tool.DataScopePublic || ref.UserID != "" {
			continue
		}
		scheduleID := modulecore.ScheduleID(ref.NativeID)
		if scheduleID.Validate() != nil {
			continue
		}
		child := taskOverviewAggregateChild{ID: string(scheduleID)}
		if ref.DisplayName != "" {
			child.DisplayName = safeTaskOverviewTitle(ref.DisplayName)
		}
		children = append(children, child)
	}
	return children
}

func projectManagedTaskOverviewItem(source taskcenter.Item) taskOverviewItem {
	item := taskOverviewItem{
		ID: source.GoalID, Title: safeTaskOverviewTitle(source.Title), Assignee: safeTaskOverviewAssignee(source.Assignee),
		Status: source.Status, TaskID: source.TaskID,
	}
	allowedAgents := append([]string{}, source.AllowedAgents...)
	item.AllowedAgents = &allowedAgents
	projectManagedTaskOverviewRecurrence(&item, source)
	projectManagedTaskOverviewNextRun(&item, source)
	return item
}

func projectManagedTaskOverviewRecurrence(item *taskOverviewItem, source taskcenter.Item) {
	if source.TimingStatus != "known" {
		item.RecurrenceState = knownTaskOverviewTimingState(source.TimingStatus)
		return
	}
	if source.Timing.Recurrence == nil {
		if source.TimingSource == "workstream.goal.task_plan" {
			item.RecurrenceState = string(taskcenter.DailyValueNone)
		} else {
			item.RecurrenceState = string(taskcenter.DailyValueUnknown)
		}
		return
	}
	frequency := source.Timing.Recurrence.Frequency
	if frequency == "" {
		item.RecurrenceState = string(taskcenter.DailyValueUnknown)
		return
	}
	if err := source.Timing.Recurrence.Validate(); err != nil {
		item.RecurrenceState = string(taskcenter.DailyValueUnsupported)
		return
	}
	switch frequency {
	case modulecore.TaskRecurrenceNone:
		item.RecurrenceState = string(taskcenter.DailyValueNone)
		item.RecurrenceFrequency = &frequency
	case modulecore.TaskRecurrenceDaily, modulecore.TaskRecurrenceWeekly, modulecore.TaskRecurrenceInterval:
		item.RecurrenceState = string(taskcenter.DailyValueKnown)
		item.RecurrenceFrequency = &frequency
	default:
		item.RecurrenceState = string(taskcenter.DailyValueUnsupported)
	}
	if frequency == modulecore.TaskRecurrenceInterval && source.Timing.Recurrence.IntervalSeconds != nil {
		seconds := *source.Timing.Recurrence.IntervalSeconds
		item.IntervalSeconds = &seconds
	}
}

func projectManagedTaskOverviewNextRun(item *taskOverviewItem, source taskcenter.Item) {
	if source.TimingStatus != "known" {
		item.NextRunState = knownTaskOverviewTimingState(source.TimingStatus)
		return
	}
	if source.Timing.ScheduledStartAt != nil {
		nextRunAt := *source.Timing.ScheduledStartAt
		item.NextRunState = string(taskcenter.DailyValueKnown)
		item.NextRunAt = &nextRunAt
		return
	}
	if source.Timing.Recurrence == nil {
		if source.TimingSource == "workstream.goal.task_plan" {
			item.NextRunState = string(taskcenter.DailyValueNone)
		} else {
			item.NextRunState = string(taskcenter.DailyValueUnknown)
		}
		return
	}
	switch source.Timing.Recurrence.Frequency {
	case modulecore.TaskRecurrenceNone:
		item.NextRunState = string(taskcenter.DailyValueNone)
	case "":
		item.NextRunState = string(taskcenter.DailyValueUnknown)
	default:
		item.NextRunState = string(taskcenter.DailyValueUnsupported)
	}
}

func knownTaskOverviewTimingState(state string) string {
	switch state {
	case "unconfirmed", "unknown":
		return string(taskcenter.DailyValueUnknown)
	case "unavailable", "unsupported":
		return state
	default:
		return string(taskcenter.DailyValueUnknown)
	}
}

func safeTaskOverviewTitle(raw string) string {
	return domaintask.TaskActivityInventorySummary(domaintask.Task{Title: raw}).Title
}

func safeTaskOverviewAssignee(raw string) string {
	return domaintask.TaskActivityInventorySummary(domaintask.Task{Assignee: raw}).Assignee
}
