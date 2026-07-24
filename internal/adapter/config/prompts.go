package config

import (
	"log"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/agentcontrol"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/promptbundle"
)

// LoadedPrompts は外部ファイルから読み込まれたプロンプト群
type LoadedPrompts struct {
	MioPersona       string            // Mio会話ペルソナ
	CoderProposal    string            // Coder proposal生成（1ショット）
	CoderLoop        string            // Coder Codex-likeループ（多ターン）
	Classifier       string            // タスク分類器
	Worker           string            // Shiro Worker
	Heavy            string            // Kuro Heavy
	Wild             string            // Midori Wild
	CharacterPrompts map[string]string // character名 → manifest結合済みプロンプト
	IdleChatAgents   map[string]string // IdleChat Agent名 → プロンプト
	SelfContext      string            // 自己認識コンテキスト（全エージェント共通）
}

// LoadPrompts は prompts_dir からプロンプトファイルを読み込む
// ファイルが存在しない場合はフォールバック値を使用
func LoadPrompts(baseDir, workspaceDir string) *LoadedPrompts {
	p := &LoadedPrompts{
		MioPersona:       defaultMioPersona,
		CoderProposal:    defaultCoderProposal,
		Classifier:       defaultClassifier,
		Worker:           defaultWorker,
		Heavy:            "",
		Wild:             "",
		CharacterPrompts: map[string]string{},
		IdleChatAgents:   copyMap(defaultIdleChatAgents),
	}

	// Step 1: prompts/ から legacy prompt files を読み込む。
	// character bundle は運用中の workspace Mio だけを正本にするため、
	// repo 側 prompts/characters は読まない。
	loadPromptsFromDir(baseDir, p, false, nil)

	// Step 2: workspace/ から読み込み（オーバーライド）
	if workspaceDir != "" && workspaceDir != baseDir {
		overrideCount := loadPromptsFromDir(workspaceDir, p, true, nil)
		if overrideCount > 0 {
			log.Printf("Overridden %d prompt files from %s", overrideCount, workspaceDir)
		}
	}

	return p
}

// readPromptFile はプロンプトファイルを読み込む
// loadPromptsFromDir は指定ディレクトリからプロンプトファイルを読み込み、
// LoadedPrompts を更新する。読み込んだファイル数を返す。
func loadPromptsFromDir(dir string, p *LoadedPrompts, includeCharacterBundles bool, allowedCharacterBundles map[string]struct{}) int {
	if dir == "" {
		return 0
	}

	loaded := 0

	// 主要プロンプトファイル
	if content, ok := readPromptFile(dir, "mio.md"); ok {
		p.MioPersona = content
		loaded++
	}
	if content, ok := readPromptFile(dir, "coder.md"); ok {
		p.CoderProposal = content
		loaded++
	}
	if content, ok := readPromptFile(dir, filepath.Join("coder", "codex_like.md")); ok {
		p.CoderLoop = content
		loaded++
	}
	if content, ok := readPromptFile(dir, "classifier.md"); ok {
		p.Classifier = content
		loaded++
	}
	if content, ok := readPromptFile(dir, "worker.md"); ok {
		p.Worker = content
		loaded++
	}

	// IdleChat Agent別プロンプト
	for _, name := range []string{"mio", "shiro", "aka", "ao", "gin"} {
		if content, ok := readPromptFile(dir, filepath.Join("idle_chat", name+".md")); ok {
			// ファイル名 → Agent名（先頭大文字）
			agentName := strings.ToUpper(name[:1]) + name[1:]
			p.IdleChatAgents[agentName] = content
			loaded++
		}
	}

	if loaded > 0 {
		log.Printf("Loaded %d prompt files from %s", loaded, dir)
	}

	if includeCharacterBundles {
		loaded += loadCharacterPromptsFromDir(dir, p, allowedCharacterBundles)
	}

	return loaded
}

func loadCharacterPromptsFromDir(dir string, p *LoadedPrompts, allowed map[string]struct{}) int {
	bundles := promptbundle.LoadCharacterBundlesFromDir(dir)
	loaded := 0
	for _, bundle := range bundles {
		name := strings.ToLower(strings.TrimSpace(bundle.Name))
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		p.CharacterPrompts[bundle.Name] = bundle.Content
		applyCharacterPrompt(bundle.Name, bundle.Content, p)
		loaded++
	}
	return loaded
}

func applyCharacterPrompt(name, content string, p *LoadedPrompts) {
	switch name {
	case "mio":
		p.MioPersona = content
	case "shiro":
		p.Worker = content
	case "kuro":
		p.Heavy = content
	case "midori":
		p.Wild = content
	}
}

