package viewer

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskActivitiesPath          = "/viewer/board/task-activities"
	maxTaskActivitiesCursor     = 512
	taskActivitiesPageScanLimit = 5
)

var taskActivityActorIDs = []string{"mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin"}

type TaskActivitiesReader interface {
	ListTaskActivityInventory(context.Context, domaintask.TaskActivityInventoryFilter, domaintask.TaskReadAuthorizationCheck) (domaintask.TaskActivityInventoryPage, error)
	SnapshotCurrentActivitiesAuthorized(context.Context, []string, domaintask.TaskReadAuthorizationCheck) (map[string]domaintask.CurrentActivitySummary, error)
}

type TaskActivitiesGoalReader interface {
	Get(context.Context, string) (taskcenter.Item, error)
}

type TaskActivitiesPublicationReader interface {
	CurrentPublicationBinding(context.Context, string) (domainsession.CanonicalSessionPublicationBinding, error)
}

type taskActivitiesHandler struct {
	reader       TaskActivitiesReader
	goals        TaskActivitiesGoalReader
	publications TaskActivitiesPublicationReader
	userID       string
	token        []byte
}

type taskActivitiesQuery struct {
	View          string
	Assignee      string
	Status        domaintask.Status
	ExcludeStatus domaintask.Status
	Category      domaintask.ActivityKind
	Limit         int
	Cursor        string
	BeforeTaskID  modulecore.TaskID
}

type taskActivitiesCursor struct {
	Version  int    `json:"version"`
	TaskID   string `json:"task_id"`
	UserID   string `json:"user_id"`
	View     string `json:"view"`
	Assignee string `json:"assignee"`
	Status   string `json:"status"`
	Category string `json:"category"`
	Limit    int    `json:"limit"`
}

type taskActivitiesCurrentDTO struct {
	ActorID         string                              `json:"actor_id"`
	TaskID          modulecore.TaskID                   `json:"task_id"`
	Revision        uint64                              `json:"revision"`
	CurrentActivity PublicCurrentActivity               `json:"current_activity"`
	Task            domaintask.TaskActivityInventoryRow `json:"task"`
}

type taskActivityCurrentListRow struct {
	Task     domaintask.TaskActivityInventoryRow
	ActorIDs map[string]struct{}
}

type taskActivitiesResponse struct {
	ObservedAt time.Time                             `json:"observed_at"`
	Current    []taskActivitiesCurrentDTO            `json:"current"`
	Items      []domaintask.TaskActivityInventoryRow `json:"items"`
	NextCursor string                                `json:"next_cursor,omitempty"`
}

func NewTaskActivitiesHandler(reader TaskActivitiesReader, goals TaskActivitiesGoalReader, userID string, token []byte, publications ...TaskActivitiesPublicationReader) http.HandlerFunc {
	var publicationReader TaskActivitiesPublicationReader
	if len(publications) > 0 {
		publicationReader = publications[0]
	}
	return (&taskActivitiesHandler{reader: reader, goals: goals, publications: publicationReader, userID: strings.TrimSpace(userID), token: append([]byte(nil), token...)}).ServeHTTP
}

