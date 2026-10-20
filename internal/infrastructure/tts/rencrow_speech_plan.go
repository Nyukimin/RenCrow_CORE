package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

const maxSpeechPlanWAVBytes = 64 << 20

const (
	speechPlanTurnTimeout      = 5 * time.Minute
	speechPlanStartedWaitLimit = 30 * time.Second
	maxSpeechPlanSynthesisMS   = uint64(speechPlanTurnTimeout / time.Millisecond)
)

type gatewaySpeechPlanRequest struct {
	SpeakerID      string            `json:"speaker_id"`
	SessionID      string            `json:"session_id"`
	TurnID         string            `json:"turn_id"`
	UtteranceIndex int               `json:"utterance_index"`
	SpeechPlan     speech.SpeechPlan `json:"speech_plan"`
}

type speechPlanSynthesisResponse struct {
	WAV             []byte
	RequestID       modulecore.RequestID
	ResponseID      modulecore.ResponseID
	ProfileRevision int
	IrodoriSeed     *uint64
	SynthesisMS     *uint64
	QueueWait       time.Duration
	FailureStage    string
	GatewayStatus   *int
}

type speechPlanResponseMetadata struct {
	ProfileRevision int
	IrodoriSeed     *uint64
	SynthesisMS     *uint64
}

type speechPlanTransportObservation struct {
	SchemaVersion        int     `json:"schema_version"`
	Tag                  string  `json:"tag"`
	Event                string  `json:"event"`
	TurnID               string  `json:"turn_id"`
	SpeakerID            string  `json:"speaker_id"`
	UtteranceIndex       int     `json:"utterance_index"`
	VoiceProfileRevision *int    `json:"voice_profile_revision"`
	IrodoriSeed          *uint64 `json:"irodori_seed"`
	TTSQueueWaitMS       *int64  `json:"tts_queue_wait_ms"`
	TTSSynthesisMS       *uint64 `json:"tts_synthesis_ms"`
	FirstAudioMS         *int64  `json:"first_audio_ms"`
	PlaybackDurationMS   *int64  `json:"playback_duration_ms"`
	FallbackReason       *string `json:"fallback_reason"`
	FailureStage         *string `json:"failure_stage,omitempty"`
	GatewayStatus        *int    `json:"gateway_status,omitempty"`
}

const (
	speechPlanTransportObservationSchema = 1
	speechPlanTransportObservationTag    = "external"

	speechPlanFallbackCancelled = "speech_cancelled"
	speechPlanFallbackTimeout   = "speech_timeout"
	speechPlanFallbackTransport = "speech_transport_failed"
	speechPlanFallbackAudioSave = "speech_audio_save_failed"
	speechPlanFallbackPlayback  = "speech_playback_failed"

	speechPlanFailureStageRequestBuild       = "request_build"
	speechPlanFailureStageReceiptBegin       = "receipt_begin"
	speechPlanFailureStageHTTPTransport      = "http_transport"
	speechPlanFailureStageGatewayStatus      = "gateway_status"
	speechPlanFailureStageResponseValidation = "response_validation"
	speechPlanFailureStageResponseReceipt    = "response_receipt"
)

