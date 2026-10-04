package storagehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	persistskill "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSkillGovernanceGroupClosedOperationsAndTypedRoundTrip(t *testing.T) {
	f := newSkillGovernanceFixture(t, t.TempDir())
	want := map[string]bool{
		skillOpSaveManifest: true, skillOpListManifests: false,
		skillOpSaveTrigger: true, skillOpListTriggers: false,
		skillOpSaveChange: true, skillOpListChanges: false,
		skillOpSaveContribution: true, skillOpListContributions: false, skillOpFindContribution: false,
		skillOpSaveExternalPR: true, skillOpListExternalPRs: false,
		skillOpSaveTranscript: true, skillOpListTranscripts: false,
	}
	got := map[string]bool{}
	for _, spec := range f.host.specs {
		if spec.Group == GroupSkillGovernance {
			got[spec.Op] = spec.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations=%v want=%v", got, want)
	}
	for op, mutating := range want {
		if mutating {
			if _, ok := f.host.recoverable[opKey(GroupSkillGovernance, op)]; !ok {
				t.Fatalf("%s is not recoverable", op)
			}
		}
	}
	manifest, trigger, change, gate, submit, transcript := skillGovernanceItems()
	ctx := context.Background()
	for name, save := range map[string]func() error{
		"manifest":   func() error { return f.client.SaveSkillManifest(ctx, manifest) },
		"trigger":    func() error { return f.client.SaveSkillTriggerLog(ctx, trigger) },
		"change":     func() error { return f.client.SaveSkillChangeLog(ctx, change) },
		"gate":       func() error { return f.client.SaveContributionGateLog(ctx, gate) },
		"submit":     func() error { return f.client.SaveExternalPRSubmitRecord(ctx, submit) },
		"transcript": func() error { return f.client.SaveCoderTranscriptEntry(ctx, transcript) },
	} {
		if err := save(); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	if v, err := f.client.ListSkillManifests(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.SkillManifest{manifest}) {
		t.Fatalf("manifests=%#v err=%v", v, err)
	}
	if v, err := f.client.ListSkillTriggerLogs(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.SkillTriggerLog{trigger}) {
		t.Fatalf("triggers=%#v err=%v", v, err)
	}
	if v, err := f.client.ListSkillChangeLogs(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.SkillChangeLog{change}) {
		t.Fatalf("changes=%#v err=%v", v, err)
	}
	if v, err := f.client.ListContributionGateLogs(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.ContributionGateLog{gate}) {
		t.Fatalf("gates=%#v err=%v", v, err)
	}
	if v, err := f.client.ListExternalPRSubmitRecords(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.ExternalPRSubmitRecord{submit}) {
		t.Fatalf("submits=%#v err=%v", v, err)
	}
	if v, err := f.client.ListCoderTranscriptEntries(ctx, 10); err != nil || !reflect.DeepEqual(v, []domainskill.CoderTranscriptEntry{transcript}) {
		t.Fatalf("transcripts=%#v err=%v", v, err)
	}
	if v, found, err := f.client.FindContributionGateByID(ctx, gate.EventID); err != nil || !found || !reflect.DeepEqual(v, gate) {
		t.Fatalf("find gate=%#v found=%v err=%v", v, found, err)
	}
}

func TestSkillGovernanceAllMutationsRecoverLostResponseAcrossOwnerAndHandlerReopen(t *testing.T) {
	manifest, trigger, change, gate, submit, transcript := skillGovernanceItems()
	tests := []struct {
		name  string
		save  func(context.Context, *SkillGovernanceClient) error
		count func(context.Context, *SkillGovernanceClient) (int, error)
	}{
		{"manifest", func(ctx context.Context, c *SkillGovernanceClient) error { return c.SaveSkillManifest(ctx, manifest) }, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListSkillManifests(ctx, 10)
			return len(v), e
		}},
		{"trigger", func(ctx context.Context, c *SkillGovernanceClient) error { return c.SaveSkillTriggerLog(ctx, trigger) }, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListSkillTriggerLogs(ctx, 10)
			return len(v), e
		}},
		{"change", func(ctx context.Context, c *SkillGovernanceClient) error { return c.SaveSkillChangeLog(ctx, change) }, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListSkillChangeLogs(ctx, 10)
			return len(v), e
		}},
		{"contribution", func(ctx context.Context, c *SkillGovernanceClient) error { return c.SaveContributionGateLog(ctx, gate) }, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListContributionGateLogs(ctx, 10)
			return len(v), e
		}},
		{"external-pr", func(ctx context.Context, c *SkillGovernanceClient) error {
			return c.SaveExternalPRSubmitRecord(ctx, submit)
		}, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListExternalPRSubmitRecords(ctx, 10)
			return len(v), e
		}},
		{"transcript", func(ctx context.Context, c *SkillGovernanceClient) error {
			return c.SaveCoderTranscriptEntry(ctx, transcript)
		}, func(ctx context.Context, c *SkillGovernanceClient) (int, error) {
			v, e := c.ListCoderTranscriptEntries(ctx, 10)
			return len(v), e
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSkillGovernanceFixture(t, t.TempDir())
			opID := "skill-recover-" + test.name
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			if err := test.save(context.Background(), f.client); skillGovernanceErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("lost response error=%v", err)
			}
			f.restart(t)
			f.rpc.opID = opID
			if err := test.save(context.Background(), f.client); err != nil {
				t.Fatalf("recovered save: %v", err)
			}
			if n, err := test.count(context.Background(), f.client); err != nil || n != 1 {
				t.Fatalf("count=%d err=%v", n, err)
			}
		})
	}
}