func (h *taskActivitiesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !security.IsDirectLoopbackRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeMemoryOwnerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL == nil || r.URL.Path != taskActivitiesPath {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if len(h.token) == 0 || h.userID == "" {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "owner_auth_unavailable")
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
	query, err := parseTaskActivitiesQuery(r.URL)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.reader == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_unavailable")
		return
	}
	if query.Cursor != "" {
		id, err := decodeTaskActivitiesCursor(query.Cursor, h.token, query, h.userID)
		if err != nil {
			writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		query.BeforeTaskID = modulecore.TaskID(id)
	}
	ctx, err := taskCenterUserContext(r.Context(), h.userID)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_auth_unavailable")
		return
	}
	current, err := h.reader.SnapshotCurrentActivitiesAuthorized(ctx, taskActivityActorIDs, h.authorizeTaskRead)
	if err != nil {
		if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) || errors.Is(err, taskcenter.ErrForbidden) {
			writeMemoryOwnerError(w, http.StatusForbidden, "task_activities_forbidden")
		} else {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_current_unavailable")
		}
		return
	}
	currentDTO, currentRows, ok := taskActivitiesCurrentSnapshot(current)
	if !ok {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_current_inconsistent")
		return
	}
	var items []domaintask.TaskActivityInventoryRow
	next := ""
	if query.View == "current" {
		items = currentTaskActivityItems(current, query)
	} else {
		items, next, err = h.listTaskActivities(ctx, query, currentRows)
		if err != nil {
			if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) || errors.Is(err, taskcenter.ErrForbidden) {
				writeMemoryOwnerError(w, http.StatusForbidden, "task_activities_forbidden")
			} else {
				writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_unavailable")
			}
			return
		}
	}
	var publicRowsOK bool
	items, publicRowsOK = taskActivitiesPublicRows(items)
	if !publicRowsOK {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_activity_invalid")
		return
	}
	if next != "" {
		next, err = encodeTaskActivitiesCursor(next, h.token, query, h.userID)
		if err != nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "task_activities_cursor_unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, taskActivitiesResponse{ObservedAt: time.Now().UTC(), Current: currentDTO, Items: items, NextCursor: next})
}

func parseTaskActivitiesQuery(raw *url.URL) (taskActivitiesQuery, error) {
	values, err := url.ParseQuery(raw.RawQuery)
	if err != nil || len(raw.RawQuery) > 4096 {
		return taskActivitiesQuery{}, errors.New("invalid query")
	}
	allowed := map[string]bool{"view": true, "assignee": true, "status": true, "category": true, "limit": true, "cursor": true}
	for key, fields := range values {
		if !allowed[key] || len(fields) != 1 || strings.TrimSpace(fields[0]) != fields[0] {
			return taskActivitiesQuery{}, errors.New("invalid query")
		}
	}
	query := taskActivitiesQuery{View: values.Get("view"), Limit: 50, Cursor: values.Get("cursor")}
	if query.View == "" {
		query.View = "all"
	}
	if query.View != "all" && query.View != "current" && query.View != "history" && query.View != "unfinished" {
		return taskActivitiesQuery{}, errors.New("invalid view")
	}
	if query.View == "unfinished" {
		query.ExcludeStatus = domaintask.StatusSucceeded
	}
	query.Assignee = values.Get("assignee")
	if query.Assignee != "" && !taskActivityActorAllowed(query.Assignee) {
		return taskActivitiesQuery{}, errors.New("invalid assignee")
	}
	query.Status = domaintask.Status(values.Get("status"))
	query.Category = domaintask.ActivityKind(values.Get("category"))
	filter := domaintask.TaskActivityInventoryFilter{Status: query.Status, ExcludeStatus: query.ExcludeStatus, Category: query.Category, Assignee: query.Assignee, Limit: 1}
	if !validTaskActivitiesFilter(filter) {
		return taskActivitiesQuery{}, errors.New("invalid filters")
	}
	if rawLimit := values.Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 200 || strconv.Itoa(limit) != rawLimit {
			return taskActivitiesQuery{}, errors.New("invalid limit")
		}
		query.Limit = limit
	}
	if len(query.Cursor) > maxTaskActivitiesCursor {
		return taskActivitiesQuery{}, errors.New("invalid cursor")
	}
	if query.View == "current" && query.Cursor != "" {
		return taskActivitiesQuery{}, errors.New("current view does not use a cursor")
	}
	return query, nil
}

func validTaskActivitiesFilter(filter domaintask.TaskActivityInventoryFilter) bool {
	if filter.Status != "" {
		switch filter.Status {
		case domaintask.StatusQueued, domaintask.StatusRunning, domaintask.StatusWaiting, domaintask.StatusBlocked,
			domaintask.StatusFailed, domaintask.StatusSucceeded, domaintask.StatusCancelled, domaintask.StatusSuperseded:
		default:
			return false
		}
	}
	if filter.Category != "" {
		switch filter.Category {
		case domaintask.ActivityKindWork, domaintask.ActivityKindChat, domaintask.ActivityKindHobby, domaintask.ActivityKindRest,
			domaintask.ActivityKindSleep, domaintask.ActivityKindMoving, domaintask.ActivityKindGame, domaintask.ActivityKindEvaluating,
			domaintask.ActivityKindRoutine:
		default:
			return false
		}
	}
	return true
}

