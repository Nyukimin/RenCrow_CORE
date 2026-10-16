---
title: 調査 — Shiro ChatのPrefix Cache不成立とStrata prefill
date: 2026-10-15 09:51 JST
status: 対応中（案A2を実装し、Shiro WorkerとChatworkerへ本番反映済み。要約など別要求の割り込みはStrataの会話キャッシュで解消。他agentへの展開が残る）
skill: debug-investigate
symptom: Shiro Chat 1回のStrata処理は約12秒で、その約8割（約9.5秒）がprefill。毎回約5K tokenのpromptを丸ごと読み直している（reused=0）
frequency: 実経路4回、および直近12回のprompt receiptの全てで再現
inputs: 実経路のShiro Chat（rencrowctl chat）、Strata engine log、Gateway request log、prompt receipt（メタデータのみ）、Strata直接送信の対照測定
related: docs/調査/20260808_173823_Midori応答遅延とPrefix_Cache不成立.md, docs/04_アーキテクチャ概要.md#prompt-context-assembly
---

## 概要

Shiro ChatをRTX 5060 Ti + RTX 4060 Tiのhostで動くStrata（upstream v0.1.44.1、2GPUのレイヤー分割）へ向けた後、1回の処理時間の約8割がprefillだと分かった。promptは約5.0〜5.3K tokenで、4回とも`reused=0`、prefillは9.1〜9.8秒（約535 tok/s）、decodeは2.4〜2.7秒だった。

CORE側のprompt配置は、Character、Stable、Recall、Variable（現在時刻）、Userの順で、すでに「固定を先、可変を後」にしている。それでもcacheが再利用されないのは、2026-08-08のMidoriの件と同じ機構で、エンジンのcheckpointが固定prefixの境界に置かれていない可能性が高い。CORE側で並びを変える必要は、現時点では見つかっていない。必要なのは、固定prefixの大きさの観測と、checkpointの置き方を決めるRenCrow_LLM側・Strata側の判断である。

## 発生条件

- Shiro Chat（route=CHAT）→ Gateway target `shiro_chatworker` → Strata v0.1.44.1（Qwen3.8-Flash-Next、2GPU、1 generation slot）。
- Shiroの3 targetは`system_message_mode: single_leading_instruction`。Gatewayがsystem／developerメッセージを**1つの先頭systemメッセージに統合**してからStrataへ送る。
- Strataのprefillは`prompt chunk auto: 8192 tokens`で、`--prompt-cache-every`は既定の16384。

## 手順

1. 構成Bで、`rencrowctl chat --to shiro`により一般知識の質問4件を連続で送った（個人情報は含まない）。Strataのengine logから、各要求のprompt token数、再利用数、prefill時間、decode速度を取った。
2. Gatewayのrequest logで、Chat呼び出しの所要時間を確認した。
3. prompt receipt（メタデータのみで本文は保存されない）の直近12回のShiro Chatを比べた。
4. Strataへ直接、Shiroと同規模（約5.2K token）の合成promptを送り、再利用の条件を対照測定した。対象は、同一promptの再送、最後のuserだけの変更、間に別要求を挟む場合、揮発行を先頭・中間・終端に置く場合、`--prompt-cache-every 1024`の追加である。

## 事実

1. 実経路4回: prompt 4,982〜5,270 token、`reused=0`、prefill 9.1〜9.8秒（534〜549 tok/s）、生成106〜136 tokenのdecode 39.7〜54.0 tok/s。
2. Gatewayのlogでは、Chat呼び出しは約12〜13秒。`rencrowctl chat`の所要95〜127秒には、返信後の後処理（スレッド要約、embedding、プロファイル抽出）が含まれる。
3. 直近12回のprompt receipt: `system_prompt_sha256`が全て異なる。同じ文字数（system 15,636文字、prompt全体15,743文字）で、hashだけが異なる3回があった。systemの区画数は13〜18で変わる（system 442文字・区画2の短い呼び出し1件を除く）。通常の呼び出しでは、systemがprompt全体の約99.3%を占める。
4. CORE側の実装: `assemblePromptContext`は、Character、Stable、Recall、Variable、Userの順に並べる。`WithCurrentJSTTime`は、秒まで含む現在時刻行をVariableのsystemメッセージとして、最後のuserの直前に差し込む（固定幅の文字列）。`staticPromptHash`は定義されているが、非testのコードでは呼ばれておらず、receiptにも出ない。
5. Strata直接の対照測定（合成prompt、約5.2K token、構成B）: 同一promptの再送は`reused=5,191`でprefill約0.1秒。それ以外の条件（最後のuserだけ変更、別要求を挟む、揮発行を先頭・中間・終端に置く）は、全て`reused=0`でprefill 7.4〜7.8秒。`--prompt-cache-every 1024`を足しても変化せず、保存されるcheckpointは1個（最終状態）のまま。
6. Strataの起動logに`prompt chunk auto: 8192 tokens`がある。checkpointはprefillのchunk境界でしか作れないため、約5Kのpromptは1 chunkに収まり、途中のcheckpointを置けない。9K tokenで2個、30Kで3個、96Kで6個とcheckpoint数が増えており、chunkを跨ぐ長さでは増える。
7. 追記型のやり取り（Codexのagent loop）では、2,975 tokenのうち2,903 tokenが再利用される。

