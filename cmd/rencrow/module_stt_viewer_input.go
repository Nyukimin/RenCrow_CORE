package main

import (
	"context"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

type sttViewerInputObserver struct {
	snapshot modulestt.ViewerInputSnapshot
}

func newSTTViewerInputObserver(rt sttRuntime) sttViewerInputObserver {
	return sttViewerInputObserver{snapshot: modulestt.BuildViewerInputSnapshot(modulestt.ViewerInputRuntimeConfig{
		BaseURL:            rt.DebugOptions.STTBaseURL,
		StreamURL:          rt.DebugOptions.STTStreamURL,
		ProviderURL:        rt.ProviderURL,
		GatewayURL:         rt.GatewayURL,
		ProviderAvailable:  rt.Provider != nil,
		WebSocketAvailable: rt.WSHandler != nil,
	})}
}

func (o sttViewerInputObserver) Health(context.Context) core.HealthReport {
	return modulestt.BuildViewerInputHealthReport(o.snapshot)
}

func (o sttViewerInputObserver) Snapshot(context.Context) (modulestt.ViewerInputSnapshot, error) {
	return o.snapshot, nil
}

func handleModuleSTTViewerInput(observer modulestt.ViewerInputObserver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !core.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		if observer == nil {
			http.Error(w, modulestt.ViewerInputObserverUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		snapshot, err := observer.Snapshot(r.Context())
		if err != nil {
			http.Error(w, modulestt.ViewerInputSnapshotFailedPrefix+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = core.WriteJSON(w, modulestt.BuildViewerInputReport(r.Context(), observer, snapshot, time.Now().UTC()))
	}
}
