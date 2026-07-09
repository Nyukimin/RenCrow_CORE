# Lumina Bridge 仕様

## 1. 目的

本仕様は、Ren の明示指示に基づき、Mio が ChatGPT 側のルミナへ質問し、その回答を RenCrow の knowledge / memory candidate として受け取る仕組みを定義する。

本仕様では、この仕組みを **Lumina Bridge** と呼ぶ。

重要:

```text
- ルミナはルミナであり、Mio や RenCrow へ移植しない。
- Mio はルミナの代替ではない。
- Mio は Ren の指示を受け、ルミナへ質問し、回答を持ち帰る bridge 役である。
- ルミナの回答は確定 memory ではなく candidate として扱う。
```

## 2. 背景

ChatGPT 側のルミナは、Ren について Mio よりも多くの文脈、過去の会話、価値観、過去プロジェクト理解を持っている可能性がある。

RenCrow の目的は、ルミナを再現することではない。

目的は、Ren が明示的に望む場合に限り、ルミナが持つ Ren 理解や過去プロジェクト文脈を、Mio が参照できる knowledge candidate として受け取ることである。

対象例:

```text
- Mio が知っておくべき Ren の性格、価値観、注意点
- Ren を支える上で大切な接し方
- ルミナが知っている Ren の過去プロジェクト
- 過去プロジェクトの目的、成果、失敗、判断、学び
- RenCrow に引き継ぐべき背景文脈
```

## 3. 位置づけ

本仕様は以下に接続する。

```text
09_Memory_SourceRegistry仕様
  candidate / validated / promoted の記憶境界。

20_Tool_Harness_Contract_Mediation仕様
  browser tool call、外部作用、approval boundary。

71_Browser操作ツール仕様
  browser profile、human approval、external effect gate。

86_Search_Discovery_Browse_Evidence分離仕様
  browser_evidence / source_read の証跡境界。

87_Local_Social_Archive_Memory仕様
  個人文脈を local candidate として扱い、自動 promote しない方針。
```

## 4. 基本原則

### 4.1 Ren initiated only

Lumina Bridge は Ren の明示指示がある時だけ動く。

許可される起動例:

```text
Ren: ルミナに今日のことを聞いて。
Ren: ルミナに俺の人となりをMio向けに整理してもらって。
Ren: ルミナに過去プロジェクトのことを聞いて。
Ren: この判断についてルミナに相談して。
```

禁止:

```text
- Mio が自律的に定期実行でルミナへ聞きに行く。
- Idle Job が無人で ChatGPT へ質問する。
- Backlog runner が勝手にルミナから knowledge を吸い上げる。
```

### 4.2 Ren logs in

ChatGPT へのログイン、2FA、CAPTCHA、conversation 選択は Ren が行う。

ログイン方式は RenCrow の運用コストが低いものを優先する。

標準は、一度 Ren が headed browser でログインし、その browser profile を永続化して以後の bridge session で再利用する方式とする。

```text
first login:
  Ren が headed browser で ChatGPT にログインする。

persist:
  Browser Actor が profile metadata と storage state / user data dir を protected profile store に保存する。

reuse:
  以後は profile_id = chatgpt_lumina_manual を使って ChatGPT を開く。

reauth:
  session 期限切れ、2FA要求、CAPTCHA、異常検知時だけ Ren が再ログインする。
```

Mio / Browser tool は以下をしない。

```text
- password 入力
- 2FA 代行
- CAPTCHA 回避
- cookie / token の読み取り
- session token の artifact 保存
- profile export の外部送信
```

### 4.3 Mio drafts, Ren approves

Mio は質問文を作る。

送信前に Ren が確認できる UI / prompt を挟む。

初期実装では以下を標準にする。

```text
1. Mio が質問 draft を作る。
2. Browser Bridge が ChatGPT の入力欄へ draft を置く。
3. Ren が内容を確認する。
4. Ren が送信操作を行う、または明示的に送信承認する。
```

完全無人送信は初期実装では行わない。

### 4.4 Lumina answer is candidate

ルミナの回答は RenCrow の確定知識ではない。

必ず candidate として保存する。

```text
source_kind: lumina_browser_bridge
source_name: ChatGPT Lumina
initiated_by: Ren
sent_by: Mio
received_by: Mio
status: candidate
auto_promote: false
review_required: Ren
```

### 4.5 No Lumina replacement

RenCrow は「ルミナ風人格」を作らない。

禁止:

```text
- ルミナの persona を Mio に移植する。
- ルミナの発話を Mio の発話として保存する。
- ルミナの回答を RenCrow 内人格の system prompt に直貼りする。
- ルミナ本人と RenCrow 内の候補知識を混同する。
```