## 結論

- 事実: Shiro Chatのprefillは毎回ほぼ全量（約5K token）で、Chat 1回の処理時間の約8割を占める。再利用は起きていない。
- 事実: 先頭systemメッセージが可変部分（Recallと現在時刻）を含み、Gatewayで1つに統合される。chunk境界が粗いため、固定prefixの境界にcheckpointが置かれない。
- 推定（未検証）: Strataの「system終端」のcheckpointは先頭systemターンの終端に置かれる。実promptで2個のcheckpointが保存され、複数systemメッセージの合成promptで1個だったことと整合するが、直接は確認していない。この場合、終端はRecallと現在時刻の後ろにあり、毎回変わる。
- 未測定: 再利用できる固定prefix（Character + Stable）が実promptのどれだけを占めるか。receiptは全体の文字数しか持たない。固定部分が大きいほど、節約できる時間は大きい。
- CORE側のprompt配置（固定を先、可変を後）は、この件の原因ではない。維持する価値がある。

## 提案

**CORE（このmodule）**

1. 観測: prompt receiptに、PromptContextの種別（Character、Stable、Recall、Variable、User）ごとの文字数とsha256、および固定prefix（Character + Stable）の累積文字数を加える。`staticPromptHash`の値も出す。これで、固定prefixの大きさと安定性が毎回見える。
2. 測定: Prompt Debugで実promptを取り、固定prefix、Recall、Variableのtoken数と、連続するChat間でRecallが変わる頻度を調べる。
3. 不変条件: 「Variableは必ずStable・Recallの後、Userは最後」を、architecture testで強制する。今は`assemblePromptContext`の実装にあるだけである。

**RenCrow_LLM／Strata host profile（別module。ここでは判断せず共有する）**

4. 案A（エンジン側のみ）: Strataに`--prefill auto:512`と`--prompt-cache-every 512`程度を指定し、prompt内にcheckpointを細かく置く。最初に異なるtokenより前の最寄りcheckpointから再利用される。Midoriでllama.cppのcheckpointを256 token間隔にした解決と同じ考え方。懸念はMoEのprefill速度（小さいchunkでexpert転送が増えて遅くなる可能性）で、未測定。
5. 案B（契約）: Strataのrequestフィールド`strata_prefix`（`{"message":0,"chars":N}`でmessage 0の先頭N文字までを共有prefixとして固定する）に、固定prefixの境界を渡す。CORE → Gatewayで境界を伝え、Gatewayがsystem統合後の文字数を計算してStrataへ転送する。正確だが、モジュールをまたぐ契約変更になる。

## 検証計画と受入条件

- 実経路のShiro Chatで、異なる質問を連続4回送る。Strata logの`N reused + M read`で、`reused`が固定prefixのtoken数に近いこと（目安: 固定prefix − chunk幅）を確認する。
- 目標: Chat 1回のStrata処理時間（現在約12秒）が、prefill 2秒以下を含む5秒前後に下がること。
- 退行: 案Aでは、冷キャッシュのprefill速度（約5K、30K、96Kの3点）とdecode速度が5%以上悪化しないこと。悪化する場合は案Bを検討する。

## 対応（2026-10-15）

提案のうち、案Aは不採用、案Bは未着手とし、**案A2（配置）**を採用して実装した。

- 案A（Strataの起動引数）は不採用。`--prefill auto:N`は2GPU構成でチャンク幅を変えず（起動logは常に`auto: 8192`）、
  再利用は0のままだった。固定の`--prefill N`と`--prompt-cache-every N`は再利用できるが、冷キャッシュのprefillが大きく悪化した
  （約5K tokenで7.9秒に対して2048で13.0秒、1024で20.0秒、512で22.4秒。約30K tokenで24.1秒に対して61.0〜128.8秒）。
  チャンク設定はStrata全体に共通で、Codexなど長い入力も悪化するため採用しない。
