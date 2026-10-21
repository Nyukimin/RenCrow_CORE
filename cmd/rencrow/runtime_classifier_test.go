package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	routinginfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeClassifierOffPreservesPrimaryAndCreatesNoShadowResources(t *testing.T) {
	cfg := classifierlab.DefaultConfig()
	primary := &runtimeClassifierTestPrimary{}
	resourceDir := filepath.Join(t.TempDir(), "must-not-be-created")
	cfg.Recording.Directory = resourceDir
	var candidateFactoryCalls, recorderFactoryCalls atomic.Int32

	wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		primary,
		cfg,
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) {
			candidateFactoryCalls.Add(1)
			return &runtimeClassifierTestCandidate{}, nil
		},
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) {
			recorderFactoryCalls.Add(1)
			return &runtimeClassifierTestRecorder{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != primary || shadow != nil || cancel != nil {
		t.Fatalf("off wiring = (%T, %v, %v), want original classifier and no lifecycle", wrapped, shadow, cancel)
	}
	if candidateFactoryCalls.Load() != 0 || recorderFactoryCalls.Load() != 0 {
		t.Fatalf("off constructed resources: candidate=%d recorder=%d", candidateFactoryCalls.Load(), recorderFactoryCalls.Load())
	}
	if _, err := os.Stat(resourceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("off created recording directory: stat err=%v", err)
	}
}

func TestRuntimeClassifierOffUsesExistingProviderAndPrompt(t *testing.T) {
	provider := &runtimeClassifierTestProvider{response: "CODE2"}
	const prompt = "unchanged classifier prompt"
	wrapped, shadow, cancel, err := buildRuntimeClassifier(provider, prompt, classifierlab.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if shadow != nil || cancel != nil {
		t.Fatalf("off mode returned shadow resources: shadow=%v cancel=%v", shadow, cancel)
	}
	input := runtimeClassifierTestTurn(t, "implement this")
	got, err := wrapped.Classify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Route != routing.RouteCODE2 || got.Confidence != 0.7 {
		t.Fatalf("primary decision=%+v, want existing CODE2/0.7", got)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.calls.Load())
	}
	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	if request.SystemPrompt != prompt {
		t.Fatalf("system prompt=%q, want unchanged prompt %q", request.SystemPrompt, prompt)
	}
	if request.MaxTokens != 8 || request.Temperature != 0.3 || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
		t.Fatalf("existing classifier request changed: %+v", request)
	}
}

