package conversation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type profileExtractorRequestProvider struct {
	response string
	req      llm.GenerateRequest
}

func (p *profileExtractorRequestProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.req = req
	return llm.GenerateResponse{Content: p.response}, nil
}

func (p *profileExtractorRequestProvider) Name() string { return "profile-extractor-test" }

func TestLLMProfileExtractorAcceptsOnlyStringPreferenceScalars(t *testing.T) {
	provider := &profileExtractorRequestProvider{
		response: `{"preferences":{"文字列":"SF"},"facts":[]}`,
	}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "SFと数値設定", nil))
	result, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.NewPreferences["文字列"] != "SF" || len(result.NewPreferences) != 1 {
		t.Fatalf("preferences=%v", result.NewPreferences)
	}
	if provider.req.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("ResponseFormat=%q want=%q", provider.req.ResponseFormat, llm.ResponseFormatJSONObject)
	}
	if !strings.Contains(provider.req.Messages[0].Content, "JSON の文字列") {
		t.Fatalf("prompt does not require string preference values: %s", provider.req.Messages[0].Content)
	}
}

func TestLLMProfileExtractorRejectsNonScalarPreferenceValues(t *testing.T) {
	for _, response := range []string{
		`{"preferences":{"number":3.25e+2},"facts":[]}`,
		`{"preferences":{"object":{"nested":true}},"facts":[]}`,
		`{"preferences":{"array":["x"]},"facts":[]}`,
		`{"preferences":{"boolean":true},"facts":[]}`,
		`{"preferences":{"null":null},"facts":[]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract succeeded for non-scalar preference value")
			}
		})
	}
}

func TestLLMProfileExtractorRejectsNonExactJSONResponse(t *testing.T) {
	for _, response := range []string{
		`prefix {"preferences":{},"facts":[]}`,
		`{"preferences":{},"facts":[]} suffix`,
		`{"preferences":{},"facts":[],"unknown":true}`,
		`{"preferences":{},"facts":[],"facts":[]}`,
		`{"preferences":{"key":"one","key":"two"},"facts":[]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract accepted non-exact JSON response")
			}
		})
	}
}

func TestLLMProfileExtractorRejectsOversizedOrInvalidFactOutput(t *testing.T) {
	for _, response := range []string{
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[""]}`,
		`{"preferences":{},"facts":["line\nbreak"]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract accepted invalid fact output")
			}
		})
	}

	provider := &profileExtractorRequestProvider{response: strings.Repeat("x", 64*1024+1)}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
	if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
		t.Fatal("Extract accepted oversized response")
	}
}

func TestSanitizeEvidenceTextStripsMarkupDumpButKeepsProse(t *testing.T) {
	dump := "以下にURL入ってると思う\n" + strings.Repeat(`<span data-testid="x" class="_ca0qidpf _u5f3idpf">`, 60) + "勉強会資料の次の図書を選定したい"

	cleaned := sanitizeEvidenceText(dump)

	if strings.Contains(cleaned, "data-testid") {
		t.Fatalf("markup survived: %q", cleaned)
	}
	for _, want := range []string{"以下にURL入ってると思う", "勉強会資料の次の図書を選定したい"} {
		if !strings.Contains(cleaned, want) {
			t.Fatalf("prose lost: %q missing from %q", want, cleaned)
		}
	}
}

func TestSanitizeEvidenceTextKeepsMarkupWhenTurnDiscussesIt(t *testing.T) {
	prose := "HTMLの<div>タグをどう扱うべきか相談したい。" + strings.Repeat("レンダリングの設計を詰めたい。", 30)

	cleaned := sanitizeEvidenceText(prose)

	if !strings.Contains(cleaned, "<div>") {
		t.Fatalf("low tag density must keep markup: %q", cleaned)
	}
}

func TestSanitizeEvidenceTextDropsShellTranscriptLines(t *testing.T) {
	transcript := "この出力どう思う？\nnyukimi@fujitsu-ubunts:~/work$ find . -type f\n/home/nyukimi/.codex/memories/MEMORY.md\n結果が多すぎる"

	cleaned := sanitizeEvidenceText(transcript)

	if strings.Contains(cleaned, "fujitsu-ubunts:~/work$") || strings.Contains(cleaned, "/home/nyukimi/.codex") {
		t.Fatalf("shell noise survived: %q", cleaned)
	}
	for _, want := range []string{"この出力どう思う？", "結果が多すぎる"} {
		if !strings.Contains(cleaned, want) {
			t.Fatalf("prose lost: %q missing from %q", want, cleaned)
		}
	}
}