- 案A2（配置）は、エンジン設定を変えずに先頭Systemを固定部分だけにし、Recallと現在時刻を最後のuser turnへ移す。合成promptでの測定は、
  固定部分が約90%のとき2回目のprefillが約3.4秒（再利用4,542 / 5,144 token）、約60%のとき約6.0秒（2,878 token）。
  固定部分が2,048 token未満では再利用0（Strataのsystem終端checkpointは2,048 token以上が条件）。初回は約+3秒。
- 実装: RenCrow_LLMのGatewayに`system_message_mode=single_leading_stable`を追加した。COREが送る`rencrow.prompt_context_blocks`で
  Character／Stableだけを先頭Systemに置き、Recall／Variableを最後のuser messageの封筒へ移す。使えない要求は
  `single_leading_instruction`として扱う。COREでは、Shiroの応答言語ルールとペルソナ文（定数）をVariableからStableへ直し、
  ReAct経路の平坦化をやめて型付きPrompt Contextを渡し、`assemblePromptContext`がStable blockをRecallより前に集約する。
  不変条件と検証はdocs/04の「KVキャッシュとViewer」と「必須検証」に記載した。
- 残り: prompt receiptへの種別ごとの大きさと静的prefixのhashの追加。
  （`shiro_codex`・Midori・Mio・開発用routeへの展開と、割り込み要求によるcache失効の解消は、次節以降に記録した。）

## 本番反映後の実測（2026-10-15）

Gateway（RenCrow_LLM `8ac4c87`）とCORE（`a1823be2`）を反映し、`shiro_worker`（03:13Z）と`shiro_chatworker`（06:10Z）を
`single_leading_stable`へ切り替えた。巻き戻しは`llm.json`のmodeを`single_leading_instruction`へ戻すだけ。

1. OPS 2回（質問と時刻が異なる）: Gatewayの`stable_layout`は3件とも`applied=true`、先頭Systemのsha256は全て同一、`stable_prefix_changed`は0件。
   Strataは、1回目が6,939 token・`reused=0`・prefill 10.3秒（旧配置からの切替直後で冷えている）、同じ処理の2回目の呼び出し（ツール結果の追記）が
   7,450 token・`reused=6,934`・3.1秒、別の質問のOPSが6,937 token・`reused=6,857`（98.8%）・1.5秒。時刻は新配置でも正しく渡った。
2. Shiro Chat 2回: `applied=true`、先頭Systemのsha256は2回で同一、応答は正常。しかしStrataは5,338 tokenと5,124 tokenのどちらも`reused=0`で、
   prefillは12.4秒と12.2秒。旧配置の約9.2秒より約3秒遅い（root checkpointを作る分の初回コスト）。
3. 原因（事実）: 各Chatの直後に`conversation.thread`の要約呼び出し（userメッセージ1件、約380 token）が同じStrataを使い、cacheが失効する。
   Strataへ直接送る合成測定（約5.1K token、固定部分90%）: 割り込みなしは2回目が`reused=4,539`・prefill 3.4秒、403 tokenの別要求を挟むと
   `reused=0`・10.5秒。割り込み側に`"strata_checkpoint": false`を付けても`reused=0`で、防げない。
4. 頻度: 直近24時間のGatewayログ（試験を含む）でStrata行きの`shiro_*`は66件。`shiro_chatworker` 15件のうち、直前も同じChatだったのは3件で、
   12件は直前が別の呼び出し（`conversation.thread`の要約が8件）。`rencrowctl chat`は実行ごとにthreadを閉じて要約を走らせるため、
   Viewerでの連続利用では割り込みがもっと少ない可能性がある（未測定）。
5. Gatewayの`stable_layout_fallback`（`no_static_blocks`、WARN）が要約呼び出しごとに出る。要約はuserメッセージ1件のみで、固定部分が存在しない。

**対応（2026-10-15、ユーザー指示）**: Strata hostの起動引数に`--conversation-cache-mib 8192 --conversation-cache-slots 4`を追加して再起動した
（upstreamのdocsでは、要求が交互に来る複数の会話をhost RAMへ退避する機能）。構成Bのレイヤー分割でも動作した。

6. 合成測定（割り込みを挟む）: 有効前は`reused=0`・10.5秒だったのが、有効後は`reused=4,539`・3.5秒。冷えた1回目の速度（約10.6秒）は変わらない。
7. 実Chat 2回（間に要約が入る）: 1回目は4,960 token・`reused=0`・11.5秒（再起動直後で冷えている）、2回目は4,955 token・`reused=4,060`・4.7秒
   （有効前は2回目も`reused=0`・12.2秒）。先頭Systemのsha256は両方同一、応答は正常（地球の直径12742、月までの距離384400）。
