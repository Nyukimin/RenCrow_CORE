package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type systemJobsObserverStub struct {
	disabled      map[string]map[string]string
	loaded        map[string]systemJobsRuntimeObservation
	inspectCalls  map[string]int
	disabledCalls map[string]int
	disabledErrs  map[string]error
	err           error
	errs          map[string]error
}

func (observer *systemJobsObserverStub) ReadDisabledStates(_ context.Context, domain string, labels []string) (map[string]string, error) {
	observer.disabledCalls[domain]++
	if observer.err != nil {
		return nil, observer.err
	}
	if err := observer.disabledErrs[domain]; err != nil {
		return nil, err
	}
	states := make(map[string]string)
	for _, label := range labels {
		if state := observer.disabled[domain][label]; state != "" {
			states[label] = state
		}
	}
	return states, nil
}

func (observer *systemJobsObserverStub) Inspect(_ context.Context, domain, label string) (systemJobsRuntimeObservation, error) {
	key := systemJobsObservationKey(domain, label)
	observer.inspectCalls[key]++
	if observer.err != nil {
		return systemJobsRuntimeObservation{}, observer.err
	}
	if err := observer.errs[domain]; err != nil {
		return systemJobsRuntimeObservation{}, err
	}
	if observation, exists := observer.loaded[key]; exists {
		return observation, nil
	}
	return systemJobsRuntimeObservation{known: true, loaded: false}, nil
}

