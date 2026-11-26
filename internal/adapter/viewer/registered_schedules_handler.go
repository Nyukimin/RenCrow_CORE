package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
)

const (
	registeredSchedulesPath                = "/viewer/board/registered-schedules"
	registeredSchedulesScanLimit           = 1000
	registeredSchedulesMaxLimit            = 100
	registeredSchedulesMaxIDRunes          = 2048
	registeredSchedulesMaxIDBytes          = registeredSchedulesMaxIDRunes * 4
	registeredSchedulesCursorOverheadBytes = len("workstream") + len("v2") + 2
	registeredSchedulesMaxCursor           = (registeredSchedulesMaxIDBytes+registeredSchedulesCursorOverheadBytes)*4/3 + 4
	registeredSchedulesMaxQuery            = registeredSchedulesMaxCursor + 64
	registeredSchedulesMaxDescriptionRunes = 8192
)

var errRegisteredSchedulesStaleCursor = errors.New("stale registered schedules cursor")

type RegisteredScheduleHeartbeatReader interface {
	ListHeartbeatSchedules(context.Context, int) ([]domainworkstream.HeartbeatSchedule, error)
}

type registeredSchedulePager interface {
	ListSchedulesPage(context.Context, string, int, func(domainscheduler.Schedule) bool) ([]domainscheduler.Schedule, bool, error)
}

type registeredHeartbeatSchedulePager interface {
	ListHeartbeatSchedulesPage(context.Context, string, int) ([]domainworkstream.HeartbeatSchedule, bool, error)
}

// RegisteredScheduleCronRecord contains only the read-only presentation of a
// current-user cron registration, never its command line or environment.
type RegisteredScheduleCronRecord struct {
	ID         string
	Title      string
	Schedule   string
	Status     string
	RecordType string
}

type RegisteredScheduleCronReader interface {
	ListRegisteredCronSchedules(context.Context) ([]RegisteredScheduleCronRecord, string, error)
}

type registeredSchedulesHandler struct {
	scheduler  SchedulerStore
	workstream RegisteredScheduleHeartbeatReader
	cron       RegisteredScheduleCronReader
	policy     protectedSchedulerPolicy
	userID     string
	token      []byte
}

type registeredSchedulesResponse struct {
	Source           string                   `json:"source"`
	ObservedAt       time.Time                `json:"observed_at"`
	Availability     string                   `json:"availability"`
	Items            []registeredScheduleItem `json:"items"`
	NextCursor       string                   `json:"next_cursor,omitempty"`
	IncompleteReason string                   `json:"incomplete_reason,omitempty"`
}

type registeredScheduleItem struct {
	ID                   string     `json:"id"`
	Title                string     `json:"title"`
	Description          string     `json:"description,omitempty"`
	DescriptionTruncated bool       `json:"description_truncated,omitempty"`
	Schedule             string     `json:"schedule"`
	Status               string     `json:"status"`
	RecordType           string     `json:"record_type,omitempty"`
	NextRunAt            *time.Time `json:"next_run_at,omitempty"`
}

// NewRegisteredSchedulesHandler creates an owner-authenticated, read-only
// projection of individually registered Scheduler, Workstream and cron schedules.
func NewRegisteredSchedulesHandler(
	scheduler SchedulerStore,
	workstream RegisteredScheduleHeartbeatReader,
	cron RegisteredScheduleCronReader,
	userID string,
	token []byte,
	protectedTargets, protectedScheduleIDs []string,
) http.HandlerFunc {
	return (&registeredSchedulesHandler{
		scheduler: scheduler, workstream: workstream, cron: cron,
		policy: newProtectedSchedulerPolicy(protectedTargets, protectedScheduleIDs),
		userID: strings.TrimSpace(userID), token: append([]byte(nil), token...),
	}).ServeHTTP
}

