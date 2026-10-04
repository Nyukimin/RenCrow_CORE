package storagehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	persistsandbox "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
)

func TestSandboxGroupClosedOperationsAndTypedRoundTrip(t *testing.T) {
	f := newSandboxGroupFixture(t, t.TempDir())
	want := map[string]bool{
		sandboxOpSave: true, sandboxOpList: false, sandboxOpFind: false,
		sandboxOpSaveArtifact: true, sandboxOpListArtifacts: false, sandboxOpFindArtifact: false,
		sandboxOpSavePromotion: true, sandboxOpListPromotions: false, sandboxOpFindPromotion: false,
		sandboxOpSaveGate: true, sandboxOpListGates: false, sandboxOpFindGate: false,
	}
	got := map[string]bool{}
	for _, v := range f.host.specs {
		if v.Group == GroupSandbox {
			got[v.Op] = v.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ops=%v want=%v", got, want)
	}
	for op, mutating := range want {
		if mutating {
			if _, ok := f.host.recoverable[opKey(GroupSandbox, op)]; !ok {
				t.Fatalf("%s not recoverable", op)
			}
		}
	}
	sandbox, artifact, promotion, gate := sandboxGroupItems()
	ctx := context.Background()
	if err := f.client.SaveSandbox(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SaveSandboxArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SavePromotionRequest(ctx, promotion); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SavePromotionGateLog(ctx, gate); err != nil {
		t.Fatal(err)
	}
	if items, err := f.client.ListSandboxes(ctx, 10); err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], sandbox) {
		t.Fatalf("sandboxes=%#v err=%v", items, err)
	}
	if items, err := f.client.ListSandboxArtifacts(ctx, 10); err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], artifact) {
		t.Fatalf("artifacts=%#v err=%v", items, err)
	}
	if items, err := f.client.ListPromotionRequests(ctx, 10); err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], promotion) {
		t.Fatalf("promotions=%#v err=%v", items, err)
	}
	if items, err := f.client.ListPromotionGateLogs(ctx, 10); err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], gate) {
		t.Fatalf("gates=%#v err=%v", items, err)
	}
	if v, found, err := f.client.FindSandboxByID(ctx, sandbox.SandboxID); err != nil || !found || !reflect.DeepEqual(v, sandbox) {
		t.Fatalf("find sandbox=%#v %v %v", v, found, err)
	}
	if v, found, err := f.client.FindSandboxArtifactByID(ctx, artifact.ArtifactID); err != nil || !found || !reflect.DeepEqual(v, artifact) {
		t.Fatalf("find artifact=%#v %v %v", v, found, err)
	}
	if v, found, err := f.client.FindPromotionRequestByID(ctx, promotion.PromotionID); err != nil || !found || !reflect.DeepEqual(v, promotion) {
		t.Fatalf("find promotion=%#v %v %v", v, found, err)
	}
	if v, found, err := f.client.FindPromotionGateLogByID(ctx, gate.EventID); err != nil || !found || !reflect.DeepEqual(v, gate) {
		t.Fatalf("find gate=%#v %v %v", v, found, err)
	}
}

