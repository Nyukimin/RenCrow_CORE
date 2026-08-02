package agent

import (
	"context"
	"strings"
)

// WithKBManager はKBManagerを設定（Phase 4.2 KB自動保存用）
func (m *MioAgent) WithKBManager(mgr KBManager) *MioAgent {
	m.kbManager = mgr
	if cacheMgr, ok := mgr.(SearchCacheManager); ok {
		m.searchCacheManager = cacheMgr
	}
	return m
}

func (m *MioAgent) WithSearchCacheManager(mgr SearchCacheManager) *MioAgent {
	m.searchCacheManager = mgr
	return m
}

func (m *MioAgent) WithUserMemoryManager(mgr UserMemoryManager) *MioAgent {
	m.userMemoryManager = mgr
	return m
}

// WithPersonaEditor はPersonaEditorを設定（ペルソナ自己編集用）
func (m *MioAgent) WithPersonaEditor(editor PersonaEditor) *MioAgent {
	m.personaEditor = editor
	return m
}

func (m *MioAgent) WithRecentContextProvider(provider func(context.Context, int) (string, error)) *MioAgent {
	m.recentContext = provider
	return m
}

func (m *MioAgent) WithSystemPrompt(prompt string) *MioAgent {
	m.systemPrompt = strings.TrimSpace(prompt)
	return m
}

// WithAgentContractsPrompt injects the validated, bounded Agent Registry
// contract index used by Mio for routing and result integration. Persona text
// is intentionally kept separate from this runtime context.
func (m *MioAgent) WithAgentContractsPrompt(prompt string) *MioAgent {
	m.agentContractsPrompt = truncateMioExpression(strings.TrimSpace(prompt), 6000)
	return m
}

// WithRecentExpressionHistory seeds the bounded wording history used for
// nearby repetition avoidance. It does not change factual conversation
// memory.
func (m *MioAgent) WithRecentExpressionHistory(history MioExpressionHistory) *MioAgent {
	m.expressionHistoryMu.Lock()
	m.expressionHistory = history.normalized()
	m.expressionHistoryMu.Unlock()
	return m
}

func (m *MioAgent) WithViewerRecipientPrompts(prompts map[string]string) *MioAgent {
	m.viewerPrompts = make(map[string]string, len(prompts))
	for name, prompt := range prompts {
		name = strings.ToLower(strings.TrimSpace(name))
		prompt = strings.TrimSpace(prompt)
		if name != "" && prompt != "" {
			m.viewerPrompts[name] = prompt
		}
	}
	return m
}
