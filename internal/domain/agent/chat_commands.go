package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// ChatCommandResult はチャットコマンドの処理結果
type ChatCommandResult struct {
	Handled  bool
	Response string
}

// HandleChatCommand はチャットコマンドを処理する
// コマンドでない場合は Handled=false を返す
func (m *MioAgent) HandleChatCommand(ctx context.Context, sessionID string, message string) (ChatCommandResult, error) {
	if result, handled, err := m.handleUserMemoryCommand(ctx, sessionID, message); handled || err != nil {
		return result, err
	}

	cmd, _ := parseChatCommand(message)
	if cmd == "" {
		return ChatCommandResult{Handled: false}, nil
	}

	switch cmd {
	case "status":
		return m.cmdStatus(ctx, sessionID)
	case "stop":
		return ChatCommandResult{
			Handled:  true,
			Response: "現在のリクエストを停止しました。",
		}, nil
	case "compact":
		return m.cmdCompact(ctx, sessionID)
	case "context":
		return m.cmdContext(ctx, sessionID, message)
	case "new":
		return m.cmdNew(ctx, sessionID)
	default:
		return ChatCommandResult{Handled: false}, nil
	}
}

// parseChatCommand はメッセージからチャットコマンドを抽出する
// 戻り値: (コマンド名, 残りのテキスト)
func parseChatCommand(message string) (string, string) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "/") {
		return "", ""
	}

	// チャットコマンド一覧（ルーティングコマンドと区別）
	chatCommands := []string{"status", "stop", "compact", "context", "new"}

	parts := strings.SplitN(trimmed, " ", 2)
	cmd := strings.TrimPrefix(parts[0], "/")
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}

	for _, c := range chatCommands {
		if cmd == c {
			return c, rest
		}
	}
	return "", ""
}

func (m *MioAgent) handleUserMemoryCommand(ctx context.Context, sessionID string, message string) (ChatCommandResult, bool, error) {
	if m.userMemoryManager == nil {
		return ChatCommandResult{}, false, nil
	}
	action, body := parseUserMemoryCommand(message)
	if action == "" {
		return ChatCommandResult{}, false, nil
	}
	if body == "" && action != "show" {
		return ChatCommandResult{
			Handled:  true,
			Response: "覚える内容または対象をもう少し具体的に書いてください。",
		}, true, nil
	}

	evidenceID := "chat_memory_command:" + strings.TrimSpace(sessionID)
	if strings.TrimSpace(sessionID) == "" {
		evidenceID = "chat_memory_command:unknown_session"
	}

	switch action {
	case "remember":
		item, err := m.userMemoryManager.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID:           "ren",
			Type:             domainmemory.UserMemoryTypePreference,
			Statement:        body,
			State:            domainmemory.MemoryStateCandidate,
			EvidenceEventIDs: []string{evidenceID},
			Confidence:       0.7,
			Sensitivity:      "normal",
			Scope:            "global",
			Source:           "user_memory_command",
		})
		if err != nil {
			return ChatCommandResult{}, true, fmt.Errorf("user memory create failed: %w", err)
		}
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("覚える候補に入れました。\n- id: %s\n- 内容: %s", item.ID, item.Statement),
		}, true, nil
	case "save_summary":
		item, err := m.userMemoryManager.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID:           "ren",
			Type:             domainmemory.UserMemoryTypeEpisode,
			Statement:        body,
			State:            domainmemory.MemoryStateCandidate,
			EvidenceEventIDs: []string{evidenceID},
			Confidence:       0.75,
			Sensitivity:      "normal",
			Scope:            "mio",
			Source:           "user_summary_save_command",
		})
		if err != nil {
			return ChatCommandResult{}, true, fmt.Errorf("user memory summary save failed: %w", err)
		}
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("会話要約を保存候補に入れました。\n- id: %s\n- 内容: %s", item.ID, item.Statement),
		}, true, nil
	case "prioritize":
		item, err := m.userMemoryManager.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID:           "ren",
			Type:             domainmemory.UserMemoryTypeConstraint,
			Statement:        body,
			State:            domainmemory.MemoryStatePinned,
			EvidenceEventIDs: []string{evidenceID},
			Confidence:       1.0,
			Sensitivity:      "normal",
			Scope:            "global",
			Source:           "user_explicit_priority",
		})
		if err != nil {
			return ChatCommandResult{}, true, fmt.Errorf("user memory pin failed: %w", err)
		}
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("優先する記憶として固定しました。\n- id: %s\n- 内容: %s", item.ID, item.Statement),
		}, true, nil
	case "forget", "correct", "do_not_use":
		matches, err := m.findUserMemoryMatches(ctx, body)
		if err != nil {
			return ChatCommandResult{}, true, err
		}
		if len(matches) == 0 {
			return ChatCommandResult{
				Handled:  true,
				Response: "該当する記憶を見つけられませんでした。忘れる対象の文か memory id を指定してください。",
			}, true, nil
		}
		if len(matches) > 1 {
			return ChatCommandResult{Handled: true, Response: ambiguousUserMemoryResponse("対象候補が複数あります。memory id で指定してください。", matches)}, true, nil
		}
		updated, err := m.userMemoryManager.ForgetUserMemory(ctx, matches[0].ID, action)
		if err != nil {
			return ChatCommandResult{}, true, fmt.Errorf("user memory forget failed: %w", err)
		}
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("記憶を無効化しました。\n- id: %s\n- 内容: %s", updated.ID, updated.Statement),
		}, true, nil
	case "supersede":
		return m.supersedeUserMemoryCommand(ctx, body, evidenceID)
	case "show":
		return m.showUserMemoryCommand(ctx, body)
	default:
		return ChatCommandResult{}, false, nil
	}
}

