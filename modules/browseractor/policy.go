package browseractor

import (
	"net/url"
	"strings"
)

const (
	RiskReadOnly       = "read_only"
	RiskDraftInput     = "draft_input"
	RiskNavigation     = "navigation"
	RiskExternalEffect = "external_effect"
	RiskBlocked        = "blocked"
)

type RiskDecision struct {
	Risk        string
	Reason      string
	ActionIndex int
	ActionType  string
}

var submitKeywords = []string{
	"submit", "send", "post", "publish", "buy", "purchase", "checkout", "delete", "remove",
	"reserve", "confirm", "apply", "upload", "支払", "購入", "投稿", "送信", "削除", "予約",
	"確定", "申し込", "申込",
}

func ClassifyRisk(req RunRequest) RiskDecision {
	hasFill := false
	hasClick := false
	for i, action := range req.Actions {
		if err := ValidateAction(action); err != nil {
			return RiskDecision{Risk: RiskBlocked, Reason: "unsupported_action", ActionIndex: i, ActionType: action.Type}
		}
		probe := strings.ToLower(strings.Join([]string{action.Type, action.Selector, action.Name, action.Key}, " "))
		for _, keyword := range submitKeywords {
			if strings.Contains(probe, strings.ToLower(keyword)) {
				return RiskDecision{Risk: RiskExternalEffect, Reason: "submit_keyword", ActionIndex: i, ActionType: action.Type}
			}
		}
		if action.Type == "press" && strings.EqualFold(strings.TrimSpace(action.Key), "Enter") {
			return RiskDecision{Risk: RiskExternalEffect, Reason: "enter_key_submit_guard", ActionIndex: i, ActionType: action.Type}
		}
		if action.Type == "fill" {
			hasFill = true
		}
		if action.Type == "click" {
			hasClick = true
		}
	}
	if hasFill {
		return RiskDecision{Risk: RiskDraftInput}
	}
	if hasClick {
		return RiskDecision{Risk: RiskNavigation}
	}
	return RiskDecision{Risk: RiskReadOnly}
}

func OriginOf(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return "", false
	}
	if u.Scheme == "file" {
		return "file://", true
	}
	if u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}
