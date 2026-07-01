// Package voicechat defines Viewer voice-direct streaming contracts.
package voicechat

const (
	RoutePathPrimary = "/voice-chat"
	RoutePathAlias   = "/voice-chat-ws"

	ErrorVoiceChatDisabled        = "VOICE_CHAT_DISABLED"
	ErrorLLMSessionUnavailable    = "LLM_SESSION_UNAVAILABLE"
	ErrorUtteranceTooShort        = "UTTERANCE_TOO_SHORT"
	ErrorSessionMismatch          = "SESSION_MISMATCH"
	ErrorLLMInferenceFailed       = "LLM_INFERENCE_FAILED"
	ErrorLLMBusy                  = "LLM_BUSY"
	ErrorInvalidRequest           = "INVALID_REQUEST"
	VoiceInputModeSTTPrimary      = "stt_primary"
	VoiceInputModeVDSSub          = "vds_sub"
	VoiceInputModeParallelCaption = "parallel_caption"

	EventSessionReady    = "session.ready"
	EventSessionProgress = "session.progress"
	EventSessionStart    = "session.start"
	EventSessionCommit   = "session.commit"
	EventSessionCancel   = "session.cancel"
	EventLLMDelta        = "llm.delta"
	EventLLMFinal        = "llm.final"
	EventError           = "error"
)

var WebSocketRoutePaths = []string{RoutePathPrimary, RoutePathAlias}

var AllowedVoiceInputModes = []string{
	VoiceInputModeSTTPrimary,
	VoiceInputModeVDSSub,
	VoiceInputModeParallelCaption,
}

func NormalizeVoiceInputMode(raw string) string {
	switch raw {
	case VoiceInputModeVDSSub, VoiceInputModeParallelCaption:
		return raw
	default:
		return VoiceInputModeSTTPrimary
	}
}
