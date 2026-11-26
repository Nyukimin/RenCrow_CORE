package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const systemJobsTestToken = "system-jobs-owner-token"

type systemJobsReaderStub struct {
	snapshot SystemJobsSnapshot
	err      error
	calls    int
}

func (s *systemJobsReaderStub) ReadSystemJobs(context.Context) (SystemJobsSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

func TestSystemJobsHandlerReturnsSafeOwnerProjection(t *testing.T) {
	reader := &systemJobsReaderStub{snapshot: SystemJobsSnapshot{
		ObservedAt: time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC), Availability: "partial",
		Sources: []SystemJobsSource{{ID: "system_launch_daemons", Domain: "system", State: "partial", Candidates: 4, Records: 1, Issues: []string{"runtime_state_unavailable"}}},
		Records: []SystemJobRecord{{
			ID: "caller-id", Domain: "system", Label: "com.rencrow.lyrics-collector", Title: "not trusted", RecordType: "not trusted",
			Source: "launchd_plist", SourceID: "system_launch_daemons", DefinitionState: "observed",
			Schedule:      SystemJobSchedule{Kind: "interval", IntervalSeconds: systemJobsInt64(300)},
			DisabledState: "unknown", LoadedState: "unloaded", RuntimeState: "not_running",
		}},
	}}
	handler := NewSystemJobsHandler(reader, "ren", []byte(systemJobsTestToken))
	request := newSystemJobsRequest(http.MethodGet, systemJobsPath, "RenCrow_BOARD", "board-studio-readonly", systemJobsTestToken, "127.0.0.1:49152")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", recorder.Code, recorder.Body.String())
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls=%d, want 1", reader.calls)
	}
	var got SystemJobsSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Availability != "partial" || len(got.Records) != 1 || got.Records[0].Title != "歌詞収集" || got.Records[0].RecordType != "job" {
		t.Fatalf("safe projection = %+v", got)
	}
	if got.Records[0].ID != "system|system_launch_daemons|com.rencrow.lyrics-collector" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("record id/cache policy = %q/%q", got.Records[0].ID, recorder.Header().Get("Cache-Control"))
	}
	for _, forbidden := range []string{"not trusted", "ProgramArguments", "secret", "/Library/LaunchDaemons"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestSystemJobsProjectionPreservesObservationReasonsAndKeepAliveEvidence(t *testing.T) {
	const input = `{"observed_at":"2026-11-24T06:56:00Z","availability":"partial","sources":[{"id":"user_launch_agents","domain":"user/501","state":"partial","candidates":1,"records":1,"truncated":false,"issues":["disabled_state_not_reported","runtime_state_unavailable"]}],"records":[{"domain":"user/501","label":"com.rencrow.future-resident","source":"launchd_plist","source_id":"user_launch_agents","definition_state":"observed","schedule":{"kind":"none"},"disabled_state":"unknown","disabled_reason":"not_reported","loaded_state":"unknown","runtime_state":"unknown","runtime_observations":[{"domain":"user/501","state":"unloaded"},{"domain":"gui/501","state":"unavailable"}],"keep_alive":true}]}`
	var snapshot SystemJobsSnapshot
	if err := json.Unmarshal([]byte(input), &snapshot); err != nil {
		t.Fatal(err)
	}
	reader := &systemJobsReaderStub{snapshot: snapshot}
	handler := NewSystemJobsHandler(reader, "ren", []byte(systemJobsTestToken))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newSystemJobsRequest(http.MethodGet, systemJobsPath, "RenCrow_BOARD", "board-studio-readonly", systemJobsTestToken, "127.0.0.1:49152"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", recorder.Code, recorder.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sources := got["sources"].([]any)
	issues := sources[0].(map[string]any)["issues"].([]any)
	issueFound := false
	for _, issue := range issues {
		issueFound = issueFound || issue == "disabled_state_not_reported"
	}
	if !issueFound {
		t.Fatalf("new source issue was not preserved: %s", recorder.Body.String())
	}
	record := got["records"].([]any)[0].(map[string]any)
	if record["disabled_reason"] != "not_reported" || record["keep_alive"] != true || record["record_type"] != "resident_service" {
		t.Fatalf("disabled/KeepAlive projection = %v", record)
	}
	observations := record["runtime_observations"].([]any)
	if len(observations) != 2 || observations[0].(map[string]any)["domain"] != "gui/501" || observations[0].(map[string]any)["state"] != "unavailable" ||
		observations[1].(map[string]any)["domain"] != "user/501" || observations[1].(map[string]any)["state"] != "unloaded" {
		t.Fatalf("runtime observations = %v", observations)
	}
}

func TestSystemJobsHandlerRequiresExactLoopbackOwnerProfileAndPath(t *testing.T) {
	reader := &systemJobsReaderStub{snapshot: SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "available",
		Sources: []SystemJobsSource{{ID: "system_launch_daemons", Domain: "system", State: "available"}},
	}}
	handler := NewSystemJobsHandler(reader, "ren", []byte(systemJobsTestToken))
	tests := []struct {
		name    string
		method  string
		path    string
		client  string
		profile string
		token   string
		remote  string
		want    int
	}{
		{name: "valid", method: http.MethodGet, path: systemJobsPath, client: "RenCrow_BOARD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusOK},
		{name: "missing bearer", method: http.MethodGet, path: systemJobsPath, client: "RenCrow_BOARD", profile: "board-studio-readonly", remote: "127.0.0.1:49152", want: http.StatusUnauthorized},
		{name: "wrong client", method: http.MethodGet, path: systemJobsPath, client: "RenCrow_CMD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusForbidden},
		{name: "missing profile", method: http.MethodGet, path: systemJobsPath, client: "RenCrow_BOARD", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusForbidden},
		{name: "wrong method", method: http.MethodPost, path: systemJobsPath, client: "RenCrow_BOARD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusMethodNotAllowed},
		{name: "query rejected", method: http.MethodGet, path: systemJobsPath + "?raw=1", client: "RenCrow_BOARD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusNotFound},
		{name: "expanded path rejected", method: http.MethodGet, path: systemJobsPath + "/extra", client: "RenCrow_BOARD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "127.0.0.1:49152", want: http.StatusNotFound},
		{name: "non loopback rejected", method: http.MethodGet, path: systemJobsPath, client: "RenCrow_BOARD", profile: "board-studio-readonly", token: systemJobsTestToken, remote: "192.0.2.10:49152", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := reader.calls
			request := newSystemJobsRequest(tt.method, tt.path, tt.client, tt.profile, tt.token, tt.remote)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.want)
			}
			if tt.want != http.StatusOK && reader.calls != before {
				t.Fatalf("untrusted request reached reader: calls %d -> %d", before, reader.calls)
			}
		})
	}
}

func TestSystemJobsHandlerKeepsUnavailableAndUnsupportedExplicit(t *testing.T) {
	unsupported := &systemJobsReaderStub{snapshot: SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "unsupported",
		Sources: []SystemJobsSource{{ID: "launchd_unavailable", Domain: "system", State: "unsupported", Issues: []string{"root_unavailable"}}},
	}}
	handler := NewSystemJobsHandler(unsupported, "ren", []byte(systemJobsTestToken))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newSystemJobsRequest(http.MethodGet, systemJobsPath, "RenCrow_BOARD", "board-studio-readonly", systemJobsTestToken, "127.0.0.1:49152"))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"availability":"unsupported"`) {
		t.Fatalf("unsupported response=%d body=%s", recorder.Code, recorder.Body.String())
	}

	failed := &systemJobsReaderStub{err: errors.New("private host detail")}
	handler = NewSystemJobsHandler(failed, "ren", []byte(systemJobsTestToken))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, newSystemJobsRequest(http.MethodGet, systemJobsPath, "RenCrow_BOARD", "board-studio-readonly", systemJobsTestToken, "127.0.0.1:49152"))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "private host detail") {
		t.Fatalf("error response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSystemJobsHandlerKeepsDuplicateDefinitionsAndDeduplicatesFullIssueSet(t *testing.T) {
	issues := []string{
		"root_unavailable", "root_unsafe", "entry_limit_reached", "file_unreadable", "file_too_large",
		"plist_malformed", "plist_unsupported", "filename_label_mismatch", "disabled_state_unavailable", "disabled_state_not_reported",
		"runtime_state_unavailable", "runtime_observation_timeout", "scan_timeout", "plist_conversion_unavailable",
		"platform_unsupported", "duplicate_label", "duplicate_label",
	}
	reader := &systemJobsReaderStub{snapshot: SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "partial",
		Sources: []SystemJobsSource{
			{ID: "system_launch_daemons", Domain: "system", State: "available", Issues: []string{}},
			{ID: "shared_launch_agents", Domain: "user/501", State: "partial", Candidates: 2, Records: 2, Truncated: true, Issues: issues},
			{ID: "user_launch_agents", Domain: "user/501", State: "available", Issues: []string{}},
			{ID: "user_rencrow_launchd", Domain: "user/501", State: "available", Issues: []string{}},
		},
		Records: []SystemJobRecord{
			{Domain: "user/501", Label: "com.rencrow.future_job", Source: "launchd_plist", SourceID: "shared_launch_agents", DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "interval", IntervalSeconds: systemJobsInt64(60)}, DisabledState: "unknown", LoadedState: "unloaded", RuntimeState: "unknown"},
			{Domain: "user/501", Label: "com.rencrow.future_job", Source: "launchd_plist", SourceID: "shared_launch_agents", DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "interval", IntervalSeconds: systemJobsInt64(300)}, DisabledState: "unknown", LoadedState: "loaded", RuntimeState: "running", RuntimeDomains: []string{"gui/501"}},
		},
	}}
	handler := NewSystemJobsHandler(reader, "ren", []byte(systemJobsTestToken))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newSystemJobsRequest(http.MethodGet, systemJobsPath, "RenCrow_BOARD", "board-studio-readonly", systemJobsTestToken, "127.0.0.1:49152"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", recorder.Code, recorder.Body.String())
	}
	var got SystemJobsSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 4 || len(got.Records) != 2 || got.Sources[1].ID != "shared_launch_agents" ||
		len(got.Sources[1].Issues) != SystemJobsMaxSourceIssues || got.Sources[1].Records != 2 {
		t.Fatalf("four-root duplicate projection=%+v", got)
	}
	if got.Records[0].ID != "user/501|shared_launch_agents|com.rencrow.future_job|duplicate-1" ||
		got.Records[1].ID != "user/501|shared_launch_agents|com.rencrow.future_job|duplicate-2" {
		t.Fatalf("duplicate IDs are not stable and distinct: %+v", got.Records)
	}
	duplicateIssueCount := 0
	for _, issue := range got.Sources[1].Issues {
		if issue == "duplicate_label" {
			duplicateIssueCount++
		}
	}
	if duplicateIssueCount != 1 {
		t.Fatalf("duplicate issue count=%d; issues=%v", duplicateIssueCount, got.Sources[1].Issues)
	}
}

func TestSystemJobsProjectionClassifiesOnlyKnownLabels(t *testing.T) {
	for _, test := range []struct {
		label     string
		wantTitle string
		wantType  string
	}{
		{label: "com.rencrow.lyrics-collector", wantTitle: "歌詞収集", wantType: "job"},
		{label: "com.rencrow.fxembed-daily", wantTitle: "FxEmbedの取得確認", wantType: "job"},
		{label: "com.rencrow.comfyui.housekeeping", wantTitle: "ComfyUI保守", wantType: "job"},
		{label: "com.rencrow.log-rotate-guard", wantTitle: "ログ整理", wantType: "job"},
		{label: "com.rencrow.resilience-stack-monitor", wantTitle: "監視・起動基盤", wantType: "execution_mechanism"},
		{label: "com.rencrow.ren2-supervisor", wantTitle: "監視・起動基盤", wantType: "execution_mechanism"},
		{label: "com.rencrow.future-collector", wantTitle: "com.rencrow.future-collector", wantType: "unknown"},
	} {
		gotTitle, gotType := knownSystemJobPresentation(test.label)
		if gotTitle != test.wantTitle || gotType != test.wantType {
			t.Errorf("presentation(%q)=(%q,%q), want (%q,%q)", test.label, gotTitle, gotType, test.wantTitle, test.wantType)
		}
	}
}

func TestSystemJobsKeepAliveDoesNotMoveKnownJobsOrInferFromSchedules(t *testing.T) {
	keepAlive := true
	known := []struct {
		label    string
		typeName string
	}{
		{label: "com.rencrow.lyrics-collector", typeName: "job"},
		{label: "com.rencrow.fxembed-daily", typeName: "job"},
		{label: "com.rencrow.comfyui.housekeeping", typeName: "job"},
		{label: "com.rencrow.log-rotate-guard", typeName: "job"},
		{label: "com.rencrow.resilience-stack-monitor", typeName: "execution_mechanism"},
		{label: "com.rencrow.ren2-supervisor", typeName: "execution_mechanism"},
	}
	records := make([]SystemJobRecord, 0, len(known)+3)
	for _, item := range known {
		records = append(records, SystemJobRecord{
			Domain: "user/501", Label: item.label, Source: "launchd_plist", SourceID: "user_launch_agents",
			DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "none"}, DisabledState: "enabled",
			LoadedState: "unloaded", RuntimeState: "not_running", KeepAlive: keepAlive,
		})
	}
	records = append(records,
		SystemJobRecord{Domain: "user/501", Label: "com.rencrow.future-interval", Source: "launchd_plist", SourceID: "user_launch_agents", DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "interval", IntervalSeconds: systemJobsInt64(60)}, DisabledState: "enabled", LoadedState: "unloaded", RuntimeState: "not_running", KeepAlive: keepAlive},
		SystemJobRecord{Domain: "user/501", Label: "com.rencrow.future-malformed", Source: "launchd_plist", SourceID: "user_launch_agents", DefinitionState: "malformed", Schedule: SystemJobSchedule{Kind: "none"}, DisabledState: "enabled", LoadedState: "unloaded", RuntimeState: "not_running", KeepAlive: keepAlive},
		SystemJobRecord{Domain: "user/501", Label: "com.rencrow.future-unknown-schedule", Source: "launchd_plist", SourceID: "user_launch_agents", DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "unknown"}, DisabledState: "enabled", LoadedState: "unloaded", RuntimeState: "not_running", KeepAlive: keepAlive},
	)
	reader := &systemJobsReaderStub{snapshot: SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "available",
		Sources: []SystemJobsSource{{ID: "user_launch_agents", Domain: "user/501", State: "available", Candidates: len(records), Records: len(records), Issues: []string{}}},
		Records: records,
	}}
	projected, err := normalizeSystemJobsSnapshot(reader.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range known {
		if projected.Records[index].RecordType != item.typeName || !projected.Records[index].KeepAlive {
			t.Fatalf("known label was moved or its explicit KeepAlive evidence lost: want %q/%q, got %+v", item.label, item.typeName, projected.Records[index])
		}
	}
	for _, record := range projected.Records[len(known):] {
		if record.RecordType != "unknown" || record.KeepAlive {
			t.Fatalf("KeepAlive was inferred outside the observed no-schedule case: %+v", record)
		}
	}
}

func TestSystemJobsProjectionRejectsInvalidOptionalObservationValues(t *testing.T) {
	base := SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "partial",
		Sources: []SystemJobsSource{{ID: "user_launch_agents", Domain: "user/501", State: "partial", Candidates: 1, Records: 1, Issues: []string{"runtime_state_unavailable"}}},
		Records: []SystemJobRecord{{
			Domain: "user/501", Label: "com.rencrow.future-job", Source: "launchd_plist", SourceID: "user_launch_agents",
			DefinitionState: "observed", Schedule: SystemJobSchedule{Kind: "none"}, DisabledState: "unknown",
			LoadedState: "unloaded", RuntimeState: "unknown",
		}},
	}
	tests := []struct {
		name   string
		mutate func(*SystemJobRecord)
	}{
		{name: "invalid disabled reason for known state", mutate: func(record *SystemJobRecord) { record.DisabledState, record.DisabledReason = "enabled", "not_reported" }},
		{name: "invalid disabled reason", mutate: func(record *SystemJobRecord) { record.DisabledReason = "guessed" }},
		{name: "runtime observation outside record domain", mutate: func(record *SystemJobRecord) {
			record.RuntimeObservations = []SystemJobRuntimeObservation{{Domain: "user/502", State: "unloaded"}}
		}},
		{name: "unknown runtime observation state", mutate: func(record *SystemJobRecord) {
			record.RuntimeObservations = []SystemJobRuntimeObservation{{Domain: "user/501", State: "probably_running"}}
		}},
		{name: "duplicate runtime observation domain", mutate: func(record *SystemJobRecord) {
			record.RuntimeObservations = []SystemJobRuntimeObservation{{Domain: "user/501", State: "unloaded"}, {Domain: "user/501", State: "unavailable"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.Sources = append([]SystemJobsSource(nil), base.Sources...)
			snapshot.Records = append([]SystemJobRecord(nil), base.Records...)
			test.mutate(&snapshot.Records[0])
			if _, err := normalizeSystemJobsSnapshot(snapshot); err == nil {
				t.Fatal("invalid optional observation value was accepted")
			}
		})
	}
}

func TestSystemJobsLabelPatternAllowsSafeBoundedASCIIVariants(t *testing.T) {
	labels := []string{
		"com.rencrow.future_job",
		"COM.RENCROW.Future--Job",
		"com.rencrow." + strings.Repeat("a", 243),
	}
	for _, label := range labels {
		if !systemJobLabelPattern.MatchString(label) {
			t.Errorf("safe launchd label %q rejected", label)
		}
	}
	if systemJobLabelPattern.MatchString("com.rencrow."+strings.Repeat("a", 244)) ||
		systemJobLabelPattern.MatchString("com.rencrow.invalid/name") ||
		systemJobLabelPattern.MatchString("com.rencrow.") {
		t.Fatal("unsafe, empty, or over-bound label accepted")
	}
}

func newSystemJobsRequest(method, target, client, profile, token, remote string) *http.Request {
	request := httptest.NewRequest(method, "http://127.0.0.1"+target, nil)
	request.RemoteAddr = remote
	if client != "" {
		request.Header.Set("X-RenCrow-Client", client)
	}
	if profile != "" {
		request.Header.Set("X-RenCrow-Interaction-Profile", profile)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func systemJobsInt64(value int64) *int64 {
	return &value
}