func (h *registeredSchedulesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if r.URL.Path != registeredSchedulesPath {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if h == nil || h.userID == "" || len(h.token) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "registered_schedules_auth_unavailable")
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
	query, err := parseRegisteredSchedulesQuery(r.URL)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	afterID, err := decodeRegisteredSchedulesCursor(query.cursor, query.source)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	page, next, availability, incompleteReason, err := h.listPage(r.Context(), query, afterID)
	if err != nil {
		if errors.Is(err, errRegisteredSchedulesStaleCursor) {
			writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		writeJSON(w, http.StatusOK, registeredSchedulesResponse{
			Source: query.source, ObservedAt: time.Now().UTC(), Availability: "unavailable",
			Items: []registeredScheduleItem{},
		})
		return
	}
	writeJSON(w, http.StatusOK, registeredSchedulesResponse{
		Source: query.source, ObservedAt: time.Now().UTC(), Availability: availability,
		Items: page, NextCursor: next, IncompleteReason: incompleteReason,
	})
}

type registeredSchedulesQuery struct {
	source string
	limit  int
	cursor string
}

func parseRegisteredSchedulesQuery(requestURL *url.URL) (registeredSchedulesQuery, error) {
	if requestURL == nil || len(requestURL.RawQuery) > registeredSchedulesMaxQuery {
		return registeredSchedulesQuery{}, errors.New("invalid query")
	}
	values, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil {
		return registeredSchedulesQuery{}, err
	}
	for key, entries := range values {
		if (key != "source" && key != "limit" && key != "cursor") || len(entries) != 1 {
			return registeredSchedulesQuery{}, errors.New("invalid query field")
		}
	}
	query := registeredSchedulesQuery{source: values.Get("source"), limit: 50, cursor: values.Get("cursor")}
	if query.source != "scheduler" && query.source != "workstream" && query.source != "cron" {
		return registeredSchedulesQuery{}, errors.New("invalid source")
	}
	if len(query.cursor) > registeredSchedulesMaxCursor || strings.TrimSpace(query.cursor) != query.cursor {
		return registeredSchedulesQuery{}, errors.New("invalid cursor")
	}
	if raw, exists := values["limit"]; exists {
		if len(raw[0]) > 3 {
			return registeredSchedulesQuery{}, errors.New("invalid limit")
		}
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > registeredSchedulesMaxLimit {
			return registeredSchedulesQuery{}, errors.New("invalid limit")
		}
		query.limit = limit
	}
	return query, nil
}

func (h *registeredSchedulesHandler) listPage(
	ctx context.Context,
	query registeredSchedulesQuery,
	afterID string,
) ([]registeredScheduleItem, string, string, string, error) {
	if query.source == "scheduler" {
		if pager, ok := h.scheduler.(registeredSchedulePager); ok {
			return h.listSchedulerPage(ctx, pager, query, afterID)
		}
	}
	if query.source == "workstream" {
		if pager, ok := h.workstream.(registeredHeartbeatSchedulePager); ok {
			return h.listWorkstreamPage(ctx, pager, query, afterID)
		}
	}
	items, availability, incompleteReason, err := h.listItems(ctx, query.source)
	if err != nil {
		return nil, "", "", "", err
	}
	page, next, dropped, err := registeredSchedulePage(items, query, afterID)
	if err != nil {
		return nil, "", "", "", err
	}
	if dropped {
		availability = "partial"
	}
	return page, next, availability, incompleteReason, nil
}

func (h *registeredSchedulesHandler) listSchedulerPage(
	ctx context.Context,
	pager registeredSchedulePager,
	query registeredSchedulesQuery,
	afterID string,
) ([]registeredScheduleItem, string, string, string, error) {
	schedules, hasMore, err := pager.ListSchedulesPage(ctx, afterID, query.limit, func(schedule domainscheduler.Schedule) bool {
		return !h.policy.protectsTarget(schedule.Target) && !h.policy.protectsID(string(schedule.ScheduleID))
	})
	if err != nil {
		return nil, "", "", "", err
	}
	items := make([]registeredScheduleItem, 0, min(len(schedules), query.limit))
	partial := len(schedules) > query.limit
	lastScanned := afterID
	for index, schedule := range schedules {
		if index >= query.limit {
			break
		}
		id := string(schedule.ScheduleID)
		if safeRegisteredScheduleID(id) != id || id <= lastScanned {
			partial = true
			continue
		}
		lastScanned = id
		if h.policy.protectsTarget(schedule.Target) || h.policy.protectsID(id) {
			partial = true
			continue
		}
		items = append(items, projectRegisteredSchedulerSchedule(schedule))
	}
	if len(items) == 0 && hasMore {
		return items, "", "partial", "", nil
	}
	next := ""
	if hasMore && len(items) > 0 {
		next = encodeRegisteredSchedulesCursor(query.source, items[len(items)-1].ID)
	}
	availability := "available"
	reason := ""
	if partial {
		availability = "partial"
	}
	return items, next, availability, reason, nil
}

func (h *registeredSchedulesHandler) listWorkstreamPage(
	ctx context.Context,
	pager registeredHeartbeatSchedulePager,
	query registeredSchedulesQuery,
	afterID string,
) ([]registeredScheduleItem, string, string, string, error) {
	schedules, hasMore, err := pager.ListHeartbeatSchedulesPage(ctx, afterID, query.limit)
	if err != nil {
		return nil, "", "", "", err
	}
	items := make([]registeredScheduleItem, 0, min(len(schedules), query.limit))
	partial := len(schedules) > query.limit
	lastScanned := afterID
	for index, schedule := range schedules {
		if index >= query.limit {
			break
		}
		id := string(schedule.ScheduleID)
		if safeRegisteredScheduleID(id) != id || id <= lastScanned {
			partial = true
			continue
		}
		lastScanned = id
		items = append(items, projectRegisteredHeartbeatSchedule(schedule))
	}
	if len(items) == 0 && hasMore {
		return items, "", "partial", "", nil
	}
	next := ""
	if hasMore && len(items) > 0 {
		next = encodeRegisteredSchedulesCursor(query.source, items[len(items)-1].ID)
	}
	availability := "available"
	reason := ""
	if partial {
		availability = "partial"
	}
	return items, next, availability, reason, nil
}

func (h *registeredSchedulesHandler) listItems(ctx context.Context, source string) ([]registeredScheduleItem, string, string, error) {
	if source == "cron" {
		if h.cron == nil {
			return nil, "", "", errors.New("cron reader unavailable")
		}
		records, availability, err := h.cron.ListRegisteredCronSchedules(ctx)
		if err != nil || (availability != "available" && availability != "partial") {
			return nil, "", "", errors.New("cron observation unavailable")
		}
		items := make([]registeredScheduleItem, 0, len(records))
		for _, record := range records {
			if safeRegisteredScheduleID(record.ID) != record.ID {
				availability = "partial"
				continue
			}
			title := safeRegisteredScheduleText(record.Title, 200)
			if title == "" {
				title = "Unnamed cron registration"
			}
			recordType := record.RecordType
			if recordType != "job" && recordType != "execution_mechanism" && recordType != "unknown" {
				recordType = "unknown"
				availability = "partial"
			}
			items = append(items, registeredScheduleItem{
				ID: safeRegisteredScheduleID(record.ID), Title: title,
				Schedule: safeRegisteredScheduleText(record.Schedule, 200),
				Status:   safeRegisteredScheduleText(record.Status, 80), RecordType: recordType,
			})
		}
		items, identified := retainIdentifiedRegisteredSchedules(items)
		if identified == "partial" {
			availability = "partial"
		}
		return items, availability, "", nil
	}
	if source == "scheduler" {
		if h.scheduler == nil {
			return nil, "", "", errors.New("scheduler store unavailable")
		}
		schedules, _, err := h.policy.listVisibleSchedules(ctx, h.scheduler, registeredSchedulesScanLimit)
		if err != nil {
			return nil, "", "", err
		}
		items := make([]registeredScheduleItem, 0, len(schedules))
		availability := "available"
		for _, schedule := range schedules {
			if safeRegisteredScheduleID(string(schedule.ScheduleID)) != string(schedule.ScheduleID) {
				availability = "partial"
				continue
			}
			if h.policy.protectsTarget(schedule.Target) || h.policy.protectsID(string(schedule.ScheduleID)) {
				availability = "partial"
				continue
			}
			items = append(items, projectRegisteredSchedulerSchedule(schedule))
		}
		items, identified := retainIdentifiedRegisteredSchedules(items)
		if identified == "partial" {
			availability = "partial"
		}
		incompleteReason := ""
		if len(schedules) >= registeredSchedulesScanLimit {
			availability = "partial"
			incompleteReason = "owner_pagination_unavailable"
		}
		return items, availability, incompleteReason, nil
	}
	if h.workstream == nil {
		return nil, "", "", errors.New("workstream schedule store unavailable")
	}
	schedules, err := h.workstream.ListHeartbeatSchedules(ctx, registeredSchedulesScanLimit)
	if err != nil {
		return nil, "", "", err
	}
	items := make([]registeredScheduleItem, 0, len(schedules))
	availability := "available"
	for _, schedule := range schedules {
		if safeRegisteredScheduleID(string(schedule.ScheduleID)) != string(schedule.ScheduleID) {
			availability = "partial"
			continue
		}
		items = append(items, projectRegisteredHeartbeatSchedule(schedule))
	}
	items, identified := retainIdentifiedRegisteredSchedules(items)
	if identified == "partial" {
		availability = "partial"
	}
	incompleteReason := ""
	if len(schedules) >= registeredSchedulesScanLimit {
		availability = "partial"
		incompleteReason = "owner_pagination_unavailable"
	}
	return items, availability, incompleteReason, nil
}

func retainIdentifiedRegisteredSchedules(items []registeredScheduleItem) ([]registeredScheduleItem, string) {
	retained := items[:0]
	seen := make(map[string]struct{}, len(items))
	availability := "available"
	for _, item := range items {
		if item.ID == "" {
			availability = "partial"
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			availability = "partial"
			continue
		}
		seen[item.ID] = struct{}{}
		retained = append(retained, item)
	}
	return retained, availability
}

func projectRegisteredSchedulerSchedule(schedule domainscheduler.Schedule) registeredScheduleItem {
	description, truncated := safeRegisteredScheduleDescription(schedule.Description, registeredSchedulesMaxDescriptionRunes)
	item := registeredScheduleItem{
		ID: safeRegisteredScheduleID(string(schedule.ScheduleID)), Title: safeRegisteredScheduleText(schedule.Name, 200),
		Description: description, DescriptionTruncated: truncated,
		Schedule: registeredSchedulerScheduleText(schedule), Status: "disabled",
	}
	if item.Title == "" {
		item.Title = "Unnamed schedule"
	}
	if schedule.Enabled {
		item.Status = "enabled"
	}
	if !schedule.NextRunAt.IsZero() {
		next := schedule.NextRunAt.UTC()
		item.NextRunAt = &next
	}
	return item
}

func registeredSchedulerScheduleText(schedule domainscheduler.Schedule) string {
	if value := safeRegisteredScheduleText(schedule.Schedule, 200); value != "" {
		return value
	}
	if schedule.Timing == nil || schedule.Timing.Recurrence == nil {
		return ""
	}
	frequency := schedule.Timing.Recurrence.Frequency
	if err := schedule.Timing.Validate(); err != nil {
		return ""
	}
	if frequency == "interval" && schedule.Timing.Recurrence.IntervalSeconds != nil {
		return "every " + strconv.FormatInt(*schedule.Timing.Recurrence.IntervalSeconds, 10) + "s"
	}
	return safeRegisteredScheduleText(string(frequency), 80)
}

func projectRegisteredHeartbeatSchedule(schedule domainworkstream.HeartbeatSchedule) registeredScheduleItem {
	description, truncated := safeRegisteredScheduleDescription(schedule.Task, registeredSchedulesMaxDescriptionRunes)
	item := registeredScheduleItem{
		ID: safeRegisteredScheduleID(string(schedule.ScheduleID)), Title: safeRegisteredScheduleText(schedule.Task, 200),
		Description: description, DescriptionTruncated: truncated,
		Schedule: safeRegisteredScheduleText(schedule.ScheduleText, 200), Status: safeRegisteredScheduleText(schedule.Status, 80),
	}
	if item.Title == "" {
		item.Title = "Heartbeat schedule"
	}
	if !schedule.NextRunAt.IsZero() {
		next := schedule.NextRunAt.UTC()
		item.NextRunAt = &next
	}
	return item
}

func safeRegisteredScheduleID(value string) string {
	if value == "" || strings.TrimSpace(value) != value {
		return ""
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return ""
		}
	}
	runes := []rune(value)
	if len(runes) <= registeredSchedulesMaxIDRunes {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	suffix := "~" + hex.EncodeToString(digest[:])
	return string(runes[:registeredSchedulesMaxIDRunes-len(suffix)]) + suffix
}

func safeRegisteredScheduleText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return ""
	}
	clean := make([]rune, 0, min(len([]rune(value)), maxRunes))
	for _, char := range value {
		if unicode.IsControl(char) {
			continue
		}
		clean = append(clean, char)
		if len(clean) == maxRunes {
			break
		}
	}
	return strings.TrimSpace(string(clean))
}

