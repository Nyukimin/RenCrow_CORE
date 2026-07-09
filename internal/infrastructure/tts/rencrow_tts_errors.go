package tts

import (
	"fmt"
	"strings"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func parseSynthesisError(body []byte) (string, string) {
	return moduletts.ParseSynthesisError(body)
}

func invalidRequestError(message string) error {
	return fmt.Errorf("code=invalid_request message=%s", strings.TrimSpace(message))
}

func normalizeErrorCode(code string) string {
	return moduletts.NormalizeSynthesisErrorCode(code)
}
