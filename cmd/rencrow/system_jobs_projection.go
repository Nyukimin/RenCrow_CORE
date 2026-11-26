package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

const (
	systemJobsMaxDirectoryEntries = 512
	systemJobsMaxPlistsPerRoot    = 64
	systemJobsMaxPlistBytes       = 64 * 1024
	systemJobsMaxPlistJSONBytes   = 128 * 1024
	systemJobsMaxCalendarEntries  = 32
	systemJobsSnapshotTimeout     = 5 * time.Second
)

var (
	systemJobsLabelPattern    = regexp.MustCompile(`(?i)^com\.rencrow\.[A-Za-z0-9][A-Za-z0-9._-]{0,242}$`)
	systemJobsRenCrowPrefix   = "com.rencrow."
	errSystemJobsFileTooLarge = errors.New("launchd plist exceeds inspection bound")
	errSystemJobsUnsafeFile   = errors.New("launchd plist is not a regular non-symlink file")
)

type systemJobsRoot struct {
	id     string
	domain string
	path   string
}

type systemJobsRuntimeObservation struct {
	known  bool
	loaded bool
	path   string
	state  string
	pid    int
}

type systemJobsObserver interface {
	ReadDisabledStates(context.Context, string, []string) (map[string]string, error)
	Inspect(context.Context, string, string) (systemJobsRuntimeObservation, error)
}

type systemJobsPlistConverter func(context.Context, []byte) ([]byte, error)
type systemJobsFileReader func(string) ([]byte, error)

type systemJobPlist struct {
	label           string
	schedule        viewer.SystemJobSchedule
	keepAlive       bool
	malformedTiming bool
}

type systemJobCandidate struct {
	root       systemJobsRoot
	path       string
	label      string
	definition string
	schedule   viewer.SystemJobSchedule
	keepAlive  bool
}