// PushSpeechPlan sends each selected utterance to the canonical Gateway route
// one at a time. Text-based callers continue through PushText/PushTextWithDisplay.
func (b *RenCrowTTSBridge) PushSpeechPlan(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	if b == nil {
		return fmt.Errorf("RenCrow_TTS bridge is unavailable")
	}
	if ctx == nil {
		return fmt.Errorf("SpeechPlan context is required")
	}
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	conversationSessionID = strings.TrimSpace(conversationSessionID)
	turnID = strings.TrimSpace(turnID)
	speakerID = strings.ToLower(strings.TrimSpace(speakerID))
	if ttsSessionID == "" {
		return fmt.Errorf("SpeechPlan tts_session_id is required")
	}
	if err := modulecore.SessionID(conversationSessionID).Validate(); err != nil {
		return fmt.Errorf("SpeechPlan conversation session_id is invalid: %w", err)
	}
	if err := modulecore.TurnID(turnID).Validate(); err != nil {
		return fmt.Errorf("SpeechPlan turn_id is invalid: %w", err)
	}
	voiceID, _, knownSpeaker := moduletts.VoiceForAgent(speakerID)
	if !knownSpeaker {
		return fmt.Errorf("SpeechPlan speaker_id is unresolved")
	}
	progress := speech.PlaybackProgressFromContext(ctx)
	if progress == nil {
		return fmt.Errorf("SpeechPlan playback progress owner is unavailable")
	}
	normalized := speech.Normalize(plan)
	if !normalized.Valid {
		return fmt.Errorf("SpeechPlan is invalid: %s", normalized.FallbackReason)
	}
	playbackOutput := SpeechPlaybackOutputViewer
	if b.cfg.DownloadAudio {
		playbackOutput = SpeechPlaybackOutputLocal
	}
	outputOwner, ok := progress.(interface{ SetPlaybackOutput(string) error })
	if !ok {
		return fmt.Errorf("SpeechPlan playback output owner is unavailable")
	}
	if err := outputOwner.SetPlaybackOutput(playbackOutput); err != nil {
		return fmt.Errorf("set SpeechPlan playback output: %w", err)
	}
	if playbackOutput == SpeechPlaybackOutputLocal {
		if _, ok := b.cfg.Sink.(SpeechPlanAudioSink); !ok {
			return fmt.Errorf("local SpeechPlan playback sink is unavailable")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	turnCtx, cancelTurn := context.WithTimeout(ctx, speechPlanTurnTimeout)
	defer cancelTurn()

	session, _, err := b.reserveSpeechPlanChunks(ttsSessionID, speakerID, voiceID, len(normalized.Plan.Utterances))
	if err != nil {
		return err
	}
	observations := make([]speechPlanTransportObservation, 0, len(normalized.Plan.Utterances))
	timing, _ := progress.(speech.PlaybackProgressTiming)
	defer func() {
		for i := range observations {
			if timing == nil {
				continue
			}
			startedAt, started := timing.PlaybackStartedAt(observations[i].UtteranceIndex)
			endedAt, ended := timing.PlaybackEndedAt(observations[i].UtteranceIndex)
			if started && ended {
				observations[i].PlaybackDurationMS = durationMilliseconds(startedAt, endedAt)
			}
		}
		for _, observation := range observations {
			logSpeechPlanTransportObservation(observation)
		}
	}()
	for utteranceIndex, utterance := range normalized.Plan.Utterances {
		if err := turnCtx.Err(); err != nil {
			observation := newSpeechPlanTransportObservation(turnID, speakerID, utteranceIndex)
			observation.FallbackReason = speechPlanFailureReason(err, speechPlanFallbackTransport)
			observations = append(observations, observation)
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}
		if utteranceIndex > 0 {
			if err := waitSpeechPlanStarted(turnCtx, progress, utteranceIndex-1); err != nil {
				setSpeechPlanFallbackReason(observations, utteranceIndex-1, speechPlanFailureReason(err, speechPlanFallbackPlayback))
				return speechPlanProgressFailure(progress, utteranceIndex-1, err)
			}
		}
		observations = append(observations, newSpeechPlanTransportObservation(turnID, speakerID, utteranceIndex))
		reqBody, err := json.Marshal(gatewaySpeechPlanRequest{
			SpeakerID:      speakerID,
			SessionID:      conversationSessionID,
			TurnID:         turnID,
			UtteranceIndex: utteranceIndex,
			SpeechPlan:     normalized.Plan,
		})
		if err != nil {
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFallbackReasonPointer(speechPlanFallbackTransport))
			return fmt.Errorf("marshal SpeechPlan Gateway request: %w", err)
		}
		result, err := b.synthesizeSpeechPlanUtterance(turnCtx, reqBody, utteranceIndex)
		observations[utteranceIndex].TTSQueueWaitMS = milliseconds(result.QueueWait)
		if err != nil {
			if result.FailureStage != "" {
				failureStage := result.FailureStage
				observations[utteranceIndex].FailureStage = &failureStage
			}
			observations[utteranceIndex].GatewayStatus = result.GatewayStatus
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackTransport))
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}
		profileRevision := result.ProfileRevision
		observations[utteranceIndex].VoiceProfileRevision = &profileRevision
		observations[utteranceIndex].IrodoriSeed = result.IrodoriSeed
		observations[utteranceIndex].TTSSynthesisMS = result.SynthesisMS
		if err := turnCtx.Err(); err != nil {
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackTransport))
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}

		savedPath, err := saveGatewayWAV(bytes.NewReader(result.WAV), b.cfg.OutputDir, "viewer-tts")
		if err != nil {
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackAudioSave))
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}
		if utteranceIndex == 0 && timing != nil {
			if createdAt := timing.TurnCreatedAt(); !createdAt.IsZero() {
				observations[utteranceIndex].FirstAudioMS = durationMilliseconds(createdAt, time.Now())
			}
		}
		audioPath := savedPath
		if relative, ok := moduletts.LocalAudioRelPath(b.cfg.OutputDir, savedPath); ok {
			audioPath = relative
		}
		audioURL := moduletts.BuildLocalAudioURL(b.cfg.OutputDir, savedPath)
		if err := turnCtx.Err(); err != nil {
			_ = os.Remove(savedPath)
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackTransport))
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}

		speechText := speechTextForUtterance(utterance)
		if err := progress.MarkReady(utteranceIndex); err != nil {
			_ = os.Remove(savedPath)
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackPlayback))
			return err
		}
		if err := turnCtx.Err(); err != nil {
			_ = os.Remove(savedPath)
			setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackPlayback))
			return speechPlanProgressFailure(progress, utteranceIndex, err)
		}
		if b.cfg.OnSpeechChunkReady != nil {
			b.cfg.OnSpeechChunkReady(SpeechPlanChunkReady{
				TTSSessionID:          ttsSessionID,
				ConversationSessionID: conversationSessionID,
				TurnID:                turnID,
				SpeakerID:             speakerID,
				PublicPlaybackRef:     session.publicPlaybackRef,
				TraceID:               string(session.traceID),
				UtteranceIndex:        utteranceIndex,
				PlaybackOutput:        playbackOutput,
				PauseAfterMS:          utterance.PauseAfterMS,
				ProfileRevision:       result.ProfileRevision,
				SpeechText:            speechText,
				DisplayText:           speechText,
				AudioPath:             audioPath,
				AudioURL:              audioURL,
			})
		}
		if playbackOutput == SpeechPlaybackOutputLocal {
			localChunk := SpeechPlanAudioChunk{
				TurnID:         turnID,
				UtteranceIndex: utteranceIndex,
				AudioPath:      savedPath,
				PauseAfterMS:   utterance.PauseAfterMS,
				FinalUtterance: utteranceIndex == len(normalized.Plan.Utterances)-1,
			}
			if err := b.cfg.Sink.(SpeechPlanAudioSink).SubmitSpeechPlanChunk(turnCtx, ttsSessionID, localChunk, progress); err != nil {
				setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackPlayback))
				return speechPlanProgressFailure(progress, utteranceIndex, err)
			}
		}
		if utteranceIndex == len(normalized.Plan.Utterances)-1 {
			if err := progress.WaitEnded(turnCtx, utteranceIndex); err != nil {
				setSpeechPlanFallbackReason(observations, utteranceIndex, speechPlanFailureReason(err, speechPlanFallbackPlayback))
				return speechPlanProgressFailure(progress, utteranceIndex, err)
			}
		}
	}
	return nil
}

