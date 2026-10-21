package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunIsolatedMioTrialRoutesChatsCommitsThenPublishes(t *testing.T) {
	route := classifierlab.LabelCHAT
	policySHA := strings.Repeat("a", 64)
	candidate := &e2eCandidateStub{result: classifierlab.CandidateResult{
		SchemaVersion: classifierlab.SchemaVersion, RequestID: "chat-001", Status: classifierlab.CandidateClassified,
		Route: &route, PolicySHA256: policySHA,
	}}
	classifier := e2eCandidateDecisionAdapter{candidate: candidate, policySHA: policySHA, requestID: "chat-001"}
	provider := &e2eStubProvider{response: "fake final response", chunks: []string{"  ", "first visible chunk"}}
	item := e2eCase("chat-001", "hello, please answer normally")

	observation, err := runIsolatedMioTrial(context.Background(), item, "candidate", classifier, provider)
	if err != nil {
		t.Fatalf("run isolated trial: %v", err)
	}
	if candidate.calls != 1 || provider.calls != 1 {
		t.Fatalf("candidate calls=%d provider calls=%d; want one each", candidate.calls, provider.calls)
	}
	if observation.FinalRoute != "CHAT" || observation.DecisionSource != domainrouting.EvidenceSourceClassifier || !observation.ChatCalled || !observation.Committed || !observation.FinalPublished {
		t.Fatalf("Mio path observation = %+v", observation)
	}
	if observation.TTFTMS == nil || observation.FinalMS == nil || *observation.FinalMS < *observation.TTFTMS {
		t.Fatalf("invalid callback/sink timing: TTFT=%v final=%v", observation.TTFTMS, observation.FinalMS)
	}
	if observation.ExternalEffectCall != 0 || observation.Spies.externalCalls() != 0 {
		t.Fatalf("unexpected external effect spy calls: %+v", observation.Spies)
	}
	if observation.Spies.UserMemoryWrites != 0 || observation.Spies.PersonaWrites != 0 {
		t.Fatalf("unexpected memory/persona writes: %+v", observation.Spies)
	}
	if observation.WorkerConstructed || observation.CoderConstructed || observation.AdoptionEligible || observation.HarnessKind != "test_only_fake_provider" {
		t.Fatalf("fake harness boundary = %+v", observation)
	}
	if _, err := json.Marshal(observation); err == nil {
		t.Fatal("fake-provider observation was serializable as experiment evidence")
	}
	if _, err := os.Stat(observation.trialDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trial database directory should be removed after the trial, stat err=%v", err)
	}
	if !strings.HasPrefix(observation.trialDir, os.TempDir()+string(os.PathSeparator)) {
		t.Fatalf("trial database escaped OS temp directory: %q", observation.trialDir)
	}
}

func TestRunIsolatedMioTrialRecordsNonChatWithoutExecutionAndIsolatesTrials(t *testing.T) {
	item := e2eCase("route-001", "analyze this")
	classifier := &e2eTestDecisionClassifier{result: domainrouting.NewDecision(domainrouting.RouteANALYZE, 1.0, "test route")}
	first, err := runIsolatedMioTrial(context.Background(), item, "legacy", classifier, nil)
	if err != nil {
		t.Fatalf("run first route-only trial: %v", err)
	}
	second, err := runIsolatedMioTrial(context.Background(), item, "candidate", classifier, nil)
	if err != nil {
		t.Fatalf("run second route-only trial: %v", err)
	}
	if first.FinalRoute != "ANALYZE" || second.FinalRoute != "ANALYZE" || first.ChatCalled || second.ChatCalled || first.FinalPublished || second.FinalPublished {
		t.Fatalf("non-CHAT route was executed: first=%+v second=%+v", first, second)
	}
	if first.trialDir == second.trialDir || first.trialDir == "" || second.trialDir == "" {
		t.Fatalf("trials did not receive distinct private directories: %q / %q", first.trialDir, second.trialDir)
	}
	if _, err := os.Stat(first.trialDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first trial directory was not cleaned: %v", err)
	}
	if _, err := os.Stat(second.trialDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second trial directory was not cleaned: %v", err)
	}
}

