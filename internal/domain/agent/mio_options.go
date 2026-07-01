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