func TestCollectSystemJobsKeepsDomainsAndDuplicateSourcePathsDistinct(t *testing.T) {
	systemRoot := t.TempDir()
	sharedAgentsRoot := t.TempDir()
	userAgentsRoot := t.TempDir()
	userRencrowRoot := t.TempDir()
	systemPath := writeSystemJobsPlist(t, systemRoot, "com.rencrow.log-rotate-guard", `"StartInterval":3600`)
	sharedAgentsPath := writeSystemJobsPlist(t, sharedAgentsRoot, "com.rencrow.log-rotate-guard", `"StartInterval":60`)
	userAgentsPath := writeSystemJobsPlist(t, userAgentsRoot, "com.rencrow.log-rotate-guard", `"StartCalendarInterval":{"Hour":9,"Minute":0}`)
	userRencrowPath := writeSystemJobsPlist(t, userRencrowRoot, "com.rencrow.log-rotate-guard", `"StartInterval":300`)
	observer := newSystemJobsObserverStub()
	observer.disabled["system"] = make(map[string]string)
	observer.disabled["user/501"] = make(map[string]string)
	observer.disabled["system"]["com.rencrow.log-rotate-guard"] = "enabled"
	observer.disabled["user/501"]["com.rencrow.log-rotate-guard"] = "disabled"
	observer.loaded[systemJobsObservationKey("system", "com.rencrow.log-rotate-guard")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: systemPath, state: "running", pid: 410,
	}
	observer.loaded[systemJobsObservationKey("user/501", "com.rencrow.log-rotate-guard")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: userAgentsPath, state: "waiting", pid: 0,
	}
	roots := []systemJobsRoot{
		{id: "system_launch_daemons", domain: "system", path: systemRoot},
		{id: "shared_launch_agents", domain: "user/501", path: sharedAgentsRoot},
		{id: "user_launch_agents", domain: "user/501", path: userAgentsRoot},
		{id: "user_rencrow_launchd", domain: "user/501", path: userRencrowRoot},
	}
	snapshot := collectSystemJobs(context.Background(), roots, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "available" || len(snapshot.Records) != 4 {
		t.Fatalf("snapshot availability/records = %q/%d: %+v", snapshot.Availability, len(snapshot.Records), snapshot)
	}
	if observer.inspectCalls[systemJobsObservationKey("system", "com.rencrow.log-rotate-guard")] != 1 ||
		observer.inspectCalls[systemJobsObservationKey("user/501", "com.rencrow.log-rotate-guard")] != 1 {
		t.Fatalf("launchctl inspect calls = %+v; want once per domain and label", observer.inspectCalls)
	}
	got := make(map[string]viewer.SystemJobRecord)
	for _, record := range snapshot.Records {
		got[record.SourceID] = record
	}
	if got["system_launch_daemons"].LoadedState != "loaded" || got["system_launch_daemons"].RuntimeState != "running" {
		t.Fatalf("system record did not preserve running observation: %+v", got["system_launch_daemons"])
	}
	if got["shared_launch_agents"].Domain != "user/501" || got["shared_launch_agents"].LoadedState != "unloaded" || got["shared_launch_agents"].RuntimeState != "unknown" {
		t.Fatalf("shared LaunchAgents inherited a different definition's loaded state: %+v", got["shared_launch_agents"])
	}
	if got["user_launch_agents"].Domain != "user/501" || got["user_launch_agents"].DisabledState != "disabled" ||
		got["user_launch_agents"].LoadedState != "loaded" || got["user_launch_agents"].RuntimeState != "not_running" {
		t.Fatalf("user record state projection = %+v", got["user_launch_agents"])
	}
	if got["user_rencrow_launchd"].LoadedState != "unloaded" || got["user_rencrow_launchd"].RuntimeState != "unknown" {
		t.Fatalf("duplicate source path incorrectly inherited loaded state: %+v", got["user_rencrow_launchd"])
	}
	if got["system_launch_daemons"].Schedule.Kind != "interval" || got["user_launch_agents"].Schedule.Kind != "calendar" ||
		got["user_rencrow_launchd"].Schedule.Kind != "interval" {
		t.Fatalf("schedule projection = %+v", snapshot.Records)
	}
	if strings.Contains(fmt.Sprint(snapshot), systemPath) || strings.Contains(fmt.Sprint(snapshot), sharedAgentsPath) ||
		strings.Contains(fmt.Sprint(snapshot), userAgentsPath) || strings.Contains(fmt.Sprint(snapshot), userRencrowPath) {
		t.Fatal("projection contains a local plist path")
	}
}

func TestCollectSystemJobsRetainsSameSourceDuplicateLabelsAndMarksPartial(t *testing.T) {
	root := t.TempDir()
	firstPath := writeSystemJobsPlistNamed(t, root, "com.rencrow.first.plist", "com.rencrow.same-label", `"StartInterval":60`)
	secondPath := writeSystemJobsPlistNamed(t, root, "com.rencrow.second.plist", "com.rencrow.same-label", `"StartInterval":300`)
	observer := newSystemJobsObserverStub()
	observer.disabled["system"] = make(map[string]string)
	observer.disabled["system"]["com.rencrow.same-label"] = "enabled"
	observer.loaded[systemJobsObservationKey("system", "com.rencrow.same-label")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: secondPath, state: "running", pid: 410,
	}
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || len(snapshot.Records) != 2 {
		t.Fatalf("duplicate definitions were not retained as partial: %+v", snapshot)
	}
	source := snapshot.Sources[0]
	if source.State != "partial" || source.Records != 2 || !containsSystemJobsIssue(source.Issues, "duplicate_label") {
		t.Fatalf("duplicate source report=%+v", source)
	}
	if snapshot.Records[0].Schedule.IntervalSeconds == nil || *snapshot.Records[0].Schedule.IntervalSeconds != 60 ||
		snapshot.Records[0].LoadedState != "unloaded" || snapshot.Records[0].RuntimeState != "unknown" {
		t.Fatalf("first path inherited another duplicate's runtime state: %+v (path %s)", snapshot.Records[0], firstPath)
	}
	if snapshot.Records[1].Schedule.IntervalSeconds == nil || *snapshot.Records[1].Schedule.IntervalSeconds != 300 ||
		snapshot.Records[1].LoadedState != "loaded" || snapshot.Records[1].RuntimeState != "running" {
		t.Fatalf("loaded duplicate path was not identified: %+v", snapshot.Records[1])
	}
}

func TestCollectSystemJobsDiscoversSafeCaseAndUnderscoreLabelVariants(t *testing.T) {
	root := t.TempDir()
	labels := []string{"com.rencrow.future_job", "COM.RENCROW.Future--Job"}
	for _, label := range labels {
		writeSystemJobsPlist(t, root, label, `"StartInterval":300`)
	}
	observer := newSystemJobsObserverStub()
	observer.disabled["system"] = map[string]string{
		labels[0]: "enabled",
		labels[1]: "enabled",
	}
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "available" || len(snapshot.Records) != len(labels) {
		t.Fatalf("safe label variants were omitted or misreported: %+v", snapshot)
	}
	got := make(map[string]bool, len(snapshot.Records))
	for _, record := range snapshot.Records {
		got[record.Label] = true
	}
	for _, label := range labels {
		if !got[label] {
			t.Errorf("label case/spelling was not preserved: %q missing from %+v", label, got)
		}
	}
}

func TestCollectSystemJobsReportsUnsafeRenCrowLabelAsPartial(t *testing.T) {
	root := t.TempDir()
	writeSystemJobsPlistNamed(t, root, "bad-label.plist", "COM.RENCROW.invalid label", `"StartInterval":300`)
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, nil, systemJobsJSONConverter, newSystemJobsObserverStub())
	if snapshot.Availability != "partial" || snapshot.Sources[0].State != "partial" || snapshot.Sources[0].Candidates != 1 ||
		len(snapshot.Records) != 0 || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "plist_unsupported") {
		t.Fatalf("unsafe RenCrow label was silently omitted or exposed: %+v", snapshot)
	}
	if strings.Contains(fmt.Sprint(snapshot), "invalid label") {
		t.Fatalf("unsafe RenCrow label leaked into projection: %+v", snapshot)
	}
}

