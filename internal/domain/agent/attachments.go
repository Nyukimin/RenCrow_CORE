package agent

import (
	"strings"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func userMessageAttachmentSummary(message string, attachments []domainattachment.Attachment) string {
	trimmed := strings.TrimSpace(message)
	if len(attachments) == 0 {
		return trimmed
	}

	var b strings.Builder
	if trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString("添付ファイル:\n")
	for _, att := range attachments {
		b.WriteString(domainattachment.SummaryLine(att))
		b.WriteByte('\n')
	}
	return b.String()
}

func userMessageWithAttachments(message string, attachments []domainattachment.Attachment) llm.Message {
	content := userMessageAttachmentSummary(message, attachments)
	if len(attachments) == 0 {
		return llm.Message{Role: "user", Content: content}
	}

	parts := []llm.MessagePart{{Type: llm.MessagePartText, Text: content}}
	for _, att := range attachments {
		if att.Kind == domainattachment.KindAudio && len(att.Data) > 0 {
			parts = append(parts, llm.MessagePart{
				Type:     llm.MessagePartAudio,
				MimeType: att.ContentType,
				Data:     append([]byte(nil), att.Data...),
			})
		}
	}

	return llm.Message{Role: "user", Content: content, Parts: parts}
}
