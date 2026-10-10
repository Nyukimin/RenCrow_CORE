package delegation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

func TestNewRuntimeRejectsUnusableSettings(t *testing.T) {
	good := newDeployment(t).settings
	cases := map[string]func(*Settings){
		"relative binary":     func(s *Settings) { s.HarnessBinary = "rencrow-harness" },
		"relative config":     func(s *Settings) { s.HarnessConfig = "harness.json" },
		"relative workspace":  func(s *Settings) { s.Workspace.Path = "work" },
		"no build revision":   func(s *Settings) { s.ExpectedBuildRevision = "" },
		"no policy":           func(s *Settings) { s.Workspace.PolicyRef = "" },
		"isolated mode":       func(s *Settings) { s.Workspace.ExecutionMode = protocol.ModeIsolated },
		"no selector":         func(s *Settings) { s.Binding.Selector = "" },
		"no execution role":   func(s *Settings) { s.Binding.ExecutionRole = "" },
		"control in revision": func(s *Settings) { s.Binding.ProfileRevision = "a\nb" },
	}
	for name, mutate := range cases {
		settings := good
		mutate(&settings)
		if _, err := NewRuntime(settings, newRecorder(), &fakeTaskOwner{}, Options{}); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%s: err = %v, want ErrInvalidSettings", name, err)
		}
	}
	if _, err := NewRuntime(good, nil, &fakeTaskOwner{}, Options{}); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("no Action owner: err = %v, want ErrInvalidSettings", err)
	}
	if _, err := NewRuntime(good, newRecorder(), nil, Options{}); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("no Task owner: err = %v, want ErrInvalidSettings", err)
	}
}

func TestAdmissionStartsTheHarnessOnceAndPassesExplicitStartSettings(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "テストを直して")

	for i := 0; i < 3; i++ {
		if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
	}
	if d.startCalls() != 1 {
		t.Fatalf("the Harness must be started once and reused, got %d starts", d.startCalls())
	}
	cfg := d.configs[0]
	if cfg.Binary != d.settings.HarnessBinary || cfg.ConfigPath != d.settings.HarnessConfig {
		t.Fatalf("binary and config must be the configured absolute paths: %+v", cfg)
	}
	if cfg.Dir != filepath.Dir(d.settings.HarnessConfig) || !filepath.IsAbs(cfg.Dir) {
		t.Fatalf("the working directory must be the absolute directory of the Harness config, got %q", cfg.Dir)
	}
	if len(cfg.Env) != 0 {
		t.Fatalf("the child must get an empty environment, got %v", cfg.Env)
	}
	if cfg.ClientName != ClientName || cfg.ClientVersion != ClientVersion {
		t.Fatalf("client identity = %q %q", cfg.ClientName, cfg.ClientVersion)
	}
	required := strings.Join(cfg.RequireCapabilities, ",")
	for _, want := range append(client.DefaultRequiredCapabilities(), "tool.runtime", "tool.runtime.structured_only") {
		if !strings.Contains(required, want) {
			t.Fatalf("required capabilities must include %q: %s", want, required)
		}
	}
	if strings.Contains(required, "tool.runtime.trusted_host") {
		t.Fatalf("only the configured execution mode is required: %s", required)
	}
	if cfg.Stderr == nil {
		t.Fatal("the child's diagnostics must be recorded, not discarded")
	}
}

func TestAdmissionRequiresTheToolRuntimeOfTheConfiguredMode(t *testing.T) {
	d := newDeployment(t)
	d.settings.Workspace.ExecutionMode = protocol.ModeTrustedHost
	runtime, err := NewRuntime(d.settings, d.rec, d.tasks, Options{Start: d.start, Log: d.log.write})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	ctx, input, _ := newTurn(t, "x")
	if err := runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.configs[0].RequireCapabilities, ","); !strings.Contains(got, "tool.runtime.trusted_host") {
		t.Fatalf("trusted_host must require its Tool runtime: %s", got)
	}
}

func TestAdmissionFailsClosedWhenTheHarnessCannotStart(t *testing.T) {
	cases := map[string]struct {
		err  error
		code string
	}{
		"start failure":        {err: errors.New("exec: no such file /secret/path/rencrow-harness"), code: codeStartFailed},
		"capabilities missing": {err: client.ErrIncompatible, code: codeIncompatible},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) { return nil, tc.err }
			ctx, input, _ := newTurn(t, "x")

			err := d.runtime.AdmitNativeCoding(ctx, input)
			if !errors.Is(err, agent.ErrNativeCodingBlocked) {
				t.Fatalf("a Harness that cannot start must block the turn, got %v", err)
			}
			mustContain(t, "refusal", err.Error(), tc.code)
			mustNotContain(t, "refusal", err.Error(), "/secret/path", "no such file")
			if refused := d.log.events(t, eventAdmissionRefused); len(refused) != 1 {
				t.Fatalf("a refused admission is logged once, got %d", len(refused))
			}
			for _, line := range d.log.all() {
				mustNotContain(t, "log", line, "/secret/path")
			}
		})
	}
}