func TestRuntimeShadowNilPrimaryDoesNotCreateCandidate(t *testing.T) {
	var candidateFactories, recorderFactories atomic.Int32
	wrapper, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		nil,
		runtimeClassifierTestShadowConfig(t.TempDir()),
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) {
			candidateFactories.Add(1)
			return &runtimeClassifierTestCandidate{}, nil
		},
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) {
			recorderFactories.Add(1)
			return &runtimeClassifierTestRecorder{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if wrapper != nil || shadow != nil || cancel != nil || candidateFactories.Load() != 0 || recorderFactories.Load() != 0 {
		t.Fatalf("nil primary constructed shadow path: wrapper=%v shadow=%v cancel=%v factories=%d/%d", wrapper, shadow, cancel, candidateFactories.Load(), recorderFactories.Load())
	}
}

func TestRuntimeShadowWrapsExistingClassifierAndRecordsWithoutMessageText(t *testing.T) {
	provider := &runtimeClassifierTestProvider{response: "CODE2"}
	primary := routinginfra.NewLLMClassifier(provider, "same classifier prompt")
	cfg := runtimeClassifierTestShadowConfig(t.TempDir())
	candidate := &runtimeClassifierTestCandidate{results: make(chan classifierlab.CandidateInput, 1)}
	recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
	wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		primary,
		cfg,
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) { return candidate, nil },
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := shadow.Close(ctx); err != nil {
			t.Errorf("close shadow classifier: %v", err)
		}
	})

	const privateMessage = "PRIVATE_CLASSIFIER_MESSAGE_7342"
	input := runtimeClassifierTestTurn(t, privateMessage)
	decision, err := wrapped.Classify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Route != routing.RouteCODE2 || decision.Confidence != 0.7 || len(decision.Evidence) != 1 {
		t.Fatalf("primary result changed by shadow: %+v", decision)
	}
	select {
	case candidateInput := <-candidate.results:
		if candidateInput.Message != privateMessage || candidateInput.RequestID != string(input.TurnID()) {
			t.Fatalf("candidate snapshot=%+v", candidateInput)
		}
	case <-time.After(time.Second):
		t.Fatal("shadow candidate was not called")
	}
	select {
	case record := <-recorder.records:
		if record.Message != "" || record.PrimaryDecision.Route != routing.RouteCODE2 {
			t.Fatalf("unsafe or incorrect comparison record: %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("comparison record was not queued")
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("primary provider calls=%d, want exactly one", provider.calls.Load())
	}
}

func TestRuntimeShadowShutdownCancelsAndJoinsCandidateWithinTwoSeconds(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	candidate := &runtimeClassifierTestCandidate{started: started, stopped: stopped}
	recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
	primary := &runtimeClassifierTestPrimary{decision: routing.NewDecision(routing.RoutePLAN, 0.8, "primary")}
	wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		primary,
		runtimeClassifierTestShadowConfig(t.TempDir()),
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) { return candidate, nil },
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Classify(context.Background(), runtimeClassifierTestTurn(t, "wait for candidate")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("candidate did not start")
	}
	deps := &Dependencies{classifierShadow: shadow, classifierShadowCancel: cancel}
	shutdownDone := make(chan struct{})
	startedAt := time.Now()
	go func() {
		deps.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		if elapsed := time.Since(startedAt); elapsed >= 2*time.Second {
			t.Fatalf("shutdown took %s, want less than 2 seconds", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join the candidate worker within 2 seconds")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("candidate worker was not canceled before shutdown returned")
	}
}

func TestRuntimeShadowShutdownDrainsCandidateBeforeCancel(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
	primary := &runtimeClassifierTestPrimary{decision: routing.NewDecision(routing.RoutePLAN, 0.8, "primary")}
	wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		primary,
		runtimeClassifierTestShadowConfig(t.TempDir()),
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) {
			return &runtimeClassifierTestCandidate{started: started, stopped: stopped, release: release}, nil
		},
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Classify(context.Background(), runtimeClassifierTestTurn(t, "drain candidate")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("candidate did not start")
	}
	shutdownDone := make(chan struct{})
	go func() {
		(&Dependencies{classifierShadow: shadow, classifierShadowCancel: cancel}).Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		t.Fatal("shutdown canceled candidate before its bounded drain completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join the drained candidate within 2 seconds")
	}
	select {
	case record := <-recorder.records:
		if record.PrimaryDecision.Route != routing.RoutePLAN {
			t.Fatalf("drained record primary route=%s, want PLAN", record.PrimaryDecision.Route)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate result was not recorded during shutdown drain")
	}
	if stats := shadow.Stats(); stats.Enqueued != 1 || stats.ShutdownDropped != 0 {
		t.Fatalf("graceful shutdown stats=%+v, want one enqueued and no shutdown drops", stats)
	}
}

func TestRuntimeShadowPreservesMioRoutingPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		ruleMatch bool
		wantRoute routing.Route
	}{
		{name: "explicit command", message: "/plan write a plan", wantRoute: routing.RoutePLAN},
		{name: "catalog intent", message: "東野圭吾の小説を教えて", wantRoute: routing.RouteCHAT},
		{name: "rule dictionary", message: "matched by test rule", ruleMatch: true, wantRoute: routing.RouteCODE4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &runtimeClassifierTestPrimary{decision: routing.NewDecision(routing.RouteWILD, 0.99, "classifier must not win")}
			candidate := &runtimeClassifierTestCandidate{results: make(chan classifierlab.CandidateInput, 1)}
			recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
			wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
				primary,
				runtimeClassifierTestShadowConfig(t.TempDir()),
				func(classifierlab.Config) (classifierlab.CandidateClassifier, error) { return candidate, nil },
				func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			rules := &runtimeClassifierTestRules{match: test.ruleMatch}
			mio := agent.NewMioAgent(nil, wrapped, rules, nil, nil, nil)
			decision, err := mio.DecideAction(context.Background(), runtimeClassifierTestTurn(t, test.message))
			if err != nil {
				t.Fatal(err)
			}
			if decision.Route != test.wantRoute {
				t.Fatalf("Mio decision=%+v, want route %s", decision, test.wantRoute)
			}
			if primary.calls.Load() != 0 || candidate.calls.Load() != 0 || shadow.Stats().Enqueued != 0 {
				t.Fatalf("priority bypass reached classifier: primary=%d candidate=%d stats=%+v", primary.calls.Load(), candidate.calls.Load(), shadow.Stats())
			}
			if test.name == "explicit command" && rules.calls.Load() != 0 {
				t.Fatalf("explicit command should precede rules; rule calls=%d", rules.calls.Load())
			}
			if test.name == "catalog intent" && rules.calls.Load() != 0 {
				t.Fatalf("catalog intent should precede generic rules; rule calls=%d", rules.calls.Load())
			}
			if test.ruleMatch && rules.calls.Load() != 1 {
				t.Fatalf("rule match calls=%d, want 1", rules.calls.Load())
			}
			cancel()
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := shadow.Close(ctx); err != nil {
				t.Fatalf("close shadow: %v", err)
			}
		})
	}
}