func (h *taskActivitiesHandler) listTaskActivities(ctx context.Context, query taskActivitiesQuery, current map[modulecore.TaskID]taskActivityCurrentListRow) ([]domaintask.TaskActivityInventoryRow, string, error) {
	before := query.BeforeTaskID
	items := make([]domaintask.TaskActivityInventoryRow, 0, query.Limit)
	seen := make(map[modulecore.TaskID]struct{}, len(current))
	for id, snapshot := range current {
		if (query.View == "all" || query.View == "unfinished") && (before == "" || id < before) && currentTaskActivityMatches(snapshot, query) {
			items = append(items, snapshot.Task)
			seen[id] = struct{}{}
		}
	}
	for scan := 0; scan < taskActivitiesPageScanLimit; scan++ {
		page, err := h.reader.ListTaskActivityInventory(ctx, domaintask.TaskActivityInventoryFilter{
			BeforeTaskID: before, Status: query.Status, ExcludeStatus: query.ExcludeStatus, Assignee: query.Assignee,
			Category: query.Category, Limit: 200,
		}, h.authorizeTaskRead)
		if err != nil {
			return nil, "", err
		}
		if page.Items == nil {
			return nil, "", errors.New("Task activity inventory returned nil items")
		}
		priorID := before
		for _, item := range page.Items {
			if item.TaskID == "" || (priorID != "" && item.TaskID >= priorID) {
				return nil, "", errors.New("Task activity inventory order is invalid")
			}
			priorID = item.TaskID
			if currentRow, isCurrent := current[item.TaskID]; isCurrent {
				if query.View == "history" {
					continue
				}
				if _, alreadyAdded := seen[item.TaskID]; alreadyAdded {
					continue
				}
				if currentTaskActivityMatches(currentRow, query) {
					items = append(items, currentRow.Task)
					seen[item.TaskID] = struct{}{}
				}
				continue
			}
			items = append(items, item)
			seen[item.TaskID] = struct{}{}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].TaskID > items[j].TaskID })
		if page.NextCursor != "" {
			if err := page.NextCursor.Validate(); err != nil || (before != "" && page.NextCursor >= before) || (priorID != "" && page.NextCursor > priorID) {
				return nil, "", errors.New("Task activity inventory cursor did not progress")
			}
		}
		if page.NextCursor == "" {
			if len(items) > query.Limit {
				items = items[:query.Limit]
				return items, string(items[len(items)-1].TaskID), nil
			}
			return items, "", nil
		}
		if len(items) >= query.Limit && page.NextCursor <= items[query.Limit-1].TaskID {
			items = items[:query.Limit]
			return items, string(items[len(items)-1].TaskID), nil
		}
		before = page.NextCursor
	}
	// The source scan is bounded. Rows older than the scan frontier must remain
	// for the next request; emitting them now with a newer cursor would skip them
	// on continuation.
	frontier := before
	eligible := items[:0]
	for _, item := range items {
		if frontier == "" || item.TaskID >= frontier {
			eligible = append(eligible, item)
		}
	}
	if len(eligible) > query.Limit {
		eligible = eligible[:query.Limit]
	}
	return eligible, string(frontier), nil
}

