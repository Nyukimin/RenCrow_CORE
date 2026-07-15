---
page_id: concept:runtime-state
type: concept
status: active
owner: core
canonical_source: docs/02_機能仕様.md
source:
  - docs/02_機能仕様.md
  - docs/06_Public_API仕様.md
related:
  - docs/wiki/modules/rencrow-core.md
  - docs/wiki/specs/public-api.md
summary: process health、provider readiness、合成、取得、再生などを別の成功条件として扱う
updated: 2026-07-15
---

# Runtime state

CORE process の liveness、外部 backend health、LLM inference readiness、TTS synthesis、audio fetch、browser playback は別々の状態である。一段が成功しても end-to-end 成功とは限らない。

`unavailable`、`pending`、`degraded`、`error` を空の成功や無言 fallback へ変換しない。runtime selection は ready 確認後に確定し、inference endpoint と management endpoint を分離する。