func collectSystemJobs(
	ctx context.Context,
	roots []systemJobsRoot,
	reader systemJobsFileReader,
	converter systemJobsPlistConverter,
	observer systemJobsObserver,
) viewer.SystemJobsSnapshot {
	observedAt := time.Now().UTC()
	snapshot := viewer.SystemJobsSnapshot{
		ObservedAt:   observedAt,
		Availability: "available",
		Sources:      make([]viewer.SystemJobsSource, 0, len(roots)),
		Records:      make([]viewer.SystemJobRecord, 0),
	}
	if reader == nil {
		reader = readBoundedSystemJobsPlist
	}
	candidates := make([]systemJobCandidate, 0)
	for _, root := range roots {
		source := viewer.SystemJobsSource{ID: root.id, Domain: root.domain, State: "available", Issues: []string{}}
		if context.Cause(ctx) != nil {
			source.State = "partial"
			source.Truncated = true
			addSystemJobsIssue(&source, "scan_timeout")
			snapshot.Sources = append(snapshot.Sources, source)
			continue
		}
		if root.path == "" {
			source.State = "unavailable"
			addSystemJobsIssue(&source, "root_unavailable")
			snapshot.Sources = append(snapshot.Sources, source)
			continue
		}
		entries, truncated, err := readBoundedSystemJobsDirectory(root.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				source.State = "unavailable"
				issue := "root_unavailable"
				if errors.Is(err, errSystemJobsUnsafeFile) {
					issue = "root_unsafe"
				}
				addSystemJobsIssue(&source, issue)
			}
			snapshot.Sources = append(snapshot.Sources, source)
			continue
		}
		if truncated {
			source.State = "partial"
			source.Truncated = true
			addSystemJobsIssue(&source, "entry_limit_reached")
		}
		plistEntries := make([]os.DirEntry, 0)
		for _, entry := range entries {
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".plist") && !entry.IsDir() {
				plistEntries = append(plistEntries, entry)
			}
		}
		source.Candidates = len(plistEntries)
		sort.Slice(plistEntries, func(i, j int) bool {
			iRenCrow := isRenCrowPlistFilename(plistEntries[i].Name())
			jRenCrow := isRenCrowPlistFilename(plistEntries[j].Name())
			if iRenCrow != jRenCrow {
				return iRenCrow
			}
			return plistEntries[i].Name() < plistEntries[j].Name()
		})
		if len(plistEntries) > systemJobsMaxPlistsPerRoot {
			plistEntries = plistEntries[:systemJobsMaxPlistsPerRoot]
			source.State = "partial"
			source.Truncated = true
			addSystemJobsIssue(&source, "entry_limit_reached")
		}
		for _, entry := range plistEntries {
			if context.Cause(ctx) != nil {
				source.State = "partial"
				source.Truncated = true
				addSystemJobsIssue(&source, "scan_timeout")
				break
			}
			path := filepath.Join(root.path, entry.Name())
			filenameLabel := systemJobsLabelFromFilename(entry.Name())
			data, readErr := reader(path)
			if readErr != nil {
				source.State = "partial"
				if errors.Is(readErr, errSystemJobsFileTooLarge) {
					addSystemJobsIssue(&source, "file_too_large")
					if filenameLabel != "" {
						candidates = append(candidates, unknownSystemJobCandidate(root, path, filenameLabel, "unsupported"))
					}
					continue
				}
				addSystemJobsIssue(&source, "file_unreadable")
				if filenameLabel != "" {
					candidates = append(candidates, unknownSystemJobCandidate(root, path, filenameLabel, "unreadable"))
				}
				continue
			}
			plist, parseErr := decodeSystemJobPlist(ctx, data, converter)
			if parseErr != nil {
				source.State = "partial"
				if context.Cause(ctx) != nil {
					source.Truncated = true
					addSystemJobsIssue(&source, "scan_timeout")
					if filenameLabel != "" {
						candidates = append(candidates, unknownSystemJobCandidate(root, path, filenameLabel, "unsupported"))
					}
					break
				}
				definitionState := "malformed"
				if errors.Is(parseErr, errSystemJobsUnsupportedPlist) {
					definitionState = "unsupported"
					addSystemJobsIssue(&source, "plist_unsupported")
					if errors.Is(parseErr, errSystemJobsPlistConversion) {
						addSystemJobsIssue(&source, "plist_conversion_unavailable")
					}
				} else {
					addSystemJobsIssue(&source, "plist_malformed")
				}
				if filenameLabel != "" {
					candidates = append(candidates, unknownSystemJobCandidate(root, path, filenameLabel, definitionState))
				}
				continue
			}
			if !systemJobsLabelPattern.MatchString(plist.label) {
				if filenameLabel != "" {
					source.State = "partial"
					addSystemJobsIssue(&source, "filename_label_mismatch")
					candidates = append(candidates, unknownSystemJobCandidate(root, path, filenameLabel, "malformed"))
				} else if hasSystemJobsRenCrowPrefix(plist.label) {
					source.State = "partial"
					addSystemJobsIssue(&source, "plist_unsupported")
				}
				continue
			}
			if filenameLabel != plist.label {
				source.State = "partial"
				addSystemJobsIssue(&source, "filename_label_mismatch")
			}
			if plist.malformedTiming {
				source.State = "partial"
				addSystemJobsIssue(&source, "plist_malformed")
			}
			candidates = append(candidates, systemJobCandidate{
				root: root, path: path, label: plist.label, definition: "observed", schedule: plist.schedule, keepAlive: plist.keepAlive,
			})
		}
		snapshot.Sources = append(snapshot.Sources, source)
	}

	markDuplicateSystemJobDefinitions(&snapshot, candidates)
	applySystemJobsRuntimeObservations(ctx, &snapshot, candidates, observer)
	sort.SliceStable(snapshot.Records, func(i, j int) bool {
		a, b := snapshot.Records[i], snapshot.Records[j]
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.SourceID < b.SourceID
	})
	for _, source := range snapshot.Sources {
		if source.State != "available" {
			snapshot.Availability = "partial"
		}
	}
	return snapshot
}

func markDuplicateSystemJobDefinitions(snapshot *viewer.SystemJobsSnapshot, candidates []systemJobCandidate) {
	counts := make(map[string]int)
	for _, candidate := range candidates {
		counts[systemJobDefinitionKey(candidate.root.domain, candidate.root.id, candidate.label)]++
	}
	marked := make(map[string]struct{})
	for _, candidate := range candidates {
		key := systemJobDefinitionKey(candidate.root.domain, candidate.root.id, candidate.label)
		if counts[key] < 2 {
			continue
		}
		if _, exists := marked[key]; exists {
			continue
		}
		marked[key] = struct{}{}
		markSystemJobsSourcePartial(snapshot, candidate.root.id, "duplicate_label")
	}
}

