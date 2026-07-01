package memory

import (
	"strings"
	"testing"
	"time"
)

func TestCutoverBoundaryAndLogicalDateUseTokyoFourAM(t *testing.T) {
	loc, err := time.LoadLocation(CutoverTimezone)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	beforeCutover := time.Date(2026, 6, 12, 3, 59, 0, 0, loc)
	boundary := GetCutoverBoundary(beforeCutover)
	if want := time.Date(2026, 6, 11, CutoverHour, 0, 0, 0, loc); !boundary.Equal(want) {
		t.Fatalf("boundary before cutover: got %s want %s", boundary, want)
	}
	logical := GetLogicalDate(beforeCutover)
	if want := time.Date(2026, 6, 11, 0, 0, 0, 0, loc); !logical.Equal(want) {
		t.Fatalf("logical date before cutover: got %s want %s", logical, want)
	}

	afterCutover := time.Date(2026, 6, 12, 4, 1, 0, 0, loc)
	boundary = GetCutoverBoundary(afterCutover)
	if want := time.Date(2026, 6, 12, CutoverHour, 0, 0, 0, loc); !boundary.Equal(want) {
		t.Fatalf("boundary after cutover: got %s want %s", boundary, want)
	}
	logical = GetLogicalDate(afterCutover)
	if want := time.Date(2026, 6, 12, 0, 0, 0, 0, loc); !logical.Equal(want) {
		t.Fatalf("logical date after cutover: got %s want %s", logical, want)
	}
}

func TestFormatCutoverNote(t *testing.T) {
	note := FormatCutoverNote("ユーザーは朝に作業した", []string{"Mio: おはよう", "User: 続けて"})
	for _, want := range []string{
		"## Session Summary",
		"ユーザーは朝に作業した",
		"## Last Messages",
		"Mio: おはよう\nUser: 続けて",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q: %s", want, note)
		}
	}
	if got := FormatCutoverNote("", nil); got != "" {
		t.Fatalf("empty note: got %q", got)
	}
}

func TestBuildNamespaceAndValidateUserMemoryType(t *testing.T) {
	namespace, err := BuildNamespace(" user ", " ren ")
	if err != nil {
		t.Fatalf("BuildNamespace failed: %v", err)
	}
	if namespace != "user:ren" {
		t.Fatalf("namespace: got %q", namespace)
	}
	if _, err := BuildNamespace("user", " "); err == nil {
		t.Fatal("empty namespace id should fail")
	}
	if _, err := BuildNamespace("misc", "ren"); err == nil {
		t.Fatal("invalid namespace kind should fail")
	}

	for _, memoryType := range []string{
		UserMemoryTypeProfile,
		UserMemoryTypePreference,
		UserMemoryTypeProject,
		UserMemoryTypeConstraint,
		UserMemoryTypeRelationship,
		UserMemoryTypeEpisode,
		UserMemoryTypeSkill,
		UserMemoryTypeSensitive,
	} {
		if err := ValidateUserMemoryType(memoryType); err != nil {
			t.Fatalf("ValidateUserMemoryType(%q) failed: %v", memoryType, err)
		}
	}
	if err := ValidateUserMemoryType("rumor"); err == nil {
		t.Fatal("invalid user memory type should fail")
	}
}

func TestValidateNamespace(t *testing.T) {
	for _, namespace := range []string{"conv:123", "user:ren", "char:mio", "kb:ai"} {
		if err := ValidateNamespace(namespace); err != nil {
			t.Fatalf("ValidateNamespace(%q) failed: %v", namespace, err)
		}
	}
	for _, namespace := range []string{"", "user:", "misc:ren", "kb:"} {
		if err := ValidateNamespace(namespace); err == nil {
			t.Fatalf("ValidateNamespace(%q) should fail", namespace)
		}
	}
}

func TestCanPromoteUserMemoryRequiresEvidence(t *testing.T) {
	for _, state := range []string{MemoryStateObserved, MemoryStateCandidate, MemoryStateConfirmed, MemoryStatePinned} {
		if err := ValidateMemoryState(state); err != nil {
			t.Fatalf("ValidateMemoryState(%q) failed: %v", state, err)
		}
	}
	if err := ValidateMemoryState("archived"); err == nil {
		t.Fatal("invalid memory state should fail")
	}
	if err := CanPromoteUserMemory("archived", nil, "normal", ""); err == nil {
		t.Fatal("invalid state promotion should fail")
	}
	if err := CanPromoteUserMemory(MemoryStateObserved, nil, "normal", ""); err != nil {
		t.Fatalf("observed should not require promotion evidence: %v", err)
	}
	if err := CanPromoteUserMemory(MemoryStateCandidate, nil, "normal", ""); err != nil {
		t.Fatalf("candidate should not require promotion evidence: %v", err)
	}
	if err := CanPromoteUserMemory(MemoryStateConfirmed, nil, "normal", "user_explicit"); err == nil {
		t.Fatal("confirmed user memory without evidence should fail")
	}
	if err := CanPromoteUserMemory(MemoryStatePinned, []string{"evt_1"}, "normal", ""); err == nil {
		t.Fatal("pinned user memory without explicit reason should fail")
	}
	if err := CanPromoteUserMemory(MemoryStatePinned, []string{"evt_1"}, "sensitive", "user_explicit"); err == nil {
		t.Fatal("sensitive memory should not auto-promote")
	}
	if err := CanPromoteUserMemory(MemoryStateConfirmed, []string{"evt_1"}, "normal", "user_explicit"); err != nil {
		t.Fatalf("confirmed with evidence should pass: %v", err)
	}
}

func TestIsUserMemoryPromptInjectable(t *testing.T) {
	base := UserMemory{
		State:       MemoryStateConfirmed,
		Active:      true,
		Sensitivity: "normal",
		Scope:       "all_personas",
	}
	if !IsUserMemoryPromptInjectable(base, "mio") {
		t.Fatal("confirmed active normal all_personas memory should be injectable")
	}
	global := base
	global.Scope = "global"
	if !IsUserMemoryPromptInjectable(global, "mio") {
		t.Fatal("global memory should be injectable")
	}
	cases := []UserMemory{
		{State: MemoryStateCandidate, Active: true, Sensitivity: "normal", Scope: "all_personas"},
		{State: MemoryStateConfirmed, Active: false, Sensitivity: "normal", Scope: "all_personas"},
		{State: MemoryStateConfirmed, Active: true, Sensitivity: "sensitive", Scope: "all_personas"},
		{State: MemoryStateConfirmed, Active: true, Sensitivity: "normal", Scope: "worker"},
		{State: MemoryStateConfirmed, Active: true, Sensitivity: "normal", Scope: "all_personas", SupersededBy: "new"},
		{State: MemoryStateConfirmed, Active: true, Sensitivity: "normal", Scope: "all_personas", LifecycleStatus: "decayed"},
	}
	for _, tc := range cases {
		if IsUserMemoryPromptInjectable(tc, "mio") {
			t.Fatalf("memory should not be injectable: %+v", tc)
		}
	}
}
