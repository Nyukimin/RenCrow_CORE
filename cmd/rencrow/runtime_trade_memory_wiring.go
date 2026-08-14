package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	configpolicy "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/policybundle"
	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	tradeshadowobservation "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
	policydecisionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/policydecision"
	tradeclient "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tradeclient"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const (
	runtimeTradeInvestmentStartupProbeTimeout = 5 * time.Second
	runtimeTradeInvestmentStartupCorrelation  = "rencrow-startup-investment-memory"
)

// runtimeTradeInvestmentClient is the complete owner boundary needed to
// install the five memory routes and the ledger routes. The concrete
// tradeclient remains responsible for authentication, HTTP and response
// validation; CORE only composes its already-narrow interfaces here.
type runtimeTradeInvestmentClient interface {
	runtimeTradeMemoryClient
	runtimeTradeLedgerClient
	applicationtradepolicy.ModuleEvaluator
	tradeshadowobservation.ModuleRecorder
	Status(context.Context, string) (moduletrade.PrivateStatus, error)
}

var _ runtimeTradeInvestmentClient = (*tradeclient.Client)(nil)

type runtimeTradeInvestmentWiringOptions struct {
	NewClient    func(*config.Config) (runtimeTradeInvestmentClient, error)
	ProbeTimeout time.Duration
	Unavailable  func(string, ...any)
}

// wireRuntimeTradeInvestmentRoutes is the production startup entrypoint. It
// does not advertise static availability: the authenticated owner status is
// checked before any route is committed to the executable registries.
func wireRuntimeTradeInvestmentRoutes(
	ctx context.Context,
	cfg *config.Config,
	recall *runtimeDataRecallRegistry,
	write *runtimeDataWriteRegistry,
	snapshots *configpolicy.Store,
	decisions *policydecisionpersistence.JSONLStore,
) error {
	return wireRuntimeTradeInvestmentRoutesWithOptions(ctx, cfg, recall, write, snapshots, decisions, runtimeTradeInvestmentWiringOptions{
		NewClient:    newRuntimeTradeInvestmentClient,
		ProbeTimeout: runtimeTradeInvestmentStartupProbeTimeout,
		Unavailable:  log.Printf,
	})
}

func newRuntimeTradeInvestmentClient(cfg *config.Config) (runtimeTradeInvestmentClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("TRADE configuration is nil")
	}
	timeout := time.Duration(cfg.Trade.TimeoutMS) * time.Millisecond
	return tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, timeout)
}

func wireRuntimeTradeInvestmentRoutesWithOptions(
	ctx context.Context,
	cfg *config.Config,
	recall *runtimeDataRecallRegistry,
	write *runtimeDataWriteRegistry,
	snapshots *configpolicy.Store,
	decisions *policydecisionpersistence.JSONLStore,
	options runtimeTradeInvestmentWiringOptions,
) error {
	if cfg == nil || !cfg.Trade.Enabled {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "trade_disabled")
		return nil
	}
	if snapshots == nil || decisions == nil {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "policy_dependencies_missing")
		return nil
	}
	if recall == nil || write == nil {
		return fmt.Errorf("TRADE investment data registries are required")
	}
	if options.NewClient == nil {
		return fmt.Errorf("TRADE investment client factory is required")
	}

	client, err := options.NewClient(cfg)
	if err != nil {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "client_configuration_invalid")
		return nil
	}
	if client == nil {
		return fmt.Errorf("TRADE investment client factory returned nil")
	}

	probeTimeout := options.ProbeTimeout
	if probeTimeout <= 0 || probeTimeout > runtimeTradeInvestmentStartupProbeTimeout {
		probeTimeout = runtimeTradeInvestmentStartupProbeTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	status, statusErr := client.Status(probeCtx, runtimeTradeInvestmentStartupCorrelation)
	cancel()
	if statusErr != nil {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "status_probe_failed")
		return nil
	}
	if err := status.ValidateDisabledFoundation(); err != nil {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "status_invalid")
		return nil
	}
	if status.CorrelationID != runtimeTradeInvestmentStartupCorrelation {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "status_correlation_mismatch")
		return nil
	}
	if !status.Dependencies.MemoryOwnerReady() || status.Dependencies.Ledger != "ready" {
		runtimeTradeInvestmentUnavailable(options.Unavailable, "owner_dependencies_unready")
		return nil
	}

	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{
		Snapshots: snapshots,
		Evaluator: client,
		Decisions: decisions,
	})
	if err != nil {
		return fmt.Errorf("construct TRADE policy service: %w", err)
	}
	shadowService, err := tradeshadowobservation.NewService(policyService, client)
	if err != nil {
		return fmt.Errorf("construct TRADE Shadow observation service: %w", err)
	}

	// Stage every route against private registries first. A registration or
	// contract error therefore cannot leave a partial investment route set in
	// the live registries.
	stagedRecall := newRuntimeDataRecallRegistry()
	stagedWrite := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataRecallTradeMemory(stagedRecall, client); err != nil {
		return fmt.Errorf("stage TRADE memory recall routes: %w", err)
	}
	if err := registerRuntimeDataRecallTradeLedger(stagedRecall, client); err != nil {
		return fmt.Errorf("stage TRADE ledger recall route: %w", err)
	}
	if err := registerRuntimeDataWriteTradeMemory(stagedWrite, client); err != nil {
		return fmt.Errorf("stage TRADE memory write routes: %w", err)
	}
	if err := registerRuntimeDataWriteTradeLedger(stagedWrite, shadowService, client); err != nil {
		return fmt.Errorf("stage TRADE ledger write route: %w", err)
	}
	if err := commitRuntimeTradeInvestmentRoutes(recall, write, stagedRecall, stagedWrite); err != nil {
		return fmt.Errorf("commit TRADE investment routes: %w", err)
	}
	return nil
}