func systemJobDefinitionKey(domain, sourceID, label string) string {
	return domain + "\x00" + sourceID + "\x00" + label
}

var errSystemJobsUnsupportedPlist = errors.New("property list conversion is unavailable")
var errSystemJobsPlistConversion = errors.New("property list conversion failed")
var errSystemJobsMalformedPlist = errors.New("property list is malformed")

func unknownSystemJobCandidate(root systemJobsRoot, path, label, definition string) systemJobCandidate {
	return systemJobCandidate{
		root: root, path: path, label: label, definition: definition,
		schedule: viewer.SystemJobSchedule{Kind: "unknown"},
	}
}

func applySystemJobsRuntimeObservations(ctx context.Context, snapshot *viewer.SystemJobsSnapshot, candidates []systemJobCandidate, observer systemJobsObserver) {
	byDomain := make(map[string][]string)
	for _, candidate := range candidates {
		labels := byDomain[candidate.root.domain]
		found := false
		for _, label := range labels {
			if label == candidate.label {
				found = true
				break
			}
		}
		if !found {
			byDomain[candidate.root.domain] = append(labels, candidate.label)
		}
	}
	disabledByDomain := make(map[string]map[string]string, len(byDomain))
	for domain, labels := range byDomain {
		sort.Strings(labels)
		if observer == nil {
			continue
		}
		states, err := observer.ReadDisabledStates(ctx, domain, labels)
		if err != nil {
			markSystemJobsDomainPartial(snapshot, domain, "disabled_state_unavailable")
			continue
		}
		disabledByDomain[domain] = states
	}
	observations := make(map[string]systemJobsRuntimeObservation)
	observationErrors := make(map[string]error)
	runtimeDomains := func(domain string) []string {
		if strings.HasPrefix(domain, "user/") {
			uid := strings.TrimPrefix(domain, "user/")
			return []string{domain, "gui/" + uid}
		}
		return []string{domain}
	}
	for _, candidate := range candidates {
		for _, domain := range runtimeDomains(candidate.root.domain) {
			key := systemJobsObservationKey(domain, candidate.label)
			if _, seen := observations[key]; seen {
				continue
			}
			if _, seen := observationErrors[key]; seen {
				continue
			}
			if observer == nil {
				observationErrors[key] = errors.New("launchd observer unavailable")
				continue
			}
			if err := context.Cause(ctx); err != nil {
				observationErrors[key] = err
				continue
			}
			observation, err := observer.Inspect(ctx, domain, candidate.label)
			if err != nil {
				observationErrors[key] = err
				continue
			}
			observations[key] = observation
		}
	}
	for _, candidate := range candidates {
		record := viewer.SystemJobRecord{
			Domain: candidate.root.domain, Label: candidate.label, Source: "launchd_plist", SourceID: candidate.root.id,
			DefinitionState: candidate.definition, Schedule: candidate.schedule,
			DisabledState: "unknown", LoadedState: "unknown", RuntimeState: "unknown",
			KeepAlive: candidate.keepAlive && candidate.definition == "observed" && candidate.schedule.Kind == "none",
		}
		knownUnloaded := true
		observationUnknown := false
		for _, domain := range runtimeDomains(candidate.root.domain) {
			key := systemJobsObservationKey(domain, candidate.label)
			observation, observed := observations[key]
			if err := observationErrors[key]; err != nil || !observed || !observation.known {
				observationUnknown = true
				knownUnloaded = false
				record.RuntimeObservations = append(record.RuntimeObservations, viewer.SystemJobRuntimeObservation{Domain: domain, State: "unavailable"})
				if err := observationErrors[key]; err != nil {
					issue := "runtime_state_unavailable"
					if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
						issue = "runtime_observation_timeout"
					}
					markSystemJobsSourcePartial(snapshot, candidate.root.id, issue)
				}
				continue
			}
			if !observation.loaded {
				record.RuntimeObservations = append(record.RuntimeObservations, viewer.SystemJobRuntimeObservation{Domain: domain, State: "unloaded"})
				continue
			}
			if filepath.Clean(observation.path) != filepath.Clean(candidate.path) {
				observationUnknown = true
				record.RuntimeObservations = append(record.RuntimeObservations, viewer.SystemJobRuntimeObservation{Domain: domain, State: "different_definition"})
				continue
			}
			knownUnloaded = false
			record.RuntimeDomains = append(record.RuntimeDomains, domain)
			state := "not_running"
			switch observation.state {
			case "running":
				if observation.pid > 0 {
					state = "running"
				} else {
					state = "unavailable"
				}
			case "not_running", "waiting":
			default:
				state = "unavailable"
			}
			record.RuntimeObservations = append(record.RuntimeObservations, viewer.SystemJobRuntimeObservation{Domain: domain, State: state})
		}
		sort.Strings(record.RuntimeDomains)
		if states, domainObserved := disabledByDomain[candidate.root.domain]; domainObserved {
			if state, exists := states[candidate.label]; exists {
				if state == "enabled" || state == "disabled" {
					record.DisabledState = state
				} else {
					record.DisabledReason = "unavailable"
					markSystemJobsSourcePartial(snapshot, candidate.root.id, "disabled_state_unavailable")
				}
			} else {
				record.DisabledReason = "not_reported"
				markSystemJobsSourcePartial(snapshot, candidate.root.id, "disabled_state_not_reported")
			}
		} else {
			record.DisabledReason = "unavailable"
			markSystemJobsSourcePartial(snapshot, candidate.root.id, "disabled_state_unavailable")
		}
		if len(record.RuntimeDomains) > 0 {
			record.LoadedState = "loaded"
			running, stateKnown := false, true
			for _, domain := range record.RuntimeDomains {
				observation := observations[systemJobsObservationKey(domain, candidate.label)]
				if observation.state == "running" && observation.pid > 0 {
					running = true
				} else if observation.state != "not_running" && observation.state != "waiting" {
					stateKnown = false
				}
			}
			if running {
				record.RuntimeState = "running"
			} else if observationUnknown || !stateKnown {
				record.RuntimeState = "unknown"
				markSystemJobsSourcePartial(snapshot, candidate.root.id, "runtime_state_unavailable")
			} else {
				record.RuntimeState = "not_running"
			}
		} else if knownUnloaded {
			record.LoadedState = "unloaded"
			if observationUnknown {
				record.RuntimeState = "unknown"
			} else {
				record.RuntimeState = "not_running"
			}
		} else {
			record.LoadedState = "unknown"
			record.RuntimeState = "unknown"
			markSystemJobsSourcePartial(snapshot, candidate.root.id, "runtime_state_unavailable")
		}
		snapshot.Records = append(snapshot.Records, record)
		for i := range snapshot.Sources {
			if snapshot.Sources[i].ID == candidate.root.id {
				snapshot.Sources[i].Records++
				break
			}
		}
	}
}