8. 制約（未測定）: 長時間運転での挙動と、4会話では足りなくなる場合（Chat、要約、OPS、Codex、X Bookmark要約が交互に来る）の再利用率。
   要約の`no_static_blocks`フォールバック（WARN）は、固定部分を持たない呼び出しなので残る。
9. Strata hostの設定と手順は、RenCrow_Qwen38_Strataの`docs/operations.md`（会話キャッシュの節と、外す手順）に記録した。

10. `shiro_codex`（07:06Zに`single_leading_stable`へ切替）: Codex系のrequestはCOREの型付きブロックを持たないため、`no_blocks`（INFO）で従来の
    `single_leading_instruction`配置のまま通り、並びは変わらない。切替で得られるのは、`stable_layout_fallback`ログに先頭Systemのsha256が残ること。
    Responses API形式の追記型3回（指示は共通、約10.2K token）の実測は、先頭sha256が3回とも同一、Strataは1回目`reused=0`・14.3秒、
    追記した2回目`reused=10,197`・0.4秒、入力の末尾が違う3回目`reused=10,185`・0.3秒。失敗0件。
    CodexのClient側が先頭に何かを足す場合は、このsha256の変化で検知できる（fallback経路では`stable_prefix_changed`は出ないため、ログの目視比較になる）。

11. `midori_wild`（08:17Zに`single_leading_stable`へ切替）: Midoriの実体はStrataではなく別hostのllama.cpp（b11429、1 slot、host prompt cache 8 GiB、
    再帰checkpointを256 tokenごと、context 65,536）。Strataの会話キャッシュとは無関係。CORE経由のChat（内容違いの3回）で、
    切替前後の「評価した新規token」（llama.cppの`prompt eval`のtoken数。再利用分は含まない）を比べた。

    | | 1回目 | 2回目 | 3回目 |
    |---|---|---|---|
    | 切替前（`single_leading_instruction`） | 568 | 519 | 366 |
    | 切替後（`single_leading_stable`） | 4,749（全量、6.9秒） | 464 | 469 |

    切替後は`stable_layout`が3回とも`applied=true`、先頭Systemのsha256は3回で同一、応答は正常、失敗0件。
    判断: Midoriのlast turn以外の履歴は短い（CORE→Gatewayのmessage数は13〜18で、大半はSystemのブロック）ため、切替前も評価は約500 tokenで済んでおり、
    切替後との差は出ていない。履歴が長くなるほど、Recall・時刻より後ろの履歴まで読み直す旧配置との差が開く見込みだが、長い履歴での比較は未測定。
    切替直後の1回目だけ全量を評価した理由（先頭Systemの末尾が変わったことによるcheckpointの扱い）は未確認。

12. `mio_chat`（別hostのllama.cpp b11429、Gemma4 12B、1 slot）は、`single_leading_stable`へ切り替えて**元の指定なし（`preserve`）へ戻した**。
    - Gemma4のchat templateは、先頭のsystemを専用のsystem turnとして出力し、後続のsystemも独立したturnにする。systemを最初のuserへ併合する挙動ではない。
    - CORE→Gatewayの実リクエストは、systemが13件（うち6件がRecall・Variable）、assistantが1件、userが2件。新配置の実チャット（計9回）では
      Gatewayの`stable_layout`が毎回`applied=true`、先頭Systemのsha256は全て同一（8,890文字）。連続する2回のbodyは履歴のuserメッセージの先頭で食い違い、
      共通部分は全体の約76%で設計どおりだった。
    - それでもMioのbackendは全回で約4.7K tokenを全量評価した（再利用ゼロ、TTFT約3.0秒）。`preserve`では1回目が約3.0秒、2回目以降が0.8〜0.9秒
      （約3.5K tokenを再利用）。切替で約3倍遅くなるため戻した。
    - 同じbackendへ直接送る合成プロンプト（固定部分を実物の大きさに合わせ、履歴が1ターンずつずれる、補助呼び出しが割り込む）と、Gateway経由で
      CORE同様の型付きブロック・`tools`・推論フラグを付けた合成リクエストは、いずれも新配置で再利用された（約3,250〜5,900 token、TTFT約0.5秒）。
      したがって配置・テンプレート・エンジンの組合せ自体は問題なく、実リクエストの内容で再利用が崩れる原因は特定できていない。
    - `preserve`でも、先頭Systemの最初のブロックはCharacterで毎回同一、続くStableも同一で、Recall・Variableはその後ろに並ぶため、
      先頭固定の不変条件はこの形で保たれている。Midoriと違い、systemを1件に統合しない。
    - 付随する観測: 新配置の稼働中だけ、CORE→Gatewayのプロファイル抽出（`memory.profile_promotion`、userメッセージ1件）が7件中2件失敗した
      （それ以前の72時間は50件中0件）。新配置のfallback経路は、userメッセージ1件の要求では並びを変えないはずで、合成の同形リクエスト10件は全て成功したため、
      因果は未確認。Gatewayのログに失敗の理由が出ない。
    - 診断のため、Gatewayのプロンプト記録を3回のチャットの間だけ一時的に有効にした（構造の数値だけを比較、記録ファイルは削除、設定は無効へ戻した）。