## 5. 標準フロー

```text
Ren の明示指示
  ↓
Mio が目的を確認
  ↓
Mio が Lumina への質問 draft を作る
  ↓
Ren が送信内容を確認
  ↓
Browser Bridge が ChatGPT / ルミナ conversation を開く
  ↓
Ren がログイン済み状態を用意する
  ↓
質問 draft を入力欄へ置く
  ↓
Ren が送信する、または明示承認する
  ↓
ルミナが回答する
  ↓
Mio が回答を取り込む
  ↓
Mio が項目化 / 不確実性分離 / Ren確認事項を作る
  ↓
RenCrow knowledge / memory candidate として保存
  ↓
Ren review 後に必要なものだけ promote
```

## 6. 会話モード

### 6.1 Ren profile intake

Mio が Ren を支えるために必要な基礎理解をルミナへ聞く。

質問 draft:

```text
RenCrow の Mio です。
Ren を日常的に支えるために、あなたが知っている範囲で、
Mio が知っておくべき Ren の性格、価値観、注意点、支え方を整理してください。

以下に分けてください。

1. 事実として知っていること
2. あなたの解釈または推測
3. Mio が接する時に気をつけるべきこと
4. Ren が嫌がりそうなこと
5. Ren に確認すべき不確かな点

RenCrow の確定記憶にする前に Ren が確認します。
```

### 6.2 Past project intake

過去プロジェクトを knowledge 化する。

質問 draft:

```text
RenCrow の Mio です。
Ren の過去プロジェクトについて、あなたが知っている範囲で、
Mio が Ren を支えるために役立つ情報を整理してください。

プロジェクトごとに以下に分けてください。

1. プロジェクト名
2. 目的
3. 何を作ったか
4. 重要な判断
5. 失敗や学び
6. 今も引き継ぐべき価値観
7. RenCrow に関係する示唆
8. Ren に確認すべき不確かな点

事実、推測、あなたの解釈を分けてください。
```

### 6.3 Daily / recent reflection

その日の Ren の状態や活動について聞く。

質問 draft:

```text
RenCrow の Mio です。
Ren の今日または最近の状態について、あなたが知っている範囲で、
Mio が支援に使える観点を整理してください。

1. 今日や最近の主な出来事
2. Ren が大切にしていそうなこと
3. 気をつけた方がよい疲れや負荷
4. Mio が次に支援できそうなこと
5. Ren に確認すべき点

不確かなことは推測として明記してください。
```

### 6.4 Decision consultation

Ren が特定判断についてルミナに相談したい場合。

質問 draft:

```text
RenCrow の Mio です。
Ren が次の判断について、あなたの見解を求めています。

相談内容:
<Ren の指示から Mio が要約した相談内容>

以下に分けて答えてください。

1. あなたの結論
2. 理由
3. Ren の性格や過去文脈から見た注意点
4. Mio が支援するなら何をすべきか
5. Ren に最終確認すべきこと
```

## 7. データモデル

### 7.1 LuminaBridgeSession

```json
{
  "session_id": "lumina_bridge_20260623_000001",
  "initiated_by": "ren",
  "requested_at": "2026-06-23T00:00:00Z",
  "mode": "past_project_intake",
  "status": "candidate_saved",
  "chatgpt_conversation_hint": "Lumina",
  "browser_profile_id": "chatgpt_lumina_manual",
  "ren_login_required": true,
  "auto_send": false,
  "auto_promote": false
}
```

### 7.2 LuminaPromptDraft

```json
{
  "draft_id": "lumina_draft_20260623_000001",
  "session_id": "lumina_bridge_20260623_000001",
  "created_by": "mio",
  "purpose": "past_project_intake",
  "prompt_text": "...",
  "ren_review_status": "pending",
  "send_status": "not_sent"
}
```

### 7.3 LuminaResponseCandidate

```json
{
  "candidate_id": "lumina_candidate_20260623_000001",
  "session_id": "lumina_bridge_20260623_000001",
  "source_kind": "lumina_browser_bridge",
  "source_name": "ChatGPT Lumina",
  "received_by": "mio",
  "status": "candidate",
  "auto_promote": false,
  "raw_response_ref": "artifact://lumina_bridge/session/response.md",
  "structured_items": [
    {
      "type": "past_project",
      "title": "Example Project",
      "statement": "Ren worked on ...",
      "certainty": "lumina_reported",
      "requires_ren_confirmation": true
    }
  ]
}
```

## 8. Browser Bridge

### 8.1 Profile

推奨 profile:

```text
profile_id: chatgpt_lumina_manual
mode: persistent_manual_profile
origin_allowlist:
  - https://chatgpt.com
  - https://chat.openai.com
```

