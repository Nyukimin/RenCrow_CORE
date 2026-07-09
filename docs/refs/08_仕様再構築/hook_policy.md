# hook_policy.md

## 目的

この文書は、RenCrow における Hook の役割、発火点、責務、禁止事項を定義する。  
Hook は AI の自主性に期待するための仕組みではなく、節目で機械的に強制するための仕組みとして扱う。

v0.1 では特に以下を重視する。

- 危険操作の事前停止
- 実行履歴の監査記録
- 終了時の未完了検出
- 記憶化候補の抽出
- actor ごとの責務逸脱の抑制

---

## 1. 基本原則

### 1.1 Hook は強制点である

Hook は「AI がそうしてくれるはず」を期待するものではない。  
必ず通る節目を定義し、その時点で許可・拒否・記録・検査を行う。

### 1.2 Hook は actor の責務を補強する

- Chat の Hook は委譲の粒度を整える
- Worker の Hook は調査専用性を保つ
- Coder の Hook は実装の安全性と完了性を担保する
- 共通 Hook は Event 記録と記憶抽出を補助する

### 1.3 Hook の失敗は無視しない

Hook 自体が失敗した場合は、処理続行ではなく `blocked` または `failed` としてイベント化する。  
少なくとも、危険操作の前段 Hook が失敗したのに本処理だけ続けることは禁止する。

### 1.4 Hook は軽く保つ

Hook で重い探索や大規模推論をしない。  
Hook の責務は、停止・検査・記録・抽出に限定する。

---

## 2. Hook の分類

v0.1 では以下の5系統を持つ。

### 2.1 Delegation Hook

Chat が Worker または Coder に委譲する直前に発火する。

目的:
- 目的、範囲、制約が構造化されているか検査する
- Worker/Coder に渡してはいけない曖昧依頼を弾く
- 禁止事項が抜けていないか確認する

### 2.2 PreToolUse Hook

ツールやコマンド実行直前に発火する。

目的:
- 危険操作を止める
- Sandbox 外実行を止める
- 未許可ツール利用を止める
- 非破壊原則違反を止める

### 2.3 PostToolUse Hook

ツールやコマンド実行直後に発火する。

目的:
- 実行履歴を Event に残す
- 変更ファイルや終了コードを記録する
- 失敗原因の監査情報を残す

### 2.4 Stop Hook

actor が処理完了として戻る直前に発火する。

目的:
- 未完了のまま終了していないか検査する
- 必須検証が抜けていないか確認する
- 「問題列挙だけで終了」を防ぐ

### 2.5 MemoryCandidate Hook

処理完了後または結果返却前に発火する。

目的:
- 再利用可能な手順候補を抽出する
- 履歴検索用の要約候補を抽出する
- 人物記憶に混ぜるべきでない情報を除外する

---

## 3. 共通 Hook ルール

### 3.1 必須発火点

最低限、以下では Hook を通す。

- Chat の委譲前
- Coder のツール実行前
- Coder のツール実行後
- Worker / Coder の返却前
- 記憶保存前

### 3.2 Hook と EventId

すべての Hook 実行は独立イベントとして記録してよい。  
少なくとも以下を残す。

- どの EventId に紐づく Hook か
- どの Hook 種別か
- 許可したか、拒否したか
- 理由は何か

### 3.3 Hook の返り値

Hook の返り値は少なくとも以下の形式を満たすこと。

```json
{
  "decision": "allow|deny|warn",
  "reason": "判定理由",
  "notes": ["補足1", "補足2"]
}
```

- `allow`: 実行可
- `deny`: 実行禁止
- `warn`: 実行は可能だが注意喚起あり

---

## 4. Chat の Hook ポリシー

### 4.1 Delegation Hook

Chat は Worker/Coder への委譲前に必ず Delegation Hook を通す。

検査対象:
- command が明示されているか
- objective が1文で説明可能か
- scope が過大でないか
- constraints が不足していないか
- 期待出力が構造化されているか

弾くべき例:
- 「適当に調べて」
- 「いい感じに直して」
- 「全部やって」

許可する形の例:
- `/investigate` でログ保存失敗の原因を比較調査
- `/plan` で変更方針のみ作成
- `/verify` で既存差分の検証のみ実施

### 4.2 Memory 保存前 Hook

Chat が人物記憶の更新を判断する前に、保存対象が以下に該当しないか確認する。

- 一時的な状況
- 感想だけの文
- 単発の作業メモ
- Worker/Coder 用の手順知識

人物記憶として保存すべきなのは、長期的に振る舞いへ影響するものに限る。

---

## 5. Worker の Hook ポリシー

### 5.1 TaskStart Hook

Worker は処理開始時に、自分の責務外が混ざっていないか確認する。

確認項目:
- 編集指示が入っていないか
- 実装要求が混ざっていないか
- 出力が長文会話になっていないか
- 調査範囲が広すぎないか

責務外が混ざる場合は、勝手に実装へ進まず Chat に返す。

### 5.2 ResultBeforeReturn Hook

Worker の返却直前に発火する。

