れんさん、サーバ側へ渡すなら、以下の仕様でよいと思います。
目的は **MLXのRenCrow_LLMサーバで、Thinkと本文をOllama相当に分離して返すこと** です。

````markdown
# RenCrow_LLM Server ThinkingBridge 仕様書

## 1. 目的

RenCrow_LLMサーバに、LLM出力中の Think / Reasoning 部分と、ユーザーに表示する本文部分を明確に分離する仕組みを追加する。

Ollama の `message.thinking` / `message.content` に近い構造を、MLX-LM ベースの RenCrow_LLMサーバでも実現する。

本機能は Chat UI 側ではなく、LLMサーバ側に実装する。

---

## 2. 背景

現在、Ollama は thinking 対応モデルに対して、推論部分と最終回答を分離して返せる。

一方、MLX-LM ではモデルが `<think>...</think>` などを生成しても、それを構造化して `reasoning` と `content` に分ける層が不足している。

そのため、RenCrow_LLMサーバ側に以下の機能を追加する。

- モデル別 chat template / thinking 制御
- reasoning / content の分離
- streaming 中の reasoning / content delta 分離
- think 内 tool call 誤爆防止
- OpenAI互換 / Ollama互換レスポンスへの正規化
- raw 出力の保持

---

## 3. 実装対象

対象は RenCrow_LLMサーバ側。

想定ファイル：

```text
src/llm_server/openai_server.py
src/llm_server/reasoning_parser.py
src/llm_server/thinking_bridge.py
src/llm_server/parser_specs.py
configs/parser_specs.toml または configs/parser_specs.yaml
````

既存構成に合わせて、ファイル名は変更してよい。

---

## 4. 責務分離

### 4.1 RenCrow_LLMサーバ側の責務

サーバ側で以下を担当する。

```text
- chat_template 適用
- think 有効 / 無効の制御
- raw generation
- reasoning / content 分離
- streaming delta 分離
- tool call firewall
- response normalization
- parse_status 付与
```

### 4.2 Chat側の責務

Chat側では以下のみ担当する。

```text
- reasoning を表示するかどうか
- 折りたたみUI
- デバッグ表示
- 会話ログ保存ポリシー
- reasoning を次ターン履歴へ含めるかどうか
```

Chat側で `<think>` タグの解析を行ってはいけない。
Think分離は必ずサーバ側で完了させる。

---

## 5. 用語定義

| 用語                | 意味                            |
| ----------------- | ----------------------------- |
| raw_text          | モデルが生成した生テキスト                 |
| reasoning_text    | `<think>...</think>` 等の内部推論部分 |
| content_text      | ユーザーに表示する本文部分                 |
| parse_status      | reasoning parser の解析結果        |
| parser_name       | 使用した parser 名                 |
| thinking          | Ollama互換の reasoning フィールド名    |
| reasoning_content | OpenAI互換寄りの reasoning フィールド名  |

---

## 6. 内部標準レスポンス形式

RenCrow_LLMサーバ内部では、バックエンド差分を吸収するため、以下の形式に正規化する。

```json
{
  "role": "assistant",
  "content": "ユーザーに見せる本文",
  "reasoning_content": "Think部分",
  "thinking": "Think部分",
  "raw_content": "<think>Think部分</think>ユーザーに見せる本文",
  "parse_status": "ok",
  "parser_name": "qwen3",
  "reasoning_visible": false
}
```

`thinking` と `reasoning_content` は同一内容でよい。
外部API互換の都合により、両方を持てるようにする。

---

## 7. parse_status

`parse_status` は以下を定義する。

```text
ok
  reasoning / content を正常に分離できた

no_reasoning
  reasoningタグやreasoning tokenが存在しなかった

unclosed_reasoning
  <think> は開始したが </think> が閉じなかった

parser_error
  parser内部で例外が発生した

disabled
  parse_reasoning=false のため解析しなかった