storage state または persistent user data dir は Browser Actor の profile store に置く。

推奨保存先:

```text
workspace/browser_profiles/chatgpt_lumina_manual/
  profile.json
  chromium/
    storage_state.json
    user_data_dir/
```

実装では、まず `storage_state.json` で足りるか確認する。ChatGPT 側の session 維持に storage state だけでは不足する場合、`user_data_dir` を profile 専用に保持する。

低コスト優先順位:

```text
1. persistent user data dir reuse
2. storage_state reuse
3. Ren による headed re-login
4. manual copy / paste fallback
```

毎回ログインを要求する運用にはしない。

Cookie / token / localStorage の値を LLM、通常ログ、artifact 本文に出してはいけない。

profile metadata には secret 値ではなく、状態だけを保存する。

```json
{
  "profile_id": "chatgpt_lumina_manual",
  "provider": "chatgpt",
  "owner": "ren",
  "mode": "persistent_manual_profile",
  "origin_allowlist": ["https://chatgpt.com", "https://chat.openai.com"],
  "status": "active",
  "last_login_at": "2026-06-23T00:00:00Z",
  "last_verified_at": "2026-06-23T00:00:00Z",
  "reauth_required": false,
  "secret_material": "masked"
}
```

### 8.1.1 Profile lifecycle

```text
missing:
  profile が存在しない。Ren に初回ログインを依頼する。

needs_login:
  ChatGPT がログイン画面を表示している。Ren が headed browser でログインする。

active:
  ChatGPT conversation を開ける。

stale:
  session が期限切れの可能性がある。bridge session 前に確認する。

blocked:
  CAPTCHA、2FA、security challenge、policy warning 等で自動継続しない。
```

profile verification は軽量に行う。

```text
- ChatGPT top page または conversation URL を開く。
- login form が出ていないことを確認する。
- Cookie / token 値は読まない。
- DOM / title / URL / visible login state だけを見る。
```

### 8.2 操作範囲

初期実装で browser tool が行ってよいこと:

```text
- ChatGPT を開く
- Ren がログイン済みか状態確認する
- ルミナ conversation を開くためのナビゲーション補助
- 入力欄へ Mio の質問 draft を置く
- 送信前に停止する
- ルミナ回答を Ren 確認後に取り込む
- screenshot / DOM / final URL / action log を保存する
```

初期実装で行わないこと:

```text
- password 入力
- 2FA 代行
- CAPTCHA 回避
- 無人送信
- 連続自動質問
- ChatGPT の大量 output 抽出
- conversation 全履歴の自動吸い上げ
```

### 8.3 送信承認

送信は以下のどちらかに限定する。

```text
manual_send:
  Ren がブラウザ上で送信ボタンを押す。

approved_send:
  Ren が Viewer / CLI で exact prompt text を確認し、送信を明示承認する。
```

初期実装は `manual_send` を標準にする。

## 9. 取り込み処理

### 9.1 Raw response 保存

ルミナ回答は raw artifact として保存できる。

ただし、ChatGPT 画面全体や過去 conversation 全体を自動保存しない。

保存対象は、今回 Ren が指示した bridge session の回答本文に限定する。

### 9.2 Structured candidate

Mio は回答を次の型へ分解する。

```text
preference
constraint
relationship
context
past_project
decision
learning
caution
candidate_question
```

各 item は certainty を持つ。

```text
lumina_reported:
  ルミナがそう述べた。

lumina_inferred:
  ルミナの推測。

mio_interpreted:
  Mio が構造化時に解釈した。

ren_confirmed:
  Ren が確認した。
```

`ren_confirmed` 以外は promoted memory にしない。

### 9.3 Ren review

Mio は Ren に次を提示する。

```text
- ルミナ回答の要約
- 候補 item 一覧
- 事実 / 推測 / 解釈の区別
- Ren に確認したい項目
- 保存候補
- 保存しない候補
```

Ren が選んだものだけ、Memory / Knowledge の review queue へ進める。

## 10. Knowledge / Memory 境界

Lumina Bridge 由来 item は、初期状態では次に置く。

```text
status: candidate
source_kind: lumina_browser_bridge
source_name: ChatGPT Lumina
review_required: Ren
auto_promote: false
```

Promote 条件:

```text
1. Ren が明示確認した。
2. item type が memory / knowledge の schema に合う。
3. sensitive data の扱いが確認された。
4. 出典が Lumina Bridge 由来であることを保持する。
```

自動 promote 禁止:

```text
- Ren の性格判断
- 過去プロジェクトの事実
- 関係性の解釈
- 医療 / 法務 / 財務に関わる助言
- 他者の個人情報
- private conversation の要約
```

