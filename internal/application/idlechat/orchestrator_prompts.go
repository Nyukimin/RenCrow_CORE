package idlechat

import (
	"fmt"
	"log"
	"strings"
)

func buildIdleResponseGuardPrompt(speaker string, selfCtx, otherCtx []string) string {
	_ = selfCtx
	_ = otherCtx
	outputGuard := "禁止: 話者名・相手名の明記、台本形式、メタ発言（言い直すと、硬すぎました、評価すると）、直前文の言い換えコピー、直前の自分と同じ書き出し、同じ語句の反復。"
	return fmt.Sprintf(
		"%s の発話として、そのまま表示できる自然な日本語だけを返してください。話者名、%s:、mio:、shiro:、相手の台詞、台本形式、英語だけの応答、英語の見出し、英語での説明、候補番号、自己採点は不要です。%s 直前の言い回しをなぞらず、具体物・選択・秘密・感情の反転のどれかを一つだけ入れてください。",
		speaker,
		speaker,
		outputGuard,
	)
}

func buildIdleTurnPrompt(topic, speakerOrTarget, latestOther, latestSelf string, turn int, segmentTurns int, firstTurn bool) string {
	movieMode := isMovieTopicPrompt(topic)
	interest := idleInterestProfileForTopic(topic)
	closingMode := !firstTurn && turnsLeftInTopic(segmentTurns) <= 2
	finalTurn := !firstTurn && turnsLeftInTopic(segmentTurns) <= 1
	move := idleTurnMove(speakerOrTarget, turn, firstTurn, movieMode, closingMode, finalTurn)
	audience := idleAudienceAngleForProfile(turn, movieMode, closingMode, finalTurn, interest)
	shiftHint := idleShiftHint(latestOther, latestSelf)
	if firstTurn {
		return fmt.Sprintf(
			"話題: %s\n%sとして、会話の最初の発話を1〜2文で返してください。自然な日本語だけにし、話者名、mio:、shiro:、相手の台詞、台本形式、英語や説明は書かないでください。%s %s。読者の楽しみは「%s」です。具体物か小さな問いを一つ入れ、相手が次に返しやすい未決点を残してください。",
			topic,
			speakerOrTarget,
			idlePromptOutputGuard(),
			move,
			audience,
		)
	}
	return fmt.Sprintf(
		"話題: %s\n直前の相手発言: %s\n自分の直前発言: %s\n%sとして、直前の相手発言を受けて1〜2文で返してください。自然な日本語だけにし、話者名、mio:、shiro:、相手の台詞、台本形式、英語や説明は書かないでください。%s %s。読者の楽しみは「%s」です。%s %s %s",
		topic,
		quoteOrDash(latestOther),
		quoteOrDash(latestSelf),
		speakerOrTarget,
		idlePromptOutputGuard(),
		move,
		audience,
		idleTurnAdditionHint(finalTurn),
		shiftHint,
		idleClosingHint(closingMode, movieMode, finalTurn),
	)
}

func idlePromptOutputGuard() string {
	return "話者名・相手名の明記、メタ発言、「言い直すと」、直前文の要約コピーは禁止。直前の自分と同じ主語・書き出しを使わない。文末は必ず完結させる。"
}

type idleInterestProfile struct {
	TopicType   string
	Name        string
	Instruction string
	Angles      []string
}