func (b *RenCrowTTSBridge) synthesizeSpeechPlanUtterance(ctx context.Context, body []byte, utteranceIndex int) (speechPlanSynthesisResponse, error) {
	queuedAt := time.Now()
	select {
	case b.speechPlanGate <- struct{}{}:
		defer func() { <-b.speechPlanGate }()
		queueWait := time.Since(queuedAt)
		result, err := b.postSpeechPlanUtterance(ctx, body, utteranceIndex)
		result.QueueWait = queueWait
		return result, err
	case <-ctx.Done():
		return speechPlanSynthesisResponse{QueueWait: time.Since(queuedAt)}, ctx.Err()
	}
}

func waitSpeechPlanStarted(ctx context.Context, progress speech.PlaybackProgress, utteranceIndex int) error {
	waitCtx, cancel := context.WithTimeout(ctx, speechPlanStartedWaitLimit)
	defer cancel()
	return progress.WaitStarted(waitCtx, utteranceIndex)
}

func speechPlanProgressFailure(progress speech.PlaybackProgress, utteranceIndex int, cause error) error {
	if progress == nil || cause == nil {
		return cause
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		if cancellation, ok := progress.(interface{ Cancel() }); ok {
			cancellation.Cancel()
		} else {
			_ = progress.MarkCancelled(utteranceIndex)
		}
		return cause
	}
	_ = progress.MarkError(utteranceIndex, cause)
	return cause
}