func TestSandboxAllMutationsRecoverLostResponseAndPartialGateRetry(t *testing.T) {
	sandbox, artifact, promotion, gate := sandboxGroupItems()
	tests := []struct {
		name  string
		save  func(context.Context, *SandboxClient) error
		count func(context.Context, *SandboxClient) (int, error)
	}{
		{"sandbox", func(ctx context.Context, c *SandboxClient) error { return c.SaveSandbox(ctx, sandbox) }, func(ctx context.Context, c *SandboxClient) (int, error) {
			v, e := c.ListSandboxes(ctx, 10)
			return len(v), e
		}},
		{"artifact", func(ctx context.Context, c *SandboxClient) error { return c.SaveSandboxArtifact(ctx, artifact) }, func(ctx context.Context, c *SandboxClient) (int, error) {
			v, e := c.ListSandboxArtifacts(ctx, 10)
			return len(v), e
		}},
		{"promotion", func(ctx context.Context, c *SandboxClient) error { return c.SavePromotionRequest(ctx, promotion) }, func(ctx context.Context, c *SandboxClient) (int, error) {
			v, e := c.ListPromotionRequests(ctx, 10)
			return len(v), e
		}},
		{"gate", func(ctx context.Context, c *SandboxClient) error { return c.SavePromotionGateLog(ctx, gate) }, func(ctx context.Context, c *SandboxClient) (int, error) {
			v, e := c.ListPromotionGateLogs(ctx, 10)
			return len(v), e
		}},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSandboxGroupFixture(t, t.TempDir())
			opID := "sandbox-recover-" + string(rune('a'+i))
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			if err := test.save(context.Background(), f.client); sandboxGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("lost err=%v", err)
			}
			f.restart(t)
			f.rpc.opID = opID
			if err := test.save(context.Background(), f.client); err != nil {
				t.Fatal(err)
			}
			n, err := test.count(context.Background(), f.client)
			if err != nil || n != 1 {
				t.Fatalf("count=%d err=%v", n, err)
			}
		})
	}

	t.Run("promotion then gate partial retry", func(t *testing.T) {
		f := newSandboxGroupFixture(t, t.TempDir())
		ctx := context.Background()
		if err := f.client.SavePromotionRequest(ctx, promotion); err != nil {
			t.Fatal(err)
		}
		f.restart(t)
		if _, found, err := f.client.FindPromotionGateLogByID(ctx, gate.EventID); err != nil || found {
			t.Fatalf("premature gate found=%v err=%v", found, err)
		}
		if err := f.client.SavePromotionGateLog(ctx, gate); err != nil {
			t.Fatal(err)
		}
		if _, found, err := f.client.FindPromotionRequestByID(ctx, promotion.PromotionID); err != nil || !found {
			t.Fatalf("promotion found=%v err=%v", found, err)
		}
		if _, found, err := f.client.FindPromotionGateLogByID(ctx, gate.EventID); err != nil || !found {
			t.Fatalf("gate found=%v err=%v", found, err)
		}
	})
}

type sandboxGroupFixture struct {
	root, databasePath string
	owner              *persistsandbox.SQLiteStore
	host               *Handler
	server             *httptest.Server
	rpc                *Client
	client             *SandboxClient
}

func newSandboxGroupFixture(t *testing.T, root string) *sandboxGroupFixture {
	t.Helper()
	f := &sandboxGroupFixture{root: root, databasePath: filepath.Join(root, "sandbox.db")}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}
func (f *sandboxGroupFixture) open(t *testing.T, reuse bool) {
	t.Helper()
	var err error
	f.owner, err = persistsandbox.NewSQLiteStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "sandbox-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterSandboxGroup(f.host, f.owner); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuse {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "sandbox-test-token", HTTPClient: f.server.Client()})
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
	f.client = NewSandboxClient(f.rpc)
}
func (f *sandboxGroupFixture) close() {
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
func (f *sandboxGroupFixture) restart(t *testing.T) { t.Helper(); f.close(); f.open(t, true) }
func sandboxGroupItems() (domainsandbox.SandboxRecord, domainsandbox.SandboxArtifact, domainsandbox.PromotionRequest, domainsandbox.PromotionGateLog) {
	now := time.Date(2026, 10, 4, 4, 0, 0, 0, time.UTC)
	s := domainsandbox.SandboxRecord{SandboxID: "sandbox-1", Type: "worktree", Path: "workspace/sandbox-1", Status: domainsandbox.SandboxStatusActive, CreatedAt: now}
	a := domainsandbox.SandboxArtifact{ArtifactID: "artifact-1", SandboxID: s.SandboxID, Type: "diff", FilePath: "evidence/diff.patch", Status: "ready", CreatedAt: now.Add(time.Second)}
	p := domainsandbox.PromotionRequest{PromotionID: "promotion-1", SandboxID: s.SandboxID, TargetPath: "target.go", DiffPath: a.FilePath, TestResultPath: "evidence/test.txt", Reason: "verified", RollbackPlanPath: "evidence/rollback.txt", CreatedAt: now.Add(2 * time.Second)}
	g := domainsandbox.PromotionGateLog{EventID: "gate-1", PromotionID: p.PromotionID, GateStatus: domainsandbox.GateStatusPassed, Reason: "passed", CreatedAt: now.Add(3 * time.Second)}
	return s, a, p, g
}
func sandboxGroupErrorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}