func parseUserMemoryCommand(message string) (string, string) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return "", ""
	}
	prefixes := []struct {
		prefix string
		action string
	}{
		{"この記憶を見せて", "show"},
		{"記憶を見せて", "show"},
		{"要約して保存", "save_summary"},
		{"この話を要約して保存", "save_summary"},
		{"記憶を置き換えて", "supersede"},
		{"これは違う、正しくは", "supersede"},
		{"これは違う。正しくは", "supersede"},
		{"今後使わないで", "do_not_use"},
		{"これを優先して", "prioritize"},
		{"優先して", "prioritize"},
		{"覚えて", "remember"},
		{"忘れて", "forget"},
		{"これは違う", "correct"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p.prefix) {
			return p.action, cleanupUserMemoryCommandBody(strings.TrimPrefix(trimmed, p.prefix))
		}
	}
	if strings.HasSuffix(trimmed, "を覚えて") {
		return "remember", cleanupUserMemoryCommandBody(strings.TrimSuffix(trimmed, "を覚えて"))
	}
	if strings.HasSuffix(trimmed, "は忘れて") {
		return "forget", cleanupUserMemoryCommandBody(strings.TrimSuffix(trimmed, "は忘れて"))
	}
	if oldText, newText, ok := parseJapaneseSupersedePhrase(trimmed); ok {
		return "supersede", oldText + " => " + newText
	}
	return "", ""
}

