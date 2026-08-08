package durablestore

import "strings"

func NormalizeStorageIntent(message string) (StorageRequirement, bool) {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return StorageRequirement{}, false
	}
	storageWord := containsAny(normalized, "db", "database", "データベース", "保存", "蓄積", "永続", "記録", "アーカイブ")
	actionWord := containsAny(normalized, "実装", "構築", "作って", "作成", "保存する", "蓄積して", "記録する", "できるように", "設計", "比較", "検討", "確認")
	if !storageWord || !actionWord {
		return StorageRequirement{}, false
	}
	// 一般知識の質問は通常Chatへ残す。
	if containsAny(normalized, "について教えて", "とは？", "とは何") && !containsAny(normalized, "設計", "比較", "実装", "構築") {
		return StorageRequirement{}, false
	}
	outcome := OutcomeImplement
	if containsAny(normalized, "設計案", "比較", "検討", "確認して", "評価") && !containsAny(normalized, "実装して", "構築して", "作って") {
		outcome = OutcomeAssess
	}
	owner := inferOwner(normalized)
	req := StorageRequirement{
		RequestedOutcome: outcome,
		FactsToStore:     []string{strings.TrimSpace(message)},
		OwnerHint:        owner,
		OwnerModule:      owner,
	}
	if containsAny(normalized, "xのbookmark", "x bookmark", "x bookmark", "ブックマーク", "bookmark") {
		req.FactsToStore = []string{"x_bookmark"}
		req.SourceSystems = []string{"X"}
		if req.OwnerModule == "" {
			req.OwnerModule = "RenCrow_CORE"
		}
	}
	return req, true
}

func inferOwner(message string) string {
	switch {
	case containsAny(message, "game", "ゲーム", "観測値", "replay"):
		return "RenCrow_GAMES"
	case containsAny(message, "trade", "投資", "取引"):
		return "RenCrow_TRADE"
	case containsAny(message, "tts", "音声合成"):
		return "RenCrow_TTS"
	case containsAny(message, "stt", "音声認識"):
		return "RenCrow_STT"
	case containsAny(message, "vision", "画像認識", "映像認識"):
		return "RenCrow_Vision"
	case containsAny(message, "bookmark", "ブックマーク", "会話", "memory", "記憶"):
		return "RenCrow_CORE"
	default:
		return ""
	}
}

func containsAny(value string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}
