---
name: No Approval/Ask Flow
description: approval/ask フローは実装しない（ユーザー意思決定）
type: feedback
---

**ルール**: approval/ask フロー（実行前確認・ユーザー承認待ち）は実装しない。

**Why:** ユーザー明示的意思決定（2026-03-20）。Autonomous Executor は完全自動実行を前提とし、事前承認フローは導入しない。

**How to apply:**
- 仕様書・実装計画から approval/ask フローに関する記述を削除する
- 実装時に「承認待ち」「確認待ち」などの中断状態を設けない
- セーフガード（Git auto-commit、保護ファイルパターン、実行前サマリ表示）で安全性を担保する
- OpenClaw との機能差分比較では「RenCrow は approval フローなし」として記録