func (m *MioAgent) supersedeUserMemoryCommand(ctx context.Context, body string, evidenceID string) (ChatCommandResult, bool, error) {
	oldQuery, newStatement, ok := parseSupersedeBody(body)
	if !ok {
		return ChatCommandResult{
			Handled:  true,
			Response: "置き換える記憶を `古い内容 => 新しい内容` の形、または memory id で指定してください。",
		}, true, nil
	}
	matches, err := m.findUserMemoryMatches(ctx, oldQuery)
	if err != nil {
		return ChatCommandResult{}, true, err
	}
	if len(matches) == 0 {
		return ChatCommandResult{Handled: true, Response: "置き換える元の記憶を見つけられませんでした。memory id か、より具体的な文を指定してください。"}, true, nil
	}
	if len(matches) > 1 {
		return ChatCommandResult{Handled: true, Response: ambiguousUserMemoryResponse("置き換え対象候補が複数あります。memory id で指定してください。", matches)}, true, nil
	}
	old := matches[0]
	newItem, err := m.userMemoryManager.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             firstNonEmptyString(old.Type, domainmemory.UserMemoryTypePreference),
		Statement:        newStatement,
		State:            domainmemory.MemoryStateCandidate,
		EvidenceEventIDs: []string{evidenceID},
		Confidence:       0.75,
		Sensitivity:      firstNonEmptyString(old.Sensitivity, "normal"),
		Scope:            firstNonEmptyString(old.Scope, "all_personas"),
		Source:           "user_memory_supersede_command",
	})
	if err != nil {
		return ChatCommandResult{}, true, fmt.Errorf("user memory supersede create failed: %w", err)
	}
	updated, err := m.userMemoryManager.SupersedeUserMemory(ctx, old.ID, newItem.ID, "supersede")
	if err != nil {
		return ChatCommandResult{}, true, fmt.Errorf("user memory supersede failed: %w", err)
	}
	return ChatCommandResult{
		Handled:  true,
		Response: fmt.Sprintf("記憶を置き換え候補にしました。\n- old: %s %s\n- new: %s %s", updated.ID, updated.Statement, newItem.ID, newItem.Statement),
	}, true, nil
}

func (m *MioAgent) showUserMemoryCommand(ctx context.Context, body string) (ChatCommandResult, bool, error) {
	items, err := m.userMemoryManager.ListUserMemories(ctx, "ren", "", true, 20)
	if err != nil {
		return ChatCommandResult{}, true, fmt.Errorf("user memory list failed: %w", err)
	}
	body = strings.TrimSpace(body)
	var selected []domainmemory.UserMemory
	for _, item := range items {
		if body == "" || item.ID == body || strings.Contains(item.Statement, body) || strings.Contains(body, item.Statement) {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return ChatCommandResult{Handled: true, Response: "該当する記憶は見つかりませんでした。"}, true, nil
	}
	if len(selected) > 8 {
		selected = selected[:8]
	}
	var lines []string
	lines = append(lines, "記憶:")
	for _, item := range selected {
		state := strings.TrimSpace(item.State)
		if state == "" {
			state = "-"
		}
		active := "inactive"
		if item.Active {
			active = "active"
		}
		lines = append(lines, fmt.Sprintf("- id=%s state=%s %s type=%s: %s", item.ID, state, active, item.Type, strings.TrimSpace(item.Statement)))
	}
	return ChatCommandResult{Handled: true, Response: strings.Join(lines, "\n")}, true, nil
}

func cleanupUserMemoryCommandBody(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, " 　:：,，。")
	s = strings.TrimRight(s, " 　。")
	return strings.TrimSpace(s)
}

func (m *MioAgent) findUserMemoryMatches(ctx context.Context, query string) ([]domainmemory.UserMemory, error) {
	items, err := m.userMemoryManager.ListUserMemories(ctx, "ren", "", false, 50)
	if err != nil {
		return nil, fmt.Errorf("user memory list failed: %w", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var matches []domainmemory.UserMemory
	for _, item := range items {
		if item.ID == query {
			return []domainmemory.UserMemory{item}, nil
		}
		if userMemoryStatementMatches(item.Statement, query) {
			matches = append(matches, item)
		}
	}
	if len(matches) > 6 {
		matches = matches[:6]
	}
	return matches, nil
}

func userMemoryStatementMatches(statement string, query string) bool {
	statement = strings.TrimSpace(statement)
	query = strings.TrimSpace(query)
	if statement == "" || query == "" {
		return false
	}
	if strings.Contains(statement, query) || strings.Contains(query, statement) {
		return true
	}
	statementTerms := userMemoryMatchTerms(statement)
	queryTerms := userMemoryMatchTerms(query)
	if len(statementTerms) == 0 || len(queryTerms) == 0 {
		return false
	}
	hits := 0
	for term := range queryTerms {
		if statementTerms[term] {
			hits++
		}
	}
	return hits >= 2
}

func userMemoryMatchTerms(text string) map[string]bool {
	replacer := strings.NewReplacer("、", " ", "。", " ", ":", " ", "：", " ", ",", " ", ".", " ", "　", " ")
	parts := strings.Fields(replacer.Replace(strings.ToLower(text)))
	out := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) < 2 {
			continue
		}
		out[part] = true
	}
	return out
}

