package chat

import (
	"errors"
	"strings"
)

type ViewerRecipient string

const (
	ViewerRecipientMio    ViewerRecipient = "mio"
	ViewerRecipientShiro  ViewerRecipient = "shiro"
	ViewerRecipientKuro   ViewerRecipient = "kuro"
	ViewerRecipientMidori ViewerRecipient = "midori"
)

const DefaultViewerRecipient = ViewerRecipientMio

var ErrUnsupportedViewerRecipient = errors.New("unsupported viewer recipient")

func NormalizeViewerRecipient(raw string) (ViewerRecipient, error) {
	recipient := strings.ToLower(strings.TrimSpace(raw))
	if recipient == "" {
		return DefaultViewerRecipient, nil
	}
	switch ViewerRecipient(recipient) {
	case ViewerRecipientMio, ViewerRecipientShiro, ViewerRecipientKuro, ViewerRecipientMidori:
		return ViewerRecipient(recipient), nil
	default:
		return "", ErrUnsupportedViewerRecipient
	}
}
