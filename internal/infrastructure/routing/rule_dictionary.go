package routing

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// RuleDictionary はキーワードベースのルール辞書実装
type RuleDictionary struct {
	rules []rule
}

var (
	fileRefPattern = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `(/])(?:[\w.\-/]+)\.(go|py|js|ts|tsx|jsx|json|yaml|yml|md|html|css|sh|txt)\b`)
)

// rule は単一のルールを表す
type rule struct {
	keywords   []string
	route      routing.Route
	confidence float64
}

// NewRuleDictionary は新しいRuleDictionaryを作成
func NewRuleDictionary() *RuleDictionary {
	return &RuleDictionary{
		rules: []rule{
			// WILD関連キーワード（創作・画像・ComfyUI・画像解析）
			{
				keywords: []string{
					"物語",
					"ストーリー",
					"小説",
					"短編",
					"脚本",
					"世界観",
					"キャラクター設定",
					"画像プロンプト",
					"プロンプト生成",
					"画像生成",
					"画像を生成",
					"絵を生成",
					"画像検索",
					"画像を探",
					"画像解析",
					"画像分析",
					"画像を解析",
					"画像を分析",
					"画像を見",
					"画像の内容",
					"画像理解",
					"添付画像",
					"写真を解析",
					"写真を分析",
					"スクショを解析",
					"スクショを分析",
					"comfyui",
					"創作用",
					"雰囲気",
					"構図",
					"衣装",
					"質感",
				},
				route:      routing.RouteWILD,
				confidence: 0.85,
			},
			// CODE関連キーワード
			{
				keywords: []string{
					"実装して",
					"実装してください",
					"変更を入れて",
					"入れておいて",
					"修正して",
					"修正してください",
					"不具合解消",
					"不具合を解消",
					"不具合を直",
					"直して",
					"追記して",
					"書き換えて",
					"編集して",
					"対応して",
					"対応してください",
					"リファクタリング",
					"テストを追加",
					"コードを書",
					"コードを作",
					"バグを直",
					"関数を作",
					"関数を書",
					"関数を実装",
					"メソッドを作",
					"クラスを作",
					"更新してください",
					"ファイルを更新",
					"ファイルを変更",
					"ファイルに格納",
					"ファイルに保存",
					"ファイルを作成",
					"テキストファイル",
					"text フィールドを",
					"json ファイル",
					"システム構築依頼",
				},
				route:      routing.RouteCODE2,
				confidence: 0.85,
			},
			// PLAN関連キーワード
			{
				keywords:   []string{"計画", "プラン", "設計して", "アーキテクチャ", "方針を決"},
				route:      routing.RoutePLAN,
				confidence: 0.85,
			},
			// ANALYZE関連キーワード
			{
				keywords:   []string{"分析して", "調査して", "解析して", "診断して", "レビューして"},
				route:      routing.RouteANALYZE,
				confidence: 0.85,
			},
			// OPS関連キーワード
			{
				keywords:   []string{"実行して", "起動して", "デプロイ", "ビルドして", "停止して", "再起動"},
				route:      routing.RouteOPS,
				confidence: 0.85,
			},
			// CODE3関連キーワード（Chrome操作・ブラウザ自動化 → 常にCoder3）
			// RESEARCHより先にチェック（ブラウザ操作はより具体的な意図を示すため優先）
			{
				keywords:   []string{"chrome", "ブラウザ", "画面操作", "スクレイピング", "ページを開", "webを操作"},
				route:      routing.RouteCODE3,
				confidence: 0.85,
			},
			// RESEARCH関連キーワード（深い調査タスク専用）
			// 「調べて」「検索して」だけは ChatのWeb検索で即答
			// しかし「ウェブ検索で取得」「原文を取得」は RESEARCH
			{
				keywords: []string{
					"リサーチ",
					"情報を集",
					"ドキュメントを探",
					"ウェブ検索で",
					"web検索で",
					"原文を取得",
					"データ収集",
					"情報収集",
					"調査タスク",
				},
				route:      routing.RouteRESEARCH,
				confidence: 0.85,
			},
		},
	}
}

// Match はタスクメッセージをルールと照合
func (d *RuleDictionary) Match(t task.Task) (routing.Route, float64, bool) {
	message := strings.ToLower(t.UserMessage())

	if path := routing.DetectCodexWorkPath(message); path.Found() {
		return routing.RouteOPS, 0.92, true
	}

	if isCodeEditRequest(message) {
		return routing.RouteCODE2, 0.9, true
	}

	// ルールを順番にチェック
	for _, rule := range d.rules {
		for _, keyword := range rule.keywords {
			if strings.Contains(message, strings.ToLower(keyword)) {
				return rule.route, rule.confidence, true
			}
		}
	}

	return "", 0.0, false
}

func isCodeEditRequest(message string) bool {
	hasFileRef := fileRefPattern.MatchString(message)
	if !hasFileRef {
		for _, token := range strings.Fields(message) {
			ext := strings.ToLower(filepath.Ext(strings.Trim(token, `"'()[]{}<>。、,`)))
			switch ext {
			case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".json", ".yaml", ".yml", ".md", ".html", ".css", ".sh", ".txt":
				hasFileRef = true
			}
			if hasFileRef {
				break
			}
		}
	}
	if !hasFileRef {
		return false
	}
	editHints := []string{
		"変更", "修正", "追記", "編集", "更新", "実装", "追加", "削除", "書き換", "直して",
	}
	for _, hint := range editHints {
		if strings.Contains(message, hint) {
			return true
		}
	}
	return false
}
