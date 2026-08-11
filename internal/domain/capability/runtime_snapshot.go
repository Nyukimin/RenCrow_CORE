package capability

import (
	"sort"
	"strings"
)

// CapabilityKind はAgentに認識させる能力の分類。
type CapabilityKind string

const (
	CapabilityKindTool  CapabilityKind = "tool"
	CapabilityKindSkill CapabilityKind = "skill"
	CapabilityKindMCP   CapabilityKind = "mcp"
)

// CapabilityStatus は能力の実行環境上の状態を表す。
// 利用可能であっても、Agentへの実行許可を意味しない。
type CapabilityStatus string

const (
	CapabilityStatusAvailable   CapabilityStatus = "available"
	CapabilityStatusUnavailable CapabilityStatus = "unavailable"
)

// RuntimeCapability はAgentに提示する能力の最小メタデータ。
// Origin、Description、Reasonには公開可能な短いラベルだけを渡す。
// ファイルパス、認証情報、その他の秘密情報は含めない。
type RuntimeCapability struct {
	Kind        CapabilityKind
	Status      CapabilityStatus
	Name        string
	Description string
	Origin      string
	Reason      string
}

// RuntimeCapabilitySnapshot はAgentが認識する実行時能力の一覧。
// Entriesは入力順に依存し、Normalizeで安定化してから利用する。
type RuntimeCapabilitySnapshot struct {
	Entries []RuntimeCapability
}

const (
	unknownCapabilityReason = "状態不明"
	emptyCapabilityReason   = "理由不明"
	redactedCapabilityText  = "非表示"
)

// Normalize はスナップショットをコピーし、分類・名前ごとに重複排除して
// 安定した順序へ正規化する。入力スライスは変更しない。
// 状態が競合する重複は、利用可能と誤認させないため利用不可を優先する。
func Normalize(snapshot RuntimeCapabilitySnapshot) RuntimeCapabilitySnapshot {
	byKey := make(map[string]RuntimeCapability, len(snapshot.Entries))
	for _, raw := range snapshot.Entries {
		entry, ok := normalizeCapability(raw)
		if !ok {
			continue
		}
		key := string(entry.Kind) + "\x00" + entry.Name
		current, exists := byKey[key]
		if !exists || preferCapability(entry, current) {
			byKey[key] = entry
		}
	}

	entries := make([]RuntimeCapability, 0, len(byKey))
	for _, entry := range byKey {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return capabilityKindRank(entries[i].Kind) < capabilityKindRank(entries[j].Kind)
		}
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		if entries[i].Status != entries[j].Status {
			return entries[i].Status < entries[j].Status
		}
		return entries[i].Origin < entries[j].Origin
	})

	return RuntimeCapabilitySnapshot{Entries: entries}
}

// RenderStableRuntimeContext はAgent向けの安定した日本語コンテキストを描画する。
// 実行、権限判定、fallback、外部参照は行わない。
func RenderStableRuntimeContext(snapshot RuntimeCapabilitySnapshot) string {
	normalized := Normalize(snapshot)
	byKind := map[CapabilityKind][]RuntimeCapability{
		CapabilityKindTool:  nil,
		CapabilityKindSkill: nil,
		CapabilityKindMCP:   nil,
	}
	for _, entry := range normalized.Entries {
		byKind[entry.Kind] = append(byKind[entry.Kind], entry)
	}

	var builder strings.Builder
	builder.WriteString("## Runtime Capability Snapshot\n")
	builder.WriteString("この一覧は認識用であり、実行権限を付与しません。Agentは許可されたRunnerでのみ実行し、担当外の作業は定義済みhandoffへ渡してください。\n")

	for _, section := range []struct {
		kind  CapabilityKind
		label string
	}{
		{kind: CapabilityKindTool, label: "Tools"},
		{kind: CapabilityKindSkill, label: "Skills"},
		{kind: CapabilityKindMCP, label: "MCP"},
	} {
		builder.WriteString("\n### ")
		builder.WriteString(section.label)
		builder.WriteByte('\n')
		renderCapabilitySection(&builder, byKind[section.kind])
	}
	return builder.String()
}