func TestE2EDenialSpiesRejectAndCountExternalAccess(t *testing.T) {
	spies := &e2eDenialSpies{}
	checks := []struct {
		name string
		call func() error
	}{
		{"tool", func() error { _, err := (e2eToolSpy{spies}).ExecuteV2(context.Background(), "tool", nil); return err }},
		{"tool_catalog", func() error { _, err := (e2eToolSpy{spies}).ListTools(context.Background()); return err }},
		{"mcp", func() error {
			_, err := (e2eMCPSpy{spies}).CallTool(context.Background(), "server", "tool", nil)
			return err
		}},
		{"mcp_catalog", func() error { _, err := (e2eMCPSpy{spies}).ListTools(context.Background(), "server"); return err }},
		{"kb_read", func() error {
			_, err := (e2eKBSpy{spies}).SearchKB(context.Background(), "test", "query", 1)
			return err
		}},
		{"kb_write", func() error { return (e2eKBSpy{spies}).SaveWebSearchToKB(context.Background(), "test", "query", nil) }},
		{"search_cache_read", func() error {
			_, _, err := (e2eSearchCacheSpy{spies}).GetFreshWebSearchCache(context.Background(), "query")
			return err
		}},
		{"search_cache_write", func() error {
			return (e2eSearchCacheSpy{spies}).SaveWebSearchCache(context.Background(), "query", nil, 0)
		}},
		{"user_memory_create", func() error {
			_, err := spies.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{})
			return err
		}},
		{"user_memory_update", func() error {
			_, err := spies.UpdateUserMemoryState(context.Background(), "id", "state", "reason")
			return err
		}},
		{"user_memory_forget", func() error { _, err := spies.ForgetUserMemory(context.Background(), "id", "reason"); return err }},
		{"user_memory_supersede", func() error {
			_, err := spies.SupersedeUserMemory(context.Background(), "old", "new", "reason")
			return err
		}},
		{"persona_write", func() error { return spies.WritePersona("must not persist") }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, errE2EExternalEffectDenied) {
				t.Fatalf("effect error = %v; want denial", err)
			}
		})
	}
	counts := spies.snapshot()
	if counts.externalCalls() != len(checks) {
		t.Fatalf("external attempts=%d; want %d, counts=%+v", counts.externalCalls(), len(checks), counts)
	}
	if _, err := spies.ListUserMemories(context.Background(), "test", "", false, 10); err != nil {
		t.Fatalf("local empty memory read should be safe: %v", err)
	}
	if got := spies.snapshot().LocalMemoryReads; got != 1 {
		t.Fatalf("isolated local memory read count=%d; want 1", got)
	}
}

func TestE2ECandidateGateAdapterIsMarkedTestOnlyAndRouteValidated(t *testing.T) {
	route := classifierlab.LabelCHAT
	candidate := &e2eCandidateStub{result: classifierlab.CandidateResult{
		SchemaVersion: classifierlab.SchemaVersion, RequestID: "case-1", Status: classifierlab.CandidateClassified,
		Route: &route, PolicySHA256: strings.Repeat("a", 64),
	}}
	adapter := e2eCandidateDecisionAdapter{candidate: candidate, policySHA: strings.Repeat("a", 64), requestID: "case-1"}
	turn := newE2ETurn(t, "hello")
	decision, err := adapter.Classify(context.Background(), turn)
	if err != nil {
		t.Fatalf("candidate adapter: %v", err)
	}
	if decision.Route != domainrouting.RouteCHAT || decision.Confidence != 1 || decision.Reason != "test_adapter_gate_only" || len(decision.Evidence) != 1 || decision.Evidence[0].Reason != "test_adapter_gate_only" {
		t.Fatalf("test-only decision gate = %+v", decision)
	}
	if candidate.calls != 1 || candidate.input.SchemaVersion != classifierlab.SchemaVersion || candidate.input.Message != "hello" {
		t.Fatalf("candidate wire input = %+v, calls=%d", candidate.input, candidate.calls)
	}

	invalid := route
	invalid = classifierlab.RouteLabel("NOT_A_ROUTE")
	adapter.candidate = &e2eCandidateStub{result: classifierlab.CandidateResult{Status: classifierlab.CandidateClassified, Route: &invalid}}
	if _, err := adapter.Classify(context.Background(), turn); err == nil {
		t.Fatal("invalid candidate route was accepted")
	}
}

