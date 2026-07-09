package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// parseExplicitCommand は明示コマンドを解析
func (m *MioAgent) parseExplicitCommand(message string) routing.Route {
	// 長いコマンドから順にチェック（/code4 を /code より先に判定）
	commands := []struct {
		cmd   string
		route routing.Route
	}{
		{"/analyze", routing.RouteANALYZE},
		{"/heavy", routing.RouteANALYZE},
		{"/research", routing.RouteRESEARCH},
		{"/wild", routing.RouteWILD},
		{"/code4", routing.RouteCODE4},
		{"/code3", routing.RouteCODE3},
		{"/code2", routing.RouteCODE2},
		{"/code1", routing.RouteCODE1},
		{"/code", routing.RouteCODE},
		{"/plan", routing.RoutePLAN},
		{"/chat", routing.RouteCHAT},
		{"/ops", routing.RouteOPS},
	}

	trimmed := strings.TrimSpace(message)
	for _, c := range commands {
		if explicitCommandMatches(trimmed, c.cmd) {
			return c.route
		}
	}

	return ""
}

func explicitCommandMatches(message, command string) bool {
	if !strings.HasPrefix(message, command) {
		return false
	}
	rest := message[len(command):]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsSpace(r)
}

// detectPersonaEditIntent はペルソナ調整意図を検出する
// トピックキーワード AND アクションキーワードの両方にマッチした場合のみ true
func detectPersonaEditIntent(message string) bool {
	topicKeywords := []string{
		"ペルソナ", "キャラ", "口調", "語尾", "喋り方", "話し方",
		"敬語", "タメ口", "カジュアル", "フォーマル",
		"テンション",
	}
	actionKeywords := []string{
		"変えて", "にして", "やめて", "直して", "調整", "修正",
		"書き換え", "編集", "更新", "して",
	}

	hasTopic := false
	for _, kw := range topicKeywords {
		if strings.Contains(message, kw) {
			hasTopic = true
			break
		}
	}
	if !hasTopic {
		return false
	}

	for _, kw := range actionKeywords {
		if strings.Contains(message, kw) {
			return true
		}
	}
	return false
}

// editPersona はペルソナファイルを LLM で書き換える
func (m *MioAgent) editPersona(ctx context.Context, userMessage string) (string, error) {
	current, err := m.personaEditor.ReadPersona()
	if err != nil {
		return "", fmt.Errorf("read persona: %w", err)
	}

	log.Printf("[Mio] Persona edit requested: %q", truncateLog(userMessage, 100))
	log.Printf("[Mio] Persona before: %q", truncateLog(current, 100))

	// LLM にペルソナ書き換えを依頼
	prompt := fmt.Sprintf(
		"以下は現在のペルソナ設定です:\n\n%s\n\n"+
			"ユーザーの要求: %s\n\n"+
			"上記の要求に基づいて、ペルソナ設定を書き換えてください。\n"+
			"形式（マークダウン）と基本構造は維持してください。\n"+
			"書き換えた設定のみを出力してください。説明や前置きは不要です。",
		current, userMessage,
	)

	req := llm.GenerateRequest{
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:   1024,
		Temperature: 0.3,
	}

	resp, err := m.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("generate new persona: %w", err)
	}

	newPersona := strings.TrimSpace(resp.Content)
	if newPersona == "" {
		return "", fmt.Errorf("LLM returned empty persona")
	}

	log.Printf("[Mio] Persona after: %q", truncateLog(newPersona, 100))

	if err := m.personaEditor.WritePersona(newPersona); err != nil {
		return "", fmt.Errorf("write persona: %w", err)
	}

	return "ペルソナ設定を更新しました。次の会話から反映されます。", nil
}