func runtimeTradeInvestmentUnavailable(logger func(string, ...any), reason string) {
	if logger != nil {
		logger("TRADE investment routes unavailable: %s", reason)
	}
}

func commitRuntimeTradeInvestmentRoutes(
	recall *runtimeDataRecallRegistry,
	write *runtimeDataWriteRegistry,
	stagedRecall *runtimeDataRecallRegistry,
	stagedWrite *runtimeDataWriteRegistry,
) error {
	if recall == nil || write == nil || stagedRecall == nil || stagedWrite == nil {
		return fmt.Errorf("TRADE investment route registries are nil")
	}
	wantRecall := map[runtimeDataRecallKey]struct{}{
		{store: runtimeTradeMemoryStore, operation: "source_record"}:                          {},
		{store: runtimeTradeMemoryStore, operation: "learning_candidate"}:                     {},
		{store: runtimeTradeMemoryStore, operation: "market_snapshot"}:                        {},
		{store: runtimeTradeMemoryStore, operation: "replay_decision"}:                        {},
		{store: runtimeTradeMemoryStore, operation: "portfolio_snapshot"}:                     {},
		{store: runtimeTradeMemoryStore, operation: runtimeTradeLedgerOutcomeReportOperation}: {},
	}
	wantWrite := map[runtimeDataWriteKey]struct{}{
		{store: runtimeTradeMemoryStore, operation: "collect_source"}:                       {},
		{store: runtimeTradeMemoryStore, operation: "import_learning_candidate"}:            {},
		{store: runtimeTradeMemoryStore, operation: "import_market_snapshot"}:               {},
		{store: runtimeTradeMemoryStore, operation: "record_replay_decision"}:               {},
		{store: runtimeTradeMemoryStore, operation: "ensure_portfolio_initialized"}:         {},
		{store: runtimeTradeMemoryStore, operation: runtimeTradeLedgerObservationOperation}: {},
	}
	if !sameRuntimeTradeRecallRouteSet(stagedRecall.registrations, wantRecall) || !sameRuntimeTradeWriteRouteSet(stagedWrite.registrations, wantWrite) {
		return fmt.Errorf("TRADE investment route set is incomplete")
	}
	if recall.registrations == nil || write.registrations == nil {
		return fmt.Errorf("TRADE target route registry is uninitialized")
	}

	// Both target maps are checked completely before either map is mutated.
	// Startup has no route consumers yet, and the two locks keep each map safe
	// for any concurrent registry snapshot during the commit.
	recall.mu.Lock()
	write.mu.Lock()
	defer write.mu.Unlock()
	defer recall.mu.Unlock()
	for key := range wantRecall {
		if _, exists := recall.registrations[key]; exists {
			return fmt.Errorf("TRADE recall route already registered: %s/%s", key.store, key.operation)
		}
	}
	for key := range wantWrite {
		if _, exists := write.registrations[key]; exists {
			return fmt.Errorf("TRADE write route already registered: %s/%s", key.store, key.operation)
		}
	}
	for key, registration := range stagedRecall.registrations {
		recall.registrations[key] = registration
	}
	for key, registration := range stagedWrite.registrations {
		write.registrations[key] = registration
	}
	return nil
}

func sameRuntimeTradeRecallRouteSet(got map[runtimeDataRecallKey]runtimeDataRecallRegistration, want map[runtimeDataRecallKey]struct{}) bool {
	if len(got) != len(want) {
		return false
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			return false
		}
	}
	return true
}

func sameRuntimeTradeWriteRouteSet(got map[runtimeDataWriteKey]runtimeDataWriteRegistration, want map[runtimeDataWriteKey]struct{}) bool {
	if len(got) != len(want) {
		return false
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			return false
		}
	}
	return true
}
