package verification

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

type DefaultClaimExtractor struct{}

var claimSplitter = regexp.MustCompile(`[。.!?\n]+`)

func (DefaultClaimExtractor) ExtractClaims(_ context.Context, req Request, level domainverification.TriggerLevel) ([]domainverification.Claim, error) {
	if level == domainverification.TriggerLow {
		return nil, nil
	}
	text := strings.TrimSpace(req.DraftResponse)
	if text == "" {
		return nil, nil
	}
	parts := claimSplitter.Split(text, -1)
	claims := make([]domainverification.Claim, 0, len(parts))
	for _, part := range parts {
		claimText := strings.TrimSpace(part)
		if !looksFactual(claimText, level) {
			continue
		}
		claims = append(claims, domainverification.Claim{
			ID:       domainverification.ClaimID(fmt.Sprintf("claim_%03d", len(claims)+1)),
			Text:     claimText,
			Priority: level,
			Status:   domainverification.StatusNotChecked,
		})
	}
	return claims, nil
}

func looksFactual(text string, level domainverification.TriggerLevel) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if level == domainverification.TriggerHigh {
		return true
	}
	keywords := []string{
		"です", "である", "ます", "公開", "発売", "発表", "ニュース", "出典",
		"検索", "KB", "Knowledge", "記憶", "保存", "年", "月", "日",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}