func safeRegisteredScheduleDescription(value string, maxRunes int) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return "", false
	}
	clean := make([]rune, 0, min(len([]rune(value)), maxRunes))
	truncated := false
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			continue
		}
		if len(clean) == maxRunes {
			truncated = true
			break
		}
		clean = append(clean, char)
	}
	return strings.TrimSpace(string(clean)), truncated
}

func registeredSchedulePage(items []registeredScheduleItem, query registeredSchedulesQuery, afterID string) ([]registeredScheduleItem, string, bool, error) {
	retained := make([]registeredScheduleItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	dropped := false
	for _, item := range items {
		if item.ID == "" || safeRegisteredScheduleID(item.ID) != item.ID {
			dropped = true
			continue
		}
		if _, exists := seen[item.ID]; exists {
			dropped = true
			continue
		}
		seen[item.ID] = struct{}{}
		retained = append(retained, item)
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].ID < retained[j].ID })
	start := sort.Search(len(retained), func(index int) bool { return retained[index].ID > afterID })
	if afterID != "" {
		if _, exists := seen[afterID]; !exists {
			return nil, "", dropped, errRegisteredSchedulesStaleCursor
		}
	}
	end := min(start+query.limit, len(retained))
	page := make([]registeredScheduleItem, end-start)
	copy(page, retained[start:end])
	if end >= len(retained) || len(page) == 0 {
		return page, "", dropped, nil
	}
	return page, encodeRegisteredSchedulesCursor(query.source, page[len(page)-1].ID), dropped, nil
}

func encodeRegisteredSchedulesCursor(source, afterID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(source + "\x00v2\x00" + afterID))
}

func decodeRegisteredSchedulesCursor(cursor, source string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(decoded), "\x00", 3)
	if len(parts) != 3 || parts[0] != source || parts[1] != "v2" || parts[2] == "" ||
		safeRegisteredScheduleID(parts[2]) != parts[2] {
		return "", errors.New("invalid cursor")
	}
	return parts[2], nil
}
