package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

type routineDiaryTranscriptLine struct {
	ActorID string `json:"actor_id"`
	Text    string `json:"text"`
}

const routineDiaryLegacyHeader = "記録の範囲: COREの確定記録と、認証済みWORLDイベント・canonical GAMES sessionで確認できた事実のみです。\n"

// routineDiaryIsLegacyFormat recognizes only the exact header emitted by the
// previous renderer. A body that merely mentions the old wording is not legacy.
func routineDiaryIsLegacyFormat(date, body string) bool {
	return strings.HasPrefix(body, date+" の記録\n\n"+routineDiaryLegacyHeader)
}

// renderRoutineDiary turns confirmed source facts into a short life record.
// It deliberately describes only observed events and transcript text.
func renderRoutineDiary(date string, facts []agentroutine.Activity, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	ordered := append([]agentroutine.Activity(nil), facts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].At.IsZero() != ordered[j].At.IsZero() {
			return !ordered[i].At.IsZero()
		}
		if !ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].At.Before(ordered[j].At)
		}
		return ordered[i].Key < ordered[j].Key
	})

	lines := make([]string, 0, 12)
	appendSleepAndWake := func() {
		for _, fact := range ordered {
			if fact.At.IsZero() || (fact.Kind != agentroutine.ActivitySleep && fact.Kind != agentroutine.ActivityWake) {
				continue
			}
			verb := "起きました"
			if fact.Kind == agentroutine.ActivitySleep {
				verb = "就寝しました"
			}
			lines = appendUniqueDiaryLine(lines, fmt.Sprintf("%sごろに%s。", fact.At.In(location).Format("15:04"), verb))
		}
	}
	appendSleepAndWake()

	lines = append(lines, routineDiaryConversationLines(ordered, location)...)
	lines = append(lines, routineDiaryRelaxLines(ordered, location)...)
	lines = append(lines, routineDiaryWorkAndActivityLines(ordered, location)...)
	lines = append(lines, routineDiaryMovementLines(ordered, location)...)

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s の記録\n\n", date)
	if len(lines) == 0 {
		builder.WriteString("この日に記録できる出来事はありませんでした。\n")
		return builder.String()
	}
	for i, line := range lines {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func routineDiaryConversationLines(facts []agentroutine.Activity, location *time.Location) []string {
	type quoteKey struct{ period, peer string }
	quotes := make(map[quoteKey]string)
	metadata := make(map[string]struct{})
	for _, fact := range facts {
		if fact.Kind != agentroutine.ActivityMorningChat || fact.Source != agentRoutineModuleID || fact.At.IsZero() {
			if (fact.Kind == agentroutine.ActivityConversation || fact.Kind == agentroutine.ActivityMorningChat) && !fact.At.IsZero() {
				metadata[routineDiaryDaypart(fact.At.In(location))] = struct{}{}
			}
			continue
		}
		var transcript []routineDiaryTranscriptLine
		period := routineDiaryDaypart(fact.At.In(location))
		if fact.ActorID == "" || json.Unmarshal([]byte(fact.Reference), &transcript) != nil || len(transcript) == 0 {
			metadata[period] = struct{}{}
			continue
		}
		selfFound := false
		for _, speaker := range transcript {
			if speaker.ActorID == fact.ActorID && strings.TrimSpace(speaker.Text) != "" {
				selfFound = true
			}
		}
		if !selfFound {
			metadata[period] = struct{}{}
			continue
		}
		hasPeer := false
		addedQuote := false
		for _, speaker := range transcript {
			if speaker.ActorID == "" || speaker.ActorID == fact.ActorID {
				continue
			}
			text := routineDiaryQuoteText(speaker.Text)
			if text == "" {
				continue
			}
			hasPeer = true
			key := quoteKey{period: period, peer: speaker.ActorID}
			if quotes[key] == "" {
				quotes[key] = text
				addedQuote = true
			}
		}
		if !hasPeer || !addedQuote {
			metadata[period] = struct{}{}
		}
	}

	keys := make([]quoteKey, 0, len(quotes))
	for key := range quotes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].period != keys[j].period {
			return routineDiaryDaypartOrder(keys[i].period) < routineDiaryDaypartOrder(keys[j].period)
		}
		return keys[i].peer < keys[j].peer
	})
	lines := make([]string, 0, len(keys)+len(metadata))
	quotedPeriods := make(map[string]struct{})
	for _, key := range keys {
		peer := routineDiaryActorName(key.peer)
		lines = append(lines, fmt.Sprintf("%sは%sと話しました。%sは「%s」と話していました。", key.period, peer, peer, quotes[key]))
		quotedPeriods[key.period] = struct{}{}
	}
	periods := make([]string, 0, len(metadata))
	for period := range metadata {
		periods = append(periods, period)
	}
	sort.Slice(periods, func(i, j int) bool {
		return routineDiaryDaypartOrder(periods[i]) < routineDiaryDaypartOrder(periods[j])
	})
	for _, period := range periods {
		if _, hasQuote := quotedPeriods[period]; hasQuote {
			continue
		}
		lines = append(lines, period+"に人との会話に応じました。")
	}
	return lines
}

