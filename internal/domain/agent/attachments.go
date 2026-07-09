package agent

import (
	"strings"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func userMessageWithAttachments(message string, attachments []domainattachment.Attachment) llm.Message {
	trimmed := strings.TrimSpace(message)
	if len(attachments) == 0 {
		return llm.Message{Role: "user", Content: trimmed}
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

	parts := []llm.MessagePart{{Type: llm.MessagePartText, Text: b.String()}}
	for _, att := range attachments {
		if att.Kind == domainattachment.KindImage && len(att.Data) > 0 {
			parts = append(parts, llm.MessagePart{
				Type:     llm.MessagePartImage,
				MimeType: att.ContentType,
				Data:     append([]byte(nil), att.Data...),
			})
		}
		if att.Kind == domainattachment.KindAudio && len(att.Data) > 0 {
			parts = append(parts, llm.MessagePart{
				Type:     llm.MessagePartAudio,
				MimeType: att.ContentType,
				Data:     append([]byte(nil), att.Data...),
			})
		}
		if att.Kind == domainattachment.KindVideo && len(att.Data) > 0 {
			parts = append(parts, llm.MessagePart{
				Type:     llm.MessagePartVideo,
				MimeType: att.ContentType,
				Data:     append([]byte(nil), att.Data...),
			})
		}
	}

	return llm.Message{Role: "user", Content: b.String(), Parts: parts}
}
