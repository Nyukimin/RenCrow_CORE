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