func newSpeechPlanTransportObservation(turnID, speakerID string, utteranceIndex int) speechPlanTransportObservation {
	return speechPlanTransportObservation{
		SchemaVersion:  speechPlanTransportObservationSchema,
		Tag:            speechPlanTransportObservationTag,
		Event:          "speech.transport.observed",
		TurnID:         turnID,
		SpeakerID:      speakerID,
		UtteranceIndex: utteranceIndex,
	}
}

func setSpeechPlanFallbackReason(observations []speechPlanTransportObservation, utteranceIndex int, reason *string) {
	if utteranceIndex < 0 || utteranceIndex >= len(observations) {
		return
	}
	observations[utteranceIndex].FallbackReason = reason
}

func speechPlanFailureReason(cause error, fallback string) *string {
	if errors.Is(cause, context.Canceled) {
		return speechPlanFallbackReasonPointer(speechPlanFallbackCancelled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return speechPlanFallbackReasonPointer(speechPlanFallbackTimeout)
	}
	return speechPlanFallbackReasonPointer(fallback)
}

func speechPlanFallbackReasonPointer(reason string) *string {
	return &reason
}

func milliseconds(duration time.Duration) *int64 {
	if duration < 0 {
		return nil
	}
	value := duration.Milliseconds()
	return &value
}

func durationMilliseconds(startedAt, endedAt time.Time) *int64 {
	if startedAt.IsZero() || endedAt.IsZero() || endedAt.Before(startedAt) {
		return nil
	}
	return milliseconds(endedAt.Sub(startedAt))
}

func logSpeechPlanTransportObservation(observation speechPlanTransportObservation) {
	encoded, err := json.Marshal(observation)
	if err != nil {
		return
	}
	log.New(log.Writer(), "", 0).Print(string(encoded))
}

func (b *RenCrowTTSBridge) reserveSpeechPlanChunks(sessionID, speakerID, voiceID string, count int) (renCrowTTSSession, int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	session := b.sessions[sessionID]
	if session == nil {
		return renCrowTTSSession{}, 0, fmt.Errorf("SpeechPlan TTS session is unavailable")
	}
	if session.characterID != speakerID || session.voiceID != voiceID {
		return renCrowTTSSession{}, 0, fmt.Errorf("SpeechPlan speaker does not match the started TTS session")
	}
	first := session.nextChunk
	session.nextChunk += count
	return *session, first, nil
}

func speechTextForUtterance(utterance speech.Utterance) string {
	var text strings.Builder
	for _, segment := range utterance.Segments {
		text.WriteString(segment.Text)
	}
	return text.String()
}

func (b *RenCrowTTSBridge) postSpeechPlanUtterance(ctx context.Context, body []byte, utteranceIndex int) (speechPlanSynthesisResponse, error) {
	owner, owned := domaintransport.ReceiptOwnerFromContext(ctx)
	requestID := modulecore.NewRequestID()
	if owned {
		receipt, err := owner.BeginRequest(ctx, "tts.synthesis")
		if err != nil {
			return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageReceiptBegin}, fmt.Errorf("begin TTS transport request: %w", err)
		}
		requestID = receipt.RequestID
	}
	requestURL := normalizeSynthesisURL(b.cfg.HTTPBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageRequestBuild}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, fmt.Errorf("build SpeechPlan Gateway request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav")
	req.Header.Set("X-RenCrow-TTS-Request-Id", string(requestID))
	if token := strings.TrimSpace(b.cfg.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *b.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageHTTPTransport}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, fmt.Errorf("SpeechPlan Gateway request failed: %w", err))
	}
	defer resp.Body.Close()
	if err := validateSpeechPlanResponseOrigin(req, resp); err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, err)
	}
	if err := ctx.Err(); err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		if err := b.completeSpeechPlanResponse(ctx, owner, owned, requestID, ttsHTTPExternalRef(resp)); err != nil {
			status := resp.StatusCode
			return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseReceipt, GatewayStatus: &status}, err
		}
		code, message := parseSynthesisError(responseBody)
		status := resp.StatusCode
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageGatewayStatus, GatewayStatus: &status}, fmt.Errorf("SpeechPlan Gateway failed status=%d code=%s message=%s", resp.StatusCode, code, message)
	}
	metadata, err := validateSpeechPlanResponseHeaders(resp.Header, string(requestID), utteranceIndex)
	if err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, err)
	}
	if resp.ContentLength > maxSpeechPlanWAVBytes {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, fmt.Errorf("SpeechPlan WAV exceeds %d bytes", maxSpeechPlanWAVBytes))
	}
	wav, err := io.ReadAll(io.LimitReader(resp.Body, maxSpeechPlanWAVBytes+1))
	if err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, fmt.Errorf("read SpeechPlan WAV response: %w", err))
	}
	if int64(len(wav)) > maxSpeechPlanWAVBytes {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, fmt.Errorf("SpeechPlan WAV exceeds %d bytes", maxSpeechPlanWAVBytes))
	}
	if err := validateWAVBytes(wav); err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, err)
	}
	if err := ctx.Err(); err != nil {
		return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseValidation}, b.failSpeechPlanRequest(ctx, owner, owned, requestID, err)
	}
	responseID := modulecore.NewResponseID()
	if owned {
		receipt, err := owner.CompleteResponse(context.WithoutCancel(ctx), requestID, ttsHTTPExternalRef(resp))
		if err != nil {
			return speechPlanSynthesisResponse{FailureStage: speechPlanFailureStageResponseReceipt}, fmt.Errorf("persist TTS transport response: %w", err)
		}
		responseID = receipt.ResponseID
	}
	return speechPlanSynthesisResponse{
		WAV:             wav,
		RequestID:       requestID,
		ResponseID:      responseID,
		ProfileRevision: metadata.ProfileRevision,
		IrodoriSeed:     metadata.IrodoriSeed,
		SynthesisMS:     metadata.SynthesisMS,
	}, nil
}