func taskActivitiesCurrentSnapshot(current map[string]domaintask.CurrentActivitySummary) ([]taskActivitiesCurrentDTO, map[modulecore.TaskID]taskActivityCurrentListRow, bool) {
	if len(current) != len(taskActivityActorIDs) {
		return nil, nil, false
	}
	result := make([]taskActivitiesCurrentDTO, 0, len(taskActivityActorIDs))
	rows := make(map[modulecore.TaskID]taskActivityCurrentListRow, len(current))
	for _, actorID := range taskActivityActorIDs {
		item, ok := current[actorID]
		if !ok || item.TaskID == "" || item.Revision == 0 || item.Title == "" || item.Activity == nil {
			return nil, nil, false
		}
		activity, ok := FormatPublicCurrentActivity(string(item.Activity.Kind), item.Activity.Label, item.Activity.Phase, actorID, item.Activity.Revision)
		if !ok {
			return nil, nil, false
		}
		task, ok := taskActivitiesPublicRow(item.TaskActivityInventoryRow, actorID)
		if !ok {
			return nil, nil, false
		}
		result = append(result, taskActivitiesCurrentDTO{ActorID: actorID, TaskID: item.TaskID, Revision: item.Revision, CurrentActivity: activity, Task: task})
		currentRow, exists := rows[item.TaskID]
		if !exists {
			currentRow = taskActivityCurrentListRow{Task: item.TaskActivityInventoryRow, ActorIDs: make(map[string]struct{})}
		}
		currentRow.ActorIDs[actorID] = struct{}{}
		rows[item.TaskID] = currentRow
	}
	return result, rows, true
}

func currentTaskActivityMatches(row taskActivityCurrentListRow, query taskActivitiesQuery) bool {
	if query.Status != "" && row.Task.Status != query.Status {
		return false
	}
	if query.ExcludeStatus != "" && row.Task.Status == query.ExcludeStatus {
		return false
	}
	if query.Category != "" && (row.Task.Activity == nil || row.Task.Activity.Kind != query.Category) {
		return false
	}
	if query.Assignee != "" {
		if query.Assignee == row.Task.Assignee {
			return true
		}
		if _, ok := row.ActorIDs[query.Assignee]; !ok {
			return false
		}
	}
	return true
}

func taskActivitiesPublicRows(rows []domaintask.TaskActivityInventoryRow) ([]domaintask.TaskActivityInventoryRow, bool) {
	for index := range rows {
		var ok bool
		rows[index], ok = taskActivitiesPublicRow(rows[index], rows[index].Assignee)
		if !ok {
			return nil, false
		}
	}
	return rows, true
}

func taskActivitiesPublicRow(row domaintask.TaskActivityInventoryRow, actorID string) (domaintask.TaskActivityInventoryRow, bool) {
	if row.Activity == nil {
		return row, true
	}
	formatted, ok := FormatPublicCurrentActivity(string(row.Activity.Kind), row.Activity.Label, row.Activity.Phase, actorID, row.Activity.Revision)
	if !ok {
		return domaintask.TaskActivityInventoryRow{}, false
	}
	row.Activity = &domaintask.TaskActivityInventoryActivity{
		Kind: row.Activity.Kind, Label: formatted.Label, Phase: formatted.Phase, Revision: formatted.Revision,
	}
	return row, true
}

func currentTaskActivityItems(current map[string]domaintask.CurrentActivitySummary, query taskActivitiesQuery) []domaintask.TaskActivityInventoryRow {
	items := make([]domaintask.TaskActivityInventoryRow, 0, len(current))
	seen := make(map[modulecore.TaskID]struct{}, len(current))
	for _, actorID := range taskActivityActorIDs {
		item := current[actorID]
		if _, exists := seen[item.TaskID]; exists {
			continue
		}
		if query.Assignee != "" && query.Assignee != actorID && query.Assignee != item.Assignee {
			continue
		}
		if query.Status != "" && item.Status != query.Status || query.ExcludeStatus != "" && item.Status == query.ExcludeStatus || query.Category != "" && (item.Activity == nil || item.Activity.Kind != query.Category) {
			continue
		}
		items = append(items, item.TaskActivityInventoryRow)
		seen[item.TaskID] = struct{}{}
	}
	return items
}

