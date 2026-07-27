package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

type VisionOptions struct {
	MaxImageBytes int64
	MaxVideoBytes int64
	MaxFrames     int
	Language      string
}

type VisionProcessingError struct {
	Code    string
	Message string
	Cause   error
}

func (e *VisionProcessingError) Error() string {
	return e.Code + ": " + e.Message
}

func (e *VisionProcessingError) Unwrap() error {
	return e.Cause
}

type visionRequestProcessor struct {
	analyzer domainvision.Analyzer
	options  VisionOptions
}

func newVisionRequestProcessor(analyzer domainvision.Analyzer, options VisionOptions) *visionRequestProcessor {
	if strings.TrimSpace(options.Language) == "" {
		options.Language = "ja"
	}
	return &visionRequestProcessor{analyzer: analyzer, options: options}
}

func (p *visionRequestProcessor) Process(
	ctx context.Context,
	request ProcessMessageRequest,
	emit messageEventEmitter,
) (ProcessMessageRequest, error) {
	visualCount := 0
	for _, item := range request.Attachments {
		if isVisualAttachment(item) {
			visualCount++
		}
	}
	if visualCount == 0 {
		return request, nil
	}
	if p == nil || p.analyzer == nil {
		return ProcessMessageRequest{}, &VisionProcessingError{
			Code:    "VISION_UNAVAILABLE",
			Message: "RenCrow_Vision is not configured",
		}
	}

	processed := request
	processed.Attachments = append([]domainattachment.Attachment(nil), request.Attachments...)
	results := make([]domainvision.AnalyzeResult, 0, visualCount)
	for index := range processed.Attachments {
		item := &processed.Attachments[index]
		if !isVisualAttachment(*item) {
			continue
		}
		size := item.SizeBytes
		if size <= 0 {
			size = int64(len(item.Data))
		}
		limit := p.options.MaxImageBytes
		if item.Kind == domainattachment.KindVideo {
			limit = p.options.MaxVideoBytes
		}
		if limit > 0 && size > limit {
			err := &VisionProcessingError{
				Code:    "VISION_FILE_TOO_LARGE",
				Message: fmt.Sprintf("%s exceeds CORE's configured Vision limit", item.Filename),
			}
			emitVisionEvent(emit, "vision.request.failed", err.Code, request)
			return ProcessMessageRequest{}, err
		}

		emitVisionEvent(emit, "vision.request.started", visionEventContent(*item, "", ""), request)
		result, err := p.analyzer.Analyze(ctx, domainvision.AnalyzeRequest{
			RequestID:   request.TraceID,
			SessionID:   request.SessionID,
			Prompt:      request.UserMessage,
			Kind:        string(item.Kind),
			Filename:    item.Filename,
			ContentType: item.ContentType,
			Data:        append([]byte(nil), item.Data...),
			MaxFrames:   p.options.MaxFrames,
			Language:    p.options.Language,
		})
		if err != nil {
			code := "VISION_REQUEST_FAILED"
			if coded, ok := err.(interface{ VisionErrorCode() string }); ok && strings.TrimSpace(coded.VisionErrorCode()) != "" {
				code = coded.VisionErrorCode()
			}
			processErr := &VisionProcessingError{Code: code, Message: err.Error(), Cause: err}
			emitVisionEvent(emit, "vision.request.failed", code, request)
			return ProcessMessageRequest{}, processErr
		}
		if !result.OK || strings.TrimSpace(result.Text) == "" {
			processErr := &VisionProcessingError{
				Code:    "VISION_EMPTY_RESULT",
				Message: "RenCrow_Vision returned no usable analysis text",
			}
			emitVisionEvent(emit, "vision.request.failed", processErr.Code, request)
			return ProcessMessageRequest{}, processErr
		}
		results = append(results, result)
		item.Data = nil
		emitVisionEvent(emit, "vision.request.completed", visionEventContent(*item, result.Provider, result.Model), request)
	}
	processed.UserMessage = appendVisionContext(request.UserMessage, processed.Attachments, results)
	return processed, nil
}

func isVisualAttachment(item domainattachment.Attachment) bool {
	return item.Kind == domainattachment.KindImage || item.Kind == domainattachment.KindVideo
}

func emitVisionEvent(emit messageEventEmitter, eventType, content string, request ProcessMessageRequest) {
	if emit == nil {
		return
	}
	emit(eventType, "rencrow-core", "rencrow-vision", content, "", request.JobID, request.SessionID, request.Channel, request.ChatID)
}

func visionEventContent(item domainattachment.Attachment, provider, model string) string {
	fields := []string{"kind=" + string(item.Kind), "filename=" + item.Filename}
	if provider != "" {
		fields = append(fields, "provider="+provider)
	}
	if model != "" {
		fields = append(fields, "model="+model)
	}
	return strings.Join(fields, " ")
}

func appendVisionContext(
	userMessage string,
	attachments []domainattachment.Attachment,
	results []domainvision.AnalyzeResult,
) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(userMessage))
	for index, result := range results {
		var item domainattachment.Attachment
		for len(attachments) > 0 {
			item, attachments = attachments[0], attachments[1:]
			if isVisualAttachment(item) {
				break
			}
		}
		builder.WriteString("\n\n[RenCrow_Vision解析結果]\n")
		builder.WriteString("filename: ")
		builder.WriteString(item.Filename)
		builder.WriteString("\nkind: ")
		builder.WriteString(result.Kind)
		if summary := strings.TrimSpace(result.Summary); summary != "" {
			builder.WriteString("\nsummary: ")
			builder.WriteString(summary)
		}
		builder.WriteString("\ntext: ")
		builder.WriteString(strings.TrimSpace(result.Text))
		if metadata := safeVisionMetadata(result.Metadata); len(metadata) > 0 {
			encoded, _ := json.Marshal(metadata)
			builder.WriteString("\nmetadata: ")
			builder.Write(encoded)
		}
		if index+1 < len(results) {
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func safeVisionMetadata(metadata map[string]any) map[string]any {
	allowed := []string{"width", "height", "duration_ms", "frames_sampled", "mime_type"}
	safe := make(map[string]any, len(allowed))
	for _, key := range allowed {
		if value, ok := metadata[key]; ok {
			safe[key] = value
		}
	}
	return safe
}
