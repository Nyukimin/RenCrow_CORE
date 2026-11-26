package viewer

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
)

const systemJobsPath = "/viewer/board/system-jobs"

// SystemJobsMaxSourceIssues is the complete safe issue-code vocabulary size.
const SystemJobsMaxSourceIssues = 16

var systemJobLabelPattern = regexp.MustCompile(`(?i)^com\.rencrow\.[A-Za-z0-9][A-Za-z0-9._-]{0,242}$`)

// SystemJobsReader returns a fresh, read-only projection of host scheduling
// definitions and runtime observations.
type SystemJobsReader interface {
	ReadSystemJobs(context.Context) (SystemJobsSnapshot, error)
}

type SystemJobsSnapshot struct {
	ObservedAt   time.Time          `json:"observed_at"`
	Availability string             `json:"availability"`
	Sources      []SystemJobsSource `json:"sources"`
	Records      []SystemJobRecord  `json:"records"`
}

type SystemJobsSource struct {
	ID         string   `json:"id"`
	Domain     string   `json:"domain"`
	State      string   `json:"state"`
	Candidates int      `json:"candidates"`
	Records    int      `json:"records"`
	Truncated  bool     `json:"truncated"`
	Issues     []string `json:"issues"`
}

type SystemJobRecord struct {
	ID                  string                        `json:"id"`
	Domain              string                        `json:"domain"`
	Label               string                        `json:"label"`
	Title               string                        `json:"title"`
	RecordType          string                        `json:"record_type"`
	Source              string                        `json:"source"`
	SourceID            string                        `json:"source_id"`
	DefinitionState     string                        `json:"definition_state"`
	Schedule            SystemJobSchedule             `json:"schedule"`
	DisabledState       string                        `json:"disabled_state"`
	DisabledReason      string                        `json:"disabled_reason,omitempty"`
	LoadedState         string                        `json:"loaded_state"`
	RuntimeState        string                        `json:"runtime_state"`
	RuntimeDomains      []string                      `json:"runtime_domains,omitempty"`
	RuntimeObservations []SystemJobRuntimeObservation `json:"runtime_observations,omitempty"`
	KeepAlive           bool                          `json:"keep_alive,omitempty"`
}

type SystemJobRuntimeObservation struct {
	Domain string `json:"domain"`
	State  string `json:"state"`
}

type SystemJobSchedule struct {
	Kind            string                      `json:"kind"`
	IntervalSeconds *int64                      `json:"interval_seconds,omitempty"`
	CalendarEntries []SystemJobCalendarSchedule `json:"calendar_entries,omitempty"`
	EventTrigger    bool                        `json:"event_trigger,omitempty"`
}

type SystemJobCalendarSchedule struct {
	Minute  *int `json:"minute,omitempty"`
	Hour    *int `json:"hour,omitempty"`
	Day     *int `json:"day,omitempty"`
	Weekday *int `json:"weekday,omitempty"`
	Month   *int `json:"month,omitempty"`
	Year    *int `json:"year,omitempty"`
}

type systemJobsHandler struct {
	reader SystemJobsReader
	userID string
	token  []byte
}

// NewSystemJobsHandler creates the owner-authenticated read-only BOARD endpoint.
func NewSystemJobsHandler(reader SystemJobsReader, userID string, token []byte) http.HandlerFunc {
	return (&systemJobsHandler{
		reader: reader,
		userID: strings.TrimSpace(userID),
		token:  append([]byte(nil), token...),
	}).ServeHTTP
}

func (h *systemJobsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r == nil || r.URL == nil || !isDirectLoopbackSystemJobsRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeMemoryOwnerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.Path != systemJobsPath || r.URL.RawQuery != "" {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if h == nil || h.userID == "" || len(h.token) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "system_jobs_auth_unavailable")
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
	if h.reader == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "system_jobs_unavailable")
		return
	}
	snapshot, err := h.reader.ReadSystemJobs(r.Context())
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "system_jobs_unavailable")
		return
	}
	projected, err := normalizeSystemJobsSnapshot(snapshot)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "system_jobs_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, projected)
}

