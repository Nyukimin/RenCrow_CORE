package delegation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	testBuild   = "0123456789abcdef0123456789abcdef01234567"
	secretText  = "SECRET-USER-TEXT-must-not-be-logged"
	secretFinal = "SECRET-FINAL-TEXT-must-not-be-logged"
)

// harnessID makes a well-formed Harness ID of a kind (the client of the real
// protocol checks the schema patterns of every ID it sends or receives).
func harnessID(prefix string) string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return prefix + "_" + id.String()
}

// fakeClient is a scripted Harness client. Like the real client it refuses an
// input that does not satisfy the protocol schema (protocol.Encode) before
// "sending" it, so a payload CORE builds is checked against the real schema.
type fakeClient struct {
	mu   sync.Mutex
	caps protocol.CapabilitiesResult
	done chan struct{}
	note chan client.Notification

	opens  []protocol.SessionOpenInput
	starts []protocol.StartInput

	// The context state each send saw: a send must not be cut short by the
	// cancellation of the turn, and has a deadline of its own.
	openCtxErrs      []error
	startCtxErrs     []error
	startHadDeadline []bool

	awaits   []awaitCall
	shutdown []protocol.ShutdownInput
	aborts   int

	onOpen     func(n int, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error)
	onStart    func(n int, in protocol.StartInput) (protocol.StartResult, error)
	onAwait    func(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error)
	onShutdown func(in protocol.ShutdownInput) error

	thread, task, run, receipt string
}

type awaitCall struct {
	runID string
	opts  client.AwaitOptions
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		caps:    protocol.CapabilitiesResult{ProtocolVersion: protocol.ProtocolVersion, BuildRevision: testBuild},
		done:    make(chan struct{}),
		note:    make(chan client.Notification, 64),
		thread:  harnessID("thr"),
		task:    harnessID("tsk"),
		run:     harnessID("run"),
		receipt: harnessID("rcp"),
	}
}

func (f *fakeClient) Capabilities() protocol.CapabilitiesResult { return f.caps }
func (f *fakeClient) Notifications() <-chan client.Notification { return f.note }
func (f *fakeClient) Done() <-chan struct{}                     { return f.done }

func (f *fakeClient) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.done:
	default:
		close(f.done)
		close(f.note)
	}
}

func (f *fakeClient) Abort() {
	f.mu.Lock()
	f.aborts++
	f.mu.Unlock()
	f.end()
}

func (f *fakeClient) Shutdown(_ context.Context, in protocol.ShutdownInput) error {
	f.mu.Lock()
	f.shutdown = append(f.shutdown, in)
	hook := f.onShutdown
	f.mu.Unlock()
	if hook != nil {
		return hook(in)
	}
	f.end()
	return nil
}

func (f *fakeClient) session() protocol.SessionInfo {
	return protocol.SessionInfo{
		SessionID: harnessID("ses"), ThreadID: f.thread, WorkspacePath: "/w", ContextRevision: 3, ControlRevision: 4,
		Binding: protocol.Binding{Kind: "alias", Selector: "s", ProfileRevision: "r"}, PolicyRef: "p", ExecutionMode: protocol.ModeStructuredOnly,
	}
}

func (f *fakeClient) SessionOpen(ctx context.Context, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
	if _, err := protocol.Encode(in); err != nil {
		return protocol.SessionOpenResult{}, err
	}
	f.mu.Lock()
	f.opens = append(f.opens, in)
	f.openCtxErrs = append(f.openCtxErrs, ctx.Err())
	n, hook := len(f.opens), f.onOpen
	f.mu.Unlock()
	if hook != nil {
		return hook(n, in)
	}
	return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: f.session()}, nil
}

func (f *fakeClient) TurnStart(ctx context.Context, in protocol.StartInput) (protocol.StartResult, error) {
	if _, err := protocol.Encode(in); err != nil {
		return protocol.StartResult{}, err
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.starts = append(f.starts, in)
	f.startCtxErrs = append(f.startCtxErrs, ctx.Err())
	f.startHadDeadline = append(f.startHadDeadline, hasDeadline)
	n, hook := len(f.starts), f.onStart
	f.mu.Unlock()
	if hook != nil {
		return hook(n, in)
	}
	return f.startResult(in), nil
}

func (f *fakeClient) startResult(in protocol.StartInput) protocol.StartResult {
	return protocol.StartResult{
		ReceiptID: f.receipt, Accepted: true, SessionID: harnessID("ses"), ThreadID: in.ThreadID, TurnID: harnessID("turn"),
		TaskID: f.task, RunID: f.run, TraceID: harnessID("trc"), EffectiveLimits: in.Limits,
	}
}

func (f *fakeClient) AwaitRun(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error) {
	f.mu.Lock()
	f.awaits = append(f.awaits, awaitCall{runID: runID, opts: opts})
	hook := f.onAwait
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, runID, opts)
	}
	return f.runResult("completed", "passed"), nil
}

func (f *fakeClient) runResult(status, verification string) protocol.RunResult {
	return protocol.RunResult{
		RunID: f.run, TaskID: f.task, Status: status, Code: "", FinalText: secretFinal,
		Verification: protocol.Verification{Status: verification, EvidenceIDs: []string{}},
		EvidenceIDs:  []string{}, UnresolvedActionIDs: []string{},
	}
}