passthrough
  対応parserがなく、rawをcontentとして返した
```

---

## 8. APIリクエスト仕様

既存の `/v1/chat/completions` に以下の追加フィールドを許可する。

```json
{
  "model": "Worker",
  "messages": [],
  "stream": true,
  "think": true,
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true,
  "chat_template_kwargs": {
    "enable_thinking": true
  }
}
```

### 8.1 think

```text
true:
  モデルに thinking を促す

false:
  モデルに thinking を抑制する

未指定:
  モデル設定の default_think に従う
```

### 8.2 parse_reasoning

```text
true:
  サーバ側で reasoning / content を分離する

false:
  分離せず raw を content として返す
```

通常は `true` を推奨する。

### 8.3 include_reasoning

```text
true:
  レスポンスに reasoning_content / thinking を含める

false:
  内部では分離するが、外部レスポンスには含めない
```

通常ユーザー向けは `false`。
開発者・デバッグ用途では `true`。

### 8.4 separate_reasoning

```text
true:
  streaming時にも reasoning delta と content delta を分離する

false:
  streaming時は raw delta として返す
```

通常は `true` を推奨する。

---

## 9. モデル別 parser spec

モデルごとに parser spec を設定ファイルで管理する。

例：

```yaml
parsers:
  qwen3:
    mode: think_tag
    start_token: "<think>"
    end_token: "</think>"
    start_may_be_in_prompt: false
    default_think: true

  qwen3_5:
    mode: think_tag
    start_token: "<think>"
    end_token: "</think>"
    start_may_be_in_prompt: true
    default_think: true

  deepseek_r1:
    mode: think_tag
    start_token: "<think>"
    end_token: "</think>"
    start_may_be_in_prompt: false
    default_think: true

  gemma4:
    mode: control_token
    think_token: "<|think|>"
    default_think: false

  none:
    mode: passthrough
    default_think: false
```

モデル設定側で parser を指定する。

例：

```toml
[model]
alias = "Worker"
path = "/Users/yukimi/models/Qwen3.5-122B-A10B-RAM-48GB-MLX"
parser = "qwen3_5"
default_think = true
```

---

## 10. ChatTemplateAdapter

### 10.1 目的

モデルに渡す入力テンプレートに、thinking 有効 / 無効の指定を正しく反映する。

### 10.2 必須仕様

以下を受け取る。

```python
messages
model_config
think: bool | None
chat_template_kwargs: dict | None
```

以下を返す。

```python
prompt_text
template_metadata
```

### 10.3 template_metadata

```json
{
  "enable_thinking": true,
  "parser_name": "qwen3_5",
  "prompt_ended_with_think": true
}
```

`prompt_ended_with_think` は、Qwen3.5 系のように `<think>` 開始タグが prompt 側に含まれる場合に使用する。

---

## 11. ReasoningParser

### 11.1 目的

モデル出力を `reasoning_text` と `content_text` に分離する。

### 11.2 状態

最低限、以下の状態を持つ。

```text
OUTSIDE_REASONING
INSIDE_REASONING
AFTER_REASONING
```

### 11.3 初期状態

通常：

```text
OUTSIDE_REASONING
```

ただし、parser spec の `start_may_be_in_prompt=true` かつ `template_metadata.prompt_ended_with_think=true` の場合：

```text
INSIDE_REASONING
```

### 11.4 非streaming処理

非streamingでは、生成完了後の raw text を parser に渡し、以下を返す。

```python
{
    "thinking": str,
    "content": str,
    "raw": str,
    "parse_status": str,
    "parser_name": str
}
```

### 11.5 streaming処理

streamingでは、生成chunkごとに parser.feed(text) を呼ぶ。

返却イベントは以下。

```python
[
    {"type": "thinking", "text": "..."},
    {"type": "content", "text": "..."}
]
```

SSEでは以下のように分ける。

```json
{
  "choices": [
    {
      "delta": {
        "reasoning_content": "thinking delta",
        "content": ""
      }
    }
  ]
}
```

または、

```json
{
  "choices": [
    {
      "delta": {
        "reasoning_content": "",
        "content": "content delta"
      }
    }
  ]
}
```

---

## 12. タグ分割への対応

streamingでは `<think>` や `</think>` がchunk境界で分割される可能性がある。

例：

```text
chunk1: "<thi"
chunk2: "nk>考えます"
```

そのため parser は短い buffer を持ち、start_token / end_token の最大長ぶんは即時flushしない。

仕様：

```text
buffer_min_keep = max(len(start_token), len(end_token)) - 1
```

parser は token 境界ではなく文字列境界で安全に処理する。

---

## 13. ToolCallFirewall

### 13.1 目的

reasoning 内に出現した JSON / tool call 風テキストを、実際の tool call として扱わない。

### 13.2 仕様

```text
- tool call parser は content_text のみに適用する
- reasoning_text には tool call parser を適用しない
- streaming中も content delta のみ tool call parser に渡す
- raw_text から直接 tool call を抽出してはいけない
```

### 13.3 失敗例として防ぐもの

```text
<think>
ここで {"tool": "search", "query": "..."} を呼ぶべきか考える
</think>
最終回答です
```

上記の JSON は tool call として扱わない。

---

## 14. ResponseNormalizer

### 14.1 OpenAI互換レスポンス

非streaming時：

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "本文",
        "reasoning_content": "Think部分"
      },
      "finish_reason": "stop"
    }
  ]
}
```

