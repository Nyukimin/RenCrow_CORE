package idlechat

import (
	"strings"
	"testing"
)

func TestForecastHorizonByDomain(t *testing.T) {
	tests := map[string]string{
		"AI技術": "1〜2年", "その他技術": "2〜3年", "医療": "3〜5年",
		"社会保障": "3〜5年", "政治": "3〜5年", "経済": "3〜5年",
	}
	for domain, want := range tests {
		if got := forecastHorizonForDomain(domain); got != want {
			t.Fatalf("domain=%s horizon=%s want=%s", domain, got, want)
		}
		prompt := generateForecastTopicPrompt(ForecastDomain{Name: domain}, nil, nil)
		if !strings.Contains(prompt, want+"後") || strings.Contains(prompt, "3〜10年") {
			t.Fatalf("domain=%s prompt has wrong horizon: %s", domain, prompt)
		}
	}
}