func isDirectLoopbackSystemJobsRequest(r *http.Request) bool {
	return r != nil && r.URL != nil && r.URL.Path == systemJobsPath && security.IsDirectLoopbackRequest(r)
}

func normalizeSystemJobsSnapshot(snapshot SystemJobsSnapshot) (SystemJobsSnapshot, error) {
	if snapshot.ObservedAt.IsZero() || !validSystemJobsAvailability(snapshot.Availability) ||
		len(snapshot.Sources) == 0 || len(snapshot.Sources) > 4 || len(snapshot.Records) > 384 {
		return SystemJobsSnapshot{}, errors.New("invalid system jobs snapshot")
	}
	if snapshot.Records == nil {
		snapshot.Records = []SystemJobRecord{}
	}
	sourcesByID := make(map[string]*SystemJobsSource, len(snapshot.Sources))
	for i := range snapshot.Sources {
		source := &snapshot.Sources[i]
		if !validSystemJobsSourceID(source.ID) || !validSystemJobsDomain(source.Domain) ||
			!validSystemJobsSourceDomain(source.ID, source.Domain) ||
			!validSystemJobsSourceState(source.State) || source.Candidates < 0 || source.Records < 0 ||
			source.Records > 128 || source.Records > source.Candidates || len(source.Issues) > 64 {
			return SystemJobsSnapshot{}, errors.New("invalid system jobs source")
		}
		if _, duplicate := sourcesByID[source.ID]; duplicate {
			return SystemJobsSnapshot{}, errors.New("duplicate system jobs source")
		}
		sourcesByID[source.ID] = source
		uniqueIssues := make([]string, 0, len(source.Issues))
		seenIssues := make(map[string]struct{}, len(source.Issues))
		for _, issue := range source.Issues {
			if !validSystemJobsIssue(issue) {
				return SystemJobsSnapshot{}, errors.New("invalid system jobs source issue")
			}
			if _, duplicate := seenIssues[issue]; duplicate {
				continue
			}
			seenIssues[issue] = struct{}{}
			uniqueIssues = append(uniqueIssues, issue)
		}
		if len(uniqueIssues) > SystemJobsMaxSourceIssues {
			return SystemJobsSnapshot{}, errors.New("too many system jobs source issues")
		}
		sort.Strings(uniqueIssues)
		source.Issues = uniqueIssues
		if source.State == "available" && (source.Truncated || len(source.Issues) > 0) {
			source.State = "partial"
		}
		hasScanLimitIssue := hasSystemJobsIssue(source.Issues, "entry_limit_reached") || hasSystemJobsIssue(source.Issues, "scan_timeout")
		if source.Truncated != hasScanLimitIssue {
			return SystemJobsSnapshot{}, errors.New("system jobs source truncation does not match its issues")
		}
		if source.State == "partial" || source.State == "unavailable" {
			snapshot.Availability = "partial"
		}
	}
	actualRecordsBySource := make(map[string]int, len(snapshot.Sources))
	duplicateRecordCounts := make(map[string]int)
	for _, record := range snapshot.Records {
		duplicateRecordCounts[systemJobRecordID(record.Domain, record.SourceID, record.Label)]++
	}
	duplicateRecordIndexes := make(map[string]int)
	for i := range snapshot.Records {
		record := &snapshot.Records[i]
		if !systemJobLabelPattern.MatchString(record.Label) || !validSystemJobsDomain(record.Domain) ||
			record.Source != "launchd_plist" || !validSystemJobsSourceID(record.SourceID) || !validSystemJobsDefinitionState(record.DefinitionState) ||
			!validSystemJobsSchedule(record.Schedule) || !validSystemJobsState(record.DisabledState, "enabled", "disabled") ||
			!validSystemJobsDisabledReason(record.DisabledState, record.DisabledReason) ||
			!validSystemJobsState(record.LoadedState, "loaded", "unloaded") ||
			!validSystemJobsState(record.RuntimeState, "running", "not_running") || !validSystemJobsRuntimeDomains(record) ||
			!validSystemJobsRuntimeObservations(record) {
			return SystemJobsSnapshot{}, errors.New("invalid system job record")
		}
		source, exists := sourcesByID[record.SourceID]
		if !exists || source.Domain != record.Domain {
			return SystemJobsSnapshot{}, errors.New("system job does not match an observed source")
		}
		actualRecordsBySource[record.SourceID]++
		baseID := systemJobRecordID(record.Domain, record.SourceID, record.Label)
		if duplicateRecordCounts[baseID] > 1 {
			duplicateRecordIndexes[baseID]++
			record.ID = baseID + "|duplicate-" + strconv.Itoa(duplicateRecordIndexes[baseID])
		} else {
			record.ID = baseID
		}
		if record.DefinitionState != "observed" || record.Schedule.Kind != "none" {
			record.KeepAlive = false
		}
		record.Title, record.RecordType = knownSystemJobPresentation(record.Label)
		if record.RecordType == "unknown" && record.KeepAlive {
			record.RecordType = "resident_service"
		}
	}
	for _, source := range snapshot.Sources {
		if actualRecordsBySource[source.ID] != source.Records {
			return SystemJobsSnapshot{}, errors.New("system jobs source record count does not match projection")
		}
	}
	if snapshot.Availability == "unsupported" && len(snapshot.Records) != 0 {
		return SystemJobsSnapshot{}, errors.New("unsupported system jobs snapshot contains records")
	}
	return snapshot, nil
}