func TestPlanEvidenceGroupsSplitsLongProseWithoutLosingTail(t *testing.T) {
	head := "本質的な高速化とは何かを整理したい。"
	tail := "最後にvllm-mlxとNative Metalの比較で締めたい。"
	middle := ""
	for i := 0; i < 700; i++ {
		middle += fmt.Sprintf("段落%dでは、同じモデルと同じ入力条件のまま速くする方法を検討する。\n\n", i)
	}

	groups, dropped := planEvidenceGroups([]string{head + "\n\n" + middle + tail})

	if len(groups) < 2 {
		t.Fatalf("long prose must be chunked: got %d groups", len(groups))
	}
	if dropped != 0 {
		t.Fatalf("nothing should be dropped here: dropped=%d", dropped)
	}
	joined := strings.Join(groups, "\n")
	for _, want := range []string{head, tail} {
		if !strings.Contains(joined, want) {
			t.Fatalf("chunking lost %q", want)
		}
	}
	for i, g := range groups {
		if utf8.RuneCountInString(g) > domainmemory.ProfilePromotionEvidenceBlockMax {
			t.Fatalf("group %d exceeds budget: %d", i, utf8.RuneCountInString(g))
		}
	}
}

func TestPlanEvidenceGroupsKeepsShortBatchInOneGroup(t *testing.T) {
	groups, dropped := planEvidenceGroups([]string{"Goで書き直したい", "SQLiteでいい", "深夜作業が多い"})

	if len(groups) != 1 || dropped != 0 {
		t.Fatalf("short batch must stay in one group: groups=%d dropped=%d", len(groups), dropped)
	}
	for _, want := range []string{"Goで書き直したい", "SQLiteでいい", "深夜作業が多い"} {
		if !strings.Contains(groups[0], want) {
			t.Fatalf("message lost: %q", want)
		}
	}
}

func TestPlanEvidenceGroupsReportsDroppedRunes(t *testing.T) {
	huge := ""
	for i := 0; i < 20000; i++ {
		huge += fmt.Sprintf("第%d節はそれぞれ異なる内容の本文がここに続く。\n\n", i)
	}

	groups, dropped := planEvidenceGroups([]string{huge})

	if len(groups) != domainmemory.ProfilePromotionEvidenceGroupMax {
		t.Fatalf("group count must be capped: got %d", len(groups))
	}
	if dropped <= 0 {
		t.Fatalf("overflow must be reported, not silent: dropped=%d", dropped)
	}
}

func TestMergeProfileExtractionResultsDeduplicatesAndCaps(t *testing.T) {
	a := &domconv.ProfileExtractionResult{
		NewPreferences: map[string]string{"言語": "Go"},
		NewFacts:       []string{"深夜に作業する"},
	}
	b := &domconv.ProfileExtractionResult{
		NewPreferences: map[string]string{"言語": "Rust", "DB": "SQLite"},
		NewFacts:       []string{"深夜に作業する ", "コーヒーを3杯飲む"},
	}

	merged := mergeProfileExtractionResults([]*domconv.ProfileExtractionResult{a, b}, 16)

	if merged.NewPreferences["言語"] != "Go" {
		t.Fatalf("first value must win: %q", merged.NewPreferences["言語"])
	}
	if merged.NewPreferences["DB"] != "SQLite" {
		t.Fatalf("later group preference lost")
	}
	if len(merged.NewFacts) != 2 {
		t.Fatalf("facts must dedupe: %v", merged.NewFacts)
	}

	capped := mergeProfileExtractionResults([]*domconv.ProfileExtractionResult{a, b}, 1)
	total := len(capped.NewPreferences) + len(capped.NewFacts)
	if total != 1 {
		t.Fatalf("limit must be honored: got %d", total)
	}
}

func TestSanitizeEvidenceTextNeutralizesBuildLogPaths(t *testing.T) {
	buildLog := `--extern flate2=C:\Users\nyuki\AppData\Local\Temp\pip-install-1320x6sd\tokenizers_8e0d\target\release\deps\libflate2-b7c442dd803ea471.rmeta --extern fs2=C:\Users\nyuki\AppData\Local\Temp\x\y.rmeta`

	cleaned := sanitizeEvidenceText(buildLog)

	if strings.Contains(cleaned, `C:\Users\nyuki\AppData`) {
		t.Fatalf("windows path survived: %q", cleaned)
	}
	if !strings.Contains(cleaned, evidencePathPlaceholder) {
		t.Fatalf("path should be replaced by a placeholder: %q", cleaned)
	}
	if strings.Contains(cleaned, `\\`) {
		t.Fatalf("backslash runs must be collapsed: %q", cleaned)
	}
}

func TestPlanEvidenceGroupsDeduplicatesRepastedDraft(t *testing.T) {
	draft := ""
	for i := 0; i < 400; i++ {
		draft += fmt.Sprintf("第%d章、宇宙船スターダスト号の船内は静かだった。カイ・ミナセは操縦席に立っていた。\n\n", i)
	}

	single, _ := planEvidenceGroups([]string{draft})
	twice, _ := planEvidenceGroups([]string{draft, "Canvas プロローグ\n\n" + draft})

	if len(twice) > len(single)+1 {
		t.Fatalf("re-pasted draft must not double the group cost: single=%d twice=%d", len(single), len(twice))
	}
}

