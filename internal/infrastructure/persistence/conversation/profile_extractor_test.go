package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type profileExtractorRequestProvider struct {
	response  string
	responses []string
	err       error
	errs      []error
	req       llm.GenerateRequest
	requests  []llm.GenerateRequest
	calls     int
}

func (p *profileExtractorRequestProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.req = req
	p.requests = append(p.requests, req)
	if len(p.errs) >= p.calls && p.errs[p.calls-1] != nil {
		return llm.GenerateResponse{}, p.errs[p.calls-1]
	}
	if p.err != nil {
		return llm.GenerateResponse{}, p.err
	}
	response := p.response
	if len(p.responses) >= p.calls {
		response = p.responses[p.calls-1]
	}
	return llm.GenerateResponse{Content: response}, nil
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

func TestLLMProfileExtractorRepairsInvalidResponseOnce(t *testing.T) {
	provider := &profileExtractorRequestProvider{responses: []string{
		`prefix {"preferences":{},"facts":[]}`,
		`{"preferences":{"言語":"Go"},"facts":[]}`,
	}}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "言語", nil))

	result, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err != nil {
		t.Fatalf("Extract failed after repair: %v", err)
	}
	if provider.calls != 2 || len(provider.requests) != 2 {
		t.Fatalf("provider calls=%d requests=%d want one initial plus one repair", provider.calls, len(provider.requests))
	}
	if provider.requests[0].MaxTokens != domainmemory.ProfilePromotionMaxTokens || provider.requests[1].MaxTokens != domainmemory.ProfilePromotionMaxTokens {
		t.Fatalf("initial/repair MaxTokens=%d/%d want %d", provider.requests[0].MaxTokens, provider.requests[1].MaxTokens, domainmemory.ProfilePromotionMaxTokens)
	}
	if initialRunes := utf8.RuneCountInString(provider.requests[0].Messages[0].Content); initialRunes > domainmemory.ProfilePromotionInitialPromptMax {
		t.Fatalf("initial prompt has %d runes, want <=%d", initialRunes, domainmemory.ProfilePromotionInitialPromptMax)
	}
	if repairRunes := utf8.RuneCountInString(provider.requests[1].Messages[1].Content); repairRunes > domainmemory.ProfilePromotionRepairInstructionMax {
		t.Fatalf("repair instruction has %d runes, want <=%d", repairRunes, domainmemory.ProfilePromotionRepairInstructionMax)
	}
	for index, request := range provider.requests {
		runes := 0
		for _, message := range request.Messages {
			runes += utf8.RuneCountInString(message.Content)
		}
		if runes > domainmemory.ProfilePromotionPromptMax {
			t.Fatalf("request %d prompt has %d runes, want <=%d", index, runes, domainmemory.ProfilePromotionPromptMax)
		}
	}
	if result.NewPreferences["言語"] != "Go" {
		t.Fatalf("repaired result=%v", result)
	}
	if len(provider.requests[1].Messages) != 2 || provider.requests[1].Messages[1].Content != profileExtractorRepairInstruction(4) {
		t.Fatalf("repair request did not use fixed corrective instruction: %#v", provider.requests[1].Messages)
	}
	if strings.Contains(provider.requests[1].Messages[1].Content, "prefix") {
		t.Fatal("repair instruction must not echo raw invalid provider output")
	}
}

func TestLLMProfileExtractorRepairsLengthViolation(t *testing.T) {
	tooLong := strings.Repeat("あ", domainmemory.ProfilePromotionPreferenceValueMax+1)
	provider := &profileExtractorRequestProvider{responses: []string{
		fmt.Sprintf(`{"preferences":{"好み":%q},"facts":[]}`, tooLong),
		`{"preferences":{"好み":"Go"},"facts":[]}`,
	}}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "好み", nil))

	result, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err != nil {
		t.Fatalf("Extract failed after length repair: %v", err)
	}
	if provider.calls != 2 || result.NewPreferences["好み"] != "Go" {
		t.Fatalf("calls=%d result=%v want one length repair", provider.calls, result)
	}
	if !strings.Contains(provider.requests[1].Messages[1].Content, "各文字列は200文字以内") ||
		!strings.Contains(provider.requests[1].Messages[1].Content, "最大4件") {
		t.Fatalf("repair instruction omits bounded contract: %q", provider.requests[1].Messages[1].Content)
	}
}

func TestLLMProfileExtractorDoesNotRepairProviderUnavailable(t *testing.T) {
	secret := errors.New("provider private payload TOP-SECRET")
	provider := &profileExtractorRequestProvider{err: secret}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))

	_, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err == nil || !errors.Is(err, domconv.ErrProfileExtractorUnavailable) {
		t.Fatalf("error=%v want unavailable category", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d want no internal retry", provider.calls)
	}
	if strings.Contains(err.Error(), "TOP-SECRET") {
		t.Fatalf("provider detail leaked through domain error: %v", err)
	}
}

