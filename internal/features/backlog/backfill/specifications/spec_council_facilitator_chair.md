# RenCrow 上位協議 Facilitator / Chair / Shared Artifact 設計判断

起点: 2026-08-20 Agent間共有と上位協議の議論

## 常任メンバー
Mio / Shiro / Kuro / Midoriは対等。固定の上下関係を作らない。

## Facilitator
固定的な協議手続きRole。
- 進行
- 論点整理
- 発言制御
- 記録
- 停滞解消
を担当する。
内容面の票や恒久的権力を持たない。

## Chair
discussion_id単位で交代する一時Role。
- その議題の内容面を主導
- 論点の掘り下げ
- 収束へ向けた整理
を担当する。
Chairの意見を他Agentより重くしない。ChairであることはAgentの上下関係を意味しない。

## Shared Artifact
Agent間の共通状態として、少なくとも以下を保持する。
- issue / 論点
- evidence
- tentative conclusion
- unresolved matters
- decision record

全員総当たり会話を避け、必要な時だけ直接会話する。Agentごとに同じ事実を複製せず、共有Artifactを正本projectionとして使う。

## Worker/Coder側との違い
上位4Agentは対等。
実装workではShiroがExecution Coordinatorとなり、Coderの提案を集約・実行・検証する。これは恒久的なAgent上下関係ではなくWork Graph上の責務。