func TestRuntimeShadowPreservesMioFallbackForPrimaryFailureAndWeakDecisions(t *testing.T) {
	primaryError := errors.New("legacy classifier failed")
	tests := []struct {
		name     string
		decision routing.Decision
		err      error
	}{
		{name: "primary error", err: primaryError},
		{name: "empty route", decision: routing.Decision{Confidence: 0.9}},
		{name: "low confidence", decision: routing.NewDecision(routing.RoutePLAN, 0.6, "weak")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &runtimeClassifierTestPrimary{decision: test.decision, err: test.err}
			candidate := &runtimeClassifierTestCandidate{}
			recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
			wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
				primary,
				runtimeClassifierTestShadowConfig(t.TempDir()),
				func(classifierlab.Config) (classifierlab.CandidateClassifier, error) { return candidate, nil },
				func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			mio := agent.NewMioAgent(nil, wrapped, &runtimeClassifierTestRules{}, nil, nil, nil)
			decision, err := mio.DecideAction(context.Background(), runtimeClassifierTestTurn(t, "uncertain request"))
			if err != nil {
				t.Fatalf("Mio returned classifier error instead of fallback: %v", err)
			}
			if decision.Route != routing.RouteCHAT || decision.Confidence != 0.7 {
				t.Fatalf("Mio fallback=%+v, want CHAT/0.7", decision)
			}
			(&Dependencies{classifierShadow: shadow, classifierShadowCancel: cancel}).Shutdown()
			if primary.calls.Load() != 1 || candidate.calls.Load() != 1 {
				t.Fatalf("primary/candidate calls=%d/%d, want exactly one each", primary.calls.Load(), candidate.calls.Load())
			}
		})
	}
}

func TestRuntimeClassifierJSONLIsPrivateAndOmitsMessageText(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "classifier-shadow-output")
	recorder, err := newRuntimeClassifierJSONLRecorder(classifierlab.RecordingConfig{
		Directory: directory, OutputQueueCapacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonl := recorder.(*runtimeClassifierJSONLRecorder)
	const secretText = "PRIVATE_BODY_MUST_NOT_BE_WRITTEN"
	if !recorder.TryRecord(classifierlab.ComparisonRecord{
		RequestID: "turn-1", CorrelationID: "trace-1", Message: secretText,
		PrimaryDecision: classifierlab.PrimaryDecisionSnapshot{Route: routing.RouteCHAT, Reason: "safe"},
	}) {
		t.Fatal("record unexpectedly dropped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jsonl.Close(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(jsonl.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("recording file mode=%#o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(jsonl.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secretText) || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("recording exposed message text or is not one JSONL record: %q", data)
	}
}

func TestRuntimeClassifierJSONLRejectsNonPrivateExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX directory permission bits")
	}
	directory := filepath.Join(t.TempDir(), "shared-output")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newRuntimeClassifierJSONLRecorder(classifierlab.RecordingConfig{
		Directory: directory, OutputQueueCapacity: 1,
	}); err == nil {
		t.Fatal("accepted a recording directory accessible to group/other users")
	}
}