func TestLLMProfileExtractorRepairsAtMostOnce(t *testing.T) {
	provider := &profileExtractorRequestProvider{responses: []string{
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[]}`,
	}}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))

	_, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err == nil || !errors.Is(err, domconv.ErrProfileExtractorInvalid) {
		t.Fatalf("error=%v want invalid category after failed repair", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d want one repair maximum", provider.calls)
	}
}

func TestLLMProfileExtractorRepairBudgetIsPerExtractAcrossGroups(t *testing.T) {
	provider := &profileExtractorRequestProvider{responses: []string{
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[]}`,
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[]}`,
	}}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, strings.Repeat("a", 5000), nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, strings.Repeat("b", 5000), nil))

	_, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err == nil || !errors.Is(err, domconv.ErrProfileExtractorInvalid) {
		t.Fatalf("error=%v want second group invalid after global repair budget", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls=%d want two initial generations plus one repair", provider.calls)
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

func TestLLMProfileExtractorUsesLogicalRequestBudgets(t *testing.T) {
	provider := &profileExtractorRequestProvider{response: `{"preferences": {}, "facts": []}`}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := &domconv.Thread{ID: 1, Turns: []domconv.Message{
		{Speaker: domconv.SpeakerUser, Msg: strings.Repeat("e", 7000)},
		{Speaker: domconv.SpeakerUser, Msg: "資料を確認したい\n\n" + strings.Repeat("資料本文。", 1000)},
	}}
	existing := domconv.UserProfile{
		Preferences: map[string]string{"topic": strings.Repeat("p", 500)},
		Facts:       []string{strings.Repeat("f", 500)},
	}

	if _, err := extractor.Extract(context.Background(), thread, existing); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("extractor did not call provider")
	}
	req := provider.requests[0]
	if req.MaxTokens != 1024 {
		t.Errorf("MaxTokens=%d want logical budget 1024", req.MaxTokens)
	}
	prompt := req.Messages[0].Content
	if !strings.Contains(prompt, "最大4件まで") {
		t.Errorf("per-group candidate limit is not bounded to four: %q", prompt)
	}
	requestRunes := 0
	for _, message := range req.Messages {
		requestRunes += utf8.RuneCountInString(message.Content)
	}
	if requestRunes > 7160 {
		t.Errorf("complete request prompt exceeds logical bound: %d", requestRunes)
	}
}

func TestLLMProfileExtractorStrictlyEnforcesPerGroupCandidateLimit(t *testing.T) {
	overflow := `{"preferences":{"a":"1","b":"2","c":"3","d":"4","e":"5"},"facts":[]}`
	provider := &profileExtractorRequestProvider{responses: []string{overflow, overflow}}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "候補上限", nil))

	_, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err == nil || !errors.Is(err, domconv.ErrProfileExtractorInvalid) {
		t.Fatalf("error=%v want invalid per-group overflow", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d want one initial plus one repair", provider.calls)
	}
	if !strings.Contains(provider.requests[1].Messages[1].Content, "最大4件") {
		t.Fatalf("repair instruction omitted per-group limit: %q", provider.requests[1].Messages[1].Content)
	}
}

func TestPlanEvidenceGroupsUsesThreeThousandRuneBlocks(t *testing.T) {
	groups, dropped := planEvidenceGroups([]string{strings.Repeat("e", 7000)})
	if dropped != 0 {
		t.Fatalf("test input should fit group ceiling: dropped=%d", dropped)
	}
	for index, group := range groups {
		if got := utf8.RuneCountInString(group); got > 3000 {
			t.Fatalf("group %d has %d runes, want <=3000", index, got)
		}
	}
}

func TestBuildMaterialDigestUsesEightHundredRuneBudget(t *testing.T) {
	digest := buildMaterialDigest([]string{strings.Repeat("a", 500), strings.Repeat("b", 500)})
	if got := utf8.RuneCountInString(digest); got > 800 {
		t.Fatalf("material digest has %d runes, want <=800", got)
	}
}

func TestLLMProfileExtractorBoundsExistingContextToCompleteLines(t *testing.T) {
	provider := &profileExtractorRequestProvider{response: `{"preferences": {}, "facts": []}`}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "既知情報の確認", nil))
	existing := domconv.UserProfile{
		Preferences: map[string]string{
			"first":  strings.Repeat("a", 300),
			"second": strings.Repeat("b", 300),
		},
		Facts: []string{strings.Repeat("c", 300)},
	}

	if _, err := extractor.Extract(context.Background(), thread, existing); err != nil {
		t.Fatal(err)
	}
	prompt := provider.req.Messages[0].Content
	start := strings.Index(prompt, "既知情報:\n")
	if start < 0 {
		t.Fatalf("existing context markers missing: %q", prompt)
	}
	endOffset := strings.Index(prompt[start:], "\n\n会話")
	if endOffset < 0 {
		t.Fatalf("existing context end marker missing: %q", prompt)
	}
	section := prompt[start : start+endOffset]
	if got := utf8.RuneCountInString(section); got > 800 {
		t.Fatalf("existing context has %d runes, want <=800", got)
	}
	if strings.HasSuffix(section, "a") || strings.HasSuffix(section, "b") || strings.HasSuffix(section, "c") {
		t.Fatalf("existing context ended mid-line: %q", section)
	}
}