func routineDiaryRelaxLines(facts []agentroutine.Activity, location *time.Location) []string {
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	segment := 0
	ordered := append([]agentroutine.Activity(nil), facts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	for _, fact := range ordered {
		if fact.Kind == agentroutine.ActivityRelax && fact.Source == agentRoutineModuleID {
			if strings.TrimSpace(fact.Summary) == "" || fact.At.IsZero() {
				continue
			}
			period := routineDiaryDaypart(fact.At.In(location))
			roomSummary := strings.TrimSpace(fact.Summary)
			key := fmt.Sprintf("%s:%d:%s", period, segment, roomSummary)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			roomSummary = strings.Replace(roomSummary, "静かに過ごしました。", "静かに過ごす", 1)
			roomSummary = strings.Replace(roomSummary, "ゆっくり過ごしました。", "ゆっくり過ごす", 1)
			lines = append(lines, period+"は"+roomSummary+"ひとときがありました。")
			continue
		}
		if routineDiaryInterruptsRest(fact) {
			segment++
		}
	}
	return lines
}

func routineDiaryInterruptsRest(fact agentroutine.Activity) bool {
	switch fact.Kind {
	case agentroutine.ActivitySleep, agentroutine.ActivityWake, agentroutine.ActivityMorningChat,
		agentroutine.ActivityConversation, agentroutine.ActivityWork, agentroutine.ActivityHobby:
		return true
	default:
		return false
	}
}

func routineDiaryWorkAndActivityLines(facts []agentroutine.Activity, location *time.Location) []string {
	work := make(map[string]agentroutine.Activity)
	lines := make([]string, 0)
	seenActivity := make(map[string]struct{})
	for _, fact := range facts {
		if fact.Kind == agentroutine.ActivityWork && fact.At.IsZero() == false {
			sourceID := fact.SourceID
			if sourceID == "" {
				sourceID = fact.Key
			}
			key := fact.Source + ":" + sourceID
			previous, ok := work[key]
			if !ok || fact.At.After(previous.At) || (fact.At.Equal(previous.At) &&
				(routineDiaryWorkOutcomePriority(fact.Summary) > routineDiaryWorkOutcomePriority(previous.Summary) ||
					routineDiaryWorkOutcomePriority(fact.Summary) == routineDiaryWorkOutcomePriority(previous.Summary) && fact.Key > previous.Key)) {
				work[key] = fact
			}
			continue
		}
		if fact.Kind == agentroutine.ActivityDiary || fact.Kind == agentroutine.ActivitySleep || fact.Kind == agentroutine.ActivityWake ||
			fact.Kind == agentroutine.ActivityMorningChat || fact.Kind == agentroutine.ActivityConversation || fact.Kind == agentroutine.ActivityRelax {
			continue
		}
		if strings.TrimSpace(fact.Summary) == "" {
			continue
		}
		text := routineDiaryActivityText(fact, location)
		if fact.Source != "routine_media_playback" && fact.Source != "canonical_game_session" {
			period := ""
			if !fact.At.IsZero() {
				period = routineDiaryDaypart(fact.At.In(location))
			}
			text = strings.TrimPrefix(text, fact.At.In(location).Format("15:04")+"ごろ、")
			key := period + ":" + strings.TrimSpace(text)
			if _, ok := seenActivity[key]; ok {
				continue
			}
			seenActivity[key] = struct{}{}
			if period != "" {
				text = period + "には" + text
			}
		}
		lines = appendUniqueDiaryLine(lines, text)
	}
	workFacts := make([]agentroutine.Activity, 0, len(work))
	for key := range work {
		workFacts = append(workFacts, work[key])
	}
	sort.SliceStable(workFacts, func(i, j int) bool {
		if !workFacts[i].At.Equal(workFacts[j].At) {
			return workFacts[i].At.Before(workFacts[j].At)
		}
		return workFacts[i].Key < workFacts[j].Key
	})
	for _, fact := range workFacts {
		action := strings.TrimSpace(fact.Summary)
		action = routineDiaryWorkAction(action)
		lines = appendUniqueDiaryLine(lines, fmt.Sprintf("%sごろに%s。", fact.At.In(location).Format("15:04"), action))
	}
	return lines
}

func routineDiaryWorkAction(summary string) string {
	summary = strings.ToLower(strings.TrimSpace(summary))
	switch {
	case strings.Contains(summary, "完了") || strings.Contains(summary, "completed"):
		return "作業を完了しました"
	case strings.Contains(summary, "失敗") || strings.Contains(summary, "failed"):
		return "作業が失敗しました"
	case strings.Contains(summary, "中止") || strings.Contains(summary, "cancelled"):
		return "作業を中止しました"
	case strings.Contains(summary, "中断") || strings.Contains(summary, "interrupted"):
		return "作業が中断されました"
	case strings.Contains(summary, "置き換") || strings.Contains(summary, "superseded"):
		return "作業が置き換わりました"
	case strings.Contains(summary, "担当が変更") || strings.Contains(summary, "reassigned"):
		return "作業の担当が変わりました"
	case strings.Contains(summary, "待機") || strings.Contains(summary, "waiting"):
		return "作業は待機状態になりました"
	case strings.Contains(summary, "停止") || strings.Contains(summary, "blocked"):
		return "作業は停止状態になりました"
	case strings.Contains(summary, "開始") || strings.Contains(summary, "started"):
		return "作業を始めました"
	default:
		return "作業の状態が更新されました"
	}
}

func routineDiaryWorkOutcomePriority(summary string) int {
	if strings.Contains(summary, "開始") || strings.Contains(strings.ToLower(summary), "started") {
		return 0
	}
	return 1
}

func routineDiaryActivityText(fact agentroutine.Activity, location *time.Location) string {
	summary := routineDiaryActivitySummary(fact)
	summary = strings.TrimSpace(summary)
	summary = strings.TrimPrefix(summary, "GAMESで")
	summary = strings.TrimSuffix(summary, "遊んだ")
	if fact.Source == "canonical_game_session" {
		summary = strings.ReplaceAll(summary, "遊んだ", "")
		summary = strings.ReplaceAll(summary, "（初回 ", "（")
		summary = strings.ReplaceAll(summary, "、最終 ", "〜")
		summary = strings.ReplaceAll(summary, " JST）", "）")
		summary += "遊びました"
	}
	summary = strings.TrimPrefix(summary, "WORLDで")
	summary = strings.ReplaceAll(summary, "運動の開始を確認しました。", "運動を始めました。")
	summary = strings.ReplaceAll(summary, "運動の完了を確認しました。", "運動を終えました。")
	summary = strings.ReplaceAll(summary, "ファッションショーの開始を確認しました。", "ファッションショーを始めました。")
	summary = strings.ReplaceAll(summary, "ファッションショーの完了を確認しました。", "ファッションショーを終えました。")
	summary = strings.ReplaceAll(summary, "BL読書の開始を確認しました。", "BL読書を始めました。")
	summary = strings.ReplaceAll(summary, "BL読書の完了を確認しました。", "BL読書を終えました。")
	summary = strings.ReplaceAll(summary, "ファッションショーを途中で停止したことを確認しました。", "ファッションショーを途中で止めました。")
	summary = strings.ReplaceAll(summary, "BL読書を途中で停止したことを確認しました。", "BL読書を途中で止めました。")
	summary = strings.ReplaceAll(summary, "鏡のポーズ「", "鏡の前で「")
	summary = strings.ReplaceAll(summary, "」の練習開始を確認しました。", "」の練習を始めました。")
	summary = strings.ReplaceAll(summary, "」の練習完了を確認しました。", "」の練習を終えました。")
	summary = strings.ReplaceAll(summary, "」の完了を確認しました。", "」のポーズを終えました。")
	summary = strings.ReplaceAll(summary, "の完了を確認しました。", "を終えました。")
	if fact.Source != "routine_media_playback" && fact.Source != "canonical_game_session" {
		summary = routineDiaryClipRunes(summary, 180)
	}
	if fact.At.IsZero() {
		return summary
	}
	return fact.At.In(location).Format("15:04") + "ごろ、" + summary
}

func routineDiaryClipRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	if maximum < 2 {
		return string(runes[:maximum])
	}
	return string(runes[:maximum-1]) + "…"
}