func TestSkillGovernanceSameOperationChangedPayloadConflicts(t *testing.T) {
	f := newSkillGovernanceFixture(t, t.TempDir())
	manifest, _, _, _, _, _ := skillGovernanceItems()
	f.rpc.opID = "skill-conflict"
	if err := f.client.SaveSkillManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Name = "changed name"
	if err := f.client.SaveSkillManifest(context.Background(), manifest); !errors.Is(err, persistskill.ErrSkillStorageHostConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
}

type skillGovernanceFixture struct {
	root, databasePath string
	owner              *persistskill.SQLiteStore
	host               *Handler
	server             *httptest.Server
	rpc                *Client
	client             *SkillGovernanceClient
}

func newSkillGovernanceFixture(t *testing.T, root string) *skillGovernanceFixture {
	t.Helper()
	f := &skillGovernanceFixture{root: root, databasePath: filepath.Join(root, "skill.db")}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}
func (f *skillGovernanceFixture) open(t *testing.T, reuse bool) {
	t.Helper()
	var err error
	f.owner, err = persistskill.NewSQLiteStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "skill-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterSkillGovernanceGroup(f.host, f.owner); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuse {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "skill-test-token", HTTPClient: f.server.Client()})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.client = NewSkillGovernanceClient(f.rpc)
}
func (f *skillGovernanceFixture) close() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.host != nil {
		_ = f.host.Close()
		f.host = nil
	}
	if f.owner != nil {
		_ = f.owner.Close()
		f.owner = nil
	}
}
func (f *skillGovernanceFixture) restart(t *testing.T) { t.Helper(); f.close(); f.open(t, true) }

func skillGovernanceItems() (domainskill.SkillManifest, domainskill.SkillTriggerLog, domainskill.SkillChangeLog, domainskill.ContributionGateLog, domainskill.ExternalPRSubmitRecord, domainskill.CoderTranscriptEntry) {
	now := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
	manifest := domainskill.SkillManifest{SkillID: "core.pr-readiness", Name: "PR Readiness", Scope: domainskill.ScopeCore, Version: "1.0.0", Path: "skills/core/pr-readiness", Enabled: true, UpdatedAt: now}
	trigger := domainskill.SkillTriggerLog{EventID: "evt_skill_1", SkillID: manifest.SkillID, TriggerType: "keyword", Status: domainskill.TriggerStatusTriggered, CreatedAt: now.Add(time.Second)}
	change := domainskill.SkillChangeLog{ChangeID: "chg_1", SkillID: manifest.SkillID, OldVersion: "1.0.0", NewVersion: "1.0.1", EvalResult: "passed", CreatedAt: now.Add(2 * time.Second)}
	gate := domainskill.ContributionGateLog{EventID: "evt_contrib_1", Repo: "example/repo", ExistingPRsChecked: true, RealProblemVerified: false, GateStatus: domainskill.GateStatusBlocked, CreatedAt: now.Add(3 * time.Second)}
	submit := domainskill.ExternalPRSubmitRecord{ActionID: modulecore.NewActionID(), ContributionEventID: gate.EventID, Repo: gate.Repo, Title: "Fix bug", SubmitStatus: domainskill.ExternalPRSubmitStatusBlocked, FailureReason: "external PR adapter is not configured", CreatedAt: now.Add(4 * time.Second)}
	transcript := domainskill.CoderTranscriptEntry{EventID: "evt_coder_1", TaskID: modulecore.NewTaskID(), Route: "CODE3", Agent: "Coder", Role: "coder", Segment: "plan", Text: "plan", CreatedAt: now.Add(5 * time.Second)}
	return manifest, trigger, change, gate, submit, transcript
}
func skillGovernanceErrorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}