func validSystemJobsAvailability(value string) bool {
	return value == "available" || value == "partial" || value == "unsupported"
}

func validSystemJobsSourceID(value string) bool {
	switch value {
	case "system_launch_daemons", "shared_launch_agents", "user_launch_agents", "user_rencrow_launchd", "launchd_unavailable":
		return true
	default:
		return false
	}
}

func validSystemJobsDomain(value string) bool {
	return value == "system" || systemJobsUserDomainPattern.MatchString(value)
}

func validSystemJobsRuntimeDomains(record *SystemJobRecord) bool {
	if record == nil || len(record.RuntimeDomains) > 2 {
		return false
	}
	seen := make(map[string]struct{}, len(record.RuntimeDomains))
	for _, domain := range record.RuntimeDomains {
		valid := record.Domain == "system" && domain == "system"
		if systemJobsUserDomainPattern.MatchString(record.Domain) {
			uid := strings.TrimPrefix(record.Domain, "user/")
			valid = domain == record.Domain || domain == "gui/"+uid
		}
		if !valid {
			return false
		}
		if _, duplicate := seen[domain]; duplicate {
			return false
		}
		seen[domain] = struct{}{}
	}
	if (record.LoadedState == "loaded") != (len(record.RuntimeDomains) > 0) {
		return false
	}
	if record.RuntimeState == "running" && len(record.RuntimeDomains) == 0 {
		return false
	}
	sort.Strings(record.RuntimeDomains)
	return true
}

func validSystemJobsSourceDomain(sourceID, domain string) bool {
	switch sourceID {
	case "system_launch_daemons", "launchd_unavailable":
		return domain == "system"
	case "shared_launch_agents", "user_launch_agents", "user_rencrow_launchd":
		return systemJobsUserDomainPattern.MatchString(domain)
	default:
		return false
	}
}

func hasSystemJobsIssue(issues []string, expected string) bool {
	for _, issue := range issues {
		if issue == expected {
			return true
		}
	}
	return false
}

var systemJobsUserDomainPattern = regexp.MustCompile(`^user/[0-9]{1,10}$`)

func validSystemJobsSourceState(value string) bool {
	switch value {
	case "available", "partial", "unavailable", "unsupported":
		return true
	default:
		return false
	}
}

func validSystemJobsIssue(value string) bool {
	switch value {
	case "root_unavailable", "root_unsafe", "entry_limit_reached", "file_unreadable", "file_too_large",
		"plist_malformed", "plist_unsupported", "filename_label_mismatch", "disabled_state_unavailable", "disabled_state_not_reported",
		"runtime_state_unavailable", "runtime_observation_timeout", "scan_timeout", "plist_conversion_unavailable", "platform_unsupported",
		"duplicate_label":
		return true
	default:
		return false
	}
}

