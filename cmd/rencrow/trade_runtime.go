package main

import (
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	configpolicy "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/policybundle"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
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
