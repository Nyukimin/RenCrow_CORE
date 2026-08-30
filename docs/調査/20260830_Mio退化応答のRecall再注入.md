# Mio退化応答のRecall再注入

## Failure

Gatewayが反復退化streamを安全停止できても、停止境界の導入前に保存された破損Agent応答が
RecallPackから次ターンへ再注入され、Mioの生成退化が自己増幅した。

## Problem

2026-08-30のIdentity Step 02 Voice E2EでSTT転写は成功したが、Mio応答が
`DEGENERATE_OUTPUT`となり、TTS生成とViewer playback ACKへ到達しなかった。

## Cause

同じ保存済みMio発話がShortContext、発言帰属ガード、最近の表現履歴へ投影された。
Conversation Memoryの保存正本と、将来の生成例として安全なprompt投影を区別する判定がなかった。

## Lesson

監査上の保存とLLMへの再注入は別の責務である。破損履歴を削除して証拠を失わせず、prompt投影だけを
決定的に制限する。Gatewayのstream停止は外部出力境界、RecallPackの除外は入力境界であり、相互に代替しない。

## Invariant

- ユーザー発話は引用や意図的反復を含め、内容を理由に除外しない。
- Agent由来の連続同一runeまたは隣接反復motifは生成例として再注入しない。
- 永続Conversation Memoryは削除・書換えしない。
- 会話prompt、帰属要約、expression historyは同じRecallPack投影結果を使う。
- 除外理由はbounded Recall traceへ残す。

## Enforcement

`internal/domain/conversation`が唯一のprompt安全判定とRecallPack投影を所有する。
Mioは投影済みRecallPackだけから会話promptと帰属要約を作り、expression historyも同じ判定を参照する。

## Tests

- 退化したMio発話だけが除外され、ユーザー引用と正常なShiro発話が保持される。
- 元のRecallPackが変更されず、boundedな`excluded` traceが追加される。
- 同一rune runと隣接motifを拒否し、正常文を保持する。
- expression historyへ退化表現が入らない。
