package capability

import (
	"fmt"
	"sort"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
)

// CoderCapability は1つの coder スロットの能力情報
type CoderCapability struct {
	Name      string // "coder1" 〜 "coder4"
	Quality   int    // LLM 品質ランク（1=低 〜 5=高）
	Available bool   // enabled かつ LLM 疎通済み
}

const (
	SelectionReasonUnavailable             = "unavailable"
	SelectionReasonBelowRequiredQuality    = "below_required_quality"
	SelectionReasonSelectable              = "selectable"
	SelectionReasonSelected                = "selected"
	SelectionReasonSelectedWithDegradation = "selected_with_degradation"
)

// CoderSelectionCandidate は1つの coder 候補を選択判定でどう扱ったかを表す。
type CoderSelectionCandidate struct {
	Name    string
	Quality int
	Reason  string
}

// CoderSelectionEvidence は capability に基づく coder 選択理由を構造化した記録。
type CoderSelectionEvidence struct {
	RequestedRoute  routing.Route
	RequiredQuality int
	Selected        string
	SelectedQuality int
	DegradedRoute   routing.Route
	Candidates      []CoderSelectionCandidate
}

// SelectCoder は route の品質要件を満たす最適な coder を選択する。
//
// 動作:
//  1. route の minQuality を決定
//  2. Available かつ Quality >= minQuality の coder から最高品質を選択
//  3. 見つからない場合は品質要件を1段階下げて再試行（縮退）
//  4. 縮退した場合、degradedRoute に縮退後の route を返す（変化なければ元 route）
//
// 全 coder が利用不可の場合は error を返す。
func SelectCoder(coders []CoderCapability, route routing.Route) (selected string, degradedRoute routing.Route, err error) {
	selected, degradedRoute, _, err = SelectCoderWithEvidence(coders, route)
	return selected, degradedRoute, err
}

// SelectCoderWithEvidence は SelectCoder と同じ選択を行い、候補ごとの判定理由を返す。
func SelectCoderWithEvidence(coders []CoderCapability, route routing.Route) (selected string, degradedRoute routing.Route, evidence CoderSelectionEvidence, err error) {
	minQuality := qualityRequirement(route)
	evidence = CoderSelectionEvidence{
		RequestedRoute:  route,
		RequiredQuality: minQuality,
		Candidates:      buildInitialSelectionCandidates(coders, minQuality),
	}

	// 品質要件を満たす候補を試行（縮退あり）
	for minQuality >= 1 {
		candidates := availableCandersWithMinQuality(coders, minQuality)
		if len(candidates) > 0 {
			// 最高品質を選択（同品質なら名前順で安定化）
			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].Quality != candidates[j].Quality {
					return candidates[i].Quality > candidates[j].Quality
				}
				return candidates[i].Name < candidates[j].Name
			})
			chosen := candidates[0].Name
			degraded := routeForQuality(candidates[0].Quality)
			if degraded == route {
				degraded = "" // 縮退なし
			}
			evidence.Selected = chosen
			evidence.SelectedQuality = candidates[0].Quality
			evidence.DegradedRoute = degraded
			markSelectedCandidate(&evidence, chosen, degraded)
			return chosen, degraded, evidence, nil
		}
		minQuality--
	}

	return "", "", evidence, fmt.Errorf("no available coder for route %s", route)
}

// qualityRequirement は route の最低品質要件を返す（仕様 §2.2）
func qualityRequirement(route routing.Route) int {
	switch route {
	case routing.RouteCODE3:
		return 5
	case routing.RouteCODE2:
		return 4
	case routing.RouteCODE1:
		return 3
	default: // CODE, CODE4
		return 2
	}
}

// routeForQuality は品質ランクに対応する代替ルートを返す（縮退通知用）
func routeForQuality(quality int) routing.Route {
	switch {
	case quality >= 5:
		return routing.RouteCODE3
	case quality >= 4:
		return routing.RouteCODE2
	case quality >= 3:
		return routing.RouteCODE1
	default:
		return routing.RouteCODE
	}
}

// availableCandersWithMinQuality は利用可能かつ minQuality 以上の coder を返す
func availableCandersWithMinQuality(coders []CoderCapability, minQuality int) []CoderCapability {
	var result []CoderCapability
	for _, c := range coders {
		if c.Available && c.Quality >= minQuality {
			result = append(result, c)
		}
	}
	return result
}

func buildInitialSelectionCandidates(coders []CoderCapability, requiredQuality int) []CoderSelectionCandidate {
	candidates := make([]CoderSelectionCandidate, 0, len(coders))
	for _, c := range coders {
		reason := SelectionReasonSelectable
		switch {
		case !c.Available:
			reason = SelectionReasonUnavailable
		case c.Quality < requiredQuality:
			reason = SelectionReasonBelowRequiredQuality
		}
		candidates = append(candidates, CoderSelectionCandidate{
			Name:    c.Name,
			Quality: c.Quality,
			Reason:  reason,
		})
	}
	return candidates
}

func markSelectedCandidate(evidence *CoderSelectionEvidence, selected string, degraded routing.Route) {
	reason := SelectionReasonSelected
	if degraded != "" {
		reason = SelectionReasonSelectedWithDegradation
	}
	for i := range evidence.Candidates {
		if evidence.Candidates[i].Name == selected {
			evidence.Candidates[i].Reason = reason
			return
		}
	}
}