`include_reasoning=false` の場合：

```json
{
  "message": {
    "role": "assistant",
    "content": "本文"
  }
}
```

ただし内部ログには reasoning を保持してよい。

### 14.2 Ollama互換レスポンス

必要に応じて以下も返せるようにする。

```json
{
  "message": {
    "role": "assistant",
    "thinking": "Think部分",
    "content": "本文"
  }
}
```

### 14.3 RenCrow内部形式

内部では必ず以下を保持する。

```json
{
  "assistant_text": "本文",
  "reasoning_text": "Think部分",
  "raw_text": "元出力",
  "parse_status": "ok",
  "parser_name": "qwen3",
  "include_reasoning": false
}
```

---

## 15. 会話履歴への戻し方

次ターンの `messages` に戻す assistant message は、原則として `content_text` のみとする。

```json
{
  "role": "assistant",
  "content": "本文"
}
```

reasoning_text を次ターン履歴に含めない。

理由：

```text
- reasoning の肥大化を防ぐ
- KV cache prefix matching の破壊を防ぐ
- internal reasoning の再注入による出力汚染を避ける
- Chat履歴をユーザー可視本文中心に保つ
```

ただし、完全保存ログには raw / reasoning / content を分離して保存できる。

---

## 16. ログ仕様

サーバ側では、デバッグ用途として以下をログ可能にする。

```json
{
  "event_id": "evt_...",
  "model": "Worker",
  "parser_name": "qwen3_5",
  "think": true,
  "parse_reasoning": true,
  "include_reasoning": false,
  "parse_status": "ok",
  "raw_length": 1234,
  "reasoning_length": 456,
  "content_length": 778
}
```

ログ本文の保存は設定で制御する。

```text
log_raw_text: false
log_reasoning_text: false
log_content_text: true
```

デフォルトでは raw_text / reasoning_text はファイルログに出さない。
必要時のみ開発者設定で有効化する。

---

## 17. 設定例

```toml
[thinking]
parse_reasoning = true
include_reasoning_default = false
separate_reasoning_stream = true
log_raw_text = false
log_reasoning_text = false
log_content_text = true

[thinking.parsers.qwen3]
mode = "think_tag"
start_token = "<think>"
end_token = "</think>"
start_may_be_in_prompt = false
default_think = true

[thinking.parsers.qwen3_5]
mode = "think_tag"
start_token = "<think>"
end_token = "</think>"
start_may_be_in_prompt = true
default_think = true

[thinking.parsers.deepseek_r1]
mode = "think_tag"
start_token = "<think>"
end_token = "</think>"
start_may_be_in_prompt = false
default_think = true

[thinking.parsers.none]
mode = "passthrough"
default_think = false
```

