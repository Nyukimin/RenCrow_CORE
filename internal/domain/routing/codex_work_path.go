package routing

import "strings"

type CodexWorkDomain string

const (
	CodexWorkDomainDrawing  CodexWorkDomain = "drawing"
	CodexWorkDomainFolktale CodexWorkDomain = "folktale"
)

type CodexWorkPath struct {
	Domain CodexWorkDomain
	Reason string
}

func (p CodexWorkPath) Found() bool {
	return p.Domain != ""
}

func DetectCodexWorkPath(message string) CodexWorkPath {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return CodexWorkPath{}
	}
	for _, keyword := range []string{
		"昔話生成",
		"昔話を生成",
		"昔話を書",
		"昔話を作",
		"昔話の生成",
		"民話生成",
		"民話を生成",
		"童話生成",
		"童話を生成",
		"folktale",
		"folk tale",
	} {
		if strings.Contains(normalized, keyword) {
			return CodexWorkPath{Domain: CodexWorkDomainFolktale, Reason: "codex domain keyword: folktale"}
		}
	}
	for _, keyword := range []string{
		"描画",
		"描いて",
		"絵を描",
		"絵にして",
		"イラストを描",
		"イラスト化",
		"drawing",
		"draw ",
	} {
		if strings.Contains(normalized, keyword) {
			return CodexWorkPath{Domain: CodexWorkDomainDrawing, Reason: "codex domain keyword: drawing"}
		}
	}
	return CodexWorkPath{}
}