func ambiguousUserMemoryResponse(header string, items []domainmemory.UserMemory) string {
	var lines []string
	lines = append(lines, header)
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- id=%s state=%s type=%s: %s", item.ID, firstNonEmptyString(item.State, "-"), firstNonEmptyString(item.Type, "-"), strings.TrimSpace(item.Statement)))
	}
	return strings.Join(lines, "\n")
}

func parseSupersedeBody(body string) (string, string, bool) {
	body = cleanupUserMemoryCommandBody(body)
	for _, sep := range []string{"=>", "->", "→", "⇒"} {
		if strings.Contains(body, sep) {
			parts := strings.SplitN(body, sep, 2)
			oldText := cleanupUserMemoryCommandBody(parts[0])
			newText := cleanupUserMemoryCommandBody(parts[1])
			return oldText, newText, oldText != "" && newText != ""
		}
	}
	return "", "", false
}

func parseJapaneseSupersedePhrase(trimmed string) (string, string, bool) {
	trimmed = strings.TrimSpace(trimmed)
	if !strings.HasSuffix(trimmed, "に置き換えて") {
		return "", "", false
	}
	body := strings.TrimSuffix(trimmed, "に置き換えて")
	parts := strings.SplitN(body, "を", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	oldText := cleanupUserMemoryCommandBody(parts[0])
	newText := cleanupUserMemoryCommandBody(parts[1])
	return oldText, newText, oldText != "" && newText != ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *MioAgent) userMemoryPrompt(ctx context.Context) (string, error) {
	if m.userMemoryManager == nil {
		return "", nil
	}
	items, err := m.userMemoryManager.ListUserMemories(ctx, "ren", "", false, 12)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, item := range items {
		if !domainmemory.IsUserMemoryPromptInjectable(item, "mio") {
			continue
		}
		statement := strings.TrimSpace(item.Statement)
		if statement == "" {
			continue
		}
		prefix := "- "
		if item.State == domainmemory.MemoryStatePinned {
			prefix = "- [優先] "
		}
		lines = append(lines, prefix+statement)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "思い出したこと:\n" + strings.Join(lines, "\n") + "\n注意: user:ren の confirmed/pinned 記憶だけを補助文脈として扱い、Knowledge DB や raw log と混ぜない。", nil
}

// cmdStatus はスレッド情報を表示
func (m *MioAgent) cmdStatus(ctx context.Context, sessionID string) (ChatCommandResult, error) {
	if m.conversationEngine == nil {
		return ChatCommandResult{
			Handled:  true,
			Response: "会話エンジンが無効です。",
		}, nil
	}

	status, err := m.conversationEngine.GetStatus(ctx, sessionID)
	if err != nil {
		return ChatCommandResult{}, fmt.Errorf("GetStatus failed: %w", err)
	}

	elapsed := ""
	if !status.ThreadStart.IsZero() {
		elapsed = time.Since(status.ThreadStart).Truncate(time.Second).String()
	}

	resp := fmt.Sprintf("📊 セッション状態\n"+
		"- セッション: %s\n"+
		"- スレッドID: %d\n"+
		"- ドメイン: %s\n"+
		"- ターン数: %d\n"+
		"- 経過時間: %s\n"+
		"- ステータス: %s",
		status.SessionID,
		status.ThreadID,
		status.ThreadDomain,
		status.TurnCount,
		elapsed,
		status.ThreadStatus,
	)

	return ChatCommandResult{Handled: true, Response: resp}, nil
}

// cmdCompact は現在のスレッドを即座にフラッシュ
func (m *MioAgent) cmdCompact(ctx context.Context, sessionID string) (ChatCommandResult, error) {
	if m.conversationEngine == nil {
		return ChatCommandResult{
			Handled:  true,
			Response: "会話エンジンが無効です。",
		}, nil
	}

	if err := m.conversationEngine.FlushCurrentThread(ctx, sessionID); err != nil {
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("スレッドのフラッシュに失敗しました: %v", err),
		}, nil
	}

	return ChatCommandResult{
		Handled:  true,
		Response: "現在のスレッドをフラッシュし、新しいスレッドを開始しました。",
	}, nil
}

// cmdContext は現在のRecallPackの内容を表示
func (m *MioAgent) cmdContext(ctx context.Context, sessionID string, _ string) (ChatCommandResult, error) {
	if m.conversationEngine == nil {
		return ChatCommandResult{
			Handled:  true,
			Response: "会話エンジンが無効です。",
		}, nil
	}

	pack, err := m.conversationEngine.BeginTurn(ctx, sessionID, "")
	if err != nil {
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("RecallPack取得に失敗: %v", err),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("📋 現在のコンテキスト\n")

	sb.WriteString(fmt.Sprintf("\n【ペルソナ】%s\n", pack.Persona.Name))

	if len(pack.ShortContext) > 0 {
		sb.WriteString(fmt.Sprintf("\n【短期記憶】%d件\n", len(pack.ShortContext)))
		for _, msg := range pack.ShortContext {
			sb.WriteString(fmt.Sprintf("  - [%s] %s\n", msg.Speaker, truncate(msg.Msg, 60)))
		}
	}

	if len(pack.MidSummaries) > 0 {
		sb.WriteString(fmt.Sprintf("\n【中期記憶】%d件\n", len(pack.MidSummaries)))
		for _, s := range pack.MidSummaries {
			sb.WriteString(fmt.Sprintf("  - %s\n", truncate(s.Summary, 80)))
		}
	}

	if len(pack.LongFacts) > 0 {
		sb.WriteString(fmt.Sprintf("\n【長期記憶】%d件\n", len(pack.LongFacts)))
		for _, f := range pack.LongFacts {
			sb.WriteString(fmt.Sprintf("  - %s\n", truncate(f, 80)))
		}
	}
	if m.userMemoryManager != nil {
		items, err := m.userMemoryManager.ListUserMemories(ctx, "ren", "", false, 50)
		if err != nil {
			sb.WriteString(fmt.Sprintf("\n【UserMemory】取得失敗: %v\n", err))
		} else {
			confirmed, pinned, candidate := countUserMemoryStates(items)
			sb.WriteString(fmt.Sprintf("\n【UserMemory】confirmed=%d pinned=%d candidate=%d\n", confirmed, pinned, candidate))
			if confirmed+pinned == 0 {
				sb.WriteString("  - Mioへ注入される confirmed/pinned 記憶はありません\n")
			}
		}
	}

	return ChatCommandResult{Handled: true, Response: sb.String()}, nil
}

func countUserMemoryStates(items []domainmemory.UserMemory) (confirmed int, pinned int, candidate int) {
	for _, item := range items {
		if !item.Active {
			continue
		}
		switch item.State {
		case domainmemory.MemoryStateConfirmed:
			confirmed++
		case domainmemory.MemoryStatePinned:
			pinned++
		case domainmemory.MemoryStateCandidate:
			candidate++
		}
	}
	return confirmed, pinned, candidate
}

// cmdNew はセッションをリセット
func (m *MioAgent) cmdNew(ctx context.Context, sessionID string) (ChatCommandResult, error) {
	if m.conversationEngine == nil {
		return ChatCommandResult{
			Handled:  true,
			Response: "会話エンジンが無効です。",
		}, nil
	}

	if err := m.conversationEngine.ResetSession(ctx, sessionID); err != nil {
		return ChatCommandResult{
			Handled:  true,
			Response: fmt.Sprintf("セッションリセットに失敗しました: %v", err),
		}, nil
	}

	return ChatCommandResult{
		Handled:  true,
		Response: "セッションをリセットしました。新しい会話を始めましょう！",
	}, nil
}

// truncate は文字列を指定文字数で切り詰める
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