// push delivers a confirmed Event notification, as the Harness would.
func (f *fakeClient) push(event protocol.Event) {
	f.note <- client.Notification{Kind: client.NotificationEvent, Event: &event}
}

// recorder is an in-memory ActionRecorder that records what the delegation did
// and issues IDs the way actionmanager does.
type recorder struct {
	mu        sync.Mutex
	created   []actionmanager.CreateInput
	completed []completion
	createErr error
	actionID  modulecore.ActionID
	attemptID modulecore.AttemptID
}

type completion struct {
	actionID  modulecore.ActionID
	attemptID modulecore.AttemptID
	attempt   domainaction.AttemptStatus
	action    domainaction.Status
	summary   string
}

func newRecorder() *recorder {
	return &recorder{actionID: modulecore.NewActionID(), attemptID: modulecore.NewAttemptID()}
}

func (r *recorder) CreateAction(_ context.Context, in actionmanager.CreateInput) (domainaction.Action, domainaction.Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.createErr
	}
	r.created = append(r.created, in)
	now := time.Now().UTC()
	action := domainaction.Action{ActionID: r.actionID, TaskID: in.TaskID, RunID: in.RunID, Kind: in.Kind, Name: in.Name,
		Status: domainaction.StatusOpen, CurrentAttemptID: r.attemptID, CreatedAt: now, UpdatedAt: now}
	attempt := domainaction.Attempt{AttemptID: r.attemptID, ActionID: r.actionID, StartReason: domainaction.AttemptStartReasonFirst,
		Status: domainaction.AttemptStatusRunning, StartedAt: now}
	return action, attempt, nil
}

func (r *recorder) CompleteAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, attempt domainaction.AttemptStatus, action domainaction.Status, summary string) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err // a cancelled context cannot record: the delegation must not use one
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, completion{actionID, attemptID, attempt, action, summary})
	return domainaction.Action{}, domainaction.Attempt{}, nil
}

// logSink collects JSON log lines.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) write(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *logSink) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// events returns the decoded entries whose event is name.
func (l *logSink) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range l.all() {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a log line is not one JSON object: %q: %v", line, err)
		}
		if entry["event"] == name {
			out = append(out, entry)
		}
	}
	return out
}

// testDeployment builds Settings and a Runtime around a scripted client factory.
type testDeployment struct {
	t        *testing.T
	settings Settings
	rec      *recorder
	log      *logSink
	runtime  *Runtime

	mu      sync.Mutex
	clients []*fakeClient
	configs []client.Config
	startFn func(n int, cfg client.Config) (*fakeClient, error)
	now     time.Time
}

func newDeployment(t *testing.T) *testDeployment {
	t.Helper()
	root := t.TempDir()
	d := &testDeployment{
		t: t,
		settings: Settings{
			HarnessBinary: filepath.Join(root, "bin", "rencrow-harness"), HarnessConfig: filepath.Join(root, "etc", "harness.json"),
			ExpectedBuildRevision: testBuild,
			Workspace:             Workspace{Path: root, PolicyRef: "workspace-write", ExecutionMode: protocol.ModeStructuredOnly},
			Binding:               Binding{Selector: "shiro-worker-exec", ProfileRevision: "rev-1", ExecutionRole: "worker"},
			Limits:                protocol.Limits{MaxModelSteps: 10, MaxToolCallsPerStep: 8, DeadlineSeconds: 1800, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 32},
			RestartCooldown:       time.Minute,
		},
		rec: newRecorder(),
		log: &logSink{},
		now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
	runtime, err := NewRuntime(d.settings, d.rec, Options{
		Start:   d.start,
		Now:     func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
		Log:     d.log.write,
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() {
		_ = runtime.Close(context.Background())
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.clients {
			c.end()
		}
	})
	return d
}

func (d *testDeployment) start(_ context.Context, cfg client.Config) (Client, error) {
	d.mu.Lock()
	d.configs = append(d.configs, cfg)
	n := len(d.configs) // the 1-based number of this start call
	fn := d.startFn
	d.mu.Unlock()
	var c *fakeClient
	if fn != nil {
		var err error
		if c, err = fn(n, cfg); err != nil {
			return nil, err
		}
	} else {
		c = newFakeClient()
	}
	d.mu.Lock()
	d.clients = append(d.clients, c)
	d.mu.Unlock()
	return c, nil
}

func (d *testDeployment) startCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.configs)
}

func (d *testDeployment) client(i int) *fakeClient {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clients[i]
}

func (d *testDeployment) advance(by time.Duration) {
	d.mu.Lock()
	d.now = d.now.Add(by)
	d.mu.Unlock()
}

// turn builds the input, the execution identity context and the typed messages
// of one delegation.
func newTurn(t *testing.T, text string) (context.Context, conversation.TurnInput, []llm.Message) {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), text, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), input.RootTaskID(), modulecore.NewRunID(), input.TraceID())
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: "system", Content: "character prompt", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "stable context", Type: llm.PromptContextStable},
		{Role: "system", Content: "recall memory", Type: llm.PromptContextRecall},
		{Role: "system", Content: "variable context", Type: llm.PromptContextVariable},
		{Role: "user", Content: text, Type: llm.PromptContextUser},
	}
	return ctx, input, messages
}

func mustContain(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must contain %q: %s", what, want, text)
		}
	}
}

func mustNotContain(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if bad != "" && strings.Contains(text, bad) {
			t.Fatalf("%s must not contain %q: %s", what, bad, text)
		}
	}
}