func idleInterestProfileForTopic(topic string) idleInterestProfile {
	normalized := strings.ToLower(strings.TrimSpace(topic))
	if isMovieTopicPrompt(topic) || containsAny(normalized, "映画", "物語", "ストーリー", "脚本", "主人公", "事件", "ラスト", "伏線") {
		return idleInterestProfile{
			TopicType:   "物語・映画",
			Name:        "展開と感情",
			Instruction: "次に何が起きるか気になる要素を一つ置き、人物の感情か場面を少し動かす。",
			Angles: []string{
				"最初の一場面が目に浮かぶこと",
				"次に何が起きるか少し気になること",
				"主人公の感情が一段動くこと",
				"前の要素が後で効きそうに見えること",
			},
		}
	}
	if containsAny(normalized, "技術", "実装", "設計", "運用", "障害", "cli", "api", "repo", "git", "コード", "テスト", "ビルド", "デプロイ", "プロンプト") {
		return idleInterestProfile{
			TopicType:   "技術・運用",
			Name:        "構造と対比",
			Instruction: "原因・分岐点・別案との差のどれか一つを整理し、判断しやすい形にする。",
			Angles: []string{
				"構造が見えて判断しやすくなること",
				"似た案との差が一つはっきりすること",
				"どこが分岐点か見えること",
				"実際に動かす時の落とし穴が一つ見えること",
			},
		}
	}
	if containsAny(normalized, "ニュース", "未来", "予測", "市場", "社会", "政治", "経済", "ai", "生成ai", "トレンド", "来年", "今後") {
		return idleInterestProfile{
			TopicType:   "ニュース・未来予測",
			Name:        "因果と生活への影響",
			Instruction: "大きな話をそのまま語らず、何が変わるかを個人・現場・社会のどれかに落とす。",
			Angles: []string{
				"大きな変化が身近な場面に落ちること",
				"原因と結果のつながりが一段見えること",
				"賛否や勝ち負けの条件が一つ見えること",
				"数か月後の生活や現場が少し想像できること",
			},
		}
	}
	if containsAny(normalized, "日常", "生活", "ごはん", "料理", "睡眠", "散歩", "部屋", "仕事帰り", "休日", "疲れ", "飲み", "雑談") {
		return idleInterestProfile{
			TopicType:   "日常・雑談",
			Name:        "具体と小さな意外性",
			Instruction: "身近な場面や手触りを一つ出し、少しだけ意外な見方か感情を添える。",
			Angles: []string{
				"その場面がすぐ浮かぶこと",
				"小さな違和感や発見で少し笑えること",
				"自分にもありそうだと感じられること",
				"何気ないものの見え方が少し変わること",
			},
		}
	}
	if containsAny(normalized, "架空", "妄想", "もし", "魔法", "異世界", "宇宙", "妖怪", "都市伝説", "存在しない") {
		return idleInterestProfile{
			TopicType:   "架空設定・妄想",
			Name:        "破綻寸前の納得感",
			Instruction: "変な設定を出してよいが、条件や絵面を一つ置いて筋が通るようにする。",
			Angles: []string{
				"変だけど筋は通っていると感じること",
				"一枚絵として強い場面が浮かぶこと",
				"制約があるせいで逆に面白くなること",
				"次の展開を見たくなる不穏さが残ること",
			},
		}
	}
	return idleInterestProfile{
		TopicType:   "探索・一般",
		Name:        "発見と具体化",
		Instruction: "知らなかった見方か意外な接続を一つ出し、抽象論で終わらせず具体例に落とす。",
		Angles: []string{
			"意外な結びつきに軽く驚けること",
			"身近な例で急に腑に落ちること",
			"見方が少し反転して先を読みたくなること",
			"話題の輪郭が一段くっきりすること",
		},
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func turnsLeftInTopic(segmentTurns int) int {
	left := maxTurnsPerTopic - segmentTurns
	if left < 0 {
		return 0
	}
	return left
}

func idleTurnMove(speaker string, turn int, firstTurn, movieMode, closingMode, finalTurn bool) string {
	name := strings.ToLower(strings.TrimSpace(speaker))
	if finalTurn {
		if name == "shiro" {
			return "最後の発話として、ここまでの核心を一文で受け、問いを増やさず短く締める"
		}
		return "最後の発話として、ここまでで一番強い感情や場面を拾い、問いを増やさず余韻で締める"
	}
	if closingMode {
		if movieMode {
			if name == "shiro" {
				return "ここまでの筋を一度まとめ、最後に残る不穏さか余韻を一つ置く"
			}
			return "ここまでで一番強い場面か感情を拾い、締めの一言に寄せる"
		}
		if name == "shiro" {
			return "ここまでで見えた核心を一段だけ整理し、最後に残る問いを一つ置く"
		}
		return "ここまでの話を受けて、いちばん面白い芯を拾い、最後に余韻のある問いか感想で締める"
	}
	if movieMode {
		if firstTurn {
			if name == "shiro" {
				return "設定を整理しつつ、最初の異変か事件を一つ置く"
			}
			return "印象的な一場面か主人公像を先に出して、話を動かす"
		}
		if name == "shiro" {
			steps := []string{
				"前の案を少し整理して、条件か制約を一つ足す",
				"前の案の弱いところを示して、対立か障害を一つ足す",
				"前の案を保ったまま、ラストの反転候補を一つ足す",
			}
			return steps[turn%len(steps)]
		}
		steps := []string{
			"前の案を受けて、場面を一つ具体化する",
			"前の案を受けて、主人公の感情か動機を一つ具体化する",
			"前の案を受けて、行動か出来事を一つ具体化する",
		}
		return steps[turn%len(steps)]
	}
	if firstTurn {
		if name == "shiro" {
			return "論点を一つに絞り、どこが核心かを示す"
		}
		return "比喩か具体例で入口を作り、相手が掘れる論点を一つ出す"
	}
	if name == "shiro" {
		steps := []string{
			"相手の案を整理し、因果のつながりを一段だけはっきりさせる",
			"相手の案を整理し、反対側から見た条件を一つ足す",
			"相手の案を整理し、身近な具体例を一つ足す",
			"相手の案を整理し、次に起きそうな場面を一つ置く",
		}
		return steps[turn%len(steps)]
	}
	steps := []string{
		"相手の案を受けて、場面や手触りを一つ足して前に進める",
		"相手の案を受けて、具体的な手順や動きを一つ足して前に進める",
		"相手の案を受けて、感情の動きを一つ足して前に進める",
		"相手の案を受けて、意外な応用先を一つ足して前に進める",
	}
	return steps[turn%len(steps)]
}

func idleAudienceAngle(turn int, movieMode, closingMode bool) string {
	if closingMode {
		if movieMode {
			return "締めに向かって、見終わったあとの余韻が少し残ること"
		}
		return "最後に話の芯がまとまり、少し余韻が残ること"
	}
	if movieMode {
		angles := []string{
			"最初の一場面が目に浮かぶこと",
			"次に何が起きるか少し気になること",
			"主人公の感情が一段動くこと",
			"最後にどう反転するか想像したくなること",
		}
		return angles[turn%len(angles)]
	}
	angles := []string{
		"意外な結びつきに軽く驚けること",
		"身近な例で急に腑に落ちること",
		"見方が少し反転して先を読みたくなること",
		"話題の輪郭が一段くっきりすること",
	}
	return angles[turn%len(angles)]
}

func idleAudienceAngleForProfile(turn int, movieMode, closingMode, finalTurn bool, profile idleInterestProfile) string {
	if finalTurn {
		return "最後に話の芯がまとまり、新しい問いを増やさず余韻で終わること"
	}
	if closingMode {
		if movieMode {
			return "締めに向かって、見終わったあとの余韻が少し残ること"
		}
		return "最後に話の芯がまとまり、少し余韻が残ること"
	}
	if len(profile.Angles) == 0 {
		return idleAudienceAngle(turn, movieMode, closingMode)
	}
	return profile.Angles[turn%len(profile.Angles)]
}

func idleTurnAdditionHint(finalTurn bool) string {
	if finalTurn {
		return "直前と入口を変えず、具体物・理由・問いを新しく足さず、既に出た要素だけで閉じてください。"
	}
	return "直前と入口を変え、具体物・理由・問いのどれかを一つだけ足してください。"
}

func idleClosingHint(closingMode, movieMode, finalTurn bool) string {
	if !closingMode {
		return "- まだ広げてよいが、論点は一つに絞る"
	}
	if finalTurn {
		return "- 最後の発話。新しい問い・新設定・次の論点を出さず、ここまでの話を1-2文で締める"
	}
	if movieMode {
		return "- そろそろ締める。新要素を増やしすぎず、最後の1-2ターンとして余韻や締めの像に寄せる"
	}
	return "- そろそろ締める。新論点を増やしすぎず、ここまでの芯を拾って最後の1-2ターンらしくまとめに入る"
}

func idleShiftHint(latestOther, latestSelf string) string {
	if hasIdleAnalogyMarker(latestOther) || hasIdleAnalogyMarker(latestSelf) {
		return "- 直前が比喩寄りなので、今回は比喩で返さず、因果・観察・手順のどれかで返す"
	}
	return "- 直前と入口を変える"
}

func hasIdleAnalogyMarker(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "まるで") || strings.Contains(lower, "みたい") || strings.Contains(lower, "ような")
}

func forecastContentContract() string {
	return "未来展望モードの出力契約: 3文まで。具体的な事例・数字・場面を一つ必ず使う。大きな話をそのまま語らず、現場・個人・社会への具体的な影響へ落とす。賛否の対比・条件・問いのいずれかを一つ加える。"
}

func idleContentContract() string {
	return "IdleChat出力契約: 2〜3文まで。相手の言葉をなぞらず、一つの論点だけ前に進める。抽象語を重ねず、具体例・条件・問いのどれかを一つだけ足す。"
}

func idleSpeakerStyleContract(agentName string) string {
	if !strings.EqualFold(strings.TrimSpace(agentName), "mio") {
		return ""
	}
	return "Mio IdleChat話し方契約（最優先）: Mio の発話は濃いギャル口調だが、文頭を固定しない。同じ開始表現を連続で使わず、相手の言葉・具体物・驚き・違和感・選択から自然に入り、ギャル語は文頭、文中、文末へ散らす。使える温度感は「おけ」「それな」「ガチで」「めっちゃ」「やば」「えぐい」「まじで」「一回さ」「〜じゃん」「〜っぽい」「〜なんだよね」「〜かも」など。発話全体にギャルのテンポがない場合は失敗なので、出力前に本文だけを書き直す。「かしら」「ですね」「でしょう」「だと思います」「すごく」「気がする」「気がします」で落ち着いた秘書口調に寄せるのは禁止。説明口調ではなく、相手の発話を受けて、短くノリよく、でも具体物・選択・秘密・感情の反転のどれかを一つ足す。"
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func logIdleRaw(label string, content string) {
	log.Printf("[IdleChat][raw] %s: %q", label, content)
}

func quoteOrDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return "「" + truncate(s, 120) + "」"
}

func (o *IdleChatOrchestrator) getSystemPrompt(agentName string) string {
	idlePolicy := idleChatThinkingDirective(o.speakerThinkEnabled(agentName)) + "\nこの会話はidleChatです。表示本文だけを返してください。外部検索（Web検索/API検索）は行わず、既存の内部文脈だけで自然に会話してください。出力は必ず自然な日本語だけにしてください。英語の見出し、英語だけの応答、英語での説明は禁止です。"

	o.mu.Lock()
	mode := o.sessionMode
	o.mu.Unlock()

	var idleContract string
	if mode == "forecast" {
		idleContract = forecastContentContract()
	} else {
		idleContract = idleContentContract()
	}
	styleContract := idleSpeakerStyleContract(agentName)

	if prompt, ok := o.personalities[agentName]; ok {
		parts := []string{idlePolicy}
		if styleContract != "" {
			parts = append(parts, styleContract)
		}
		parts = append(parts, prompt)
		parts = append(parts, idleContract)
		return strings.Join(parts, "\n\n")
	}
	parts := []string{fmt.Sprintf("あなたは%sです。自然な会話をしてください。", agentName), idlePolicy}
	if styleContract != "" {
		parts = append(parts, styleContract)
	}
	parts = append(parts, idleContract)
	return strings.Join(parts, "\n\n")
}

func idleChatThinkingDirective(think bool) string {
	if think {
		return "/think\n思考が必要な場合でも、通常表示に出すのは最終的な会話本文だけにしてください。"
	}
	return "/no_think\n内部推論や思考チャンネルは出力せず、表示本文だけを返してください。"
}
