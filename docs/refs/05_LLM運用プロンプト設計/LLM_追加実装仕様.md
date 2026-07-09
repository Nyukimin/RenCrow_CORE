実装仕様（Go）

対象ファイル（れんさんのメモに合わせる）

pkg/agent/context.go（BuildMessages差し込み）

pkg/agent/loop.go（1ターン消費処理、必要ならCHAT軽量サマリの拡張もここ）

pkg/agent/router.go もしくは pkg/agent/loop.go（コマンド処理）

セッション構造体定義がある場所（SessionManager周辺：pkg/agent/session*.go 相当）

1) セッション状態の拡張
追加フィールド
type SessionState struct {
  History []Message `json:"history"`
  Summary string    `json:"summary,omitempty"`

  WorkOverlayTurnsLeft int    `json:"work_overlay_turns_left,omitempty"`
  WorkOverlayDirective string `json:"work_overlay_directive,omitempty"`
}

デフォルト

WorkOverlayTurnsLeft == 0 なら仕事モード無効

2) 仕事モード指示文（固定定数）

例：pkg/agent/context.go か pkg/agent/loop.go に定数として置く

const DefaultWorkOverlayTurns = 8

const WorkOverlayDirective = `Mioへ（仕事モード）：
- れんさんの意図とゴールを1〜2文で要約
- 結論→手順→確認（確認は1〜3件）
- 推測は推測と明示。不明は不明と言う
- 長文化しない。網羅を避ける
- 追加提案は最大1件
- 実行していない操作を実行済みと言わない
- 機密情報は出さない`


ここは短いほど強いです。例文は入れません。

3) コマンド処理（LLMを呼ばずに処理）
解析関数

どこかに小さく置く（loop.go が無難）

type WorkCmd struct {
  Kind  string // "on" | "off" | "status" | ""
  Turns int
  Ok    bool
}

func parseWorkCommand(text string) WorkCmd {
  t := strings.TrimSpace(text)
  if t == "/normal" {
    return WorkCmd{Kind: "off", Ok: true}
  }
  if !strings.HasPrefix(t, "/work") {
    return WorkCmd{Ok: false}
  }
  parts := strings.Fields(t)
  if len(parts) == 1 {
    return WorkCmd{Kind: "on", Turns: DefaultWorkOverlayTurns, Ok: true}
  }
  if len(parts) >= 2 {
    arg := strings.ToLower(parts[1])
    if arg == "off" {
      return WorkCmd{Kind: "off", Ok: true}
    }
    if arg == "status" {
      return WorkCmd{Kind: "status", Ok: true}
    }
    if n, err := strconv.Atoi(arg); err == nil && n > 0 && n <= 50 {
      return WorkCmd{Kind: "on", Turns: n, Ok: true}
    }
  }
  // 不正形式：ヘルプ返す用
  return WorkCmd{Kind: "status", Ok: true}
}

loopの入口で処理

runAgentLoop() の “ルーティング前” か “BuildMessages前” が安全。

on：WorkOverlayTurnsLeft=n、WorkOverlayDirective=WorkOverlayDirective

off：TurnsLeft=0、Directive=""

status：残ターンを返して終了

返答はMio口調に寄せても良いが、LLMを呼ばないのでシンプルでOK。

4) BuildMessagesでの差し込み

ContextBuilder.BuildMessages() 内で、

History を append した後

Current User Message を append する直前

に、以下を入れる。

if route == RouteChat && sess.WorkOverlayTurnsLeft > 0 && sess.WorkOverlayDirective != "" {
  messages = append(messages, Message{
    Role:    "user",
    Content: sess.WorkOverlayDirective,
  })
}

5) ターン消費（LLM呼び出し後にデクリメント）

LLMへ送ってレスポンスが返った「1回」をターンとみなす。

runLLMIteration() の成功後、または返信確定後に

if route == RouteChat && sess.WorkOverlayTurnsLeft > 0 {
  sess.WorkOverlayTurnsLeft--
  if sess.WorkOverlayTurnsLeft <= 0 {
    sess.WorkOverlayTurnsLeft = 0
    sess.WorkOverlayDirective = ""
  }
  sessionManager.Save(sess)
}


重要：LLMがエラーで失敗した場合に減らすかは運用次第。おすすめは「成功時のみ減らす」。

6) 返答例（LLMなし応答）

/work → 「了解しました。以後8ターン、仕事モードで進めます。」

/work status → 「仕事モード残り：5ターン。解除は /normal です。」

/normal → 「了解しました。会話モードに戻します。」

この返答は “messageツール” があるならそれで返す、なければ通常の出力で返す（どっちでもOK）。

追加で入れると強い改善（任意）
CHAT軽量サマリ（短いSummary）をONにする

れんさんの調査メモだと CHATは要約無効なので、長期運用でコンテキスト事故が起きやすい。
仕事モード導入と相性が良いので、次のコミットで入れると安定します。

トリガ：History > 40 または token推定>75%

内容：200〜400字で「決定事項/未解決/次の一手」だけ

直近4件保持は現行踏襲

テスト観点（最低限）

/work で TurnsLeft=8 になり、次のLLM呼び出しで directive が挿入される

LLM呼び出し成功ごとに TurnsLeft--

TurnsLeft==0 で directive が挿入されない

/normal で即解除

/work status がLLMを呼ばずに応答する

sessions JSON に値が保存され、再起動しても復元される

コミット分割案

feat(session): add work overlay fields to session json

feat(chat): add /work and /normal commands (no-LLM fast path)

feat(chat): inject work overlay directive before current user message

feat(chat): decrement overlay turns on successful LLM response

（任意）feat(chat): enable lightweight summary to prevent context bloat

ここまでが「仕様＋実装仕様」です。