func TestRuntimeClassifierJSONLConcurrentCloseAndDeadlineCleanup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "classifier-shadow-output")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(directory, "close-test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	async, err := classifierlab.NewAsyncRecorder(context.Background(), 1, func(ctx context.Context, _ classifierlab.ComparisonRecord) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	recorder := &runtimeClassifierJSONLRecorder{async: async, file: file, closeDone: make(chan struct{})}
	if !recorder.TryRecord(classifierlab.ComparisonRecord{RequestID: "turn-close"}) {
		t.Fatal("record unexpectedly dropped")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("recorder sink did not start")
	}

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	longCtx, cancelLong := context.WithTimeout(context.Background(), time.Second)
	defer cancelLong()
	results := make(chan error, 2)
	go func() { results <- recorder.Close(shortCtx) }()
	go func() { results <- recorder.Close(longCtx) }()
	first, second := <-results, <-results
	if !errors.Is(first, context.DeadlineExceeded) && !errors.Is(second, context.DeadlineExceeded) {
		t.Fatalf("concurrent close errors = %v, %v; expected the bounded close deadline", first, second)
	}
	select {
	case <-recorder.closeDone:
	case <-time.After(time.Second):
		t.Fatal("background cleanup did not finish after caller deadline")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("recorder file remains open after close cleanup")
	}
}

func TestRuntimeClassifierTestConfigRejectsCandidateAndHybridModes(t *testing.T) {
	for _, mode := range []string{"candidate", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "classifier.yaml")
			if err := os.WriteFile(path, []byte("schema_version: routing-classifier-test/v1\nmode: "+mode+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(classifierlab.ConfigPathEnv, path)
			if _, err := loadRuntimeClassifierTestConfig(); err == nil {
				t.Fatalf("mode %q was accepted by normal runtime config", mode)
			}
		})
	}
}

func TestRuntimeClassifierTerminationSummaryIsAggregateAndMarksTurnsUnmeasured(t *testing.T) {
	stats := classifierlab.ShadowStats{
		ClassifierReached: 12, Sampled: 5, Enqueued: 4, QueueFullDropped: 1,
		OversizedInputDropped: 2, StaleDropped: 1, Completed: 3,
		CandidateErrors: 1, RecorderDropped: 2, PendingAtShutdown: 1,
		CancelledAtShutdown: 1, TerminationComplete: true,
	}
	encoded, err := runtimeClassifierTerminationJSON(stats)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &summary); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"classifier_reached", "sampled", "enqueued", "dropped_queue_full", "dropped_too_large",
		"dropped_stale", "completed", "candidate_errors", "recording_dropped",
		"pending_at_shutdown", "cancelled_at_shutdown", "outstanding_remaining",
		"rejected_after_shutdown", "termination_complete",
	} {
		if _, ok := summary[field]; !ok {
			t.Errorf("termination summary lacks %q: %s", field, encoded)
		}
	}
	if string(summary["turns_total"]) != "null" || string(summary["turns_total_status"]) != `"unmeasured"` {
		t.Fatalf("turns_total measurement status = %s / %s, want null / unmeasured", summary["turns_total"], summary["turns_total_status"])
	}
	if strings.Contains(encoded, "PRIVATE") || strings.Contains(encoded, "message") || strings.Contains(encoded, "request_id") {
		t.Fatalf("termination summary includes request data: %s", encoded)
	}
}

func TestRuntimeShutdownEmitsClassifierShadowTerminationSummary(t *testing.T) {
	primary := &runtimeClassifierTestPrimary{decision: routing.NewDecision(routing.RouteCHAT, 0.8, "primary")}
	candidate := &runtimeClassifierTestCandidate{}
	recorder := &runtimeClassifierTestRecorder{records: make(chan classifierlab.ComparisonRecord, 1)}
	wrapped, shadow, cancel, err := wrapRuntimeClassifierWithFactories(
		primary,
		runtimeClassifierTestShadowConfig(t.TempDir()),
		func(classifierlab.Config) (classifierlab.CandidateClassifier, error) { return candidate, nil },
		func(classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Classify(context.Background(), runtimeClassifierTestTurn(t, "summary only")); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)
	(&Dependencies{classifierShadow: shadow, classifierShadowCancel: cancel}).Shutdown()

	const prefix = "classifier_shadow_termination="
	entry := logs.String()
	start := strings.Index(entry, prefix)
	if start < 0 {
		t.Fatalf("shutdown omitted aggregate shadow summary: %s", entry)
	}
	entry = entry[start+len(prefix):]
	if end := strings.IndexByte(entry, '\n'); end >= 0 {
		entry = entry[:end]
	}
	entry = strings.TrimSpace(entry)
	var summary map[string]json.RawMessage
	if err := json.Unmarshal([]byte(entry), &summary); err != nil {
		t.Fatalf("decode shutdown summary %q: %v", entry, err)
	}
	if string(summary["classifier_reached"]) != "1" || string(summary["sampled"]) != "1" ||
		string(summary["enqueued"]) != "1" || string(summary["turns_total_status"]) != `"unmeasured"` {
		t.Fatalf("shutdown summary counters/status=%s", entry)
	}
	if strings.Contains(entry, "summary only") || strings.Contains(entry, "request_id") {
		t.Fatalf("shutdown summary contains request data: %s", entry)
	}
}

type runtimeClassifierTestProvider struct {
	response string
	calls    atomic.Int32
	mu       sync.Mutex
	request  llm.GenerateRequest
}

func (p *runtimeClassifierTestProvider) Name() string { return "runtime-classifier-test" }

func (p *runtimeClassifierTestProvider) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.request = request
	p.mu.Unlock()
	return llm.GenerateResponse{Content: p.response}, nil
}

