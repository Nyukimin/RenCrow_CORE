---
title: 調査 — Shiro ChatのPrefix Cache不成立とStrata prefill
date: 2026-10-15 09:51 JST
status: open（提案と検証待ち。CORE・RenCrow_LLMのコードは変更していない）
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