---

## 18. 実装ステップ

### Step 1: 非streaming分離

* raw text から `<think>...</think>` を分離
* `reasoning_content` と `content` を返す
* parse_status を付与

### Step 2: parser spec 導入

* qwen3 / qwen3_5 / deepseek_r1 / none を設定化
* model config から parser を選択

### Step 3: streaming分離

* chunkごとに ReasoningParser.feed()
* SSE delta を reasoning_content / content に分ける

### Step 4: ChatTemplateAdapter

* think=true/false をテンプレートに反映
* Qwen系の enable_thinking を扱う
* prompt_ended_with_think を metadata に入れる

### Step 5: ToolCallFirewall

* tool call parser を content のみに接続
* reasoning 内 JSON を無視

### Step 6: ログとデバッグ

* raw / reasoning / content の長さ
* parse_status
* parser_name
* include_reasoning
* think設定

### Step 7: Chat Viewer連携

* Chat側は reasoning_content / thinking を折りたたみ表示できるようにする
* 通常表示は content のみ

---

## 19. 受け入れ条件

### 19.1 非streaming

入力：

```text
<think>内部で考える</think>最終回答です。
```

期待結果：

```json
{
  "reasoning_content": "内部で考える",
  "content": "最終回答です。",
  "parse_status": "ok"
}
```

### 19.2 reasoningなし

入力：

```text
最終回答です。
```

期待結果：

```json
{
  "reasoning_content": "",
  "content": "最終回答です。",
  "parse_status": "no_reasoning"
}
```

### 19.3 未閉じreasoning

入力：

```text
<think>内部で考える
```

期待結果：

```json
{
  "reasoning_content": "内部で考える",
  "content": "",
  "parse_status": "unclosed_reasoning"
}
```

### 19.4 thinking内tool call誤爆防止

入力：

```text
<think>{"tool":"search","query":"test"}</think>検索は不要です。
```

期待結果：

```text
tool call は発火しない
content は「検索は不要です。」
```

### 19.5 streamingタグ分割

入力chunk：

```text
"<thi"
"nk>考える"
"</th"
"ink>答え"
```

期待結果：

```text
reasoning_content: "考える"
content: "答え"
parse_status: "ok"
```

---

## 20. 非目標

本仕様では以下を実装対象外とする。

```text
- reasoning内容の品質改善
- モデルのthinking能力そのものの改善
- reasoningの要約
- reasoningの記憶化
- reasoningを使った自己改善ループ
- UIの詳細デザイン
```

本仕様の対象は、あくまで LLM出力の構造化である。

---

## 21. 推奨方針

RenCrow_LLMサーバでは、常に以下の原則を守る。

```text
- raw_text は破棄せず内部保持可能にする
- ユーザー表示は content_text のみ
- reasoning_text は通常ログに出さない
- 次ターン履歴には content_text のみ戻す
- tool call は content_text からのみ抽出する
- parser spec はモデル別に設定化する
```

---

## 22. 最終結論

ThinkingBridge は RenCrow_LLMサーバ側に実装する。

Chat側では解析しない。

サーバ側で reasoning / content を分離し、Chat側は表示・保存・デバッグの扱いだけを担当する。

```

これをそのまま、CoderやWorkerに渡せる粒度にしてあります。  
特に大事なのは、`parse_reasoning` と `include_reasoning` を分けた点です。

`parse_reasoning=true` は常時でいいです。  
でも `include_reasoning=false` にしておけば、内部では安全に分離しつつ、通常UIにはThinkを出さない運用ができます。
```

