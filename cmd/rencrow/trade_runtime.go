package main

import (
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	configpolicy "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/policybundle"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	applicationpreview "github.com/Nyukimin/RenCrow_CORE/internal/application/traderiskpreview"
	applicationshadow "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
	applicationshadowoutcome "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcome"
	applicationshadowoutcomereport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcomereport"
	applicationshadowreview "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreview"
	applicationshadowreviewreport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreviewreport"
	applicationcommit "github.com/Nyukimin/RenCrow_CORE/internal/application/tradesimulationcommit"
	policydecisionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/policydecision"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tradeclient"
)

func newTradeStatusHandler(cfg *config.Config) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeStatus(viewer.TradeStatusOptions{})
	}
	client, err := tradeclient.NewClient(
		cfg.Trade.BaseURL,
		cfg.Trade.AuthTokenFile,
		time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		log.Printf("RenCrow_TRADE bridge unavailable: %v", err)
		return viewer.HandleTradeStatus(viewer.TradeStatusOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE read-only bridge enabled")
	return viewer.HandleTradeStatus(viewer.TradeStatusOptions{Enabled: true, Reader: client})
}

func newTradePolicyEvaluationHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradePolicyEvaluation(viewer.TradePolicyEvaluationOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE policy evaluation unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradePolicyEvaluation(viewer.TradePolicyEvaluationOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(
		cfg.Trade.BaseURL,
		cfg.Trade.AuthTokenFile,
		time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		log.Printf("RenCrow_TRADE policy evaluation unavailable: %v", err)
		return viewer.HandleTradePolicyEvaluation(viewer.TradePolicyEvaluationOptions{Enabled: true})
	}
	service, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{
		Snapshots: snapshots,
		Evaluator: client,
		Decisions: decisions,
	})
	if err != nil {
		log.Printf("RenCrow_TRADE policy evaluation unavailable: %v", err)
		return viewer.HandleTradePolicyEvaluation(viewer.TradePolicyEvaluationOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE pure policy evaluation enabled")
	return viewer.HandleTradePolicyEvaluation(viewer.TradePolicyEvaluationOptions{Enabled: true, Runner: service})
}

func newTradeRiskPreviewHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE risk preview unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(
		cfg.Trade.BaseURL,
		cfg.Trade.AuthTokenFile,
		time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		log.Printf("RenCrow_TRADE risk preview unavailable: %v", err)
		return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{Enabled: true})
	}
	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{
		Snapshots: snapshots,
		Evaluator: client,
		Decisions: decisions,
	})
	if err != nil {
		log.Printf("RenCrow_TRADE risk preview policy unavailable: %v", err)
		return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{Enabled: true})
	}
	service, err := applicationpreview.NewService(policyService, client)
	if err != nil {
		log.Printf("RenCrow_TRADE risk preview unavailable: %v", err)
		return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE non-mutating portfolio risk preview enabled")
	return viewer.HandleTradeRiskPreview(viewer.TradeRiskPreviewOptions{Enabled: true, Runner: service})
}

func newTradeSimulationCommitHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE simulation commit unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE simulation commit unavailable: %v", err)
		return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{Enabled: true})
	}
	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{Snapshots: snapshots, Evaluator: client, Decisions: decisions})
	if err != nil {
		log.Printf("RenCrow_TRADE simulation commit policy unavailable: %v", err)
		return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{Enabled: true})
	}
	service, err := applicationcommit.NewService(policyService, client)
	if err != nil {
		log.Printf("RenCrow_TRADE simulation commit unavailable: %v", err)
		return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE simulation-only portfolio commit enabled")
	return viewer.HandleTradeSimulationCommit(viewer.TradeSimulationCommitOptions{Enabled: true, Runner: service})
}

func newTradeShadowObservationHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE Shadow observation unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow observation unavailable: %v", err)
		return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{Enabled: true})
	}
	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{Snapshots: snapshots, Evaluator: client, Decisions: decisions})
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow observation policy unavailable: %v", err)
		return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{Enabled: true})
	}
	service, err := applicationshadow.NewService(policyService, client)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow observation unavailable: %v", err)
		return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE immutable Shadow observation record enabled")
	return viewer.HandleTradeShadowObservation(viewer.TradeShadowObservationOptions{Enabled: true, Runner: service})
}

func newTradeShadowOutcomeHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE Shadow outcome unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow outcome unavailable: %v", err)
		return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{Enabled: true})
	}
	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{Snapshots: snapshots, Evaluator: client, Decisions: decisions})
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow outcome policy unavailable: %v", err)
		return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{Enabled: true})
	}
	service, err := applicationshadowoutcome.NewService(policyService, client)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow outcome unavailable: %v", err)
		return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE immutable Shadow outcome record enabled")
	return viewer.HandleTradeShadowOutcome(viewer.TradeShadowOutcomeOptions{Enabled: true, Runner: service})
}

func newTradeShadowOutcomeReportHandler(cfg *config.Config) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeShadowOutcomeReport(viewer.TradeShadowOutcomeReportOptions{})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow outcome report unavailable: %v", err)
		return viewer.HandleTradeShadowOutcomeReport(viewer.TradeShadowOutcomeReportOptions{Enabled: true})
	}
	service, err := applicationshadowoutcomereport.NewService(client)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow outcome report unavailable: %v", err)
		return viewer.HandleTradeShadowOutcomeReport(viewer.TradeShadowOutcomeReportOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE read-only Shadow outcome report enabled")
	return viewer.HandleTradeShadowOutcomeReport(viewer.TradeShadowOutcomeReportOptions{Enabled: true, Runner: service})
}

func newTradeShadowReviewHandler(cfg *config.Config, snapshots *configpolicy.Store, decisions *policydecisionpersistence.JSONLStore) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{})
	}
	if snapshots == nil || decisions == nil {
		log.Printf("RenCrow_TRADE Shadow review unavailable: policy snapshot or evidence store is unavailable")
		return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{Enabled: true})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow review unavailable: %v", err)
		return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{Enabled: true})
	}
	policyService, err := applicationtradepolicy.NewService(applicationtradepolicy.Options{Snapshots: snapshots, Evaluator: client, Decisions: decisions})
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow review policy unavailable: %v", err)
		return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{Enabled: true})
	}
	service, err := applicationshadowreview.NewService(policyService, client)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow review unavailable: %v", err)
		return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE immutable Shadow outcome review record enabled")
	return viewer.HandleTradeShadowReview(viewer.TradeShadowReviewOptions{Enabled: true, Runner: service})
}

func newTradeShadowReviewReportHandler(cfg *config.Config) http.HandlerFunc {
	if cfg == nil || !cfg.Trade.Enabled {
		return viewer.HandleTradeShadowReviewReport(viewer.TradeShadowReviewReportOptions{})
	}
	client, err := tradeclient.NewClient(cfg.Trade.BaseURL, cfg.Trade.AuthTokenFile, time.Duration(cfg.Trade.TimeoutMS)*time.Millisecond)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow review report unavailable: %v", err)
		return viewer.HandleTradeShadowReviewReport(viewer.TradeShadowReviewReportOptions{Enabled: true})
	}
	service, err := applicationshadowreviewreport.NewService(client)
	if err != nil {
		log.Printf("RenCrow_TRADE Shadow review report unavailable: %v", err)
		return viewer.HandleTradeShadowReviewReport(viewer.TradeShadowReviewReportOptions{Enabled: true})
	}
	log.Printf("RenCrow_TRADE read-only Shadow review report enabled")
	return viewer.HandleTradeShadowReviewReport(viewer.TradeShadowReviewReportOptions{Enabled: true, Runner: service})
}
