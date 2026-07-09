# 2026-03-17 IdleChat Story Tuning 進捗メモ

## 現状

- story mode の主実装は [story_mode.go](/home/nyukimi/RenCrow_CORE/internal/application/idlechat/story_mode.go) にある。
- source ごとの骨格定義は [story_specs.go](/home/nyukimi/RenCrow_CORE/internal/application/idlechat/story_specs.go) にある。
- live probe 用の最小実行入口は [main.go](/home/nyukimi/RenCrow_CORE/cmd/test-story/main.go) にある。
- 回帰テストは [orchestrator_test.go](/home/nyukimi/RenCrow_CORE/internal/application/idlechat/orchestrator_test.go) に追加済み。

## 今回入れた変更

- `reviseStoryNarrative()` で、settling / prose / skeleton を落とした revision を reject するようにした。
- `retryStoryDraft()` に安全装置を追加した。
  - `deterministicStoryDraft()`
  - `safeStoryRetelling()`
- draft が source retelling にかなり近い場合は revision を飛ばしてそのまま採用するようにした。
- `repairStoryDraft()` でもメタ漏れ除去後に skeleton を満たせない場合、fallback 経路へ戻すようにした。
- `StorySource.Text` を短い梗概から長めの scene-aware synopsis に置き換えるため、`storySourceText(id)` を導入した。

## 現時点の評価

- 壊れた revision をそのまま採用する頻度は下がった。
- source tale の recognizability は以前より上がった。
- ただし quality bar は未達。
- 現状の system は「面白い改作を安定生成する」より「壊れた story を抑止する」寄り。
- non-LLM fallback は安全装置としては有効だが、完成形ではない。

## 重要な判断

- `StorySource.Text` が短すぎると、LLM は source の流れを理解できず、summary 化・outline 化・勝手な glue scene の捏造へ流れやすい。
- そのため、まず source material を厚くするのが正しい。
- ただし、`StorySource.Text` を厚くしても、fallback が本体になってはいけない。
- 本来の改善点は `generateStoryDraftByBeats()` の一次 draft 品質。

## 検証結果

以下は通っている。

```bash
env GOCACHE=/home/nyukimi/RenCrow_CORE/.gocache GOMODCACHE=/home/nyukimi/RenCrow_CORE/.serena/gomodcache go test ./internal/application/idlechat
env GOCACHE=/home/nyukimi/RenCrow_CORE/.gocache GOMODCACHE=/home/nyukimi/RenCrow_CORE/.serena/gomodcache go build -o test-story ./cmd/test-story
```

live probe では、recognizable な retelling fallback は出るが、「改作として面白い」を安定して満たせていない。

## 未解決

- `generateStoryDraftByBeats()` の LLM 一次出力が弱い。
- draft が 3 回失敗して fallback に流れるケースがまだ多い。
- revision は reject 強化で悪化採用を止めたが、改善率自体はまだ低い。
- `storySourceText(id)` は長文化したが、まだ source ごとに十分な scene density になっていない可能性がある。

## 次にやること

1. `storySourceText(id)` を source ごとに再点検する。
   - 主要イベントだけでなく、導入、転換、誤解、決断、帰結が抜けていないかを見る。
   - 童話ごとの見せ場が summary に潰れていないかを見る。

2. `generateStoryDraftByBeats()` を改善する。
   - 4 beat を単に説明させるのではなく、scene を書かせる prompt に寄せる。
   - 前段落との因果接続をより明示する。
   - `source.Text` のどの部分を今回の beat で参照すべきかを指定する。

3. fallback を本筋にしない。
   - `deterministicStoryDraft()` と `safeStoryRetelling()` は保険として残してよい。
   - ただし、live probe の合格判定では「ただの source retelling」は不合格とみなす。

4. live probe を 3 連続で回す。
   - `timeout 180s ./test-story`
   - `story_text` が title 抜きで source を認識できるか。
   - `story_draft_text` より良くなっているか。
   - retelling ではなく twist が機能しているか。

## skill 関連

- 既存 skill `idlechat-story-tuning` は更新済み。
- 更新先:
  - [/home/nyukimi/.codex/skills/idlechat-story-tuning/SKILL.md](/home/nyukimi/.codex/skills/idlechat-story-tuning/SKILL.md)
  - [/home/nyukimi/.codex/skills/idlechat-story-tuning/references/workflow.md](/home/nyukimi/.codex/skills/idlechat-story-tuning/references/workflow.md)
- その skill には、今回の判断が反映済み。
  - `StorySource.Text` は long synopsis として扱う
  - non-LLM fallback は safety rail であり目標ではない
  - 合格条件に「単なる retelling を含めない」

## 2026-03-22 追記: バリデーション閾値整合 + Q8 先読み修正

### 修正済み

| 問題 | 修正内容 |
|---|---|
| `full draft: prose check failed`（原因不明） | `storyHasOverblownSetting` の まるで 閾値 `>= 5` → `>= 9`（per-beat 最大 2×4=8 を許可している不整合を解消） |
| Q8 先読み | `forbiddenBeats` に構造ラベルでなく `spec.content`（"機を織るの場面。〜"）を渡すよう変更。禁止リストを箇条書き形式に整形 |
| `storyHasDistractingDigression` | per-beat バリデーションに追加して早期検出 |
| Temperature | draft: 0.3 → 0.6、revision: 0.25 → 0.5（創造的多様性のため引き上げ） |

### ペンディング: Step 8 インフレ（Q4）

**症状**: revision が draft の 2〜3 倍に文章を膨らませる。「まるで」も大量追加される。
**対処済み**: MaxTokens=1800 に削減、「第1稿より文を増やさない」制約をプロンプトに追加。ただし未解決。
**次の案**:
- revision の まるで 上限チェックを追加（Step 8 は現在 prose check を skip している）
- MaxTokens をさらに削減（1200〜1500）
- revision prompt のシステムプロンプトを「圧縮・修正のみ」に特化

### ペンディング: Q9 PLAN が本文に効かない（フォールバック時）

**症状**: Setting / Tone / Twist が生成文に反映されず、元話の再話になる。
**根本原因**: Step 7 が全 retry 失敗 → `deterministicStoryDraft()` フォールバック発動 → Plan が完全無視される構造。
**現状**: Step 7 が 5/5 リトライゼロで安定しているため実害なし。
**未修正の点**: `deterministicStoryDraft()` は今も Plan を無視する実装のまま。Step 7 が将来不安定になれば再発する。
**次の案**: フォールバック発動時も Plan の Setting / EndingFlavor 最低限を組み込む、またはフォールバックを廃止して Step 7 retry 上限を増やす。

## 一言でいうと

今は「壊れた story を止める段階」はある程度できた。次は「面白い draft を最初から出す段階」に戻すこと。
