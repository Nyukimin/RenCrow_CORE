---
type: concept
status: active
owner: llm
canonical_source: docs/05_LLM運用プロンプト設計/API利用者ガイド.md
source:
  - docs/05_LLM運用プロンプト設計/API利用者ガイド.md
  - docs/10_新仕様/04_Chat_Worker_Coder仕様.md
related:
  - docs/wiki/concepts/recall-pack.md
  - docs/wiki/modules/picoclaw-multillm.md
updated: 2026-06-25
---

# ChatWorker

ChatWorker は RenCrow_LLM の Worker endpoint 上にある短文会話用 alias である。

`Worker` と同じ backing model runner を共有するが、会話テンポを優先し、reasoning を返さず、出力上限と有効入力 budget を小さく扱う。

## 境界

- Chat / IdleChat の短い Shiro 応答では `ChatWorker` を使う。
- 通常作業、要約、整理、調査結果処理では `Worker` を使う。
- `ChatWorker` は Worker 実行権限や Coder 選定の別人格ではない。
- patch / shell / git / tool execution は通常の Worker 境界に残す。

## API

- base URL: `http://127.0.0.1:8082`
- endpoint: `/v1/chat/completions`
- model alias: `ChatWorker`

## Recall との関係

ChatWorker の発話でも、Mio / Chat と同様に余計な外部知識を入れすぎない。
仕様確認や明示的な調査意図がある場合だけ、RecallPack の Wiki snippet / Knowledge snippet を使う。