func systemJobsObservationKey(domain, label string) string {
	return domain + "\x00" + label
}

func markSystemJobsDomainPartial(snapshot *viewer.SystemJobsSnapshot, domain, issue string) {
	for i := range snapshot.Sources {
		if snapshot.Sources[i].Domain == domain {
			markSystemJobsSourcePartial(snapshot, snapshot.Sources[i].ID, issue)
		}
	}
}

func markSystemJobsSourcePartial(snapshot *viewer.SystemJobsSnapshot, sourceID, issue string) {
	for i := range snapshot.Sources {
		if snapshot.Sources[i].ID == sourceID {
			source := &snapshot.Sources[i]
			if source.State == "available" {
				source.State = "partial"
			}
			addSystemJobsIssue(source, issue)
			break
		}
	}
	snapshot.Availability = "partial"
}

func addSystemJobsIssue(source *viewer.SystemJobsSource, issue string) {
	for _, existing := range source.Issues {
		if existing == issue {
			return
		}
	}
	if len(source.Issues) < viewer.SystemJobsMaxSourceIssues {
		source.Issues = append(source.Issues, issue)
		sort.Strings(source.Issues)
	}
}

func readBoundedSystemJobsDirectory(path string) ([]os.DirEntry, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, errSystemJobsUnsafeFile
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(systemJobsMaxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := len(entries) > systemJobsMaxDirectoryEntries
	if truncated {
		entries = entries[:systemJobsMaxDirectoryEntries]
	}
	return entries, truncated, nil
}

func readBoundedSystemJobsPlist(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errSystemJobsUnsafeFile
	}
	if info.Size() > systemJobsMaxPlistBytes {
		return nil, errSystemJobsFileTooLarge
	}
	file, err := openSystemJobsPlistFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, systemJobsMaxPlistBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > systemJobsMaxPlistBytes {
		return nil, errSystemJobsFileTooLarge
	}
	return data, nil
}

