# RenCrow_CORE Routing Policy

## このファイルの役割

この文書は、現在のCORE実装における「どんな発話が、どのrouteになり、どの実Agentが何をするか」を定義する。製品契約は [`docs/README.md`](../docs/README.md) から `docs/02_機能仕様.md` と `docs/03_キャラクター・エージェント仕様.md` を参照する。実装との不一致を見つけた場合は、実際の判断・実行経路を確認してから契約を更新する。

routeは処理分類であり、実行権限ではない。認証、利用者scope、安全policy、操作ごとの実行gateは別に適用する。実AgentはMio、Shiro、Kuro、Midori。Coder、Worker、Wild、LLM、provider、runtimeは実装機構またはslotであり、実Agentではない。Coderを選んだ場合もコードrouteの実Actorと作業責任はShiroに残る。

## 判断の優先順位

通常の入力は次の順で処理する。これは現在の `MessageOrchestrator` と `MioAgent.DecideAction` の順序である。

1. **明示宛先のViewer CHAT**: Viewerで `to=shiro|kuro|midori` が選ばれ、メッセージにslash commandがない場合、選択Agentが直接 `CHAT` を処理する。Mioの `DecideAction`、rule dictionary、classifierを呼ばない。これは通常CHATであり、OPSやコード実行を意味しない。`to=mio`、他channel、slash command付きの入力は通常のroute判断へ進む。
2. **明示slash command**: `/chat`、`/plan`、`/analyze`、`/heavy`、`/ops`、`/research`、`/wild`、`/code`、`/code1`、`/code2`、`/code3`、`/code4` を先頭commandとして解釈する。`/heavy` は `ANALYZE` のalias。command名の直後は終端または空白でなければならない。
3. **専用カタログ判断**: indexed person-related catalog lookupは、一般的な創作・知識分類より先にMioの `CHAT` pathで扱う。
4. **RuleDictionary**: 専用条件と自然文keywordを評価する。最初に合致したruleがrouteを確定する。順序は、Codex work path → file referenceを含むcode edit request → keyword rule（`WILD` → `CODE2` → `PLAN` → `ANALYZE` → `OPS` → `CODE3` → `RESEARCH`）→ 明白な挨拶／高確度の通常会話（`CHAT`）。
5. **Classifier**: rule不一致時にだけ分類する。空route、error、confidence `< 0.7` は採用せず `CHAT` にfallbackする。legacy classifier promptで意味が定義されていないrouteは、parserがroute文字列を受理してもclassifierの意味選択が確立したとは扱わない。
6. **Deep-dive policy**: route決定後、利用者が深掘りを明示しpolicyが要求を返した場合、元routeが `CHAT`、`PLAN`、`RESEARCH` のときは `ANALYZE` へ昇格できる。これは再分類ではなく、Mio判断後のpolicy処理である。

明示宛先Viewer CHATはMio判断より前に処理する特例である。slash commandがある場合は会話相手の選択よりcommandを優先する。選択されたruntimeが利用不能な場合、Mioへ黙って切り替えない。

## Routeと実Actorの一覧

| Route | 実Actor | 現在の処理 |
| --- | --- | --- |
| `CHAT` | 通常Mio。Viewerが選択した場合はShiro／Kuro／Midori | Mioまたは選択Agentが会話応答 |
| `PLAN` | Mio | Mioの計画応答 |
| `ANALYZE` | Kuro | Kuroが分析し、結果を返す |
| `OPS` | Shiro | Shiroが運用処理を実行 |
| `RESEARCH` | Mio | Mioの調査応答 |
| `CODE` | Shiro | local executorがcoder1 slotを固定選択 |
| `CODE1` | Shiro | local executorがcoder1 slotへ仕様設計の指示 |
| `CODE2` | Shiro | local executorがcoder2 slotへ実装指示 |
| `CODE3` | Shiro | local executorがcoder3 slotへreview/reasoning指示 |
| `CODE4` | Shiro | local executorがcoder4 slotへ高速試作・実験の指示 |
| `WILD` | Midori | MidoriがWild処理経路で応答 |

