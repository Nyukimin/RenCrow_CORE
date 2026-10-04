package storagehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	persistadvisor "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
)

func TestAdvisorGroupRegistersOnlyClosedTypedOperations(t *testing.T) {
	root := t.TempDir()
	owner, err := persistadvisor.NewSQLiteStore(filepath.Join(root, "advisor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	host, err := NewHandler(HandlerConfig{Token: "advisor-test-token", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := RegisterAdvisorGroup(host, owner); err != nil {
		t.Fatalf("RegisterAdvisorGroup: %v", err)
	}
	want := map[string]bool{
		"save_advice_run": true, "list_advice_runs": false, "find_advice_run": false,
		"save_advisor_adoption": true, "list_advisor_adoptions": false, "find_advisor_adoption": false,
		"save_advisor_score_snapshot": true, "list_advisor_score_snapshots": false,
		"save_agent_policy_decision": true, "list_agent_policy_decisions": false,
	}
	got := map[string]bool{}
	for _, spec := range host.specs {
		if spec.Group == GroupAdvisor {
			got[spec.Op] = spec.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("advisor operations=%v, want %v", got, want)
	}
	for operation, mutating := range want {
		if mutating {
			if _, ok := host.recoverable[opKey(GroupAdvisor, operation)]; !ok {
				t.Fatalf("mutation %q is not recoverable", operation)
			}
		}
	}
}

func TestAdvisorClientRoundTripsAllCanonicalMethods(t *testing.T) {
	f := newAdvisorGroupFixture(t, t.TempDir())
	ctx := context.Background()
	run, adoption, score, policy := advisorGroupItems()
	if err := f.client.SaveAdviceRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SaveAdvisorAdoption(ctx, adoption); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SaveAdvisorScoreSnapshot(ctx, score); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SaveAgentPolicyDecision(ctx, policy); err != nil {
		t.Fatal(err)
	}
	runs, err := f.client.ListAdviceRuns(ctx, 100000)
	if err != nil || len(runs) != 1 || !reflect.DeepEqual(runs[0], run) {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	adoptions, err := f.client.ListAdvisorAdoptions(ctx, 100000)
	if err != nil || len(adoptions) != 1 || !reflect.DeepEqual(adoptions[0], adoption) {
		t.Fatalf("adoptions=%#v err=%v", adoptions, err)
	}
	scores, err := f.client.ListAdvisorScoreSnapshots(ctx, 100000)
	if err != nil || len(scores) != 1 || !reflect.DeepEqual(scores[0], score) {
		t.Fatalf("scores=%#v err=%v", scores, err)
	}
	policies, err := f.client.ListAgentPolicyDecisions(ctx, 100000)
	if err != nil || len(policies) != 1 || !reflect.DeepEqual(policies[0], policy) {
		t.Fatalf("policies=%#v err=%v", policies, err)
	}
	foundRun, found, err := f.client.FindAdviceRunByID(ctx, run.RunID)
	if err != nil || !found || !reflect.DeepEqual(foundRun, run) {
		t.Fatalf("find run=%#v found=%v err=%v", foundRun, found, err)
	}
	foundAdoption, found, err := f.client.FindAdvisorAdoptionByID(ctx, adoption.AdoptionID)
	if err != nil || !found || !reflect.DeepEqual(foundAdoption, adoption) {
		t.Fatalf("find adoption=%#v found=%v err=%v", foundAdoption, found, err)
	}
	if _, found, err := f.client.FindAdviceRunByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing run found=%v err=%v", found, err)
	}
}

func TestAdvisorAllMutationsRecoverLostResponseAfterOwnerAndHostReopen(t *testing.T) {
	run, adoption, score, policy := advisorGroupItems()
	tests := []struct {
		name string
		save func(context.Context, *AdvisorClient) error
		list func(context.Context, *AdvisorClient) (int, error)
	}{
		{"advice run", func(ctx context.Context, c *AdvisorClient) error { return c.SaveAdviceRun(ctx, run) }, func(ctx context.Context, c *AdvisorClient) (int, error) {
			v, e := c.ListAdviceRuns(ctx, 10)
			return len(v), e
		}},
		{"adoption", func(ctx context.Context, c *AdvisorClient) error { return c.SaveAdvisorAdoption(ctx, adoption) }, func(ctx context.Context, c *AdvisorClient) (int, error) {
			v, e := c.ListAdvisorAdoptions(ctx, 10)
			return len(v), e
		}},
		{"score", func(ctx context.Context, c *AdvisorClient) error { return c.SaveAdvisorScoreSnapshot(ctx, score) }, func(ctx context.Context, c *AdvisorClient) (int, error) {
			v, e := c.ListAdvisorScoreSnapshots(ctx, 10)
			return len(v), e
		}},
		{"policy", func(ctx context.Context, c *AdvisorClient) error { return c.SaveAgentPolicyDecision(ctx, policy) }, func(ctx context.Context, c *AdvisorClient) (int, error) {
			v, e := c.ListAgentPolicyDecisions(ctx, 10)
			return len(v), e
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newAdvisorGroupFixture(t, t.TempDir())
			opID := "advisor-recovery-" + string(rune('a'+index))
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			if err := test.save(context.Background(), f.client); advisorGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("lost response err=%v", err)
			}
			f.restart(t)
			f.rpc.opID = opID
			if err := test.save(context.Background(), f.client); err != nil {
				t.Fatalf("recover: %v", err)
			}
			count, err := test.list(context.Background(), f.client)
			if err != nil || count != 1 {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

func TestAdvisorAdoptionOwnerAtomicReplayAndConflict(t *testing.T) {
	f := newAdvisorGroupFixture(t, t.TempDir())
	_, adoption, _, _ := advisorGroupItems()
	f.rpc.opID = "advisor-adoption-first"
	replayed, err := f.client.SaveAdvisorAdoptionWithReceipt(context.Background(), adoption)
	if err != nil || replayed {
		t.Fatalf("first replayed=%v err=%v", replayed, err)
	}
	f.rpc.opID = "advisor-adoption-second"
	replayed, err = f.client.SaveAdvisorAdoptionWithReceipt(context.Background(), adoption)
	if err != nil || !replayed {
		t.Fatalf("second replayed=%v err=%v", replayed, err)
	}
	changed := adoption
	changed.Reason = "changed"
	f.rpc.opID = "advisor-adoption-third"
	if _, err := f.client.SaveAdvisorAdoptionWithReceipt(context.Background(), changed); !errors.Is(err, persistadvisor.ErrAdvisorStorageHostConflict) {
		t.Fatalf("changed adoption err=%v", err)
	}
}

type advisorGroupFixture struct {
	root, databasePath string
	owner              *persistadvisor.SQLiteStore
	host               *Handler
	server             *httptest.Server
	rpc                *Client
	client             *AdvisorClient
}

func newAdvisorGroupFixture(t *testing.T, root string) *advisorGroupFixture {
	t.Helper()
	f := &advisorGroupFixture{root: root, databasePath: filepath.Join(root, "advisor.db")}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}
func (f *advisorGroupFixture) open(t *testing.T, reuse bool) {
	t.Helper()
	var err error
	f.owner, err = persistadvisor.NewSQLiteStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "advisor-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAdvisorGroup(f.host, f.owner); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuse {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "advisor-test-token", HTTPClient: f.server.Client()})
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
	f.client = NewAdvisorClient(f.rpc)
}
func (f *advisorGroupFixture) close() {
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
func (f *advisorGroupFixture) restart(t *testing.T) { t.Helper(); f.close(); f.open(t, true) }

func advisorGroupItems() (domainadvisor.AdviceRunRecord, domainadvisor.AdvisorAdoptionRecord, domainadvisor.AdvisorScoreSnapshot, domainagentprofile.PolicyDecision) {
	now := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	run := domainadvisor.AdviceRunRecord{RunID: "advisor-run-1", RequestID: "request-1", RequestedByAgent: "shiro", AdvisorID: domainadvisor.AdvisorCodex, Status: domainadvisor.AdviceStatus(domainadvisor.StatusCompleted), StartedAt: now, FinishedAt: now.Add(time.Second), LatencyMillis: 1000}
	adoption := domainadvisor.AdvisorAdoptionRecord{AdoptionID: "advisor-adoption-1", RunID: run.RunID, AdvisorID: run.AdvisorID, AdoptedByAgent: "shiro", Adopted: true, Outcome: "success", CreatedAt: now.Add(2 * time.Second)}
	score := domainadvisor.AdvisorScoreSnapshot{SnapshotID: "advisor-score-1", AdvisorID: run.AdvisorID, WindowStart: now.Add(-24 * time.Hour), WindowEnd: now, RequestCount: 1, CompletedCount: 1, Score: 1, CreatedAt: now.Add(3 * time.Second)}
	policy := domainagentprofile.PolicyDecision{DecisionID: "policy-1", AgentID: "shiro", Action: "advise", Decision: domainagentprofile.PolicyAllowed, Reason: "allowed", CreatedAt: now.Add(4 * time.Second)}
	return run, adoption, score, policy
}

func advisorGroupErrorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}
