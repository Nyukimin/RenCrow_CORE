package core

const (
	ModuleManifestEndpoint          = "/viewer/modules/manifest"
	ModuleHealthEndpoint            = "/viewer/modules/health"
	ModuleLLMDiagnosticsEndpoint    = "/viewer/modules/llm/diagnostics"
	ModuleChatRouteEndpoint         = "/viewer/modules/chat/route"
	ModuleWorkerDiagnosticsEndpoint = "/viewer/modules/worker/diagnostics"
	ModuleTTSDiagnosticsEndpoint    = "/viewer/modules/tts/diagnostics"
	ModuleTTSPlaybackStateEndpoint  = "/viewer/modules/tts/playback-state"
	ModuleSTTDiagnosticsEndpoint    = "/viewer/modules/stt/diagnostics"
	ModuleSTTViewerInputEndpoint    = "/viewer/modules/stt/viewer-input"
)

func RegisteredModuleEndpointPaths() []string {
	return []string{
		ModuleManifestEndpoint,
		ModuleHealthEndpoint,
		ModuleLLMDiagnosticsEndpoint,
		ModuleChatRouteEndpoint,
		ModuleWorkerDiagnosticsEndpoint,
		ModuleTTSDiagnosticsEndpoint,
		ModuleTTSPlaybackStateEndpoint,
		ModuleSTTDiagnosticsEndpoint,
		ModuleSTTViewerInputEndpoint,
	}
}

func CurrentModuleDescriptors() []ModuleDescriptor {
	return []ModuleDescriptor{
		{
			Name:        "core",
			Owner:       StateOwnerCore,
			Kind:        "contract",
			Contracts:   []string{"HealthReport", "AggregateHealthReports", "ModuleDescriptor", "OwnedState", "Event"},
			Endpoints:   []string{ModuleManifestEndpoint, ModuleHealthEndpoint},
			Description: "shared module contracts, health aggregation, state ownership metadata",
		},
		{
			Name:        "llm",
			Owner:       StateOwnerLLM,
			Kind:        "provider",
			Contracts:   []string{"Provider", "Router", "GenerateRequest", "GenerateResponse"},
			Endpoints:   []string{ModuleLLMDiagnosticsEndpoint, ModuleHealthEndpoint},
			Description: "language model provider contracts and runtime provider health",
		},
		{
			Name:        "chat",
			Owner:       StateOwnerChat,
			Kind:        "service",
			Contracts:   []string{"Service", "RoutePolicy", "RouteDecision", "RuntimePorts"},
			Endpoints:   []string{ModuleChatRouteEndpoint, ModuleHealthEndpoint},
			Description: "user-facing dialogue and routing contracts",
		},
		{
			Name:        "worker",
			Owner:       StateOwnerWorker,
			Kind:        "executor",
			Contracts:   []string{"Executor", "Planner", "Action", "Result", "ToolProposalPatch"},
			Endpoints:   []string{ModuleWorkerDiagnosticsEndpoint, ModuleHealthEndpoint},
			Description: "tool execution and proposal patch execution contracts",
		},
		{
			Name:      "tts",
			Owner:     StateOwnerTTS,
			Kind:      "provider",
			Contracts: []string{"Provider", "SynthesisRequest", "SynthesisResult", "AudioChunk", "PlaybackStateObserver"},
			Endpoints: []string{ModuleTTSDiagnosticsEndpoint, ModuleTTSPlaybackStateEndpoint, ModuleHealthEndpoint},
			OwnsState: []OwnedState{
				{Name: "synthesis_provider", Owner: StateOwnerTTS, Unit: "provider", Lifetime: "process", RebuildBy: "runtime factory"},
			},
			Description: "speech synthesis contracts plus playback state observer boundary",
		},
		{
			Name:      "tts.playback",
			Owner:     StateOwnerChat,
			Kind:      "state-observer",
			Contracts: []string{"PlaybackStateObserver", "PlaybackStateSnapshot"},
			Endpoints: []string{ModuleTTSPlaybackStateEndpoint, ModuleHealthEndpoint},
			OwnsState: []OwnedState{
				{Name: "idlechat_tts_pending", Owner: StateOwnerChat, Unit: "session/response", Lifetime: "playback wait", RebuildBy: "viewer playback ack or timeout"},
				{Name: "tts_public_session_routes", Owner: StateOwnerChat, Unit: "public tts session", Lifetime: "playback route", RebuildBy: "tts enqueue and cleanup"},
			},
			Description: "Viewer playback ACK and IdleChat pending state boundary, separated from TTS provider state",
		},
		{
			Name:        "stt",
			Owner:       StateOwnerSTT,
			Kind:        "provider",
			Contracts:   []string{"Provider", "TranscribeRequest", "TranscribeResult", "ViewerInputObserver"},
			Endpoints:   []string{ModuleSTTDiagnosticsEndpoint, ModuleSTTViewerInputEndpoint, ModuleHealthEndpoint},
			Description: "speech transcription provider contracts and readiness",
		},
		{
			Name:      "stt.viewer_input",
			Owner:     StateOwnerChat,
			Kind:      "state-observer",
			Contracts: []string{"ViewerInputObserver", "ViewerInputSnapshot"},
			Endpoints: []string{ModuleSTTViewerInputEndpoint, ModuleHealthEndpoint},
			OwnsState: []OwnedState{
				{Name: "viewer_microphone_input", Owner: StateOwnerChat, Unit: "browser input", Lifetime: "viewer session", RebuildBy: "viewer runtime"},
				{Name: "transcript_injection", Owner: StateOwnerChat, Unit: "chat input", Lifetime: "accepted transcript", RebuildBy: "/stt/chat-input"},
			},
			Description: "Viewer microphone and transcript injection boundary, separated from STT provider state",
		},
		{
			Name:        "voicechat",
			Owner:       StateOwnerVoice,
			Kind:        "contract",
			Contracts:   []string{"BridgePlan", "WebSocketRoutePaths", "VoiceInputMode", "RuntimeURLPlan"},
			Description: "Viewer voice-direct route, VDS bridge, runtime URL, and WebSocket planning contracts",
		},
		{
			Name:        "browseractor",
			Owner:       StateOwnerWeb,
			Kind:        "contract",
			Contracts:   []string{"RunRequest", "RunResponse", "RiskDecision", "DoctorResponse"},
			Description: "browser automation request/response, safety risk classification, and artifact contract",
		},
		{
			Name:        "webgather",
			Owner:       StateOwnerWeb,
			Kind:        "contract",
			Contracts:   []string{"FetchRequest", "FetchResponse", "SearchRequest", "SearchResponse", "FetchPolicy"},
			Description: "web discovery, fetch, extraction, staging, and search contract boundary",
		},
	}
}
