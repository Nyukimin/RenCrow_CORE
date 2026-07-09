package routing

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestNewRuleDictionary(t *testing.T) {
	dict := NewRuleDictionary()

	if dict == nil {
		t.Fatal("NewRuleDictionary should not return nil")
	}
}

func TestRuleDictionary_Match_AllConfiguredKeywords(t *testing.T) {
	dict := NewRuleDictionary()

	seen := 0
	for _, rule := range dict.rules {
		for _, keyword := range rule.keywords {
			seen++
			t.Run(string(rule.route)+"/"+keyword, func(t *testing.T) {
				testTask := task.NewTask(task.NewJobID(), "前置き "+keyword+" 後置き", "line", "U123")

				route, confidence, matched := dict.Match(testTask)
				if !matched {
					t.Fatalf("keyword %q did not match", keyword)
				}
				if route != rule.route {
					t.Fatalf("keyword %q route = %s, want %s", keyword, route, rule.route)
				}
				if confidence != rule.confidence {
					t.Fatalf("keyword %q confidence = %f, want %f", keyword, confidence, rule.confidence)
				}
			})
		}
	}
	if seen == 0 {
		t.Fatal("no routing keywords were checked")
	}
}

func TestRuleDictionary_Match_NoMatch(t *testing.T) {
	dict := NewRuleDictionary()

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "普通の会話メッセージ", "line", "U123")

	route, confidence, matched := dict.Match(testTask)

	if matched {
		t.Error("Should not match for normal conversation")
	}

	if route != "" {
		t.Errorf("Route should be empty when not matched, got '%s'", route)
	}

	if confidence != 0.0 {
		t.Errorf("Confidence should be 0.0 when not matched, got %f", confidence)
	}
}