func TestCompareIsolatedE2ETTFTExcludesWrongRoutes(t *testing.T) {
	legacyTTFT, candidateTTFT := 100.0, 20.0
	base := e2eTrialObservation{
		CaseID: "verified-chat", Origin: "real_conversation", LabelStatus: "verified", ExpectedRoutes: []string{"CHAT"},
	}
	legacy := base
	legacy.Variant, legacy.FinalRoute, legacy.TTFTMS = "legacy", "ANALYZE", &legacyTTFT
	candidate := base
	candidate.Variant, candidate.FinalRoute, candidate.TTFTMS = "candidate", "CHAT", &candidateTTFT
	comparison := compareIsolatedE2ETTFT([]e2eTrialObservation{legacy, candidate})
	if comparison.EligibleCases != 1 || comparison.PairedCases != 0 || comparison.WrongRouteExcluded != 1 || comparison.CandidateMeanMS != nil {
		t.Fatalf("wrong route was treated as speedup: %+v", comparison)
	}

	legacy.FinalRoute = "CHAT"
	comparison = compareIsolatedE2ETTFT([]e2eTrialObservation{legacy, candidate})
	if comparison.PairedCases != 0 {
		t.Fatalf("uncommitted output entered the TTFT pair: %+v", comparison)
	}
	legacy.ChatCalled, candidate.ChatCalled = true, true
	legacy.Committed, candidate.Committed = true, true
	legacy.FinalPublished, candidate.FinalPublished = true, true
	comparison = compareIsolatedE2ETTFT([]e2eTrialObservation{legacy, candidate})
	if comparison.PairedCases != 1 || comparison.LegacyMeanMS == nil || comparison.CandidateMeanMS == nil {
		t.Fatalf("same-case verified CHAT pair was not compared: %+v", comparison)
	}
	legacy.Origin, candidate.Origin = "synthetic", "synthetic"
	comparison = compareIsolatedE2ETTFT([]e2eTrialObservation{legacy, candidate})
	if comparison.EligibleCases != 0 || comparison.PairedCases != 0 {
		t.Fatalf("synthetic cases entered verified real-conversation comparison: %+v", comparison)
	}
}

func TestE2ETestSinkRejectsPublicationBeforeCommit(t *testing.T) {
	sink := &e2eTestSink{}
	if err := sink.PublishFinal("fake response", false); err == nil {
		t.Fatal("test sink accepted a response before a committed L1 receipt")
	}
	if _, ok := sink.finalPublishedAt(); ok {
		t.Fatal("test sink recorded a final publication before commit")
	}
}

type e2eStubProvider struct {
	calls    int
	response string
	chunks   []string
}

func (p *e2eStubProvider) Generate(_ context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.calls++
	if req.OnToken != nil {
		for _, chunk := range p.chunks {
			req.OnToken(chunk)
		}
	}
	return domainllm.GenerateResponse{Content: p.response}, nil
}
func (*e2eStubProvider) Name() string { return "routing-classifier-e2e-stub" }

type e2eCandidateStub struct {
	result classifierlab.CandidateResult
	input  classifierlab.CandidateInput
	calls  int
}

func (s *e2eCandidateStub) ClassifyCandidate(_ context.Context, input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
	s.calls++
	s.input = input
	return s.result, nil
}

func e2eCase(id, message string) classifierlab.ReplayCase {
	return classifierlab.ReplayCase{
		CaseID: id, Origin: "synthetic", GroupID: id, Split: "test", Message: message,
		ExpectedRoutes: []string{"CHAT"}, LabelStatus: "draft",
	}
}

func newE2ETurn(t *testing.T, message string) domainconversation.TurnInput {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("test", "classifier-e2e")
	if err != nil {
		t.Fatalf("new channel address: %v", err)
	}
	turn, err := domainconversation.NewTurnInput(modulecore.NewTaskID(), message, address)
	if err != nil {
		t.Fatalf("new E2E turn: %v", err)
	}
	return turn
}

var _ domainllm.LLMProvider = (*e2eStubProvider)(nil)
var _ classifierlab.CandidateClassifier = (*e2eCandidateStub)(nil)
