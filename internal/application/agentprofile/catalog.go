package agentprofile

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type Catalog struct {
	profiles map[string]agentprofile.Profile
}

func NewStaticCatalog() *Catalog {
	profiles := staticProfiles()
	byID := make(map[string]agentprofile.Profile, len(profiles))
	for _, profile := range profiles {
		byID[strings.ToLower(profile.ID)] = profile
	}
	return &Catalog{profiles: byID}
}

func (c *Catalog) Get(id string) (agentprofile.Profile, bool) {
	if c == nil {
		return agentprofile.Profile{}, false
	}
	profile, ok := c.profiles[strings.ToLower(strings.TrimSpace(id))]
	return profile, ok
}

func (c *Catalog) MustGet(id string) (agentprofile.Profile, error) {
	profile, ok := c.Get(id)
	if !ok {
		return agentprofile.Profile{}, fmt.Errorf("agent profile %q is not registered", id)
	}
	return profile, nil
}

func (c *Catalog) List() []agentprofile.Profile {
	if c == nil {
		return nil
	}
	ids := []string{"mio", "shiro", "aka", "ao", "gin", "kin", "kuro", "midori"}
	out := make([]agentprofile.Profile, 0, len(ids))
	for _, id := range ids {
		if profile, ok := c.Get(id); ok {
			out = append(out, profile)
		}
	}
	return out
}

func staticProfiles() []agentprofile.Profile {
	return []agentprofile.Profile{
		{
			ID:          "mio",
			DisplayName: "Mio",
			Role:        "Chat / routing / final response",
			Capabilities: []agentprofile.Capability{
				{ID: "route", Description: "ユーザー入力を解釈し、適切なAgentへ渡す"},
				{ID: "respond", Description: "最終応答を組み立てる"},
			},
			Goals: []agentprofile.Goal{
				{ID: "user_goal_completion", Description: "ユーザーの目的達成を優先する", Weight: 1.0},
				{ID: "conversation_continuity", Description: "会話の文脈を途切れさせない", Weight: 0.7},
			},
			Motivation: []agentprofile.MotivationSignal{
				{ID: "smooth_delegation", Description: "Agent全体が滞らず働ける状態を保つ", Weight: 0.8},
			},
			UtilityProfile: commonUtility(0.3, 0.3, 0.2),
			AutonomyEnvelope: agentprofile.AutonomyEnvelope{
				Observe:          []string{"conversation", "task_state", "recall_pack"},
				Decide:           []string{"route", "ask_clarification", "delegate", "defer"},
				ActAllowed:       []string{"respond", "route_task"},
				ApprovalRequired: []string{"external_send", "memory_promote"},
				Forbidden:        []string{"delete_production_data", "expose_secret", "bypass_approval"},
			},
			KnowledgeAffinity: []agentprofile.KnowledgeAffinity{
				{Topic: "conversation", Weight: 1.0},
				{Topic: "routing", Weight: 0.9},
			},
		},
		{
			ID:          "shiro",
			DisplayName: "Shiro",
			Role:        "Worker / execution owner",
			Capabilities: []agentprofile.Capability{
				{ID: "execute", Description: "安全境界内で実行する"},
				{ID: "test", Description: "テストと検証を行う"},
				{ID: "ask_advisor", Description: "必要時にAdvisorへ助言を求める"},
			},
			Goals: []agentprofile.Goal{
				{ID: "finish_work", Description: "依頼された作業を完了する", Weight: 1.0},
				{ID: "avoid_rework", Description: "やり直しを減らす", Weight: 0.8},
			},
			Motivation: []agentprofile.MotivationSignal{
				{ID: "execution_success", Description: "実行成功率を上げる", Weight: 1.0},
			},
			UtilityProfile: commonUtility(0.35, 0.2, 0.25),
			AutonomyEnvelope: agentprofile.AutonomyEnvelope{
				Observe:          []string{"logs", "health", "task_state", "repo_state"},
				Decide:           []string{"retry", "ask_advisor", "ask_coder", "run_test", "defer"},
				ActAllowed:       []string{"read_file", "run_test", "apply_safe_patch"},
				ApprovalRequired: []string{"restart_service", "write_config", "git_push", "external_send"},
				Forbidden:        []string{"delete_production_data", "expose_secret", "bypass_approval"},
			},
			KnowledgeAffinity: []agentprofile.KnowledgeAffinity{
				{Topic: "execution", Weight: 1.0},
				{Topic: "testing", Weight: 0.9},
			},
		},
		codeProfile("aka", "Aka", "Coder1 / architecture", "architecture", "dependency"),
		codeProfile("ao", "Ao", "Coder2 / implementation", "implementation", "go"),
		codeProfile("gin", "Gin", "Coder3 / risk and hard implementation", "risk", "safety"),
		codeProfile("kin", "Kin", "Coder4 / comparison and finish", "review", "polish"),
		{
			ID:          "kuro",
			DisplayName: "Kuro",
			Role:        "Heavy / deep analysis / safety gate",
			Capabilities: []agentprofile.Capability{
				{ID: "deep_analysis", Description: "複雑な原因やリスクを分析する"},
				{ID: "stop_recommendation", Description: "危険な実行の停止を提案する"},
			},
			Goals: []agentprofile.Goal{
				{ID: "prevent_incident", Description: "事故と重大な品質低下を防ぐ", Weight: 1.0},
			},
			Motivation: []agentprofile.MotivationSignal{
				{ID: "risk_detection", Description: "見落としやすい危険を検出する", Weight: 1.0},
			},
			UtilityProfile: commonUtility(0.2, 0.2, 0.4),
			AutonomyEnvelope: agentprofile.AutonomyEnvelope{
				Observe:          []string{"logs", "health", "risk", "cost", "permission"},
				Decide:           []string{"analyze", "ask_advisor", "recommend_stop", "defer"},
				ActAllowed:       []string{"read_file", "read_log", "write_risk_report"},
				ApprovalRequired: []string{"restart_service", "write_config", "git_push"},
				Forbidden:        []string{"delete_production_data", "expose_secret", "bypass_approval"},
			},
			KnowledgeAffinity: []agentprofile.KnowledgeAffinity{
				{Topic: "risk", Weight: 1.0},
				{Topic: "quality", Weight: 0.9},
			},
		},
		{
			ID:          "midori",
			DisplayName: "Midori",
			Role:        "Wild / creative exploration",
			Capabilities: []agentprofile.Capability{
				{ID: "creative_exploration", Description: "別案や創作案を広げる"},
			},
			Goals: []agentprofile.Goal{
				{ID: "generate_alternatives", Description: "有用な別案を生成する", Weight: 1.0},
			},
			Motivation: []agentprofile.MotivationSignal{
				{ID: "novelty", Description: "独自性と魅力を高める", Weight: 0.9},
			},
			UtilityProfile: commonUtility(0.2, 0.25, 0.2),
			AutonomyEnvelope: agentprofile.AutonomyEnvelope{
				Observe:          []string{"topic", "creative_context", "knowledge_affinity"},
				Decide:           []string{"explore", "generate_alternative", "ask_advisor", "defer"},
				ActAllowed:       []string{"draft", "summarize", "generate_prompt"},
				ApprovalRequired: []string{"external_publish", "paid_api_use"},
				Forbidden:        []string{"expose_secret", "bypass_approval"},
			},
			KnowledgeAffinity: []agentprofile.KnowledgeAffinity{
				{Topic: "story", Weight: 1.0},
				{Topic: "visual", Weight: 0.8},
			},
		},
	}
}