func TestCollectSystemJobsRetainsMalformedUnreadableOversizedAndUnknownDefinitions(t *testing.T) {
	root := t.TempDir()
	malformedPath := filepath.Join(root, "com.rencrow.broken.plist")
	if err := os.WriteFile(malformedPath, []byte(`{"Label":`), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadablePath := filepath.Join(root, "com.rencrow.unreadable.plist")
	if err := os.WriteFile(unreadablePath, []byte(systemJobsTestPlist("com.rencrow.unreadable", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	oversizedPath := filepath.Join(root, "com.rencrow.oversized.plist")
	if err := os.WriteFile(oversizedPath, make([]byte, systemJobsMaxPlistBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	unknownPath := writeSystemJobsPlist(t, root, "com.rencrow.future-watch", `"ProgramArguments":["/private/run","secret-value"],"EnvironmentVariables":{"TOKEN":"private-env-value"},"WatchPaths":["/private/watch"]`)
	reader := func(path string) ([]byte, error) {
		if path == unreadablePath {
			return nil, errors.New("private read error")
		}
		return readBoundedSystemJobsPlist(path)
	}
	observer := newSystemJobsObserverStub()
	observer.disabled["system"] = map[string]string{
		"com.rencrow.broken": "enabled", "com.rencrow.unreadable": "enabled", "com.rencrow.oversized": "enabled", "com.rencrow.future-watch": "enabled",
	}
	observer.loaded[systemJobsObservationKey("system", "com.rencrow.future-watch")] = systemJobsRuntimeObservation{
		known: true, loaded: false,
	}
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, reader, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || len(snapshot.Records) != 4 {
		t.Fatalf("partial snapshot/record count = %q/%d: %+v", snapshot.Availability, len(snapshot.Records), snapshot)
	}
	byLabel := make(map[string]viewer.SystemJobRecord)
	for _, record := range snapshot.Records {
		byLabel[record.Label] = record
	}
	if byLabel["com.rencrow.broken"].DefinitionState != "malformed" || byLabel["com.rencrow.unreadable"].DefinitionState != "unreadable" ||
		byLabel["com.rencrow.oversized"].DefinitionState != "unsupported" {
		t.Fatalf("unreadable definitions lost or misclassified: %+v", byLabel)
	}
	unknown := byLabel["com.rencrow.future-watch"]
	if unknown.RecordType != "" || unknown.Schedule.Kind != "event" || !unknown.Schedule.EventTrigger {
		t.Fatalf("future job was silently omitted or promoted: %+v", unknown)
	}
	if strings.Contains(fmt.Sprint(snapshot), "secret-value") || strings.Contains(fmt.Sprint(snapshot), "private-env-value") ||
		strings.Contains(fmt.Sprint(snapshot), "/private/run") || strings.Contains(fmt.Sprint(snapshot), "/private/watch") || strings.Contains(fmt.Sprint(snapshot), unknownPath) {
		t.Fatalf("projection leaked property list contents: %+v", snapshot)
	}
	if snapshot.Sources[0].Records != 4 || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "plist_malformed") ||
		!containsSystemJobsIssue(snapshot.Sources[0].Issues, "file_unreadable") || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "file_too_large") {
		t.Fatalf("source coverage/issues = %+v", snapshot.Sources[0])
	}
}

func TestCollectSystemJobsAggregatesOnlyPathMatchingUserAndGUIDomains(t *testing.T) {
	root := t.TempDir()
	path := writeSystemJobsPlist(t, root, "com.rencrow.future-job", `"StartInterval":60`)
	observer := newSystemJobsObserverStub()
	observer.disabled["user/501"] = map[string]string{"com.rencrow.future-job": "enabled"}
	observer.loaded[systemJobsObservationKey("user/501", "com.rencrow.future-job")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: path, state: "running", pid: 501,
	}
	observer.loaded[systemJobsObservationKey("gui/501", "com.rencrow.future-job")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: filepath.Join(root, "another.plist"), state: "running", pid: 502,
	}
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if len(snapshot.Records) != 1 {
		t.Fatalf("records=%+v, want one definition", snapshot.Records)
	}
	record := snapshot.Records[0]
	if record.LoadedState != "loaded" || record.RuntimeState != "running" || len(record.RuntimeDomains) != 1 || record.RuntimeDomains[0] != "user/501" {
		t.Fatalf("runtime domains included a different path: %+v", record)
	}
	if snapshot.Availability != "available" || containsSystemJobsIssue(snapshot.Sources[0].Issues, "runtime_state_unavailable") ||
		len(record.RuntimeObservations) != 2 || record.RuntimeObservations[0].Domain != "user/501" || record.RuntimeObservations[0].State != "running" ||
		record.RuntimeObservations[1].Domain != "gui/501" || record.RuntimeObservations[1].State != "different_definition" {
		t.Fatalf("different-definition evidence was hidden: snapshot=%+v record=%+v", snapshot, record)
	}
	if observer.inspectCalls[systemJobsObservationKey("user/501", record.Label)] != 1 || observer.inspectCalls[systemJobsObservationKey("gui/501", record.Label)] != 1 {
		t.Fatalf("user/gui observations were not independently read: %+v", observer.inspectCalls)
	}
}

func TestCollectSystemJobsKeepsRuntimeUnknownWhenOneUserDomainCannotBeObserved(t *testing.T) {
	root := t.TempDir()
	writeSystemJobsPlist(t, root, "com.rencrow.future-job", `"StartInterval":60`)
	observer := newSystemJobsObserverStub()
	observer.disabled["user/501"] = map[string]string{"com.rencrow.future-job": "enabled"}
	observer.errs["gui/501"] = errors.New("unavailable")
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || len(snapshot.Records) != 1 || snapshot.Records[0].LoadedState != "unknown" || snapshot.Records[0].RuntimeState != "unknown" {
		t.Fatalf("one unobservable runtime domain was treated as known: %+v", snapshot)
	}
}

func TestCollectSystemJobsPreservesRunningEvidenceWhenOtherUserDomainIsUnknown(t *testing.T) {
	root := t.TempDir()
	path := writeSystemJobsPlist(t, root, "com.rencrow.future-job", `"StartInterval":60`)
	observer := newSystemJobsObserverStub()
	observer.disabled["user/501"] = map[string]string{"com.rencrow.future-job": "enabled"}
	observer.loaded[systemJobsObservationKey("user/501", "com.rencrow.future-job")] = systemJobsRuntimeObservation{
		known: true, loaded: true, path: path, state: "running", pid: 501,
	}
	observer.errs["gui/501"] = errors.New("unavailable")
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || snapshot.Records[0].RuntimeState != "running" || len(snapshot.Records[0].RuntimeDomains) != 1 ||
		len(snapshot.Records[0].RuntimeObservations) != 2 || snapshot.Records[0].RuntimeObservations[0].State != "running" ||
		snapshot.Records[0].RuntimeObservations[1].State != "unavailable" {
		t.Fatalf("positive runtime evidence was discarded with a partial domain read: %+v", snapshot)
	}
}

func TestCollectSystemJobsPreservesPerDomainRuntimeAndDisabledReasons(t *testing.T) {
	root := t.TempDir()
	writeSystemJobsPlist(t, root, "com.rencrow.future-resident", `"KeepAlive":true`)
	observer := newSystemJobsObserverStub()
	observer.loaded[systemJobsObservationKey("user/501", "com.rencrow.future-resident")] = systemJobsRuntimeObservation{
		known: true, loaded: false,
	}
	observer.errs["gui/501"] = errors.New("GUI domain unavailable")
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || len(snapshot.Records) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	record := snapshot.Records[0]
	if record.DisabledState != "unknown" || record.DisabledReason != "not_reported" {
		t.Fatalf("successful missing disabled entry was not distinguished: %+v", record)
	}
	if record.LoadedState != "unknown" || record.RuntimeState != "unknown" || !record.KeepAlive {
		t.Fatalf("aggregate state or KeepAlive = %+v", record)
	}
	if len(record.RuntimeObservations) != 2 || record.RuntimeObservations[0].Domain != "user/501" || record.RuntimeObservations[0].State != "unloaded" ||
		record.RuntimeObservations[1].Domain != "gui/501" || record.RuntimeObservations[1].State != "unavailable" {
		t.Fatalf("per-domain observations = %+v", record.RuntimeObservations)
	}
	if !containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_not_reported") ||
		containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_unavailable") ||
		!containsSystemJobsIssue(snapshot.Sources[0].Issues, "runtime_state_unavailable") {
		t.Fatalf("source issues hide or conflate observations: %+v", snapshot.Sources[0].Issues)
	}

	observer = newSystemJobsObserverStub()
	observer.disabledErrs["user/501"] = errors.New("print-disabled unavailable")
	snapshot = collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Records[0].DisabledReason != "unavailable" || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_unavailable") ||
		containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_not_reported") {
		t.Fatalf("CLI failure was not distinguished from missing label entry: record=%+v source=%+v", snapshot.Records[0], snapshot.Sources[0])
	}
	observer = newSystemJobsObserverStub()
	observer.disabled["user/501"] = map[string]string{"com.rencrow.future-resident": "unrecognized"}
	snapshot = collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Records[0].DisabledReason != "unavailable" || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_unavailable") ||
		containsSystemJobsIssue(snapshot.Sources[0].Issues, "disabled_state_not_reported") {
		t.Fatalf("unrecognized returned value was treated as not-reported: record=%+v source=%+v", snapshot.Records[0], snapshot.Sources[0])
	}
}

func TestParseSystemJobKeepsOnlyExplicitBooleanKeepAlive(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "explicit true", value: `true`, want: true},
		{name: "explicit false", value: `false`},
		{name: "dictionary", value: `{"SuccessfulExit":false}`},
		{name: "string", value: `"true"`},
		{name: "integer", value: `1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseSystemJobPlistJSON([]byte(fmt.Sprintf(`{"Label":"com.rencrow.keepalive","KeepAlive":%s}`, test.value)))
			if err != nil || parsed.keepAlive != test.want {
				t.Fatalf("KeepAlive=%s parsed=%+v err=%v; want %v", test.value, parsed, err, test.want)
			}
		})
	}
}

func TestCollectSystemJobsRecoversAfterRuntimeDomainBecomesAvailable(t *testing.T) {
	root := t.TempDir()
	writeSystemJobsPlist(t, root, "com.rencrow.recoverable", "")
	roots := []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: root}}
	firstObserver := newSystemJobsObserverStub()
	firstObserver.disabled["user/501"] = map[string]string{"com.rencrow.recoverable": "enabled"}
	firstObserver.errs["gui/501"] = errors.New("GUI domain unavailable")
	first := collectSystemJobs(context.Background(), roots, nil, systemJobsJSONConverter, firstObserver)
	if first.Availability != "partial" || first.Records[0].RuntimeState != "unknown" || first.Records[0].RuntimeObservations[1].State != "unavailable" {
		t.Fatalf("first observation did not preserve unavailable domain: %+v", first)
	}
	secondObserver := newSystemJobsObserverStub()
	secondObserver.disabled["user/501"] = map[string]string{"com.rencrow.recoverable": "enabled"}
	second := collectSystemJobs(context.Background(), roots, nil, systemJobsJSONConverter, secondObserver)
	if second.Availability != "available" || second.Records[0].LoadedState != "unloaded" || second.Records[0].RuntimeState != "not_running" ||
		len(second.Sources[0].Issues) != 0 || second.Records[0].RuntimeObservations[0].State != "unloaded" || second.Records[0].RuntimeObservations[1].State != "unloaded" {
		t.Fatalf("recovered observation retained stale partial state: %+v", second)
	}
}

func TestCollectSystemJobsDoesNotTreatUnknownRuntimeStateAsNotRunning(t *testing.T) {
	for _, test := range []struct {
		name         string
		state        string
		pid          int
		wantRuntime  string
		wantObserved string
		wantPartial  bool
	}{
		{name: "not running", state: "not_running", wantRuntime: "not_running", wantObserved: "not_running"},
		{name: "waiting", state: "waiting", wantRuntime: "not_running", wantObserved: "not_running"},
		{name: "running", state: "running", pid: 91, wantRuntime: "running", wantObserved: "running"},
		{name: "unknown state", state: "future-state", pid: 91, wantRuntime: "unknown", wantObserved: "unavailable", wantPartial: true},
		{name: "running without positive pid", state: "running", wantRuntime: "unknown", wantObserved: "unavailable", wantPartial: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := writeSystemJobsPlist(t, root, "com.rencrow.runtime-state", "")
			observer := newSystemJobsObserverStub()
			observer.disabled["system"] = map[string]string{"com.rencrow.runtime-state": "enabled"}
			observer.loaded[systemJobsObservationKey("system", "com.rencrow.runtime-state")] = systemJobsRuntimeObservation{
				known: true, loaded: true, path: path, state: test.state, pid: test.pid,
			}
			snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, nil, systemJobsJSONConverter, observer)
			record := snapshot.Records[0]
			if record.RuntimeState != test.wantRuntime || len(record.RuntimeObservations) != 1 || record.RuntimeObservations[0].State != test.wantObserved {
				t.Fatalf("runtime state projection = %+v", record)
			}
			if (snapshot.Availability == "partial") != test.wantPartial {
				t.Fatalf("availability=%q, want partial=%t; source=%+v", snapshot.Availability, test.wantPartial, snapshot.Sources[0])
			}
		})
	}
}

func TestCollectSystemJobsMarksRootAndEntryBoundsPartial(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < systemJobsMaxPlistsPerRoot+1; index++ {
		label := fmt.Sprintf("com.rencrow.future-%03d", index)
		writeSystemJobsPlist(t, root, label, "")
	}
	observer := newSystemJobsObserverStub()
	snapshot := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "system_launch_daemons", domain: "system", path: root}}, nil, systemJobsJSONConverter, observer)
	if snapshot.Availability != "partial" || len(snapshot.Records) != systemJobsMaxPlistsPerRoot {
		t.Fatalf("bounded scan availability/records = %q/%d", snapshot.Availability, len(snapshot.Records))
	}
	source := snapshot.Sources[0]
	if source.Candidates != systemJobsMaxPlistsPerRoot+1 || source.Records != systemJobsMaxPlistsPerRoot || !source.Truncated ||
		!containsSystemJobsIssue(source.Issues, "entry_limit_reached") {
		t.Fatalf("bounded scan source = %+v", source)
	}

	unavailable := collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: filepath.Join(root, "missing")}}, nil, nil, observer)
	if unavailable.Availability != "available" || unavailable.Sources[0].State != "available" || len(unavailable.Records) != 0 {
		t.Fatalf("absent optional root was treated as a failed read: %+v", unavailable)
	}
	unsafe := filepath.Join(root, "root-link")
	if err := os.Symlink(root, unsafe); err != nil {
		t.Fatal(err)
	}
	unavailable = collectSystemJobs(context.Background(), []systemJobsRoot{{id: "user_launch_agents", domain: "user/501", path: unsafe}}, nil, nil, observer)
	if unavailable.Availability != "partial" || unavailable.Sources[0].State != "unavailable" ||
		!containsSystemJobsIssue(unavailable.Sources[0].Issues, "root_unsafe") {
		t.Fatalf("symlinked root was not reported partial: %+v", unavailable)
	}
}

func TestCollectSystemJobsDeadlineLeavesExplicitPartialSources(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot := collectSystemJobs(ctx, []systemJobsRoot{
		{id: "system_launch_daemons", domain: "system", path: root},
		{id: "shared_launch_agents", domain: "user/501", path: root},
		{id: "user_launch_agents", domain: "user/501", path: root},
		{id: "user_rencrow_launchd", domain: "user/501", path: root},
	}, nil, systemJobsJSONConverter, newSystemJobsObserverStub())
	if snapshot.Availability != "partial" || len(snapshot.Sources) != 4 || len(snapshot.Records) != 0 {
		t.Fatalf("deadline response did not retain partial sources: %+v", snapshot)
	}
	for _, source := range snapshot.Sources {
		if source.State != "partial" || !source.Truncated || !containsSystemJobsIssue(source.Issues, "scan_timeout") {
			t.Fatalf("deadline source state=%+v", source)
		}
	}
}

func TestParseSystemJobPlistUsesOnlyScheduleAndLabelKeys(t *testing.T) {
	data := []byte(systemJobsTestPlist("com.rencrow.fxembed-daily", `"ProgramArguments":["secret-value"],"StartCalendarInterval":[{"Hour":9,"Minute":0}],"StartOnMount":false`))
	parsed, err := parseSystemJobPlistJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.label != "com.rencrow.fxembed-daily" || parsed.schedule.Kind != "calendar" || len(parsed.schedule.CalendarEntries) != 1 ||
		parsed.schedule.CalendarEntries[0].Hour == nil || *parsed.schedule.CalendarEntries[0].Hour != 9 || parsed.schedule.EventTrigger {
		t.Fatalf("parsed plist projection = %+v", parsed)
	}
	if _, err := parseSystemJobPlistJSON(append(data, []byte(`{"extra":true}`)...)); err == nil {
		t.Fatal("trailing JSON content was accepted")
	}
	malformed := []byte(systemJobsTestPlist("com.rencrow.invalid-time", `"StartCalendarInterval":{"Hour":24}`))
	parsed, err = parseSystemJobPlistJSON(malformed)
	if err != nil || !parsed.malformedTiming || parsed.schedule.Kind != "unknown" {
		t.Fatalf("invalid schedule should remain explicit unknown: parsed=%+v err=%v", parsed, err)
	}
}

func TestDecodeSystemJobsBinaryPlistUsesBoundedConversionInput(t *testing.T) {
	data := []byte("bplist00bounded fixture")
	called := false
	parsed, err := decodeSystemJobPlist(context.Background(), data, func(_ context.Context, got []byte) ([]byte, error) {
		called = true
		if string(got) != string(data) {
			t.Fatalf("converter input differs: %q", got)
		}
		return []byte(systemJobsTestPlist("com.rencrow.future-calendar", `"StartInterval":300`)), nil
	})
	if err != nil || !called || parsed.label != "com.rencrow.future-calendar" || parsed.schedule.IntervalSeconds == nil || *parsed.schedule.IntervalSeconds != 300 {
		t.Fatalf("binary plist projection = %+v, called=%v, err=%v", parsed, called, err)
	}
}

func newSystemJobsObserverStub() *systemJobsObserverStub {
	return &systemJobsObserverStub{
		disabled:      make(map[string]map[string]string),
		loaded:        make(map[string]systemJobsRuntimeObservation),
		inspectCalls:  make(map[string]int),
		disabledCalls: make(map[string]int),
		disabledErrs:  make(map[string]error),
		errs:          make(map[string]error),
	}
}

func writeSystemJobsPlist(t *testing.T, root, label, extra string) string {
	t.Helper()
	path := filepath.Join(root, label+".plist")
	return writeSystemJobsPlistNamed(t, root, filepath.Base(path), label, extra)
}

func writeSystemJobsPlistNamed(t *testing.T, root, filename, label, extra string) string {
	t.Helper()
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, []byte(systemJobsTestPlist(label, extra)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func systemJobsTestPlist(label, extra string) string {
	if extra == "" {
		return fmt.Sprintf(`{"Label":%q}`, label)
	}
	return fmt.Sprintf(`{"Label":%q,%s}`, label, extra)
}

func systemJobsJSONConverter(_ context.Context, data []byte) ([]byte, error) {
	return data, nil
}

func containsSystemJobsIssue(issues []string, want string) bool {
	for _, issue := range issues {
		if issue == want {
			return true
		}
	}
	return false
}