## Route別の入力基準

各節はclassifier policy inspectorがrouteごとのcriterionとして読み取る。`CHAT`から`WILD`まで11 routeすべてに節を置き、発話の意味、実Actor、処理を記す。

### CHAT
雑談、挨拶、高確度の直接質問、曖昧で安全なfallback。通常はMioが応答する。ViewerでShiro、Kuro、Midoriを明示選択しslash commandがない場合は、選択Agentが直接CHATを処理し、Mioの判定を通らない。

### PLAN
作業計画、手順、プラン、設計、実装順序の整理。RuleDictionary例は「計画」「プラン」「設計して」「アーキテクチャ」。実ActorはMioで、PLAN pathから計画応答を返す。Coderへの割当routeではない。

### ANALYZE
原因、ログ、挙動、コード、差分、影響の分析。RuleDictionary例は「分析して」「調査して」「解析して」「診断して」「レビューして」。実ActorはKuroで、分析結果を返す。利用者の明示deep-dive要求とpolicy判定もこのrouteを選び得る。

### OPS
command、test、build、deploy、health checkなど運用処理の実行。RuleDictionary例は「実行して」「起動して」「デプロイ」「ビルドして」「停止して」「再起動」。実ActorはShiroで、OPS処理を実行する。routeだけで認証や操作許可が付与されることはない。

### RESEARCH
外部情報の深い調査、資料収集、仕様確認。RuleDictionary例は「リサーチ」「情報を集」「ドキュメントを探」「ウェブ検索で」「原文を取得」「情報収集」。実ActorはMioで、RESEARCH pathから調査応答を作る。Coderへの割当routeではない。

### CODE
より具体的なcoder routeが指定されていない汎用code依頼。`/code`またはclassifierがroute文字列を返した場合に選ばれ得る。通常の自然文code edit ruleはCODE2へ分類する。実ActorはShiroで、local code executorはcoder1を固定選択する。coder1が未配置・busy、または外部providerの場合はerrorにし、別coderへ自動切替しない。

### CODE1
仕様設計、初期案、骨格案。`/code1`またはclassifierで選ばれる。実ActorはShiroで、local code executorがcoder1 slotへ仕様設計用の指示を渡す。Coder1はActorではない。

### CODE2
既存codeの実装、修正、file edit、test追加。file referenceと編集意図の組合せ、および実装系keywordをRuleDictionaryがこのrouteへ自動分類する。`/code2`またはclassifierでも選ばれる。実ActorはShiroで、local code executorがcoder2 slotへimplementation用の指示を渡す。生成物の実行・適用はCORE/Shiroの実行policyに従う。

### CODE3
高品質なcode reasoning/review、またはbrowser/chrome操作を含むrule対象の依頼。RuleDictionary例は「chrome」「ブラウザ」「画面操作」「スクレイピング」「ページを開」「webを操作」。`/code3`またはclassifierでも選ばれる。実ActorはShiroで、local code executorがcoder3 slotへreview/reasoning用の指示を渡す。自然文browser ruleがあることは、全てのCODE3依頼がbrowser操作という意味ではない。

### CODE4
現在のroute意味は高速な試作・実験的codingであり、`/code4`明示commandで選べる。RuleDictionaryに自然文の専用CODE4規則はない。legacy `prompts/classifier.md` にCODE4の意味定義がない一方、`LLMClassifier.parseRoute`は応答文字列CODE4を受理する。この非対称性のため、classifierがCODE4を意味的に選ぶことは未定義であり、route parserの受理だけから選択可能とはみなさない。CODE4がlocal executorへ渡された場合、実ActorはShiro、slotはcoder4で、指示は高速試作・実験的codingとなる。比較レビューや仕上げのrouteではない。