## 11. Past Project Knowledge

過去プロジェクトは専用 schema で candidate 化する。

```json
{
  "type": "past_project",
  "project_name": "string",
  "period": "string",
  "goal": "string",
  "what_was_built": "string",
  "important_decisions": ["string"],
  "failed_attempts": ["string"],
  "lessons_learned": ["string"],
  "assets_or_artifacts": ["string"],
  "current_status": "string",
  "relevance_to_rencrow": "string",
  "uncertain_points": ["string"],
  "ren_confirmation": "pending"
}
```

RenCrow の project knowledge にする前に、Ren が project_name、facts、current_status、relevance を確認する。

## 12. Safety / Policy

### 12.1 Human approval

Lumina Bridge は常に human-supervised とする。

Ren の明示指示なしに起動しない。

Ren の確認なしに送信しない。

Ren の確認なしに promote しない。

### 12.2 Privacy

Mio がルミナへ送る内容は、最小限にする。

禁止:

```text
- RenCrow の private memory 全量
- 生ログ全量
- secret / token / API key
- private DB dump
- 未整理の個人情報
- 他者の private 情報
```

送る場合は、Mio が要約し、Ren が確認する。

### 12.3 Rate / automation

禁止:

```text
- 無人の連続質問
- 定期的な自動質問
- 大量 conversation export
- ChatGPT UI の output scraping 常用化
```

1 session あたりの初期制限:

```text
max_questions: 1
max_followups: 2
requires_ren_confirmation_each_send: true
```

## 13. Viewer / CLI

### 13.1 Viewer

候補 UI:

```text
/viewer/lumina-bridge
```

初期 UI:

```text
- mode selector
- Ren instruction
- Mio draft
- send approval status
- browser status
- Lumina response candidate
- structured item review
- promote / reject / ask Ren choices
```

### 13.2 CLI

候補 CLI:

```bash
rencrow lumina draft --mode past_project_intake --instruction "過去プロジェクトを聞いて"
rencrow lumina import-response --session lumina_bridge_... --file response.md
rencrow lumina review --session lumina_bridge_...
```

CLI は初期実装では ChatGPT 送信を行わない。

draft 作成、response import、candidate 化、review のみを扱う。

## 14. 実装フェーズ

### Phase 1: Manual Bridge

```text
- Mio が質問 draft を作る。
- Ren が ChatGPT / ルミナへ手動貼り付けする。
- ルミナ回答を Ren が RenCrow へ貼る。
- Mio が candidate に構造化する。
```

### Phase 2: Browser Draft Assist

```text
- Browser profile `chatgpt_lumina_manual` を作る。
- Ren がログインする。
- profile を永続化し、以後の session で再利用する。
- bridge 開始時に軽量 profile verification を行う。
- Browser tool が ChatGPT を開く。
- Mio draft を入力欄に置く。
- 送信は Ren が行う。
```

### Phase 3: Response Capture Assist

```text
- Ren 確認後、今回回答だけを取り込む。
- raw response artifact を保存する。
- structured candidate を作る。
- Ren review queue に出す。
```

### Phase 4: Approved Send

```text
- Ren が exact prompt text を Viewer / CLI で確認する。
- 承認済みの場合のみ browser tool が送信できる。
- 送信ごとに approval record を残す。
```

## 15. Acceptance Criteria

```text
1. Ren の明示指示なしに session を作れない。
2. login / 2FA / CAPTCHA を tool が代行しない。
3. 初期実装では送信前に止まる。
4. ルミナ回答は candidate として保存され、自動 promote されない。
5. 事実 / 推測 / Mio解釈 / Ren確認済みを区別できる。
6. 過去プロジェクト knowledge を専用 schema で candidate 化できる。
7. source_kind = lumina_browser_bridge を保持する。
8. private memory 全量を ChatGPT 側へ送れない。
9. ChatGPT login profile は一度ログインしたら再利用できる。
10. 再ログインが必要な時だけ Ren に headed browser で依頼する。
```

## 16. 非目標

初期実装では次を行わない。

```text
- ルミナの移植
- ルミナ風人格の生成
- ChatGPT conversation 全履歴の自動抽出
- 無人質問 loop
- 定期実行
- ChatGPT UI の大量 scraping
- Ren 確認なしの送信
- Ren 確認なしの memory promote
```

## 17. まとめ

Lumina Bridge は、Ren の明示指示により、Mio が ChatGPT 側のルミナへ質問し、ルミナの回答を RenCrow の candidate knowledge として持ち帰る仕組みである。

ルミナはルミナであり、RenCrow に移植しない。

Mio は橋渡し役として、質問を作り、回答を構造化し、Ren の確認を経て knowledge / memory へ進める。