func TestAdmissionDoesNotRestartAFailingHarnessForEveryTurnAndRetriesAfterTheCooldown(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(n int, _ client.Config) (*fakeClient, error) {
		if n == 1 {
			return nil, errors.New("boom")
		}
		return newFakeClient(), nil
	}
	ctx, input, _ := newTurn(t, "x")

	if err := d.runtime.AdmitNativeCoding(ctx, input); !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("first admission: %v", err)
	}
	// Within the cooldown the refusal is immediate: no second start.
	d.advance(10 * time.Second)
	err := d.runtime.AdmitNativeCoding(ctx, input)
	if !errors.Is(err, agent.ErrNativeCodingBlocked) || d.startCalls() != 1 {
		t.Fatalf("during the cooldown: err=%v starts=%d", err, d.startCalls())
	}
	mustContain(t, "cooldown refusal", err.Error(), codeCooldown, codeStartFailed)
	// After the cooldown the Harness is started again, and works.
	d.advance(2 * time.Minute)
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
	if d.startCalls() != 2 {
		t.Fatalf("starts = %d, want 2", d.startCalls())
	}
}

func TestAdmissionRefusesAnotherBuildOfTheHarnessAndLeavesNoChildBehind(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.caps.BuildRevision = "ffffffffffffffffffffffffffffffffffffffff"
		return c, nil
	}
	ctx, input, _ := newTurn(t, "x")

	err := d.runtime.AdmitNativeCoding(ctx, input)
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a Harness of another build must block the turn: %v", err)
	}
	mustContain(t, "refusal", err.Error(), codeBuildMismatch)
	if c := d.client(0); c.aborts != 1 {
		t.Fatalf("the mismatching child must be aborted, aborts = %d", c.aborts)
	}
	select {
	case <-d.client(0).Done():
	default:
		t.Fatal("the mismatching client must be gone")
	}
}

func TestAdmissionRejectsWhatTheHarnessCannotReceiveAndBlocksWithoutAWorkspace(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "これを直して")

	withAttachment := input.WithAttachments([]attachment.Attachment{{Kind: attachment.KindImage, Filename: "a.png"}})
	if err := d.runtime.AdmitNativeCoding(ctx, withAttachment); !errors.Is(err, agent.ErrNativeCodingRejected) {
		t.Fatalf("an attachment must be rejected: %v", err)
	}
	if err := d.runtime.AdmitNativeCoding(ctx, input.WithMessageText("   ")); !errors.Is(err, agent.ErrNativeCodingRejected) {
		t.Fatalf("an empty request must be rejected: %v", err)
	}
	if d.startCalls() != 0 {
		t.Fatal("a rejected turn must not start the Harness")
	}

	d.runtime.settings.Workspace.Path = filepath.Join(t.TempDir(), "absent")
	err := d.runtime.AdmitNativeCoding(ctx, input)
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a missing workspace must block the turn: %v", err)
	}
	mustContain(t, "refusal", err.Error(), codeWorkspaceAbsent)
	if d.startCalls() != 0 {
		t.Fatal("an absent workspace must be refused before the Harness is started")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.runtime.settings.Workspace.Path = file
	if err := d.runtime.AdmitNativeCoding(ctx, input); !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a workspace that is not a directory must block the turn: %v", err)
	}
}

func TestAdmissionStartsANewHarnessAfterTheConnectionEnded(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "x")
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatal(err)
	}
	d.client(0).end() // the child exited
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatalf("admission after an ended connection: %v", err)
	}
	if d.startCalls() != 2 {
		t.Fatalf("a new Harness must be started once, starts = %d", d.startCalls())
	}
	if len(d.log.events(t, eventClientEnded)) != 1 {
		t.Fatal("the end of the connection is logged")
	}
}

func TestConcurrentAdmissionsStartExactlyOneHarness(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "x")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- d.runtime.AdmitNativeCoding(ctx, input)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("admission: %v", err)
		}
	}
	if d.startCalls() != 1 {
		t.Fatalf("concurrent admissions must share one start, got %d", d.startCalls())
	}
}

func TestCloseStopsInTwoStagesIsIdempotentAndBlocksLaterWork(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "x")
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatal(err)
	}
	c := d.client(0)

	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(c.shutdown) != 1 || c.shutdown[0].Mode != "cancel" || c.shutdown[0].DeadlineSeconds <= 0 {
		t.Fatalf("the orderly stage must cancel the Runs within a deadline: %+v", c.shutdown)
	}
	if c.aborts != 1 {
		t.Fatalf("the immediate stage must always follow, aborts = %d", c.aborts)
	}
	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("a second Close must be a no-op: %v", err)
	}
	if len(c.shutdown) != 1 {
		t.Fatal("Close must be idempotent")
	}
	if err := d.runtime.AdmitNativeCoding(ctx, input); !errors.Is(err, agent.ErrNativeCodingBlocked) || d.startCalls() != 1 {
		t.Fatalf("a closed Runtime must not start a Harness again: %v starts=%d", err, d.startCalls())
	}
}

func TestCloseAbortsWhenTheOrderlyShutdownFails(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "x")
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatal(err)
	}
	c := d.client(0)
	c.onShutdown = func(protocol.ShutdownInput) error { return client.ErrShutdownTimeout }
	if err := d.runtime.Close(context.Background()); !errors.Is(err, client.ErrShutdownTimeout) {
		t.Fatalf("Close = %v, want the shutdown timeout", err)
	}
	if c.aborts != 1 {
		t.Fatal("a failed orderly shutdown must still end with the kill")
	}
}

func TestWarmReportsTheRefusalOfTheNextAdmission(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) { return nil, errors.New("down") }
	if err := d.runtime.Warm(context.Background()); !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("Warm = %v", err)
	}
}