func (h *taskActivitiesHandler) authorizeTaskRead(ctx context.Context, input domaintask.TaskReadAuthorizationInput) error {
	if input.TaskID == "" {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	// Legacy Tasks without publication provenance retain their existing owner policy.
	if input.OriginPublication != nil {
		if err := h.authorizeTaskPublication(ctx, input); err != nil {
			return domaintask.ErrPrivateTaskAccessForbidden
		}
	}
	if input.Scope == domaintask.TaskAccessScopeUser && (input.AuthenticatedUserID == "" || input.AuthenticatedUserID != h.userID) {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if input.GoalID == "" && input.WorkstreamID == "" {
		if input.Scope == "" || input.Scope == domaintask.TaskAccessScopeUser {
			return nil
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if input.GoalID == "" || input.WorkstreamID == "" || h.goals == nil {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	item, err := h.goals.Get(ctx, string(input.GoalID))
	if err != nil || item.GoalID != string(input.GoalID) || item.WorkstreamID != string(input.WorkstreamID) || item.TaskID != string(input.TaskID) {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if item.DataScope == tool.DataScopeUser {
		if input.Scope != domaintask.TaskAccessScopeUser || input.AuthenticatedUserID != h.userID {
			return domaintask.ErrPrivateTaskAccessForbidden
		}
	} else if item.DataScope != tool.DataScopePublic || input.Scope != "" {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	return nil
}

func (h *taskActivitiesHandler) authorizeTaskPublication(ctx context.Context, input domaintask.TaskReadAuthorizationInput) error {
	proof := input.OriginPublication
	if proof == nil || input.OriginSessionID.Validate() != nil || h.publications == nil || h.userID == "" {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if err := proof.Validate(input.OriginSessionID); err != nil {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if proof.Audience == conversation.SessionAudiencePrivate && proof.OwnerID != h.userID {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	current, err := h.publications.CurrentPublicationBinding(ctx, string(input.OriginSessionID))
	if err != nil || current.Validate() != nil {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	address := current.ChannelAddress()
	if current.SessionID() != input.OriginSessionID || current.PublicationIncarnation() != proof.PublicationIncarnation ||
		address.ChannelType() != proof.Channel || address.ExternalConversationID() != proof.ChannelAddress ||
		current.Audience() != proof.Audience || current.OwnerID() != proof.OwnerID {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	return nil
}

func taskActivityActorAllowed(id string) bool {
	for _, allowed := range taskActivityActorIDs {
		if id == allowed {
			return true
		}
	}
	return false
}

func taskActivitiesCursorKey(token []byte) []byte {
	hash := sha256.Sum256(append([]byte("RenCrow_BOARD_task-activities_cursor_v1\x00"), token...))
	return hash[:]
}

func encodeTaskActivitiesCursor(taskID string, token []byte, query taskActivitiesQuery, userID string) (string, error) {
	if err := modulecore.TaskID(taskID).Validate(); err != nil {
		return "", err
	}
	plain, err := json.Marshal(taskActivitiesCursor{Version: 1, TaskID: taskID, UserID: userID,
		View: query.View, Assignee: query.Assignee, Status: string(query.Status), Category: string(query.Category), Limit: query.Limit})
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(taskActivitiesCursorKey(token))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(taskActivitiesPath))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decodeTaskActivitiesCursor(value string, token []byte, query taskActivitiesQuery, userID string) (string, error) {
	if len(value) == 0 || len(value) > maxTaskActivitiesCursor {
		return "", errors.New("invalid task activity cursor")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("invalid task activity cursor")
	}
	block, err := aes.NewCipher(taskActivitiesCursorKey(token))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", errors.New("invalid task activity cursor")
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(taskActivitiesPath))
	if err != nil {
		return "", errors.New("invalid task activity cursor")
	}
	var cursor taskActivitiesCursor
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.UserID != userID ||
		cursor.View != query.View || cursor.Assignee != query.Assignee || cursor.Status != string(query.Status) || cursor.Category != string(query.Category) || cursor.Limit != query.Limit {
		return "", errors.New("invalid task activity cursor")
	}
	if err := modulecore.TaskID(cursor.TaskID).Validate(); err != nil {
		return "", fmt.Errorf("invalid task activity cursor: %w", err)
	}
	return cursor.TaskID, nil
}