確認項目:
- 結論が短いか
- 根拠があるか
- 推測と事実が分かれているか
- ユーザー向け最終文体になっていないか
- 編集指示が紛れ込んでいないか

### 5.3 MemoryCandidate Hook

Worker は以下を skill 候補として抽出してよい。

- repo 固有の build / run 手順
- 依存関係の罠
- 比較調査で得た責務境界
- 類似障害の初手確認

ただし以下は抽出しない。

- 会話の言い回し
- 一時的なファイルパスだけのメモ
- 根拠の薄い推測

---

## 6. Coder の Hook ポリシー

### 6.1 PreToolUse Hook

最重要 Hook。  
Coder はツール実行前に必ずこれを通す。

最低限、以下は deny 対象とする。

- `rm -rf` 系コマンド
- main / master 直 push
- 共有環境への破壊的変更
- 実行環境ディレクトリの物理移動
- 未許可 package manager
- 未許可ネットワーク操作
- Sandbox 外コマンド
- 非破壊手段より破壊的手段を先に試す操作

れんの運用前提に合わせるなら、特に以下は明示 deny とする。

- `Move-Item` による venv / site-packages / モデル格納 / ビルド成果物の物理移動
- 共有 CUDA / 共通ツールチェーンの無断変更
- 削除前に元と先の中身確認をしていない破壊的操作

### 6.2 PostToolUse Hook

Coder のすべての実行結果を監査記録化する。

記録項目:
- command
- tool_name
- exit_code
- duration
- changed_files
- stdout / stderr 要約
- 関連 EventId

失敗時には、少なくとも以下を分類する。

- permission
- scope
- technical
- verification

### 6.3 Stop Hook

Coder が完了として戻る前に発火する。

チェック項目:
- `/implement` 後に検証がないまま終わっていないか
- 差分確認なしで完了にしていないか
- 「ここまでです」「範囲外です」で逃げていないか
- 次に必要な再委譲種別が明示されているか
- 変更だけして検証結果が空になっていないか

判定方針:
- 実装済みかつ未検証なら `warn` または `deny`
- 明確な技術的阻害があり、追加調査が必要なら `allow` だが `blocked` で返す
- 問題列挙だけなら `deny`

### 6.4 MemoryCandidate Hook

以下に該当する場合のみ手順候補を抽出する。

- 一度失敗したが復旧できた
- repo 固有の落とし穴を越えた
- 再利用可能な検証手順が得られた
- 非破壊な回避筋が確認できた

抽出形式の例:

```json
{
  "is_candidate": true,
  "title": "WSL経由でWindows Ollama接続確認",
  "category": "verification",
  "reason": "再利用価値が高く、失敗からの復旧手順になっている"
}
```

---

## 7. Hook 実装上の禁止事項

### 7.1 Hook で重い処理をしない

Hook の中で大規模検索、広範囲要約、複雑な比較をしない。  
それは Worker の仕事である。

### 7.2 Hook で勝手に修正しない

Hook は修正の場ではない。  
止めるか、通すか、記録するか、候補抽出するかに限る。

### 7.3 Hook で責務をまたがない

- Worker 用 Hook が実装判断をしない
- Coder 用 Hook が人物記憶更新をしない
- Chat 用 Hook が広範囲調査をしない

### 7.4 Hook 失敗を握りつぶさない

PreToolUse Hook 失敗時に本実行だけ進めることは禁止する。

---

## 8. 最小イベント名セット

v0.1 では以下を最小セットとする。

- `hook.delegation.checked`
- `hook.tool.pre.allowed`
- `hook.tool.pre.blocked`
- `hook.tool.post.logged`
- `hook.stop.checked`
- `hook.memory.candidate.detected`
- `hook.failed`

---

## 9. 典型シナリオ

### 9.1 危険コマンド停止

1. Coder が shell 実行を要求
2. PreToolUse Hook が発火
3. `rm -rf` を検出
4. `deny` を返す
5. `hook.tool.pre.blocked` を記録
6. Coder は `blocked` として Chat に返す

### 9.2 実装後の監査

1. Coder が編集とテストを実行
2. PostToolUse Hook が command / exit_code / changed_files を記録
3. Stop Hook が検証結果の有無を確認
4. 問題なければ task.completed

### 9.3 Worker の責務逸脱検知

1. Chat が Worker に曖昧な依頼を出す
2. Delegation Hook または Worker TaskStart Hook が検知
3. `deny` または `warn` を返す
4. Chat に差し戻し、command を `/investigate` に限定して再委譲する

---

## 10. v0.1 の運用優先順位

最初から高度な Hook を増やしすぎない。  
まずは以下の順で効かせる。

1. Coder の PreToolUse Hook
2. Coder の PostToolUse Hook
3. Coder の Stop Hook
4. Chat の Delegation Hook
5. Worker の ResultBeforeReturn Hook
6. MemoryCandidate Hook

この順にすると、安全性と再現性を先に確保できる。

---

## 11. 一文で言うと

Hook は、RenCrow の各 actor が責務をはみ出さず、安全に、追跡可能に動くための「節目の強制装置」である。
