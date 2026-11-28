package attachment

import "context"

type trustedRequestAttachmentsContextKey struct{}

// WithTrustedRequestAttachments binds the attachments accepted and stored by a
// trusted request ingress to the current request context. Tool arguments and
// prompt text cannot populate this capability.
func WithTrustedRequestAttachments(ctx context.Context, attachments []Attachment) context.Context {
	if ctx == nil || len(attachments) == 0 {
		return ctx
	}
	return context.WithValue(ctx, trustedRequestAttachmentsContextKey{}, cloneAttachmentReferences(attachments))
}

// TrustedRequestAttachmentsFromContext returns a copy of the attachment list
// installed by request ingress so delegated Worker Tools can resolve opaque
// attachment IDs without accepting a path or URL from the model.
func TrustedRequestAttachmentsFromContext(ctx context.Context) ([]Attachment, bool) {
	if ctx == nil {
		return nil, false
	}
	attachments, ok := ctx.Value(trustedRequestAttachmentsContextKey{}).([]Attachment)
	if !ok {
		return nil, false
	}
	return cloneAttachments(attachments), true
}

// TrustedRequestAttachmentsByIDFromContext clones only matching entries from
// the current request scope, keeping opaque-ID lookup bounded when other
// attachments are also present.
func TrustedRequestAttachmentsByIDFromContext(ctx context.Context, attachmentID string) ([]Attachment, bool) {
	if ctx == nil {
		return nil, false
	}
	attachments, found := ctx.Value(trustedRequestAttachmentsContextKey{}).([]Attachment)
	if !found {
		return nil, false
	}
	matches := make([]Attachment, 0, 1)
	for _, attachment := range attachments {
		if attachment.ID == attachmentID {
			matches = append(matches, cloneAttachmentReference(attachment))
		}
	}
	return matches, true
}

func cloneAttachmentReferences(attachments []Attachment) []Attachment {
	cloned := make([]Attachment, len(attachments))
	for i, attachment := range attachments {
		cloned[i] = cloneAttachmentReference(attachment)
	}
	return cloned
}

func cloneAttachmentReference(attachment Attachment) Attachment {
	// The scoped reference carries only server-authored metadata. Bytes are
	// rehydrated from its workspace-owned path after opaque-ID authorization.
	attachment.Data = nil
	attachment.SecurityWarnings = append([]string(nil), attachment.SecurityWarnings...)
	return attachment
}

func cloneAttachments(attachments []Attachment) []Attachment {
	cloned := make([]Attachment, len(attachments))
	for i, attachment := range attachments {
		cloned[i] = cloneAttachment(attachment)
	}
	return cloned
}

func cloneAttachment(attachment Attachment) Attachment {
	attachment.Data = append([]byte(nil), attachment.Data...)
	attachment.SecurityWarnings = append([]string(nil), attachment.SecurityWarnings...)
	return attachment
}