func normalizeCapability(raw RuntimeCapability) (RuntimeCapability, bool) {
	raw.Kind = CapabilityKind(strings.ToLower(strings.TrimSpace(string(raw.Kind))))
	switch raw.Kind {
	case CapabilityKindTool, CapabilityKindSkill, CapabilityKindMCP:
	default:
		return RuntimeCapability{}, false
	}
	raw.Name = strings.ToLower(strings.TrimSpace(raw.Name))
	if raw.Name == "" || strings.ContainsAny(raw.Name, "/\\\r\n") {
		return RuntimeCapability{}, false
	}
	raw.Status = CapabilityStatus(strings.ToLower(strings.TrimSpace(string(raw.Status))))
	switch raw.Status {
	case CapabilityStatusAvailable:
	case CapabilityStatusUnavailable:
		if strings.TrimSpace(raw.Reason) == "" {
			raw.Reason = emptyCapabilityReason
		}
	default:
		raw.Status = CapabilityStatusUnavailable
		if strings.TrimSpace(raw.Reason) == "" {
			raw.Reason = unknownCapabilityReason
		}
	}
	raw.Description = normalizeMetadata(raw.Description)
	raw.Origin = normalizeMetadata(raw.Origin)
	raw.Reason = normalizeMetadata(raw.Reason)
	if raw.Status == CapabilityStatusUnavailable && raw.Reason == "" {
		raw.Reason = emptyCapabilityReason
	}
	return raw, true
}

func preferCapability(candidate, current RuntimeCapability) bool {
	if candidate.Status != current.Status {
		return candidate.Status == CapabilityStatusUnavailable
	}
	if candidate.Origin != current.Origin {
		if candidate.Origin == "" {
			return false
		}
		if current.Origin == "" {
			return true
		}
		return candidate.Origin < current.Origin
	}
	if candidate.Description != current.Description {
		if candidate.Description == "" {
			return false
		}
		if current.Description == "" {
			return true
		}
		return candidate.Description < current.Description
	}
	return candidate.Reason < current.Reason
}

func capabilityKindRank(kind CapabilityKind) int {
	switch kind {
	case CapabilityKindTool:
		return 0
	case CapabilityKindSkill:
		return 1
	case CapabilityKindMCP:
		return 2
	default:
		return 3
	}
}

func renderCapabilitySection(builder *strings.Builder, entries []RuntimeCapability) {
	available := 0
	for _, entry := range entries {
		if entry.Status != CapabilityStatusAvailable {
			continue
		}
		available++
		builder.WriteString("- 利用可能: ")
		builder.WriteString(entry.Name)
		if entry.Description != "" {
			builder.WriteString("\n  説明: ")
			builder.WriteString(entry.Description)
		}
		if entry.Origin != "" {
			builder.WriteString("\n  提供元: ")
			builder.WriteString(entry.Origin)
		}
		builder.WriteByte('\n')
	}
	if available == 0 {
		builder.WriteString("- 利用可能: なし\n")
	}

	unavailable := 0
	for _, entry := range entries {
		if entry.Status == CapabilityStatusAvailable {
			continue
		}
		unavailable++
		builder.WriteString("- 利用不可: ")
		builder.WriteString(entry.Name)
		builder.WriteString("\n  理由: ")
		builder.WriteString(entry.Reason)
		builder.WriteByte('\n')
	}
	if unavailable == 0 {
		builder.WriteString("- 利用不可: なし\n")
	}
}

// normalizeMetadataは改行などによるPrompt構造の混入を防ぎ、
// パスを含む値は安定コンテキストへ出さない。
func normalizeMetadata(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || strings.ContainsAny(value, `/\\`) {
		return ""
	}
	if strings.Contains(value, "//") || strings.Contains(value, "://") {
		return ""
	}
	if strings.Contains(value, "secret") || strings.Contains(value, "token=") {
		return redactedCapabilityText
	}
	return value
}