type runtimeClassifierTestPrimary struct {
	decision routing.Decision
	err      error
	calls    atomic.Int32
}

func (p *runtimeClassifierTestPrimary) Classify(context.Context, conversation.TurnInput) (routing.Decision, error) {
	p.calls.Add(1)
	return p.decision, p.err
}

type runtimeClassifierTestCandidate struct {
	calls   atomic.Int32
	results chan classifierlab.CandidateInput
	started chan struct{}
	stopped chan struct{}
	release <-chan struct{}
}

func (c *runtimeClassifierTestCandidate) ClassifyCandidate(ctx context.Context, input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
	c.calls.Add(1)
	if c.started != nil {
		close(c.started)
	}
	if c.results != nil {
		c.results <- input
	}
	if c.stopped != nil {
		defer close(c.stopped)
		if c.release != nil {
			select {
			case <-c.release:
			case <-ctx.Done():
				return classifierlab.CandidateResult{}, ctx.Err()
			}
		} else {
			<-ctx.Done()
			return classifierlab.CandidateResult{}, ctx.Err()
		}
	}
	return classifierlab.CandidateResult{SchemaVersion: classifierlab.SchemaVersion, RequestID: input.RequestID, PolicySHA256: input.PolicySHA256}, nil
}

type runtimeClassifierTestRecorder struct {
	records chan classifierlab.ComparisonRecord
	dropped atomic.Uint64
}

func (r *runtimeClassifierTestRecorder) TryRecord(record classifierlab.ComparisonRecord) bool {
	if r.records == nil {
		return false
	}
	select {
	case r.records <- record:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *runtimeClassifierTestRecorder) Dropped() uint64 { return r.dropped.Load() }

type runtimeClassifierTestRules struct {
	match bool
	calls atomic.Int32
}

func (r *runtimeClassifierTestRules) Match(conversation.TurnInput) (routing.Route, float64, bool) {
	r.calls.Add(1)
	if r.match {
		return routing.RouteCODE4, 0.91, true
	}
	return "", 0, false
}

func runtimeClassifierTestTurn(t *testing.T, message string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress("viewer", "classifier-test-user")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), message, address)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func runtimeClassifierTestShadowConfig(directory string) classifierlab.Config {
	cfg := classifierlab.DefaultConfig()
	cfg.Mode = classifierlab.ModeShadow
	cfg.ExperimentID = "runtime-integration-test"
	cfg.Candidate = classifierlab.CandidateConfig{
		Endpoint:              "https://127.0.0.1:18791",
		AuthTokenEnv:          "RENCROW_TEST_CANDIDATE_TOKEN",
		TimeoutMS:             1000,
		ExpectedFamily:        "test-family",
		ExpectedModelRevision: strings.Repeat("c", 40),
		ExpectedPolicySHA256:  strings.Repeat("a", 64),
	}
	cfg.Shadow.SampleRate = 1
	cfg.Shadow.QueueCapacity = 2
	cfg.Shadow.Workers = 1
	cfg.Shadow.DrainTimeoutMS = 2000
	cfg.ReferenceWorker.PythonExecutable = "/tmp/reference-python"
	cfg.ReferenceWorker.ModelDirectory = "/tmp/reference-model"
	cfg.Recording.Directory = directory
	cfg.Recording.OutputQueueCapacity = 2
	return cfg
}

var _ agent.Classifier = (*runtimeClassifierTestPrimary)(nil)