func TestRuleDictionary_Match_CodexWorkPath(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "drawing bypasses wild",
			message: "この場面を描画して",
		},
		{
			name:    "folktale generation bypasses wild story",
			message: "桃太郎の昔話生成をして",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTask := task.NewTask(task.NewJobID(), tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)
			if !matched {
				t.Fatal("Codex work path should match")
			}
			if route != routing.RouteOPS {
				t.Fatalf("route = %s, want %s", route, routing.RouteOPS)
			}
			if confidence < 0.9 {
				t.Fatalf("confidence = %f, want >= 0.9", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_CodeKeywords(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		expectRoute routing.Route
	}{
		{
			name:        "実装してのキーワード",
			message:     "このファイルを実装して",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "修正してのキーワード",
			message:     "このバグを修正して",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "不具合解消のキーワード",
			message:     "直近のSystemログ確認して、不具合解消してください",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "対応してのキーワード",
			message:     "ログの中で可逆圧縮できるものは、積極的に圧縮対応してください",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "入れておいてのキーワード",
			message:     "CPU負荷を抑える設定を入れておいて",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "リファクタリングのキーワード",
			message:     "このコードをリファクタリングして",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "テストを追加",
			message:     "テストを追加してください",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "ファイル更新依頼",
			message:     "JSON ファイルの text フィールドを更新してください",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "システム構築依頼でもコード扱い",
			message:     "これは、システム構築依頼です。/tmp/data/story ディレクトリにある JSON ファイルの text フィールドを更新してください",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "ファイルパスと変更指示でコード扱い",
			message:     "README.md に1行だけ変更を入れて。具体的には末尾に確認用コメントを1行追記してください。",
			expectRoute: routing.RouteCODE2,
		},
		{
			name:        "Goファイル修正依頼",
			message:     "internal/adapter/viewer/viewer.html を修正して",
			expectRoute: routing.RouteCODE2,
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Error("Should match code-related keywords")
			}

			if route != tt.expectRoute {
				t.Errorf("Expected route '%s', got '%s'", tt.expectRoute, route)
			}

			if confidence <= 0.7 {
				t.Errorf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestIsCodeEditRequest(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "markdown file with edit verb",
			message: "README.md に変更を入れて",
			want:    true,
		},
		{
			name:    "json file with update verb",
			message: "/tmp/test.json を更新してください",
			want:    true,
		},
		{
			name:    "file ref without edit verb",
			message: "README.md の内容を教えて",
			want:    false,
		},
		{
			name:    "edit verb without file ref",
			message: "変更してほしいです",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCodeEditRequest(strings.ToLower(tt.message))
			if got != tt.want {
				t.Fatalf("isCodeEditRequest(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestRuleDictionary_Match_WildKeywords(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "物語生成",
			message: "星を拾った少女の短い物語を生成して",
		},
		{
			name:    "画像プロンプト生成",
			message: "森の魔女の画像プロンプトを作って",
		},
		{
			name:    "画像生成",
			message: "ComfyUIでMioの画像生成をして",
		},
		{
			name:    "画像検索",
			message: "参考にする画像検索をして",
		},
		{
			name:    "画像解析",
			message: "添付画像を解析して、写っている内容を説明して",
		},
		{
			name:    "画像分析",
			message: "この写真を分析して",
		},
		{
			name:    "スクショ解析",
			message: "このスクショを解析して",
		},
		{
			name:    "創作用の画像解析",
			message: "このスクショから雰囲気・構図・衣装・質感を抽出して",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTask := task.NewTask(task.NewJobID(), tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Fatal("Should match wild creative keywords")
			}
			if route != routing.RouteWILD {
				t.Fatalf("Expected route WILD, got %s", route)
			}
			if confidence <= 0.7 {
				t.Fatalf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_PlanKeywords(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "計画を立てて",
			message: "この機能の実装計画を立てて",
		},
		{
			name:    "プランニング",
			message: "プランニングしてください",
		},
		{
			name:    "設計して",
			message: "この機能を設計して",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Error("Should match plan-related keywords")
			}

			if route != routing.RoutePLAN {
				t.Errorf("Expected route PLAN, got '%s'", route)
			}

			if confidence <= 0.7 {
				t.Errorf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_AnalyzeKeywords(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "分析して",
			message: "このコードを分析して",
		},
		{
			name:    "調査して",
			message: "このエラーを調査して",
		},
		{
			name:    "解析して",
			message: "このログを解析して",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Error("Should match analyze-related keywords")
			}

			if route != routing.RouteANALYZE {
				t.Errorf("Expected route ANALYZE, got '%s'", route)
			}

			if confidence <= 0.7 {
				t.Errorf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_OpsKeywords(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "実行して",
			message: "このスクリプトを実行して",
		},
		{
			name:    "起動して",
			message: "サーバーを起動して",
		},
		{
			name:    "デプロイして",
			message: "本番環境にデプロイして",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Error("Should match ops-related keywords")
			}

			if route != routing.RouteOPS {
				t.Errorf("Expected route OPS, got '%s'", route)
			}

			if confidence <= 0.7 {
				t.Errorf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_ResearchKeywords(t *testing.T) {
	dict := NewRuleDictionary()

	// 「調べて」「検索して」はChatのWeb検索で即答するためルール辞書から除外
	// → マッチしない（ルータがCHATにフォールバックする）
	t.Run("調べて_はルール辞書でマッチしない", func(t *testing.T) {
		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, "この技術について調べて", "line", "U123")
		_, _, matched := dict.Match(testTask)
		if matched {
			t.Error("「調べて」はChatのWeb検索で処理するためルール辞書でマッチすべきでない")
		}
	})

	t.Run("検索して_はルール辞書でマッチしない", func(t *testing.T) {
		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, "最新の情報を検索して", "line", "U123")
		_, _, matched := dict.Match(testTask)
		if matched {
			t.Error("「検索して」はChatのWeb検索で処理するためルール辞書でマッチすべきでない")
		}
	})

	// 「リサーチして」は深い調査タスクとしてRESEARCHにルーティング
	t.Run("リサーチして_はRESEARCHにマッチする", func(t *testing.T) {
		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, "競合をリサーチして", "line", "U123")
		route, confidence, matched := dict.Match(testTask)
		if !matched {
			t.Error("「リサーチして」はRESEARCHキーワードにマッチすべき")
		}
		if route != routing.RouteRESEARCH {
			t.Errorf("Expected route RESEARCH, got '%s'", route)
		}
		if confidence <= 0.7 {
			t.Errorf("Expected high confidence (>0.7), got %f", confidence)
		}
	})
}

func TestRuleDictionary_Match_Code3Keywords(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "Chrome操作",
			message: "Chromeでページを開いてデータを取得して",
		},
		{
			name:    "ブラウザ操作",
			message: "ブラウザで検索結果を取得して",
		},
		{
			name:    "画面操作",
			message: "画面操作でフォームに入力して",
		},
		{
			name:    "スクレイピング",
			message: "このサイトをスクレイピングして",
		},
		{
			name:    "Webを操作",
			message: "Webを操作してデータ収集して",
		},
		{
			name:    "chrome小文字",
			message: "chromeを使ってログインして",
		},
	}

	dict := NewRuleDictionary()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			route, confidence, matched := dict.Match(testTask)

			if !matched {
				t.Error("Should match code3-related keywords")
			}

			if route != routing.RouteCODE3 {
				t.Errorf("Expected route CODE3, got '%s'", route)
			}

			if confidence <= 0.7 {
				t.Errorf("Expected high confidence (>0.7), got %f", confidence)
			}
		})
	}
}

func TestRuleDictionary_Match_MultipleKeywords(t *testing.T) {
	dict := NewRuleDictionary()

	jobID := task.NewJobID()
	// 複数のキーワードが含まれる場合、最初にマッチしたものを返す
	testTask := task.NewTask(jobID, "このコードを分析して実装して", "line", "U123")

	route, confidence, matched := dict.Match(testTask)

	if !matched {
		t.Error("Should match when multiple keywords present")
	}

	// どちらかにマッチすればOK（最初にマッチしたものが返される）
	if route != routing.RouteANALYZE && route != routing.RouteCODE2 {
		t.Errorf("Expected route ANALYZE or CODE2, got '%s'", route)
	}

	if confidence <= 0.7 {
		t.Errorf("Expected high confidence (>0.7), got %f", confidence)
	}
}
