package capability

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRuntimeCapabilitySnapshotIsSortedAndDeduplicated(t *testing.T) {
	input := RuntimeCapabilitySnapshot{Entries: []RuntimeCapability{
		{Kind: CapabilityKindMCP, Name: "serena.search", Status: CapabilityStatusAvailable},
		{Kind: CapabilityKindTool, Name: " shell ", Status: CapabilityStatusAvailable, Origin: "core_runtime"},
		{Kind: CapabilityKindSkill, Name: "review", Status: CapabilityStatusAvailable},
		{Kind: CapabilityKindTool, Name: "file_read", Status: CapabilityStatusUnavailable, Reason: "runner未接続"},
		{Kind: CapabilityKindTool, Name: "Shell", Status: CapabilityStatusAvailable, Description: "重複定義"},
		{Kind: CapabilityKindMCP, Name: "serena.fetch", Status: CapabilityStatusUnavailable, Reason: "サーバー未起動"},
		{Kind: CapabilityKindTool, Name: "browser.run", Status: CapabilityStatusAvailable},
		{Kind: CapabilityKindSkill, Name: "review", Status: CapabilityStatusAvailable, Description: "重複定義"},
	}}
	original := append([]RuntimeCapability(nil), input.Entries...)

	got := Normalize(input)
	wantNames := []string{"browser.run", "file_read", "shell", "review", "serena.fetch", "serena.search"}
	if len(got.Entries) != len(wantNames) {
		t.Fatalf("Normalize() returned %d entries, want %d: %#v", len(got.Entries), len(wantNames), got.Entries)
	}
	for i, want := range wantNames {
		if got.Entries[i].Name != want {
			t.Errorf("entry %d name = %q, want %q", i, got.Entries[i].Name, want)
		}
	}
	if got.Entries[1].Status != CapabilityStatusUnavailable || got.Entries[1].Reason != "runner未接続" {
		t.Fatalf("conflicting duplicate must fail closed, got %#v", got.Entries[1])
	}
	if !reflect.DeepEqual(input.Entries, original) {
		t.Fatalf("Normalize() mutated input: %#v", input.Entries)
	}
}

func TestRenderStableRuntimeContextIncludesSectionsStatusAndExecutionBoundary(t *testing.T) {
	got := RenderStableRuntimeContext(RuntimeCapabilitySnapshot{Entries: []RuntimeCapability{
		{Kind: CapabilityKindTool, Name: "z_tool", Status: CapabilityStatusAvailable, Origin: "core_runtime"},
		{Kind: CapabilityKindTool, Name: "a_tool", Status: CapabilityStatusAvailable},
		{Kind: CapabilityKindSkill, Name: "review", Status: CapabilityStatusUnavailable, Reason: "無効化"},
		{Kind: CapabilityKindMCP, Name: "serena.search", Status: CapabilityStatusAvailable, Origin: "serena"},
	}})

	for _, want := range []string{
		"### Tools",
		"### Skills",
		"### MCP",
		"利用可能: a_tool",
		"利用可能: z_tool",
		"利用不可: review",
		"理由: 無効化",
		"利用可能: serena.search",
		"この一覧は認識用であり、実行権限を付与しません。",
		"許可されたRunner",
		"定義済みhandoff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered context does not contain %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "利用可能: a_tool") > strings.Index(got, "利用可能: z_tool") {
		t.Errorf("tools are not sorted:\n%s", got)
	}
	if strings.Count(got, "a_tool") != 1 {
		t.Errorf("tool should appear once, got %d:\n%s", strings.Count(got, "a_tool"), got)
	}
}

func TestRenderStableRuntimeContextShowsEmptySectionsWithoutClaimingAvailability(t *testing.T) {
	got := RenderStableRuntimeContext(RuntimeCapabilitySnapshot{})
	for _, section := range []string{"Tools", "Skills", "MCP"} {
		if !strings.Contains(got, "### "+section) {
			t.Errorf("empty context is missing %s section:\n%s", section, got)
		}
	}
	if strings.Count(got, "利用可能: なし") != 3 || strings.Count(got, "利用不可: なし") != 3 {
		t.Fatalf("empty sections must explicitly show none for both states:\n%s", got)
	}
	if strings.Contains(got, "利用可能: あり") {
		t.Fatalf("empty context must not claim availability:\n%s", got)
	}
}

func TestNormalizeUnavailableEntryAlwaysHasReason(t *testing.T) {
	got := Normalize(RuntimeCapabilitySnapshot{Entries: []RuntimeCapability{
		{Kind: CapabilityKindTool, Name: "shell", Status: CapabilityStatusUnavailable},
		{Kind: CapabilityKindSkill, Name: "review", Status: "unknown"},
	}})
	for _, entry := range got.Entries {
		if entry.Status != CapabilityStatusUnavailable {
			t.Errorf("unknown/unavailable status was not fail-closed: %#v", entry)
		}
		if strings.TrimSpace(entry.Reason) == "" {
			t.Errorf("unavailable entry has no reason: %#v", entry)
		}
	}
}
