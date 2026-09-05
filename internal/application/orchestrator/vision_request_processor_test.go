package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

type visionAnalyzerStub struct {
	requests []domainvision.AnalyzeRequest
	result   domainvision.AnalyzeResult
	err      error
}

func (s *visionAnalyzerStub) Analyze(_ context.Context, request domainvision.AnalyzeRequest) (domainvision.AnalyzeResult, error) {
	s.requests = append(s.requests, request)
	return s.result, s.err
}

func (s *visionAnalyzerStub) Health(context.Context) (domainvision.HealthReport, error) {
	return domainvision.HealthReport{}, nil
}

type visionEvent struct {
	eventType string
	content   string
}

func TestVisionRequestProcessorReplacesRawVisualMediaWithNormalizedContext(t *testing.T) {
	analyzer := &visionAnalyzerStub{result: domainvision.AnalyzeResult{
		OK:        true,
		RequestID: "trace-1",
		Provider:  "rencrow_vision",
		Model:     "Wild",
		Kind:      "image",
		Summary:   "机の上の写真です。",
		Text:      "机の上に青いカップがあります。",
		Metadata:  map[string]any{"width": 640, "height": 480},
	}}
	processor := newVisionRequestProcessor(analyzer, VisionOptions{
		MaxImageBytes: 20 << 20,
		MaxVideoBytes: 100 << 20,
		MaxFrames:     8,
		Language:      "ja",
	})
	var events []visionEvent
	emit := func(eventType, _, _, content, _, _, _, _, _ string) {
		events = append(events, visionEvent{eventType: eventType, content: content})
	}
	request := ProcessMessageRequest{
		RootTaskID:  "tsk_00000000-0000-5000-8000-000000000002",
		TraceID:     "trace-1",
		SessionID:   "session-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "この画像を説明して",
		Attachments: []domainattachment.Attachment{{
			Kind:        domainattachment.KindImage,
			Filename:    "photo.png",
			ContentType: "image/png",
			SizeBytes:   int64(len("raw-image")),
			Data:        []byte("raw-image"),
		}},
	}

	got, err := processor.Process(context.Background(), request, emit)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(analyzer.requests) != 1 {
		t.Fatalf("analyze calls = %d", len(analyzer.requests))
	}
	analyzeRequest := analyzer.requests[0]
	if analyzeRequest.RequestID != "trace-1" || analyzeRequest.SessionID != "session-1" {
		t.Fatalf("unexpected correlation: %+v", analyzeRequest)
	}
	if analyzeRequest.Prompt != request.UserMessage || analyzeRequest.MaxFrames != 8 {
		t.Fatalf("unexpected analyze request: %+v", analyzeRequest)
	}
	if string(analyzeRequest.Data) != "raw-image" {
		t.Fatalf("analyzer did not receive raw image bytes: %q", analyzeRequest.Data)
	}
	if len(got.Attachments) != 0 {
		t.Fatalf("processed visual attachment must be consumed before downstream routing: %+v", got.Attachments)
	}
	if !strings.Contains(got.UserMessage, "RenCrow_Vision解析結果") ||
		!strings.Contains(got.UserMessage, "机の上に青いカップがあります。") {
		t.Fatalf("normalized context missing: %q", got.UserMessage)
	}
	if strings.Contains(got.UserMessage, "raw-image") {
		t.Fatalf("raw media leaked into text context: %q", got.UserMessage)
	}
	if len(events) != 2 || events[0].eventType != "vision.request.started" || events[1].eventType != "vision.request.completed" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestVisionRequestProcessorConsumesVisualAttachmentsButPreservesNonVisual(t *testing.T) {
	analyzer := &visionAnalyzerStub{result: domainvision.AnalyzeResult{
		OK: true, Kind: "video", Text: "動画に猫が映っています。",
	}}
	processor := newVisionRequestProcessor(analyzer, VisionOptions{MaxImageBytes: 20 << 20, MaxVideoBytes: 100 << 20, MaxFrames: 4})
	nonVisual := domainattachment.Attachment{
		ID: "doc-1", Kind: domainattachment.KindDocument, Filename: "notes.txt", ContentType: "text/plain",
		SizeBytes: 5, Data: []byte("notes"), ExtractedText: "notes",
	}
	visual := domainattachment.Attachment{
		ID: "video-1", Kind: domainattachment.KindVideo, Filename: "clip.mp4", ContentType: "video/mp4",
		SizeBytes: 5, Data: []byte("video"),
	}
	request := ProcessMessageRequest{
		TraceID: "trace-mixed", SessionID: "session-mixed", UserMessage: "確認して",
		Attachments: []domainattachment.Attachment{visual, nonVisual},
	}

	got, err := processor.Process(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(analyzer.requests) != 1 || string(analyzer.requests[0].Data) != "video" {
		t.Fatalf("analyzer request did not preserve raw visual bytes: %+v", analyzer.requests)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].ID != nonVisual.ID || string(got.Attachments[0].Data) != "notes" {
		t.Fatalf("non-visual attachment was not preserved while visual was consumed: %+v", got.Attachments)
	}
	if !strings.Contains(got.UserMessage, "動画に猫が映っています。") {
		t.Fatalf("normalized Vision context missing: %q", got.UserMessage)
	}
}

func TestVisionRequestProcessorRejectsOversizedMediaBeforeCallingVision(t *testing.T) {
	analyzer := &visionAnalyzerStub{}
	processor := newVisionRequestProcessor(analyzer, VisionOptions{
		MaxImageBytes: 4,
		MaxVideoBytes: 8,
		MaxFrames:     8,
	})
	_, err := processor.Process(context.Background(), ProcessMessageRequest{
		TraceID:     "trace-limit",
		UserMessage: "説明して",
		Attachments: []domainattachment.Attachment{{
			Kind:      domainattachment.KindImage,
			Filename:  "large.png",
			SizeBytes: 5,
			Data:      []byte("12345"),
		}},
	}, nil)
	var processErr *VisionProcessingError
	if !errors.As(err, &processErr) || processErr.Code != "VISION_FILE_TOO_LARGE" {
		t.Fatalf("error = %T %v", err, err)
	}
	if len(analyzer.requests) != 0 {
		t.Fatalf("analyze calls = %d, want 0", len(analyzer.requests))
	}
}

func TestVisionRequestProcessorDoesNotFallbackWhenVisionFails(t *testing.T) {
	analyzer := &visionAnalyzerStub{err: errors.New("Wild unavailable")}
	processor := newVisionRequestProcessor(analyzer, VisionOptions{
		MaxImageBytes: 20 << 20,
		MaxVideoBytes: 100 << 20,
		MaxFrames:     8,
	})
	var events []visionEvent
	emit := func(eventType, _, _, content, _, _, _, _, _ string) {
		events = append(events, visionEvent{eventType: eventType, content: content})
	}
	_, err := processor.Process(context.Background(), ProcessMessageRequest{
		TraceID:     "trace-fail",
		UserMessage: "説明して",
		Attachments: []domainattachment.Attachment{{
			Kind:     domainattachment.KindImage,
			Filename: "photo.png",
			Data:     []byte("raw-image"),
		}},
	}, emit)
	var processErr *VisionProcessingError
	if !errors.As(err, &processErr) || processErr.Code != "VISION_REQUEST_FAILED" {
		t.Fatalf("error = %T %v", err, err)
	}
	if len(events) != 2 || events[1].eventType != "vision.request.failed" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestMessageOrchestratorRunsVisionBeforeAgentRouting(t *testing.T) {
	analyzer := &visionAnalyzerStub{result: domainvision.AnalyzeResult{
		OK:      true,
		Kind:    "image",
		Summary: "要約",
		Text:    "白い猫が座っています。",
	}}
	var routedTask conversation.TurnInput
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		response: "確認しました。",
		decideFunc: func(_ context.Context, input conversation.TurnInput) (routing.Decision, error) {
			routedTask = input
			return routing.NewDecision(routing.RouteCHAT, 1, "chat"), nil
		},
	}
	orch := NewMessageOrchestrator(
		newMockSessionRepository(),
		mio,
		&mockShiroAgent{},
		nil, nil, nil, nil, nil,
	)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetVisionAnalyzer(analyzer, VisionOptions{
		MaxImageBytes: 20 << 20,
		MaxVideoBytes: 100 << 20,
		MaxFrames:     8,
	})

	response, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "vision-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "この画像を説明して",
		Attachments: []domainattachment.Attachment{{
			Kind:        domainattachment.KindImage,
			Filename:    "cat.png",
			ContentType: "image/png",
			Data:        []byte("raw-image"),
		}},
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if response.Response != "確認しました。" {
		t.Fatalf("response = %q", response.Response)
	}
	if !strings.Contains(routedTask.MessageText(), "白い猫が座っています。") {
		t.Fatalf("Vision result not routed as text context: %q", routedTask.MessageText())
	}
	if attachments := routedTask.Attachments(); len(attachments) != 0 {
		t.Fatalf("raw visual attachment reached routed task: %+v", attachments)
	}
}