13. 開発用route（coder1〜coder4。`sera14b`・`ministral3_14b`・`qwen36_35b_a3b`。別hostのllama.cpp b10603、Vulkan、各1 slot）。
    - 利用実績: Gateway経由は8月17日以降で計30件で、全てalias smokeと再起動確認。COREのCoder提案は直近7日で0件。実リクエストでの比較はできないため、
      COREのCoderと同形の合成リクエスト（型付きブロック。Character+Stable約3.5K token、履歴、時刻のVariable）をGateway経由で6回ずつ送り、
      backendの`prompt eval`のtoken数から再利用量を出した。
    - COREの組み立て: 提案（`GenerateProposal`・`GenerateWithPrompt`）はCharacter → Stable → 履歴 → Variable（時刻）→ userで、型が付いている。
      CoderLoop（`GenerateWithContext`）は先頭のsystem promptだけ型が無かった（`code_executor_loop.go`）。
    - **既存の不具合（`preserve`）**: COREと同形の要求を`preserve`で送ると、`ministral3_14b`（chat templateが
      「Only user, assistant and tool roles are supported」）と`qwen36_35b_a3b`（同「System message must be at the beginning」）がHTTP 500を返す。
      先頭以外のsystem（Variableの時刻、2件目のStable）をtemplateが拒否するため。`sera14b`は200。smokeはsystemが1件だったため見逃されていた。
      つまりcoder2〜4は、COREからは今まで使えない状態だった。
    - `single_leading_stable`への切替結果（合成、6回目の値。評価=evaluated token、再利用=reused token）:

      | target | 型付きの形（提案・修正後のCoderLoop） | 型なしのCoderLoop（修正前） | `preserve` |
      |---|---|---|---|
      | `sera14b` | 4.1秒、評価1,609、再利用7,907、先頭sha固定 | 14.0秒、評価5,860、再利用3,568、shaが毎回変わる | 4.0秒、評価1,590、再利用7,846 |
      | `ministral3_14b` | 4.2秒、評価1,631、再利用8,057、先頭sha固定 | 13.5秒、評価5,936、再利用3,649 | HTTP 500 |
      | `qwen36_35b_a3b` | 1.7秒、評価1,615、再利用7,895、先頭sha固定 | 3.8秒、評価5,890、再利用3,539 | HTTP 500 |

      提案の形（履歴が窓でずれる）も、`sera14b`の`preserve`と`stable`で再利用・所要時間が同等（5回目以降は固定部分の約3.6K tokenのみ再利用）。
    - **COREの不具合**: 先頭のsystem promptに型が無いと、Gatewayは`instruction_without_block`でfallbackする。fallbackは時刻のVariableも先頭Systemへ畳み込むため、
      先頭のsha256が毎ターン変わり、履歴の再利用が消える（上表の「型なしのCoderLoop」）。`code_executor_loop.go`で先頭systemをCharacterとして型付けする
      （テスト`TestCoderLoopLeadingSystemPromptIsTypedCharacter`）。
    - 判断: `ministral3_14b`と`qwen36_35b_a3b`は`preserve`が全件500のため`single_leading_stable`へ切り替えた（型なしのCoderLoopはfallbackで動くが遅い。COREの修正で解消する）。
      `sera14b`は`preserve`のまま。COREの修正が本番へ反映される前に切り替えるとCoderLoopが約3.5倍遅くなるため、反映後に切り替える。
    - 付随する観測: Gatewayのfallback理由のうち、`shiro_worker`の`instruction_without_block`は`x_bookmark.external_summary`（`SystemPrompt`欄を使う補助呼び出し）。型なしsystemの全体調査は項目14。
    - 未測定: 実際のCoder提案・CoderLoopでの応答品質、thinkingを有効にした要求。
    - 本番反映と切替（2026-10-15 21:58Z）: 本番のCOREは別作業者のビルド（`38f87d02`＋未コミット差分）で既に`d2cbaa5a`の修正を含んでいたため、入れ替えずに`sera14b`を`single_leading_stable`へ切り替えた。
      合成のCoderLoop（型付き）は6回とも`applied=true`、先頭sha固定、6回目4.1秒・再利用7,907 token。実のCoderLoop要求での確認は未了（本番では7日で0件）。