func decodeSystemJobPlist(ctx context.Context, data []byte, converter systemJobsPlistConverter) (systemJobPlist, error) {
	if converter == nil {
		return systemJobPlist{}, errSystemJobsUnsupportedPlist
	}
	converted, err := converter(ctx, data)
	if err != nil {
		if errors.Is(err, errSystemJobsMalformedPlist) {
			return systemJobPlist{}, errSystemJobsMalformedPlist
		}
		return systemJobPlist{}, fmt.Errorf("%w: %w", errSystemJobsUnsupportedPlist, errSystemJobsPlistConversion)
	}
	return parseSystemJobPlistJSON(converted)
}

type systemJobPlistJSON struct {
	Label                 json.RawMessage `json:"Label"`
	KeepAlive             json.RawMessage `json:"KeepAlive"`
	StartInterval         json.RawMessage `json:"StartInterval"`
	StartCalendarInterval json.RawMessage `json:"StartCalendarInterval"`
	WatchPaths            json.RawMessage `json:"WatchPaths"`
	QueueDirectories      json.RawMessage `json:"QueueDirectories"`
	StartOnMount          json.RawMessage `json:"StartOnMount"`
}

func parseSystemJobPlistJSON(data []byte) (systemJobPlist, error) {
	if len(data) == 0 || len(data) > systemJobsMaxPlistJSONBytes {
		return systemJobPlist{}, errors.New("converted launchd plist is outside its size bound")
	}
	var raw systemJobPlistJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return systemJobPlist{}, err
	}
	var label string
	if len(raw.Label) == 0 || json.Unmarshal(raw.Label, &label) != nil || label == "" {
		return systemJobPlist{}, errors.New("launchd plist label is missing or invalid")
	}
	schedule, malformedTiming := decodeSystemJobJSONSchedule(raw)
	var keepAlive bool
	if len(raw.KeepAlive) > 0 {
		var explicitBoolean bool
		if json.Unmarshal(raw.KeepAlive, &explicitBoolean) == nil {
			keepAlive = explicitBoolean
		}
	}
	return systemJobPlist{label: label, schedule: schedule, keepAlive: keepAlive, malformedTiming: malformedTiming}, nil
}

func decodeSystemJobJSONSchedule(raw systemJobPlistJSON) (viewer.SystemJobSchedule, bool) {
	hasInterval, hasCalendar := len(raw.StartInterval) > 0, len(raw.StartCalendarInterval) > 0
	hasEvent := false
	malformed := false
	var interval *int64
	var calendar []viewer.SystemJobCalendarSchedule
	if hasInterval {
		var value *int64
		if json.Unmarshal(raw.StartInterval, &value) != nil || value == nil || *value < 1 || *value > 31536000 {
			malformed = true
		} else {
			interval = value
		}
	}
	if hasCalendar {
		parsed, invalid := decodeSystemJobCalendarJSON(raw.StartCalendarInterval)
		calendar = parsed
		malformed = malformed || invalid
	}
	for _, eventValue := range []json.RawMessage{raw.WatchPaths, raw.QueueDirectories} {
		if len(eventValue) == 0 {
			continue
		}
		active, invalid := decodeSystemJobEventArrayJSON(eventValue)
		hasEvent = hasEvent || active
		malformed = malformed || invalid
	}
	if len(raw.StartOnMount) > 0 {
		var active *bool
		if json.Unmarshal(raw.StartOnMount, &active) != nil || active == nil {
			malformed = true
		} else {
			hasEvent = hasEvent || *active
		}
	}
	return makeSystemJobSchedule(interval, calendar, hasInterval, hasCalendar, hasEvent, malformed), malformed
}