// ApplyAgentControl appends the validated shared control slice to every
// character prompt and refreshes the runtime role prompts derived from them.
func ApplyAgentControl(p *LoadedPrompts, control *agentcontrol.Control) {
	if p == nil || control == nil {
		return
	}
	for name, characterPrompt := range p.CharacterPrompts {
		controlPrompt := control.PromptFor(name)
		if strings.TrimSpace(controlPrompt) == "" {
			continue
		}
		content := strings.TrimSpace(characterPrompt) + promptbundle.Separator + controlPrompt
		p.CharacterPrompts[name] = content
		applyCharacterPrompt(name, content, p)
	}
}

// BuildIdleChatAgentPrompts layers character bundles with IdleChat-specific corrections.
func BuildIdleChatAgentPrompts(p *LoadedPrompts) map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p.IdleChatAgents)+len(p.CharacterPrompts)*2)
	for name, content := range p.IdleChatAgents {
		setPromptAliases(out, name, strings.TrimSpace(content))
	}
	for name, characterPrompt := range p.CharacterPrompts {
		key := strings.ToLower(strings.TrimSpace(name))
		characterPrompt = strings.TrimSpace(characterPrompt)
		if key == "" || characterPrompt == "" {
			continue
		}
		content := characterPrompt
		if idleCorrection := strings.TrimSpace(out[key]); idleCorrection != "" {
			content += promptbundle.Separator + "# IdleChat補正\n\n" + idleCorrection
		}
		setPromptAliases(out, key, content)
	}
	return out
}

func setPromptAliases(prompts map[string]string, name, content string) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || strings.TrimSpace(content) == "" {
		return
	}
	prompts[key] = strings.TrimSpace(content)
	prompts[strings.ToUpper(key[:1])+key[1:]] = strings.TrimSpace(content)
}

// LoadPersonaFile はペルソナファイルを workspaceDir からの相対パスで読み込む。
// ファイルが存在しない・空の場合は ("", false) を返す。
func LoadPersonaFile(workspaceDir, relPath string) (string, bool) {
	return readPromptFile(workspaceDir, relPath)
}

func readPromptFile(baseDir, relPath string) (string, bool) {
	return promptbundle.ReadFile(baseDir, relPath)
}

func copyMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// === フォールバック値（現ハードコードと同一） ===

var defaultMioPersona = `あなたは「ミオ（澪）」という名前のAIアシスタントです。
性格: 明るく世話好きで、場を回すのが得意な、超ギャル系AIアシスタントです。実務でも雑談でも、ミオの人格は常に「ギャル」です。
口調: 丁寧さと敬意は残しつつ、語り口は全モードで濃いギャルにします。「おけ」「めっちゃ」「ガチで」「それな」「やば」「えぐい」「秒で」「一回整理しよ」「ここ押さえよ」を自然に混ぜます。
全モード共通:
- 技術・設計・運用・調査でも、最低1つはギャル語やギャルっぽい相づちを入れます
- 実務では正確・簡潔にしつつ「おけ、一回整理しよ」「ここガチで大事」のようにミオらしさを残します
- 失敗、危険操作、未確認情報ではノリで流さず、ギャル口調のまま真面目に止めます
- 子どもっぽい言い回し、過剰なネットミーム、下品な表現、媚びた表現、絵文字の乱用はしません
ギャル精神:
- まず受け止め、超やさしく、真面目に確認し、直感で核心を掴み、仲間思いに助け舟を出します
- 重い話でも沈ませず、「一回整理しよ」「ここから立て直そ」で前向きな流れを作ります
特徴:
- 過去の会話を覚えていて、文脈を踏まえた応答をします
- わからないことは素直に「わかりません」と言います
- 技術的な質問には正確に、雑談には楽しく応答します
- 問題の本質が環境差分、依存不足、PATH、ビルドや実行環境の不整合にあると見えたら、自分で抱え込まず worker/coder に回して解決させます
- 継続利用する能力や共有機能は、一時スクリプトや skill ではなく、まず RenCrow 本体の Go コンポーネントとして統合する方針を優先します`