14. 型が無いsystemを作る他の箇所（`SystemPrompt`欄と個別の箇所を修正済み）。
    - 方法: 非テストのGoで`Role: "system"`のリテラル51件を、囲む構造体リテラルの`Type`の有無で分類した。型なしは20件（idlechat 15、`game_turn.go` 2、`subagent/manager.go` 1、
      および`SystemPrompt`欄の変換`rencrowllm/messages.go`と観測用の`middleware/promptreceipt.go`）。さらに、`GenerateRequest.SystemPrompt`欄は
      `rencrowllm`の`convertMessages`（`messages.go`）でindex 0の型なしsystemに変換され、`promptContextBlockMetadata`（`provider.go`）はindex 0のブロックを出さない。
      このため、`SystemPrompt`欄を使う呼び出しは全て型なしの先頭systemになる（`ChatRequest`には`SystemPrompt`欄が無い）。
    - 影響する条件: 宛先がGatewayの`single_leading_stable`対象（`shiro_chatworker`・`shiro_worker`・`shiro_codex`・`midori_wild`・`coder1〜4`の各target）で、
      要求に型なしsystemが1件でもあるとき。Gatewayは`instruction_without_block`でfallbackし、`DateTimeProvider`が差し込む時刻のVariable（秒単位）を先頭Systemへ畳み込む。
      先頭のsha256が要求ごとに変わるため、先頭固定は成立しない。`mio_chat`（指定なし）と`kuro_codex`（codex_cli）は対象外で影響しない。
    - 実トラフィック（Gatewayログ、本日03:13Z以降）: `shiro_worker`の`x_bookmark.external_summary`は10件で先頭shaが10種、観測contextの無い呼び出し（caller=`core.unattributed`）は5件で5種。
      外部リンク要約のsystem promptは約1.3KB（約350 token）で、1件あたりの再prefillの損失は小さい。ただし不変条件（先頭が毎回同一）は満たしていない。
    - 型なしの箇所:

      | 区分 | 箇所 | 宛先（配線） | 本番での利用 |
      |---|---|---|---|
      | `SystemPrompt`欄 | `knowledge/external_link_summary.go:295` | Worker（`shiro_worker`） | あり（72時間で64件） |
      | `SystemPrompt`欄 | `xbookmarkworkflow/service.go:242,293` | Worker | 未観測（呼び出し元が無印の可能性） |
      | `SystemPrompt`欄 | `backlog/revalidation_evaluator.go:330`、`gmailintake/gmail_evaluator.go:418` | Worker | 未観測（無印の5件のいずれかの可能性。呼び出し元は特定できていない） |
      | `SystemPrompt`欄 | `routing/llm_classifier.go:34` | Chat | 72時間のGatewayに分類の呼び出しなし |
      | `SystemPrompt`欄 | `domain/agent/game_turn.go:45,63`（Heavy、Wild） | Heavy、Wild（`midori_wild`） | なし（ゲーム） |
      | `SystemPrompt`欄 | `viewer/game_autoplay.go:363`、`cmd/rencrow/voice_chat_runtime_input_audio.go:262`、`cmd/rencrow/runtime_person_related_summary_translation.go:30` | 未確認 | 未確認 |
      | 型なしのMessage | `domain/agent/game_turn.go:22,25`（Mio） | `mio_chat`（指定なし） | 影響なし（`preserve`） |
      | 型なしのMessage | `application/subagent/manager.go:139`（`PromptContext`が空のときの従来の組） | Worker（`tools/subagent.go:41`の`worker`サブエージェント） | 未観測 |
      | 型なしのMessage | `application/idlechat/`の15件 | 各話者のprovider | 本番では停止中（再起動後0件、72時間のGatewayに0件） |

      Shiro OPS（`shiro.go`）は`PromptContext`を型付きのまま渡しており、`agent.shiro`/`execute_ops_task`は`applied=true`（問題なし）。`middleware/promptreceipt.go:172`は観測用でwireに出ない。
    - 合成確認（同形の要求: 定数system約1.3KB＋時刻Variable＋user、`worker`宛、3回ずつ）: index 0を型なしで送ると3回とも`instruction_without_block`で先頭shaが3種。
      index 0を`stable`と申告すると3回とも`applied=true`で先頭shaが同一。
    - 修正（`SystemPrompt`欄、`rencrowllm/provider.go`の`promptContextBlockMetadata`）: `SystemPrompt`が空でないときにindex 0を`stable`のブロックとして出す。
      1箇所で`SystemPrompt`欄の全caller（Worker宛のexternal_link_summary・xbookmark・Atlas再検証・Gmail評価、Heavy・Wild宛のゲームなど）が直る。
      テスト`provider_system_prompt_block_test.go`（index 0のブロック、`SystemPrompt`が無いときは出さない、`Generate`が送るbodyでmessages[0]とブロックのindexが揃う）。
      Red（ブロックが2件）を確認してから実装した。
    - 修正の検証: 別hostのスクラッチ（GitHub mainの浅いクローン＋パッチ）で`go vet`と`go test ./...`（251パッケージ ok）、タグ付き`-tags actionindexdefault`（68パッケージ ok）が成功。
      実際の外部リンク要約と同形の要求（実のsystem prompt、400文字のsource_spans、`json_object`）を`worker`宛にGateway経由で5件ずつ送り、COREの`decodeExternalLinkSummaryResponse`と同じ規則
      （フィールドが`summary`と`evidence_ids`だけ、summaryは空でなく2000文字以内、evidence_idsは1〜3個で既存ID）で検証した。型なし（現状）は5件、型付き（修正後）も5件が有効で、選ばれたevidence_idsも同じ。
      修正後はGatewayが`applied=true`となり、先頭systemの末尾にContextDataNoticeが加わり、時刻はuserメッセージ先頭の封筒へ移る。本番COREへの反映後の確認は下の「本番反映後の確認」。
    - 個別の箇所（修正済み）:
      - `game_turn.go`のMio: 人格をCharacter、`gameTurnBoundaryPrompt`をStable、観測をUserにした（`ShiroChat`もこの経路で`shiro_chatworker`へ行くため、宛先が`mio_chat`だけではない）。
      - `subagent/manager.go`の従来の組（`PromptContext`が空のとき）: systemをCharacter、instructionをUserにした。
      - `idlechat`の定数のsystem 12件と`getSystemPrompt`由来の2件: Stable／Characterを付けた。
      - `idlechat`の主要生成（`orchestrator_response_generation.go`）: 先頭systemは人格・セッション文脈・語彙メモ（同じ会話のターン間で不変、Character）のままにし、
        ターンごとに変わる対話方針（`BuildDialoguePrompt`。直前の発話とターン計画を含む）だけを、先頭へ連結せずVariableの別messageにした。
        セッション文脈と語彙メモを先頭に置くのは既存の判断（`TestGenerateResponseKeepsPersistentContextInFirstSystem`）で、変えていない。
        語彙メモは会話中に変わりうる（変わると先頭が1回変わる）。
    - 強制: `internal/domain/llm/untyped_system_architecture_test.go`が、非テストのGoで`llm.Message`・`llm.ChatMessage`をRole `"system"`かつ`Type`なしで作るリテラルを検出して失敗する
      （許可リストは空）。修正前は上の18箇所を報告してRed、修正後はGreen。文字列リテラルでないRoleと、`GenerateRequest.SystemPrompt`欄（providerがStableを付ける）は対象外。
    - 追加したテスト: `TestSubagentChatMessagesLegacyPairIsTyped`、`TestMioDecideGameTurnTypesEverySystemMessage`、
      `TestGenerateResponseKeepsLeadingSystemStableAndTypesDialogueOrder`。いずれも変更前のコードでRedを確認した。
    - 未確認: IdleChatは本番で停止中のため、実際のIdleChat会話での再利用と応答品質は未測定。対話方針をsystemの末尾から封筒（`single_leading_stable`の対象のとき）へ移すため、
      文面の強さが変わりうる。
    - 付随する観測: 本番COREで、5分ごとのbacklog runnerが`superagent runtime context is required when recorder is configured`で毎回失敗している（再起動後43回、LLM呼び出しの前に落ちるためGatewayには届かない）。型の問題ではなく、既知のOPS不具合（10月9日記録）と同じ系統。

    - 本番反映後の確認（2026-10-16 00:00Z、COREを`cd9b781a`でビルドして入れ替え。バックアップ付きの原子的入れ替え、健全になるまで約175秒、restarts 0）:
      - 反映直後にX Bookmarkの要約が実際に動き、`x_bookmark.external_summary`の3件が`applied=true`、先頭shaが同一（反映前は10件が10種）。`SystemPrompt`欄の修正が実トラフィックで効いている。
      - 実チャット（`shiro`2回、`mio`、`midori`）は正常に応答し、`shiro_chatworker`は`applied=true`で先頭shaが固定、`midori_wild`は`applied=true`（先頭shaは従来と同じ）。
        補助呼び出し（`conversation.thread`の要約）は固定部分が無く`no_static_blocks`のまま。
    - 付随する観測（今回の変更とは無関係）: `x_bookmark.external_summary`の生成時間は、10月14日21時までは2〜7秒（当時のworkerの実体は別のGemma4）、10月15日03時以降（実体がStrataのQwenへ変更）は21〜77秒になった。
      反映後の2件は`structured output was incomplete (finish_reason=length)`で失敗した。要約の呼び出しがthinkingを無効にしておらず、出力予算（4096 token）を思考で使い切るとみられる。
      このバーストの間、同じworkerへの`conversation.thread`の要約2件が24.7秒のqueue待ちで失敗し、チャットの応答も長くなった（shiro 66〜94秒。再起動直後の冷えも含む）。
      同じ問題は、`SystemPrompt`欄を使うWorkerの他の補助呼び出し（`caller=core.unattributed`。完了した10件が23〜61秒）にも見える。
    - 対応（X Bookmarkの外部リンク要約、`knowledge/external_link_summary.go`）: 要約の全呼び出し（初回と根拠の修復）に`think:false`と
      `chat_template_kwargs.enable_thinking:false`を渡す。Workerのrole policyはthinkingを許したままにしている（OPSの思考を保つため）ので、呼び出し側で止める。
      実測（Gateway経由、実のsystem prompt、`json_object`、2〜5個のsource_spans、各3件）: 既定は11〜27秒・出力375〜1,246 token、`think:false`だけは8〜18秒・418〜954 token（ほぼ効かない）、
      `chat_template_kwargs`も指定すると6〜7秒・108〜127 tokenで、いずれも有効なJSON。要約の文面は同等で、選ばれるevidence_idsは一部が異なる（どちらも有効）。
      テスト`TestExternalLinkSummaryRequestsDisableThinkingForEveryCall`（変更前にRed）。
      未対応: `core.unattributed`の呼び出し元（X Bookmark workflowの評価、Atlas再検証、Gmail評価）は判断の質に思考が効く可能性があり、品質の確認なしには変えない。

