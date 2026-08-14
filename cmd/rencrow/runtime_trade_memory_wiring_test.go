package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	configpolicy "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/policybundle"
	policydecisionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/policydecision"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type runtimeTradeInvestmentClientStub struct {
	memory            runtimeTradeMemoryClientStub
	ledger            runtimeTradeLedgerClientStub
	status            moduletrade.PrivateStatus
	statusErr         error
	statusWait        bool
	statusCalls       int
	statusCorrelation string
	statusStarted     time.Time
	statusDeadline    time.Time
}

func (stub *runtimeTradeInvestmentClientStub) ReadSourceRecord(ctx context.Context, id string) (moduletrade.SourceRecordReadResponse, error) {
	return stub.memory.ReadSourceRecord(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) CollectSource(ctx context.Context, id string) (moduletrade.SourceRecordWriteResponse, error) {
	return stub.memory.CollectSource(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) ReadLearningCandidate(ctx context.Context, id string) (moduletrade.LearningCandidateReadResponse, error) {
	return stub.memory.ReadLearningCandidate(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) ImportLearningCandidate(ctx context.Context, id string) (moduletrade.LearningCandidateWriteResponse, error) {
	return stub.memory.ImportLearningCandidate(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) ReadMarketSnapshot(ctx context.Context, id string) (moduletrade.MarketSnapshotReadResponse, error) {
	return stub.ledger.ReadMarketSnapshot(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) ImportMarketSnapshot(ctx context.Context, runID, instrumentID, tradeDate string) (moduletrade.MarketSnapshotWriteResponse, error) {
	return stub.memory.ImportMarketSnapshot(ctx, runID, instrumentID, tradeDate)
}

func (stub *runtimeTradeInvestmentClientStub) ReadReplayDecision(ctx context.Context, id string) (moduletrade.ReplayDecisionReadResponse, error) {
	return stub.ledger.ReadReplayDecision(ctx, id)
}

func (stub *runtimeTradeInvestmentClientStub) RecordReplayDecision(ctx context.Context, runID, instrumentID, tradeDate, action string) (moduletrade.ReplayDecisionWriteResponse, error) {
	return stub.memory.RecordReplayDecision(ctx, runID, instrumentID, tradeDate, action)
}

func (stub *runtimeTradeInvestmentClientStub) ReadPortfolioSnapshot(ctx context.Context) (moduletrade.PortfolioSnapshotReadResponse, error) {
	return stub.memory.ReadPortfolioSnapshot(ctx)
}

func (stub *runtimeTradeInvestmentClientStub) EnsurePortfolioInitialized(ctx context.Context) (moduletrade.PortfolioSnapshotWriteResponse, error) {
	return stub.memory.EnsurePortfolioInitialized(ctx)
}

func (stub *runtimeTradeInvestmentClientStub) ShadowOutcomeReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error) {
	return stub.ledger.ShadowOutcomeReport(ctx, correlationID, studyID)
}

func (stub *runtimeTradeInvestmentClientStub) Evaluate(context.Context, string, moduletrade.PolicyEvaluationRequest) (moduletrade.PrivatePolicyEvaluation, error) {
	return moduletrade.PrivatePolicyEvaluation{}, nil
}

func (stub *runtimeTradeInvestmentClientStub) RecordShadowObservation(context.Context, string, moduletrade.ShadowObservationRequest) (moduletrade.PrivateShadowObservation, error) {
	return moduletrade.PrivateShadowObservation{}, nil
}

func (stub *runtimeTradeInvestmentClientStub) Status(ctx context.Context, correlationID string) (moduletrade.PrivateStatus, error) {
	stub.statusCalls++
	stub.statusCorrelation = correlationID
	stub.statusStarted = time.Now()
	stub.statusDeadline, _ = ctx.Deadline()
	if stub.statusWait {
		<-ctx.Done()
		return moduletrade.PrivateStatus{}, ctx.Err()
	}
	if stub.statusErr != nil {
		return moduletrade.PrivateStatus{}, stub.statusErr
	}
	return stub.status, nil
}

func TestRuntimeTradeInvestmentWiringInstallsExactlyTwelveRoutes(t *testing.T) {
	client := &runtimeTradeInvestmentClientStub{status: runtimeTradeInvestmentReadyStatus()}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	snapshots, decisions := runtimeTradeInvestmentPolicyDependencies(t)
	var reasons []string
	factoryCalls := 0
	started := time.Now()
	err := wireRuntimeTradeInvestmentRoutesWithOptions(context.Background(), runtimeTradeInvestmentConfig(), recall, write, snapshots, decisions, runtimeTradeInvestmentWiringOptions{
		NewClient: func(*config.Config) (runtimeTradeInvestmentClient, error) {
			factoryCalls++
			return client, nil
		},
		ProbeTimeout: 100 * time.Millisecond,
		Unavailable: func(format string, args ...any) {
			reasons = append(reasons, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("wire routes: %v", err)
	}
	if factoryCalls != 1 || client.statusCalls != 1 || client.statusCorrelation != runtimeTradeInvestmentStartupCorrelation {
		t.Fatalf("factory/status calls=%d/%d", factoryCalls, client.statusCalls)
	}
	if client.statusDeadline.IsZero() || client.statusDeadline.Before(started) || client.statusDeadline.Sub(client.statusStarted) > 100*time.Millisecond {
		t.Fatalf("status deadline=%v started=%v", client.statusDeadline, client.statusStarted)
	}
	if len(reasons) != 0 {
		t.Fatalf("unexpected unavailable reasons=%v", reasons)
	}
	if got := len(recall.Snapshot()); got != 6 {
		t.Fatalf("recall route count=%d want=6", got)
	}
	if got := len(write.Snapshot()); got != 6 {
		t.Fatalf("write route count=%d want=6", got)
	}
	if client.status.Ready {
		t.Fatal("test must prove status.Ready is not required")
	}
	if client.status.Portfolio.Status != "not_initialized" {
		t.Fatal("test must prove not_initialized portfolio is allowed")
	}
}

func TestRuntimeTradeInvestmentWiringUnavailableLeavesRegistriesEmpty(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		snapshots  *configpolicy.Store
		decisions  *policydecisionpersistence.JSONLStore
		client     *runtimeTradeInvestmentClientStub
		factoryErr error
	}{
		{name: "disabled", cfg: &config.Config{}},
		{name: "missing-policy", cfg: runtimeTradeInvestmentConfig()},
		{name: "client-config", cfg: runtimeTradeInvestmentConfig(), factoryErr: errors.New("invalid")},
		{name: "status-unreachable", cfg: runtimeTradeInvestmentConfig(), client: &runtimeTradeInvestmentClientStub{statusErr: errors.New("unreachable")}},
		{name: "status-invalid", cfg: runtimeTradeInvestmentConfig(), client: &runtimeTradeInvestmentClientStub{status: moduletrade.PrivateStatus{}}},
		{name: "status-correlation-mismatch", cfg: runtimeTradeInvestmentConfig(), client: &runtimeTradeInvestmentClientStub{status: func() moduletrade.PrivateStatus {
			status := runtimeTradeInvestmentReadyStatus()
			status.CorrelationID = "different-correlation"
			return status
		}()}},
		{name: "memory-unready", cfg: runtimeTradeInvestmentConfig(), client: &runtimeTradeInvestmentClientStub{status: func() moduletrade.PrivateStatus {
			status := runtimeTradeInvestmentReadyStatus()
			status.Dependencies.MemoryOwner = "unavailable"
			return status
		}()}},
		{name: "ledger-unready", cfg: runtimeTradeInvestmentConfig(), client: &runtimeTradeInvestmentClientStub{status: func() moduletrade.PrivateStatus {
			status := runtimeTradeInvestmentReadyStatus()
			status.Dependencies.Ledger = "unconfigured"
			return status
		}()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recall := newRuntimeDataRecallRegistry()
			write := newRuntimeDataWriteRegistry()
			var reasons []string
			snapshots, decisions := runtimeTradeInvestmentPolicyDependencies(t)
			if test.name == "missing-policy" {
				snapshots, decisions = nil, nil
			}
			factoryCalls := 0
			err := wireRuntimeTradeInvestmentRoutesWithOptions(context.Background(), test.cfg, recall, write, snapshots, decisions, runtimeTradeInvestmentWiringOptions{
				NewClient: func(*config.Config) (runtimeTradeInvestmentClient, error) {
					factoryCalls++
					if test.factoryErr != nil {
						return nil, test.factoryErr
					}
					return test.client, nil
				},
				ProbeTimeout: 20 * time.Millisecond,
				Unavailable: func(format string, args ...any) {
					reasons = append(reasons, fmt.Sprintf(format, args...))
				},
			})
			if err != nil {
				t.Fatalf("wire unavailable case: %v", err)
			}
			if len(recall.Snapshot()) != 0 || len(write.Snapshot()) != 0 {
				t.Fatalf("unavailable case installed routes: recall=%#v write=%#v", recall.Snapshot(), write.Snapshot())
			}
			if len(reasons) != 1 {
				t.Fatalf("unavailable reasons=%v want exactly one", reasons)
			}
			if test.name == "disabled" || test.name == "missing-policy" {
				if factoryCalls != 0 {
					t.Fatalf("factory called before prerequisites: %d", factoryCalls)
				}
			}
		})
	}
}

func TestRuntimeTradeInvestmentWiringProbeTimeoutIsBounded(t *testing.T) {
	client := &runtimeTradeInvestmentClientStub{statusWait: true}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	snapshots, decisions := runtimeTradeInvestmentPolicyDependencies(t)
	var reasons []string
	started := time.Now()
	err := wireRuntimeTradeInvestmentRoutesWithOptions(context.Background(), runtimeTradeInvestmentConfig(), recall, write, snapshots, decisions, runtimeTradeInvestmentWiringOptions{
		NewClient:    func(*config.Config) (runtimeTradeInvestmentClient, error) { return client, nil },
		ProbeTimeout: 10 * time.Millisecond,
		Unavailable: func(format string, args ...any) {
			reasons = append(reasons, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("probe timeout returned fatal error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("status probe exceeded bounded test timeout: %s", elapsed)
	}
	if len(reasons) != 1 || len(recall.Snapshot()) != 0 || len(write.Snapshot()) != 0 {
		t.Fatalf("timeout result reasons=%v recall=%#v write=%#v", reasons, recall.Snapshot(), write.Snapshot())
	}
}

func TestRuntimeTradeInvestmentWiringCommitIsAllOrNoneOnDuplicate(t *testing.T) {
	client := &runtimeTradeInvestmentClientStub{status: runtimeTradeInvestmentReadyStatus()}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := recall.Register(runtimeTradeMemoryStore, "source_record", dataRecallAccessInternal, func(context.Context, tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeDataRecallResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshots, decisions := runtimeTradeInvestmentPolicyDependencies(t)
	err := wireRuntimeTradeInvestmentRoutesWithOptions(context.Background(), runtimeTradeInvestmentConfig(), recall, write, snapshots, decisions, runtimeTradeInvestmentWiringOptions{
		NewClient:    func(*config.Config) (runtimeTradeInvestmentClient, error) { return client, nil },
		ProbeTimeout: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("duplicate route did not return a fatal registration error")
	}
	if got := recall.Snapshot(); len(got) != 1 || got[0].Operation != "source_record" {
		t.Fatalf("partial recall commit after duplicate: %#v", got)
	}
	if got := write.Snapshot(); len(got) != 0 {
		t.Fatalf("partial write commit after duplicate: %#v", got)
	}
}

func runtimeTradeInvestmentConfig() *config.Config {
	return &config.Config{Trade: config.TradeConfig{Enabled: true}}
}

func runtimeTradeInvestmentPolicyDependencies(t *testing.T) (*configpolicy.Store, *policydecisionpersistence.JSONLStore) {
	t.Helper()
	root := t.TempDir()
	snapshots := configpolicy.NewStore(filepath.Join(root, "workspace"))
	decisions, err := policydecisionpersistence.NewJSONLStore(filepath.Join(root, "policy-decisions.jsonl"))
	if err != nil {
		t.Fatalf("policy decision store: %v", err)
	}
	return snapshots, decisions
}

func runtimeTradeInvestmentReadyStatus() moduletrade.PrivateStatus {
	return moduletrade.PrivateStatus{
		ContractVersion: moduletrade.PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   runtimeTradeInvestmentStartupCorrelation,
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Ready:           false,
		KillSwitch:      "ON",
		Dependencies: moduletrade.DependencyStatuses{
			Broker: "disabled", Ledger: "ready", MarketData: "unavailable", MemoryOwner: "ready",
		},
		Policy: moduletrade.PolicyStatus{
			ExecutionMode: "DISABLED", KillSwitch: "ON", BrokerAdapter: "none",
			ModulePolicyRevision: "sha256:module", BinaryContractRevision: "trade-binary/v1",
			Capabilities: map[string]bool{"live_order": false, "paper_order": false, "broker_network": false},
		},
		Portfolio: moduletrade.PortfolioProjection{Status: "not_initialized"},
	}
}