### WILD
物語・文章・創作、画像検索／生成／解析、視覚内容の理解。RuleDictionaryはCodex work pathと創作・画像関連keywordを認識する。`/wild`明示commandでも選べる。legacy `prompts/classifier.md`にWILDの意味定義はないため、classifierの意味選択は定義済みと扱わない。実ActorはMidoriで、Wild処理経路から応答する。画像生成はCOREからRenCrow_Imageの公開contractを通る。Wildは実Actorではない。

## Code routeとtopologyの境界

- local `DefaultCodeExecutor`では汎用`CODE`はlocal `coder1`固定。`CODE1`、`CODE2`、`CODE3`、`CODE4`は指定slotを選ぶ。Coder slotは実Actorではなく、生成提案の実行責任も引き受けない。
- RuleDictionaryの通常code editは`CODE2`へ自動分類する。browser/chrome関連の自然文ruleは`CODE3`へ分類する。`CODE4`には専用の自然文ruleがないため、`CODE4`の意味から暗黙に自動分類してはならない。
- `internal/domain/routing/route.go`はCODE4を高速coding／実験として定義するが、legacy classifier promptはCODE4を定義していない。`internal/infrastructure/routing/llm_classifier.go`のparserがCODE4を受理することは、この意味差を解消しない。promptはこの作業範囲では変更しない。
- 現行distributed pathの`isCodeRoute`は`CODE4`を含まない。localの`CODE4`→`coder4`対応をすべてのtopologyへ一般化せず、distributed pathで常に`coder4`が使用されると保証しない。
- `OPS`、`CODE`系routeの実Actor責任はShiroに残る。Coder、Worker、transport、provider、modelへの委譲を独立Actorやroute ownerとして記録しない。

## 実行安全とコーディング作業

route決定後も、認証、利用者scope、安全policy、操作ごとの検証を通す。曖昧な作業を`OPS`へ分類せず、実行語だけで破壊的操作を許可しない。調査・実装・実行を同じrouteの実Actorへ混同させない。

コーディング依頼は [`rules/PROJECT_AGENT.md`](PROJECT_AGENT.md) に従い、役割に渡す前にSafe Build Mode / Tool Build Modeを判定する。

- Safe Build Mode: 既存code、既存DB、設定、環境、運用、memory、Source Registry、本番系へ触れる作業。
- Tool Build Mode: 既存本体から切り離した新規tool、小物script、補助app、検証用CLI。
- 横断的に再利用するTool Build Modeの成果物は`/home/nyukimi/RenCrow/RenCrow_Tools`を正本とする。`RenCrow_CORE/tools/`は既存互換または本体密結合の残置場所であり、新規の横断tool置き場にしない。
- 迷う場合はSafe Build Modeとして追加確認が必要な状態にする。Tool Build Modeでも既存本体への接続、正式DBへの保存、memory確定、Source Registry更新、設定変更が必要になればSafe Build Modeへ切り替える。

## 実装照合の参照先

routingを変更する場合は、対応する判断・Actor割当・実行経路を合わせて確認する。

- route順序とclassifier閾値: `internal/domain/agent/mio.go` の `MioAgent.DecideAction`
- 明示command: `internal/domain/agent/mio_persona.go` の `parseExplicitCommand`
- 自然文規則: `internal/infrastructure/routing/rule_dictionary.go` の `NewRuleDictionary` / `Match`
- legacy classifier promptとparser: `prompts/classifier.md`、`internal/infrastructure/routing/llm_classifier.go`
- Viewer宛先とdeep-dive policy: `internal/application/orchestrator/message_orchestrator_routing.go`
- 実Actor割当: `internal/application/orchestrator/task_lifecycle.go` の `actualCoreActorForRoute`
- routeごとの実処理: `internal/application/orchestrator/message_orchestrator_routes.go`
- local coder選択: `internal/application/orchestrator/code_executor_selection.go`
- distributed route差: `internal/application/orchestrator/distributed_orchestrator_execution.go` と `distributed_orchestrator_coder_selection.go`