func TestSplitEvidenceTurnSeparatesLeadInFromPastedMaterial(t *testing.T) {
	cases := []struct{ name, lead, body string }{
		{"URL貼り付け", "以下にURL入ってると思う", strings.Repeat("勉強会資料の選定について。", 100)},
		{"翻訳依頼", "まず、対訳して", strings.Repeat("Claude Fable 5 promotional access details. ", 60)},
		{"意見要求", "どう思う？", strings.Repeat("Piyasalar neden düşüyor sorusu. ", 80)},
		{"小説", "ちょっと書いてみたよ", strings.Repeat("宇宙船スターダスト号の船内は静かだった。", 80)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lead, material := splitEvidenceTurn(c.lead + "\n\n" + c.body)
			if lead != c.lead {
				t.Fatalf("lead mismatch: got %q want %q", lead, c.lead)
			}
			if !strings.Contains(material, strings.TrimSpace(c.body)[:20]) {
				t.Fatalf("material not captured: %.60s", material)
			}
		})
	}
}

func TestSplitEvidenceTurnKeepsOrdinaryTurnWhole(t *testing.T) {
	turn := "RenCrowのCOREをGoで書き直したい。\n\nDDDっぽくlayerを分けて、infrastructureにsqlite実装を置きたい。"

	lead, material := splitEvidenceTurn(turn)

	if material != "" {
		t.Fatalf("ordinary turn must not produce material: %q", material)
	}
	if lead != turn {
		t.Fatalf("ordinary turn must stay whole: %q", lead)
	}
}

func TestSplitEvidenceTurnTreatsPromptlessDumpAsMaterial(t *testing.T) {
	dump := `--extern flate2=C:\Users\nyuki\AppData\Local\Temp\x\libflate2.rmeta ` + strings.Repeat("--extern fs2=C:\\Users\\nyuki\\y.rmeta ", 40)

	lead, material := splitEvidenceTurn(dump)

	if lead != "" {
		t.Fatalf("machine dump must not be read as the user's words: %q", lead)
	}
	if material == "" {
		t.Fatalf("machine dump must become material")
	}
}

func TestBuildMaterialDigestStaysShortAndDeduplicates(t *testing.T) {
	long := strings.Repeat("同じ資料の本文がここに続く。", 500)

	digest := buildMaterialDigest([]string{long, long, strings.Repeat("別の資料の本文。", 500)})

	if utf8.RuneCountInString(digest) > domainmemory.ProfilePromotionMaterialDigestMax+len("- ")*3 {
		t.Fatalf("digest too long: %d", utf8.RuneCountInString(digest))
	}
	if strings.Count(digest, "同じ資料の本文") > domainmemory.ProfilePromotionMaterialExcerptMax {
		t.Fatalf("duplicate material must be collapsed")
	}
	if !strings.Contains(digest, "別の資料の本文") {
		t.Fatalf("second material lost: %.80s", digest)
	}
}

func TestExtractSendsMaterialAsTopicNotEvidence(t *testing.T) {
	provider := &profileExtractorRequestProvider{response: `{"preferences": {}, "facts": []}`}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := &domconv.Thread{ID: 1, Turns: []domconv.Message{{
		Speaker: domconv.SpeakerUser,
		Msg:     "ちょっと書いてみたよ\n\n" + strings.Repeat("宇宙船スターダスト号の船内は静かだった。カイ・ミナセは操縦席にいた。", 60),
	}}}

	if _, err := extractor.Extract(context.Background(), thread, domconv.NewUserProfile("ren")); err != nil {
		t.Fatal(err)
	}

	prompt := provider.req.Messages[0].Content
	if !strings.Contains(prompt, "参照資料（ユーザーが貼り付けた資料の冒頭") {
		t.Fatalf("material must be labelled as reference material")
	}
	leadIdx := strings.Index(prompt, "ちょっと書いてみたよ")
	materialIdx := strings.Index(prompt, "参照資料（ユーザーが貼り付けた資料の冒頭")
	if leadIdx < 0 || materialIdx < 0 || leadIdx > materialIdx {
		t.Fatalf("the user's own words must appear before the material section")
	}
	// 資料は抜粋だけが載る。原文60回分がそのまま入っていないことを確認する。
	occurrences := strings.Count(prompt, "カイ・ミナセ")
	if occurrences == 0 {
		t.Fatalf("material excerpt missing from prompt")
	}
	if occurrences >= 60 {
		t.Fatalf("whole draft leaked into the prompt: %d occurrences", occurrences)
	}
	if utf8.RuneCountInString(prompt) > 6000 {
		t.Fatalf("material must not inflate the prompt: %d runes", utf8.RuneCountInString(prompt))
	}
}
