package main

import (
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
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
