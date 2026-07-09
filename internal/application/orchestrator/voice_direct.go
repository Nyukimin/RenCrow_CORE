package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/voiceinput"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const voiceChatSurfaceReason = voiceinput.SurfaceVoiceChat

// ProcessVoiceDirectRequest は voice_chat surface の input_audio/VDS 確定後の orchestrator 連携入力。
// Phase 1 では RenCrow_LLM WS が推論し、rencrow は FinalText を受け取って Chat SSE を出す。
type ProcessVoiceDirectRequest struct {
	UtteranceID   string
	SessionID     string
	Channel       string
	ChatID        string
	ViewerSession string
	Prompt        string
	SampleRate    int
	Channels      int
	AudioWAVPath  string
	UserText      string
	FinalText     string
	StartedAt     time.Time
	CommitAt      time.Time
	FirstTokenAt  time.Time
}

func (req ProcessVoiceDirectRequest) normalizedChannel() string {
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		return "viewer"
	}
	return channel
}

func (req ProcessVoiceDirectRequest) normalizedSessionID() string {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID != "" {
		return sessionID
	}
	if viewerSession := strings.TrimSpace(req.ViewerSession); viewerSession != "" {
		return viewerSession
	}
	return "viewer"
}

func (req ProcessVoiceDirectRequest) normalizedChatID() string {
	chatID := strings.TrimSpace(req.ChatID)
	if chatID != "" {
		return chatID
	}
	return "viewer-user"
}

func validateProcessVoiceDirectRequest(req ProcessVoiceDirectRequest) error {
	if strings.TrimSpace(req.FinalText) == "" {
		return errors.New("voice direct final text is required")
	}
	if strings.TrimSpace(req.UtteranceID) == "" {
		return errors.New("voice direct utterance_id is required")
	}
	channel := req.normalizedChannel()
	if channel != "viewer" {
		return fmt.Errorf("voice direct is only allowed on viewer channel, got %q", channel)
	}
	return nil
}

// ProcessVoiceDirect は LLM WS 推論完了後に voice_chat surface の Chat SSE イベントを発行する。
// 追加の Mio.Chat LLM 呼び出しはせず、target_agent=Mio / route=CHAT の会話イベントへ正規化する。
func (o *MessageOrchestrator) ProcessVoiceDirect(ctx context.Context, req ProcessVoiceDirectRequest) (ProcessMessageResponse, error) {
	if o == nil {
		return ProcessMessageResponse{}, errors.New("message orchestrator is nil")
	}
	if err := validateProcessVoiceDirectRequest(req); err != nil {
		return ProcessMessageResponse{}, err
	}
	startedAt := req.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	ctx = contextWithLatencyTrace(ctx, startedAt)

	sessionID := req.normalizedSessionID()
	channel := req.normalizedChannel()
	chatID := req.normalizedChatID()
	result, err := voiceinput.BuildFromLLMFinal(voiceinput.BuildLLMRequest{
		UtteranceID:  req.UtteranceID,
		SessionID:    sessionID,
		Channel:      channel,
		ChatID:       chatID,
		UserTextHint: req.UserText,
		FinalText:    req.FinalText,
		StartedAt:    startedAt,
		CommitAt:     req.CommitAt,
		FirstTokenAt: req.FirstTokenAt,
		FinalAt:      time.Now(),
	})
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	decision := routing.NewDecision(routing.RouteCHAT, 1.0, voiceChatSurfaceReason)

	published, err := voiceinput.Publisher{
		Events:     o.events,
		TurnLogger: o.sessionTurnLogger,
		NewJobID: func() string {
			return task.NewJobID().String()
		},
		EmitMetric: func(kind, point string, startedAt time.Time, route, jobID, sessionID, channel, chatID, detail string) {
			emitLatencyMetric(o.events.Emit, kind, point, startedAt, route, jobID, sessionID, channel, chatID, detail)
		},
	}.Publish(result)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	jobID, _ := task.ParseJobID(published.JobID)
	if jobID.IsZero() {
		jobID = task.NewJobID()
	}

	if !req.FirstTokenAt.IsZero() {
		emitVoiceDirectPointLatency(
			o.events.Emit,
			"llm",
			"first_token",
			startedAt,
			req.FirstTokenAt,
			string(routing.RouteCHAT),
			published.JobID,
			sessionID,
			channel,
			chatID,
			req.UtteranceID,
		)
	}

	_ = ctx
	return o.responses.Build(result.Reply, decision, jobID), nil
}

// NotifyVoiceDirectFirstToken は bridge が初回 llm.delta を転送したタイミングで呼ぶ。
func (o *MessageOrchestrator) NotifyVoiceDirectFirstToken(ctx context.Context, req ProcessVoiceDirectRequest, jobID task.JobID, firstTokenAt time.Time) {
	if o == nil || firstTokenAt.IsZero() {
		return
	}
	startedAt := req.StartedAt
	if startedAt.IsZero() {
		startedAt = firstTokenAt
	}
	sessionID := req.normalizedSessionID()
	channel := req.normalizedChannel()
	chatID := req.normalizedChatID()
	if jobID.IsZero() {
		jobID = task.NewJobID()
	}
	emitVoiceDirectPointLatency(
		o.events.Emit,
		"llm",
		"first_token",
		startedAt,
		firstTokenAt,
		string(routing.RouteCHAT),
		jobID.String(),
		sessionID,
		channel,
		chatID,
		req.UtteranceID,
	)
	_ = ctx
}

func emitVoiceDirectPointLatency(
	emit messageEventEmitter,
	kind, point string,
	startedAt, at time.Time,
	route, jobID, sessionID, channel, chatID, detail string,
) {
	if emit == nil || startedAt.IsZero() || at.IsZero() {
		return
	}
	payload := latencyMetricPayload{
		Kind:      kind,
		Point:     point,
		ElapsedMS: float64(at.Sub(startedAt).Microseconds()) / 1000.0,
		SinceMS:   float64(at.Sub(startedAt).Microseconds()) / 1000.0,
		AtUnixMS:  at.UnixMilli(),
		Detail:    detail,
	}
	content, err := json.Marshal(payload)
	if err != nil {
		content = []byte(fmt.Sprintf(`{"kind":%q,"point":%q,"at_unix_ms":%d}`, kind, point, at.UnixMilli()))
	}
	emit("metrics.latency", "metrics", "viewer", string(content), route, jobID, sessionID, channel, chatID)
}