func validSystemJobsDisabledReason(state, reason string) bool {
	if reason == "" {
		return true
	}
	if state != "unknown" {
		return false
	}
	return reason == "not_reported" || reason == "unavailable"
}

func validSystemJobsRuntimeObservations(record *SystemJobRecord) bool {
	if record == nil || len(record.RuntimeObservations) > 2 {
		return false
	}
	seen := make(map[string]struct{}, len(record.RuntimeObservations))
	uid := strings.TrimPrefix(record.Domain, "user/")
	for _, observation := range record.RuntimeObservations {
		validDomain := record.Domain == "system" && observation.Domain == "system"
		if systemJobsUserDomainPattern.MatchString(record.Domain) {
			validDomain = observation.Domain == record.Domain || observation.Domain == "gui/"+uid
		}
		if !validDomain {
			return false
		}
		switch observation.State {
		case "running", "not_running", "unloaded", "different_definition", "unavailable":
		default:
			return false
		}
		if _, duplicate := seen[observation.Domain]; duplicate {
			return false
		}
		seen[observation.Domain] = struct{}{}
	}
	sort.Slice(record.RuntimeObservations, func(i, j int) bool {
		return record.RuntimeObservations[i].Domain < record.RuntimeObservations[j].Domain
	})
	return true
}

func validSystemJobsDefinitionState(value string) bool {
	switch value {
	case "observed", "unreadable", "malformed", "unsupported":
		return true
	default:
		return false
	}
}

func validSystemJobsSchedule(schedule SystemJobSchedule) bool {
	switch schedule.Kind {
	case "interval", "calendar", "combined", "event", "none", "unknown":
	default:
		return false
	}
	if schedule.IntervalSeconds != nil && (*schedule.IntervalSeconds < 1 || *schedule.IntervalSeconds > 31536000) {
		return false
	}
	if len(schedule.CalendarEntries) > 32 {
		return false
	}
	for _, entry := range schedule.CalendarEntries {
		if !validOptionalInt(entry.Minute, 0, 59) || !validOptionalInt(entry.Hour, 0, 23) ||
			!validOptionalInt(entry.Day, 1, 31) || !validOptionalInt(entry.Weekday, 0, 7) ||
			!validOptionalInt(entry.Month, 1, 12) || !validOptionalInt(entry.Year, 1, 9999) {
			return false
		}
	}
	hasInterval := schedule.IntervalSeconds != nil
	hasCalendar := len(schedule.CalendarEntries) > 0
	components := 0
	if hasInterval {
		components++
	}
	if hasCalendar {
		components++
	}
	if schedule.EventTrigger {
		components++
	}
	switch schedule.Kind {
	case "interval":
		return hasInterval && !hasCalendar && !schedule.EventTrigger
	case "calendar":
		return !hasInterval && hasCalendar && !schedule.EventTrigger
	case "event":
		return !hasInterval && !hasCalendar && schedule.EventTrigger
	case "combined":
		return components >= 2
	case "none":
		return components == 0
	case "unknown":
		return components == 0
	default:
		return false
	}
}

func validSystemJobsState(value string, known ...string) bool {
	if value == "unknown" {
		return true
	}
	for _, candidate := range known {
		if value == candidate {
			return true
		}
	}
	return false
}

func validOptionalInt(value *int, minimum, maximum int) bool {
	return value == nil || (*value >= minimum && *value <= maximum)
}

func knownSystemJobPresentation(label string) (string, string) {
	switch label {
	case "com.rencrow.lyrics-collector":
		return "歌詞収集", "job"
	case "com.rencrow.fxembed-daily":
		return "FxEmbedの取得確認", "job"
	case "com.rencrow.comfyui.housekeeping":
		return "ComfyUI保守", "job"
	case "com.rencrow.log-rotate-guard":
		return "ログ整理", "job"
	case "com.rencrow.resilience-stack-monitor", "com.rencrow.ren2-supervisor":
		return "監視・起動基盤", "execution_mechanism"
	default:
		return label, "unknown"
	}
}

func systemJobRecordID(domain, source, label string) string {
	return domain + "|" + source + "|" + label
}
