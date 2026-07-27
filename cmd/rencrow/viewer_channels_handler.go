package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// channel 診断を Public API として公開する。
//
// RenCrow_CMD は実装本体を持たず CORE Public API 経由で診断を行う契約
// （docs/06_Public_API仕様.md の client profile）に従い、CLI の
// `rencrow channels list` / `probe` と同じ情報を HTTP で返す。
// 読み取り専用であり、送信などの副作用を持つ操作は含めない。

// handleViewerChannelsList は設定済みチャネルの一覧を返す
func handleViewerChannelsList(registry channelRegistry, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		names := registry.List()
		if names == nil {
			names = []string{}
		}
		status := "empty"
		if len(names) > 0 {
			status = "configured"
		}
		writeViewerChannelsJSON(w, map[string]any{
			"ok":        true,
			"timestamp": now().UTC().Format(time.RFC3339),
			"component": "channels",
			"status":    status,
			"details": map[string]any{
				"channels": names,
			},
		})
	}
}

// handleViewerChannelsProbe は各チャネルの疎通結果を返す
func handleViewerChannelsProbe(registry channelRegistry, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		names := registry.List()
		results := registry.ProbeAll(r.Context())
		perChannel := make(map[string]any, len(names))
		hasErr := false
		for _, name := range names {
			if err := results[name]; err != nil {
				hasErr = true
				perChannel[name] = map[string]any{"ok": false, "error": err.Error()}
				continue
			}
			perChannel[name] = map[string]any{"ok": true}
		}
		status := "ok"
		switch {
		case len(perChannel) == 0:
			status = "empty"
		case hasErr:
			status = "degraded"
		}
		writeViewerChannelsJSON(w, map[string]any{
			"ok":        !hasErr,
			"timestamp": now().UTC().Format(time.RFC3339),
			"component": "channels",
			"status":    status,
			"details": map[string]any{
				"results": perChannel,
			},
		})
	}
}

func writeViewerChannelsJSON(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}
