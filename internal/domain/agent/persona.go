package agent

// AgentPersona は Shiro/Coder 向けの軽量ペルソナ定義。
// conversation.PersonaState（Mio 専用）とは独立した型。
type AgentPersona struct {
	Name        string // 表示名（例: "赤", "Aka"）
	Personality string // ペルソナ記述（system prompt 先頭に前置される）
	Tone        string // 口調ヒント（例: "calm", "analytical"）将来の TTS 連携用
}

// BuildSystemPrompt はペルソナブロックとタスクプロンプトを合成する。
// Personality が空文字の場合は taskPrompt をそのまま返す（後方互換）。
// 合成順序: Personality + "\n\n" + taskPrompt
func (p AgentPersona) BuildSystemPrompt(taskPrompt string) string {
	if p.Personality == "" {
		return taskPrompt
	}
	return p.Personality + "\n\n" + taskPrompt
}