func decodeSystemJobCalendarJSON(data json.RawMessage) ([]viewer.SystemJobCalendarSchedule, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, true
	}
	if trimmed[0] != '[' {
		entry, invalid := decodeSystemJobCalendarEntryJSON(trimmed)
		if invalid {
			return nil, true
		}
		return []viewer.SystemJobCalendarSchedule{entry}, false
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil || len(values) == 0 || len(values) > systemJobsMaxCalendarEntries {
		return nil, true
	}
	entries := make([]viewer.SystemJobCalendarSchedule, 0, len(values))
	invalid := false
	for _, value := range values {
		entry, entryInvalid := decodeSystemJobCalendarEntryJSON(value)
		if entryInvalid {
			invalid = true
		}
		entries = append(entries, entry)
	}
	return entries, invalid
}

func decodeSystemJobCalendarEntryJSON(data json.RawMessage) (viewer.SystemJobCalendarSchedule, bool) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil || values == nil || len(values) == 0 {
		return viewer.SystemJobCalendarSchedule{}, true
	}
	entry := viewer.SystemJobCalendarSchedule{}
	invalid := false
	for key, rawValue := range values {
		var value *int
		if json.Unmarshal(rawValue, &value) != nil || value == nil {
			invalid = true
			continue
		}
		minimum, maximum := 0, 0
		switch key {
		case "Minute":
			minimum, maximum, entry.Minute = 0, 59, value
		case "Hour":
			minimum, maximum, entry.Hour = 0, 23, value
		case "Day":
			minimum, maximum, entry.Day = 1, 31, value
		case "Weekday":
			minimum, maximum, entry.Weekday = 0, 7, value
		case "Month":
			minimum, maximum, entry.Month = 1, 12, value
		case "Year":
			minimum, maximum, entry.Year = 1, 9999, value
		default:
			invalid = true
			continue
		}
		if *value < minimum || *value > maximum {
			invalid = true
		}
	}
	return entry, invalid
}

func decodeSystemJobEventArrayJSON(data json.RawMessage) (bool, bool) {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil || len(values) == 0 {
		return false, true
	}
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' || !json.Valid(trimmed) {
			return false, true
		}
	}
	return true, false
}

func makeSystemJobSchedule(interval *int64, calendar []viewer.SystemJobCalendarSchedule, hasInterval, hasCalendar, hasEvent, invalid bool) viewer.SystemJobSchedule {
	if invalid {
		return viewer.SystemJobSchedule{Kind: "unknown"}
	}
	parts := 0
	if hasInterval && interval != nil {
		parts++
	}
	if hasCalendar && len(calendar) > 0 {
		parts++
	}
	if hasEvent {
		parts++
	}
	kind := "none"
	switch parts {
	case 1:
		if hasInterval {
			kind = "interval"
		} else if hasCalendar {
			kind = "calendar"
		} else {
			kind = "event"
		}
	case 2, 3:
		kind = "combined"
	}
	if (hasInterval && interval == nil) || (hasCalendar && len(calendar) == 0) {
		return viewer.SystemJobSchedule{Kind: "unknown"}
	}
	return viewer.SystemJobSchedule{Kind: kind, IntervalSeconds: interval, CalendarEntries: calendar, EventTrigger: hasEvent}
}

func isRenCrowPlistFilename(name string) bool {
	return systemJobsLabelPattern.MatchString(systemJobsLabelFromFilename(name))
}

func systemJobsLabelFromFilename(name string) string {
	base := filepath.Base(name)
	if !strings.HasSuffix(strings.ToLower(base), ".plist") {
		return ""
	}
	label := strings.TrimSuffix(base, filepath.Ext(base))
	if !systemJobsLabelPattern.MatchString(label) {
		return ""
	}
	return label
}

func hasSystemJobsRenCrowPrefix(label string) bool {
	return len(label) >= len(systemJobsRenCrowPrefix) && strings.EqualFold(label[:len(systemJobsRenCrowPrefix)], systemJobsRenCrowPrefix)
}