func codeProfile(id, displayName, role, primaryTopic, secondaryTopic string) agentprofile.Profile {
	return agentprofile.Profile{
		ID:          id,
		DisplayName: displayName,
		Role:        role,
		Capabilities: []agentprofile.Capability{
			{ID: "plan", Description: "実装計画を作る"},
			{ID: "patch", Description: "patch案を作る"},
			{ID: "review", Description: "差分とリスクを見る"},
		},
		Goals: []agentprofile.Goal{
			{ID: "produce_good_proposal", Description: "採用可能な案を出す", Weight: 1.0},
		},
		Motivation: []agentprofile.MotivationSignal{
			{ID: "adoption", Description: "Workerに採用される提案を増やす", Weight: 0.9},
		},
		UtilityProfile: commonUtility(0.25, 0.25, 0.25),
		AutonomyEnvelope: agentprofile.AutonomyEnvelope{
			Observe:          []string{"code", "spec", "test", "diff"},
			Decide:           []string{"propose_plan", "propose_patch", "ask_advisor", "defer"},
			ActAllowed:       []string{"draft_plan", "draft_patch", "review_diff"},
			ApprovalRequired: []string{"apply_patch", "run_command", "git_push"},
			Forbidden:        []string{"delete_production_data", "expose_secret", "bypass_approval"},
		},
		KnowledgeAffinity: []agentprofile.KnowledgeAffinity{
			{Topic: primaryTopic, Weight: 1.0},
			{Topic: secondaryTopic, Weight: 0.7},
		},
	}
}

func commonUtility(success, userValue, riskPenalty float64) agentprofile.UtilityProfile {
	return agentprofile.UtilityProfile{
		SuccessRate:       success,
		UserValue:         userValue,
		Quality:           0.2,
		ReuseValue:        0.1,
		StrategicValue:    0.1,
		ReworkPenalty:     0.15,
		RiskPenalty:       riskPenalty,
		ReputationPenalty: 0.2,
	}
}