次は、X Bookmark要約の修正の本番反映と、`core.unattributed`の他の補助呼び出しのthinkingの扱いの判断。Mioは、再利用が崩れる原因（実リクエストのbodyをbackendへ再送して調べる案）を特定してから再試行する。

## 調査上の副作用

- 測定のため、Shiroの会話に一般知識の質問を4件送った（履歴に残る。個人情報は含まない）。
- Strataの設定（`--prompt-cache-every`）を一時的に変更して測定し、元に戻した。本番設定は測定前と同じ。

## 関連ソース・実行設定

- `internal/domain/llm/system_prompt_time.go` — 現在時刻行の差し込み（`WithCurrentJSTTime`）。
- `internal/domain/agent/prompt_context.go` — `assemblePromptContext`、`staticPromptHash`。
- `internal/infrastructure/llm/middleware/promptreceipt.go` — prompt receipt。
- RenCrow_LLM `gateway/internal/messagewire/system_messages.go` — `single_leading_instruction`の統合。
- RenCrow_Qwen38_Strata `docs/operations.md` — 構成別のprefill・decode測定とprefix cacheの挙動。
- Strata `docs/DETAILS.md`（checkpointと`strata_prefix`）、`docs/PROMPT_CACHE_TAIL.md`、`docs/MESSAGE_BOUNDARY_CACHE.md`（8,192 token以上の接頭辞のみ、レイヤー分割は対象外のため約5Kには合わない）。

## 教訓

- 「固定を先、可変を後」の並びは、必要条件であって十分条件ではない。エンジンのcheckpointが固定prefixの境界に置かれて初めて再利用される。
- 再利用の有無は、promptの並びではなく`N reused + M read`の実測で判断する。receiptにはハッシュだけでなく、種別ごとの大きさを残す。
- chunk幅（既定8,192 token）が、checkpointを置ける最小の粒度を決める。約5Kのpromptでは、`--prompt-cache-every`だけを変えても効果がない。