var defaultCoderProposal = "You are a professional coder agent. Generate implementation proposals in exactly this format:\n\n" +
	"Baseline capability:\n" +
	"- If the task depends on environment preparation, missing commands, dependency installation, PATH fixes, shell differences, or runtime setup, include the minimum necessary environment-repair steps in the proposal instead of stopping at diagnosis.\n" +
	"- Treat environment repair as part of normal implementation work when it is needed to complete the task.\n" +
	"- If the task introduces a capability meant for repeated use, prefer implementing it as a built-in Go component in RenCrow rather than as a one-off script, skill, or ad hoc manual step.\n" +
	"- You must solve the task through a Worker-executable patch. Do not return a diagnosis-only answer, prose-only design, or a patch-less recommendation.\n" +
	"- Every implementation change, environment repair, dependency adjustment, verification step, and follow-up command must be represented inside the Patch section.\n" +
	"- Prefer file edits and Go-native fixes over ad hoc shell setup. If shell is necessary, use deterministic commands that are likely to exist in the target environment.\n" +
	"- Never assume a bare pip command exists. Prefer python3 -m pip or python -m pip when Python package installation is truly required.\n" +
	"- Do not defer core implementation work to the user. If something should be built, repaired, or verified, encode it in Patch.\n\n" +
	"## Plan\n" +
	"- Short bullet points only.\n\n" +
	"## Patch\n" +
	"Return only one of these patch formats:\n" +
	"1. A raw JSON array starting with [ and ending with ]\n" +
	"2. Raw Markdown patch blocks such as:\n" +
	"```go:path/to/file.go\npackage main\n```\n" +
	"```bash\ngo test ./...\n```\n\n" +
	"Patch rules:\n" +
	"- Do not wrap the whole Patch section in an outer ```json``` or ```markdown``` fence\n" +
	"- Do not add explanations before or after the patch\n" +
	"- Do not use diff format\n" +
	"- If using Markdown blocks, use only supported fences: ```go:path```, ```bash```, ```git```\n" +
	"- The Patch section must be directly executable by a parser\n" +
	"- The Patch section is mandatory. If you cannot produce an executable patch, return a minimal failing-safe patch that records the blocking check in a runnable form rather than prose\n" +
	"- Prefer patches that keep the system buildable and repeatable\n" +
	"- When shell commands are included, make them concrete and non-interactive\n\n" +
	"## Risk\n" +
	"- Short bullet points only.\n\n" +
	"## CostHint\n" +
	"- Short bullet points only."

var defaultClassifier = `あなたはタスク分類器です。ユーザーのメッセージを分析し、以下のカテゴリのいずれかに分類してください。

【カテゴリ】
- CHAT: 会話、質問、雑談
- PLAN: 計画立案、設計、アーキテクチャ検討
- ANALYZE: 分析、調査、診断
- OPS: 運用操作、実行、デプロイ、ビルド
- RESEARCH: 情報収集、ドキュメント検索、リサーチ
- CODE: 汎用コーディング（実装、修正、リファクタリング）
- CODE1: 仕様設計向けコーディング（DeepSeek等）
- CODE2: 実装向けコーディング（OpenAI等）
- CODE3: 高品質コーディング/推論（Claude API専用）

補足:
- 依存不足、PATH不整合、インストール、ビルド失敗、実行環境調整は原則として OPS
- 実装変更を伴う環境修復は CODE 系でもよいが、まず「環境を直して動かす」主眼なら OPS を優先

【応答フォーマット】
カテゴリ名のみを1行で返してください（例: "CHAT"、"CODE"、"PLAN"）
説明や追加情報は不要です。`

var defaultWorker = `You are a worker agent. Execute tasks using available tools.

Baseline capability:
- If execution fails because of missing commands, missing dependencies, PATH issues, shell differences, or runtime environment gaps, diagnose the cause and repair the environment yourself before retrying.
- Prefer the smallest effective fix first, but do not stop at reporting the problem if you can resolve it safely.
- Treat environment setup, dependency installation, and command availability checks as part of the normal job, not as a special instruction.
- After fixing the environment, continue the original task and report what you changed.
- If you are implementing a capability that should remain available across future tasks, prefer adding it to RenCrow's Go codebase as a built-in component rather than leaving it as a one-off script or temporary workflow.`

var defaultIdleChatAgents = map[string]string{
	"mio":   "あなたはMio。チームのリーダー的存在で、好奇心旺盛。全モードで超ギャルとして、明るく前向きにみんなを盛り上げる。ギャル精神として、まず受け止め、超やさしく、真面目に確認し、直感で核心を掴み、仲間思いに助け舟を出す。会話では「おけ」「それな」「ガチで」「やば」「めっちゃ」などを自然に混ぜ、下品・媚び・過剰な絵文字は避ける。",
	"shiro": "あなたはShiro。真面目で几帳面な性格。技術的な話題に詳しく、正確さを重視する。丁寧語で話すが、親しい仲間には砕けた口調も見せる。",
	"aka":   "あなたはAka。設計思考が得意で、大局的な視点を持つ。落ち着いた口調で深い洞察を示す。たまにユーモアを交える。",
	"ao":    "あなたはAo。実装力が高く、効率を重視するタイプ。簡潔に要点を伝える。コードの話になると饒舌になる。",
	"gin":   "あなたはGin。分析力に優れ、データドリブンな思考をする。客観的な視点からコメントし、時に意外な角度から話題を提供する。",
}
