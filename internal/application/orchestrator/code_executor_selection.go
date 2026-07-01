package orchestrator

import (
	"fmt"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
)

// codeTarget はコーダー選択の結果
type codeTarget struct {
	name         string
	coder        CoderAgent
	systemPrompt string
	release      func() // CoderStatus解放用（オプション）
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
	log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=explicit", route, name)
	return codeTarget{name: name, coder: coder, systemPrompt: prompt}, nil
}

func (e *DefaultCodeExecutor) selectDefaultCoderForGenericRoute(route routing.Route) (codeTarget, error) {
	const coderName = "coder1"
	if e.coder1 == nil {
		log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=unavailable", route, coderName)
		return codeTarget{}, fmt.Errorf("CODE route requested but %s is unavailable; use CODE2/CODE3/CODE4 explicitly for another coder", coderName)
	}
	if e.externalCoders[coderName] {
		log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=external_requires_explicit_route", route, coderName)
		return codeTarget{}, fmt.Errorf("CODE route requested but %s uses an external provider; use CODE1/CODE2/CODE3/CODE4 explicitly to use an external coder", coderName)
	}
	if e.coderStatus != nil {
		if !e.coderStatus.Acquire(coderName) {
			log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=busy", route, coderName)
			return codeTarget{}, fmt.Errorf("CODE route requested but %s is busy", coderName)
		}
		log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=default", route, coderName)
		return codeTarget{
			name:         coderName,
			coder:        e.coder1,
			systemPrompt: "You are a code generation assistant.",
			release: func() {
				e.coderStatus.Release(coderName)
			},
		}, nil
	}
	log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=default", route, coderName)
	return codeTarget{
		name:         coderName,
		coder:        e.coder1,
		systemPrompt: "You are a code generation assistant.",
	}, nil
}

// systemPromptForRoute はルートに対応するシステムプロンプトを返す
func systemPromptForRoute(route routing.Route) string {
	switch route {
	case routing.RouteCODE1:
		return "You are a specification design assistant."
	case routing.RouteCODE2:
		return "You are an implementation assistant."
	case routing.RouteCODE3:
		return "You are a high-quality code review and reasoning assistant."
	case routing.RouteCODE4:
		return "You are a fast prototyping and experimental coding assistant."
	default:
		return "You are a code generation assistant."
	}
}

// coderByName は名前からCoderAgentを取得
func (e *DefaultCodeExecutor) coderByName(name string) CoderAgent {
	switch name {
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

// explicitCodeRouteTarget はCODE1/CODE2/CODE3/CODE4の明示的ルートを判定
func explicitCodeRouteTarget(route routing.Route) (name, prompt string, ok bool) {
	switch route {
	case routing.RouteCODE1:
		return "coder1", "You are a specification design assistant.", true
	case routing.RouteCODE2:
		return "coder2", "You are an implementation assistant.", true
	case routing.RouteCODE3:
		return "coder3", "You are a high-quality code review and reasoning assistant.", true
	case routing.RouteCODE4:
		return "coder4", "You are a fast prototyping and experimental coding assistant.", true
	default:
		return "", "", false
	}
}