func (b *RenCrowTTSBridge) failSpeechPlanRequest(ctx context.Context, owner domaintransport.ReceiptOwner, owned bool, requestID modulecore.RequestID, cause error) error {
	if owned {
		if receiptErr := owner.FailWithoutResponse(context.WithoutCancel(ctx), requestID, cause.Error()); receiptErr != nil {
			return fmt.Errorf("%w; persist TTS transport failure: %v", cause, receiptErr)
		}
	}
	return cause
}

func (b *RenCrowTTSBridge) completeSpeechPlanResponse(ctx context.Context, owner domaintransport.ReceiptOwner, owned bool, requestID modulecore.RequestID, externalRef string) error {
	if !owned {
		return nil
	}
	if _, err := owner.CompleteResponse(context.WithoutCancel(ctx), requestID, externalRef); err != nil {
		return fmt.Errorf("persist TTS transport response: %w", err)
	}
	return nil
}

func validateSpeechPlanResponseOrigin(request *http.Request, response *http.Response) error {
	if request == nil || request.URL == nil || response == nil || response.Request == nil || response.Request.URL == nil {
		return fmt.Errorf("SpeechPlan Gateway response origin is unavailable")
	}
	if !sameHTTPOrigin(request.URL, response.Request.URL) {
		return fmt.Errorf("SpeechPlan Gateway response origin mismatch")
	}
	return nil
}

func sameHTTPOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || strings.ToLower(a.Scheme) != strings.ToLower(b.Scheme) {
		return false
	}
	scheme := strings.ToLower(a.Scheme)
	if (scheme != "http" && scheme != "https") || a.User != nil || b.User != nil {
		return false
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && effectiveHTTPPort(a) == effectiveHTTPPort(b)
}

func effectiveHTTPPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func validateSpeechPlanResponseHeaders(headers http.Header, requestID string, utteranceIndex int) (speechPlanResponseMetadata, error) {
	serviceID, ok := singleSpeechPlanHeader(headers, "X-RenCrow-TTS-Gateway-Service")
	if !ok || serviceID != gatewayServiceID {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway service identity mismatch")
	}
	echoedRequestID, ok := singleSpeechPlanHeader(headers, "X-RenCrow-TTS-Request-Id")
	if !ok || echoedRequestID == "" || echoedRequestID != requestID {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway request identity mismatch")
	}
	profileValue, ok := singleSpeechPlanHeader(headers, "X-RenCrow-TTS-Profile-Revision")
	if !ok {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway profile revision must be positive")
	}
	profileRevision, err := strconv.Atoi(profileValue)
	if err != nil || profileRevision <= 0 {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway profile revision must be positive")
	}
	indexValue, ok := singleSpeechPlanHeader(headers, "X-RenCrow-TTS-Utterance-Index")
	if !ok {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway utterance index mismatch")
	}
	gotIndex, err := strconv.Atoi(indexValue)
	if err != nil || gotIndex != utteranceIndex {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway utterance index mismatch")
	}
	irodoriSeed, err := optionalSpeechPlanUintHeader(headers, "X-RenCrow-TTS-Irodori-Seed", ^uint64(0))
	if err != nil {
		return speechPlanResponseMetadata{}, err
	}
	synthesisMS, err := optionalSpeechPlanUintHeader(headers, "X-RenCrow-TTS-Synthesis-Ms", maxSpeechPlanSynthesisMS)
	if err != nil {
		return speechPlanResponseMetadata{}, err
	}
	contentTypeValues := headers.Values("Content-Type")
	if len(contentTypeValues) > 1 {
		return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway response has ambiguous content type")
	}
	if contentType := strings.TrimSpace(headers.Get("Content-Type")); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || (mediaType != "audio/wav" && mediaType != "audio/x-wav" && mediaType != "audio/wave") {
			return speechPlanResponseMetadata{}, fmt.Errorf("SpeechPlan Gateway response content type is not WAV")
		}
	}
	return speechPlanResponseMetadata{ProfileRevision: profileRevision, IrodoriSeed: irodoriSeed, SynthesisMS: synthesisMS}, nil
}

func optionalSpeechPlanUintHeader(headers http.Header, name string, maximum uint64) (*uint64, error) {
	values := headers.Values(name)
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("SpeechPlan Gateway %s must be a single unsigned integer", name)
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return nil, fmt.Errorf("SpeechPlan Gateway %s must be a single unsigned integer", name)
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return nil, fmt.Errorf("SpeechPlan Gateway %s must be a single unsigned integer", name)
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > maximum {
		return nil, fmt.Errorf("SpeechPlan Gateway %s is outside its allowed range", name)
	}
	return &parsed, nil
}

func singleSpeechPlanHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func validateWAVBytes(data []byte) error {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return fmt.Errorf("SpeechPlan Gateway returned invalid WAV data")
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return fmt.Errorf("SpeechPlan Gateway returned malformed WAV length")
	}
	var hasFormat, hasData bool
	for offset := 12; offset < len(data); {
		if offset+8 > len(data) {
			return fmt.Errorf("SpeechPlan Gateway returned malformed WAV chunk header")
		}
		chunkSize := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkStart := uint64(offset + 8)
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > uint64(len(data)) {
			return fmt.Errorf("SpeechPlan Gateway returned malformed WAV chunk")
		}
		chunkID := string(data[offset : offset+4])
		if chunkID == "fmt " && chunkSize >= 16 {
			hasFormat = true
		}
		if chunkID == "data" {
			hasData = true
		}
		if chunkEnd%2 == 1 {
			if chunkEnd >= uint64(len(data)) {
				return fmt.Errorf("SpeechPlan Gateway returned malformed WAV chunk padding")
			}
			chunkEnd++
		}
		offset = int(chunkEnd)
	}
	if !hasFormat || !hasData {
		return fmt.Errorf("SpeechPlan Gateway WAV is missing format or audio data")
	}
	return nil
}
