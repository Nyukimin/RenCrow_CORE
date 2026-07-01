package browseractor

import (
	"regexp"
)

const Mask = "[MASKED]"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(bearer\s+)?[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(cookie\s*[:=]\s*)[^;\n\r]+`),
	regexp.MustCompile(`(?i)(set-cookie\s*[:=]\s*)[^;\n\r]+`),
	regexp.MustCompile(`(?i)((password|token|secret|apikey|api_key|session|csrf)\s*[:=]\s*)[^\s"'&]+`),
}

func MaskSecrets(text string) string {
	out := text
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllString(out, `${1}`+Mask)
	}
	return out
}
