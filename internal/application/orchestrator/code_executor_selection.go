package orchestrator

import (
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// codeTarget はコーダー選択の結果
type codeTarget struct {
	name         string
	coder        CoderAgent
	systemPrompt string
	release      func() // CoderStatus解放用
}

// selectCoderForRoute はルートに応じてCoderを選択
func (e *DefaultCodeExecutor) selectCoderForRoute(route routing.Route) (codeTarget, error) {
	if name, prompt, ok := explicitCodeRouteTarget(route); ok {
		return e.selectExplicitCoderForRoute(route, name, prompt)
	}

	switch route {
	case routing.RouteCODE:
		return e.selectDefaultCoderForGenericRoute(route)
	default:
		return codeTarget{}, fmt.Errorf("unknown code route: %s", route)
	}
}

func (e *DefaultCodeExecutor) selectExplicitCoderForRoute(route routing.Route, name, prompt string) (codeTarget, error) {
	coder := e.coderByName(name)
	if coder == nil {
		return codeTarget{}, fmt.Errorf("%s route requested but no %s available", route, name)
	}
	if e.coderCaps != nil {
		available := false
		for _, cap := range e.coderCaps {
			if normalizeCoderStatusName(cap.Name) == name && cap.Available {
				available = true
				break
			}
		}
		if !available {
			return codeTarget{}, fmt.Errorf("%s route requested but %s is unavailable", route, name)
		}
	}
	if !e.coderStatus.Acquire(name) {
		return codeTarget{}, fmt.Errorf("%s route requested but %s is busy", route, name)
	}
	log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=explicit", route, name)
	return codeTarget{
		name:         name,
		coder:        coder,
		systemPrompt: prompt,
		release:      func() { e.coderStatus.Release(name) },
	}, nil
}

func (e *DefaultCodeExecutor) selectDefaultCoderForGenericRoute(route routing.Route) (codeTarget, error) {
	available := make(map[string]bool, len(e.coderCaps))
	for _, cap := range e.coderCaps {
		name := normalizeCoderStatusName(cap.Name)
		if name != "" && cap.Available {
			available[name] = true
		}
	}

	candidates := make([]string, 0, len(coderStatusSlotOrder))
	externalCandidates := make([]string, 0)
	for _, name := range coderStatusSlotOrder {
		if !available[name] || e.coderByName(name) == nil {
			continue
		}
		if e.externalCoders[name] {
			externalCandidates = append(externalCandidates, name)
			log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=external_requires_explicit_route", route, name)
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) == 0 {
		if len(externalCandidates) > 0 {
			return codeTarget{}, fmt.Errorf("CODE route requested but %s uses an external provider; use CODE1/CODE2/CODE3/CODE4 explicitly to use an external coder", externalCandidates[0])
		}
		return codeTarget{}, fmt.Errorf("CODE route requested but no enabled, probe-ready local coder is available")
	}

	name, ok := e.coderStatus.AcquireNext(candidates)
	if !ok {
		log.Printf("[CodeExecutor] coder skip route=%s reason=all_ready_candidates_busy candidates=%d", route, len(candidates))
		return codeTarget{}, fmt.Errorf("CODE route requested but all ready coders are busy")
	}
	coder := e.coderByName(name)
	log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=round_robin candidates=%d", route, name, len(candidates))
	return codeTarget{
		name:         name,
		coder:        coder,
		systemPrompt: coderSelectionPrompt(route),
		release:      func() { e.coderStatus.Release(name) },
	}, nil
}

// coderSelectionPrompt はrouteによるCoderの役割差を追加しない。
func coderSelectionPrompt(_ routing.Route) string {
	return ""
}

// coderByName は名前からCoderAgentを取得
func (e *DefaultCodeExecutor) coderByName(name string) CoderAgent {
	switch normalizeCoderStatusName(name) {
	case "coder1":
		return e.coder1
	case "coder2":
		return e.coder2
	case "coder3":
		return e.coder3
	case "coder4":
		return e.coder4
	default:
		return nil
	}
}

// explicitCodeRouteTarget は明示code routeから指定Coderと共通promptを返す。
func explicitCodeRouteTarget(route routing.Route) (name, prompt string, ok bool) {
	switch route {
	case routing.RouteCODE1:
		return "coder1", coderSelectionPrompt(route), true
	case routing.RouteCODE2:
		return "coder2", coderSelectionPrompt(route), true
	case routing.RouteCODE3:
		return "coder3", coderSelectionPrompt(route), true
	case routing.RouteCODE4:
		return "coder4", coderSelectionPrompt(route), true
	default:
		return "", "", false
	}
}