func routineDiaryMovementLines(facts []agentroutine.Activity, location *time.Location) []string {
	if len(facts) == 0 {
		return nil
	}
	for _, fact := range facts {
		if fact.Source != "world_life_event" || !strings.Contains(fact.Summary, "移動") {
			return nil
		}
	}
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	arrivals := make([]agentroutine.Activity, 0)
	stopped := make([]agentroutine.Activity, 0)
	starts := make([]agentroutine.Activity, 0)
	for _, fact := range facts {
		if fact.Source != "world_life_event" || fact.At.IsZero() || !strings.Contains(fact.Summary, "移動完了") {
			if fact.Source == "world_life_event" && !fact.At.IsZero() {
				switch {
				case strings.Contains(fact.Summary, "移動を途中で停止"):
					stopped = append(stopped, fact)
				case strings.Contains(fact.Summary, "移動を開始"):
					starts = append(starts, fact)
				}
			}
			continue
		}
		arrivals = append(arrivals, fact)
	}
	selected := arrivals
	if len(selected) == 0 {
		selected = stopped
	}
	if len(selected) == 0 {
		selected = starts
	}
	for _, fact := range selected {
		room := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(fact.Summary, "WORLDで自宅内の"), "への移動完了を確認しました。"))
		verb := "へ移りました"
		if strings.Contains(fact.Summary, "移動を途中で停止") {
			room = "自宅内の移動"
			verb = "を途中で止めました"
		} else if strings.Contains(fact.Summary, "移動を開始") {
			room = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(fact.Summary, "WORLDで自宅内の"), "への移動を開始しました。"))
			verb = "への移動を始めました"
		}
		if room == "" || room == fact.Summary {
			continue
		}
		key := routineDiaryDaypart(fact.At.In(location)) + ":" + room
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, fmt.Sprintf("%sごろ、%s%s。", fact.At.In(location).Format("15:04"), room, verb))
	}
	return lines
}

func routineDiaryQuoteText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) > 64 {
		value = string([]rune(value)[:63]) + "…"
	}
	return value
}

func routineDiaryActorName(actorID string) string {
	names := map[string]string{"mio": "ミオ", "shiro": "シロ", "midori": "ミドリ", "kuro": "クロ", "aka": "アカ", "ao": "アオ", "kin": "キン", "gin": "ギン"}
	if name := names[actorID]; name != "" {
		return name
	}
	return "相手"
}

func routineDiaryDaypart(at time.Time) string {
	switch hour := at.Hour(); {
	case hour < 5:
		return "深夜"
	case hour < 10:
		return "朝"
	case hour < 12:
		return "午前"
	case hour < 17:
		return "日中"
	case hour < 19:
		return "夕方"
	default:
		return "夜"
	}
}

func routineDiaryDaypartOrder(period string) int {
	for i, value := range []string{"深夜", "朝", "午前", "日中", "夕方", "夜"} {
		if period == value {
			return i
		}
	}
	return len([]rune(period)) + 10
}

func appendUniqueDiaryLine(lines []string, line string) []string {
	for _, existing := range lines {
		if existing == line {
			return lines
		}
	}
	return append(lines, line)
}
