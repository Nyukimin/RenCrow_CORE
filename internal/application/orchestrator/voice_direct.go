package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/voiceinput"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	address, err := conversation.NewChannelAddress(channel, chatID)
	if err != nil {
		return ProcessMessageResponse{}, fmt.Errorf("build voice direct channel address: %w", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), result.UserText, address)
	if err != nil {
		return ProcessMessageResponse{}, fmt.Errorf("build voice direct turn input: %w", err)
	}
	input = input.
		WithSessionID(sessionID).
		WithViewerRecipient("mio").
		WithRoute(routing.RouteCHAT)
	decision := routing.NewDecision(routing.RouteCHAT, 1.0, voiceChatSurfaceReason)
	jobID := task.NewJobID()
	o.events.BindTrace(jobID.String(), input.TraceID())
	defer o.events.ReleaseTrace(jobID.String())

	published, err := voiceinput.Publisher{
		Events:     o.events,
		TurnLogger: o.sessionTurnLogger,
		Input:      input,
		NewJobID: func() string {
			return jobID.String()
		},
		EmitMetric: func(kind, point string, startedAt time.Time, route, jobID, sessionID, channel, chatID, detail string) {
			emitLatencyMetric(o.events.Emit, kind, point, startedAt, route, jobID, sessionID, channel, chatID, detail)
		},
	}.Publish(result)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	publishedJobID, _ := task.ParseJobID(published.JobID)
	if publishedJobID.IsZero() {
		publishedJobID = jobID
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
	response := o.responses.Build(result.Reply, decision, publishedJobID)
	response.TurnID = string(input.TurnID())
	response.TraceID = string(input.TraceID())
	response.RootTaskID = string(input.RootTaskID())
	response.MessageID = string(input.AgentMessageID())
	_ = o.events.TakeResponseMessageID(published.JobID)
	return response, nil
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
