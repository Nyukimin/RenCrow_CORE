package main

import (
	"encoding/json"
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// web-gather の診断を Public API として公開する。
//
// RenCrow_CMD は実装本体を持たず CORE Public API 経由で診断を行う契約
// （docs/06_Public_API仕様.md の client profile）に従い、CLI の
// `rencrow web-gather doctor` と同じ結果を HTTP で返す。
//
// 公開するのは doctor のみとする。url / search / webwright-fetch は外部への
// HTTP アクセスを発生させ、import 系はローカルファイルを読むため、Public API
// 化すると CORE が任意 URL 取得の踏み台になる、あるいは CORE ホスト上の
// パスに依存する設計になる。これらは CORE の CLI から実行する。

// newWebGatherDiagnosticsDeps は doctor が参照する依存だけを組み立てる
//
// doctor は staging store の有無、SearXNG 設定、webwright の runner／python／
// endpoint を確認する。フェッチ実行に必要な Fetcher や SearchCache は診断では
// 参照されないため組み立てない。
func newWebGatherDiagnosticsDeps(cfg *config.Config, store *l1sqlite.L1SQLiteStore) func() webGatherCLIDeps {
	if cfg == nil {
		return nil
	}
	return func() webGatherCLIDeps {
		deps := webGatherCLIDeps{
			WebwrightFetch: cfg.WebwrightFetch,
			SearXNGBaseURL: cfg.WebGather.SearXNGBaseURL,
			YaCyBaseURL:    cfg.WebGather.YaCyBaseURL,
		}
		if store != nil {
			deps.StagingStore = store
		}
		return deps
	}
}

// handleViewerWebGatherDoctor は web-gather の依存構成を診断して返す
func handleViewerWebGatherDoctor(deps func() webGatherCLIDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if deps == nil {
			http.Error(w, "web-gather dependencies are unavailable", http.StatusServiceUnavailable)
			return
		}
		result := runWebGatherDoctor(r.Context(), deps())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(result)
	}
}
