package main

import (
	"context"
	"net/http"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func handleModuleHealth(
	llmProviders map[string]modulellm.Provider,
	chatService namedHealthProvider,
	ttsProvider namedHealthProvider,
	ttsPlayback namedHealthProvider,
	sttProvider namedHealthProvider,
	sttViewerInput namedHealthProvider,
	workerExecutor namedHealthProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		_ = modulecore.WriteJSON(w, buildModuleHealthSnapshot(ctx, llmProviders, chatService, ttsProvider, ttsPlayback, sttProvider, sttViewerInput, workerExecutor, time.Now().UTC()))
	}
}

func buildModuleHealthSnapshot(
	ctx context.Context,
	llmProviders map[string]modulellm.Provider,
	chatService namedHealthProvider,
	ttsProvider namedHealthProvider,
	ttsPlayback namedHealthProvider,
	sttProvider namedHealthProvider,
	sttViewerInput namedHealthProvider,
	workerExecutor namedHealthProvider,
	updatedAt time.Time,
) modulecore.HealthSnapshot {
	llmReports := modulellm.CollectHealthReports(ctx, llmProviders, updatedAt)
	return modulecore.BuildRuntimeHealthSnapshot(ctx, modulecore.RuntimeHealthProviders{
		LLMReports:     llmReports,
		Chat:           chatService,
		Worker:         workerExecutor,
		TTS:            ttsProvider,
		TTSPlayback:    ttsPlayback,
		STT:            sttProvider,
		STTViewerInput: sttViewerInput,
	}, updatedAt)
}

type namedHealthProvider interface {
	Health(context.Context) modulecore.HealthReport
}

type chatModuleService interface {
	modulechat.Service
	namedHealthProvider
}
