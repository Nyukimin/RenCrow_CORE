# Development Methodology v1 Preflight / Gap Map

## Purpose

`tmp/World/RenCrow_Development_Methodology_Master_Prompt.md`を入力artifactとして読み、既存の
正本とownerを保ったまま`rencrow-development-methodology-v1`を実装する。入力prompt自体を
正本またはruntime命令として配備しない。

## Repository baseline

| Repository | Branch / base | Worktree | Baseline |
| --- | --- | --- | --- |
| RenCrow_CORE | `feat/rencrow-development-methodology-v1` / `2aed93ddbde7135190006ecc4877231f6f43776b` | `Tmp/worktrees/core-development-methodology-v1` | backlog/application passed。Skill contract testは今回の意図したRED後にGREEN |
| RenCrow_CMD | `feat/rencrow-development-methodology-v1` / `706ba77ea3933f7cb1292fffa3ac4f0eb4d30d8d` | `Tmp/worktrees/cmd-development-methodology-v1` | `go test ./cmd/rencrow` passed |
| RenCrow_EcoSystem | `feat/rencrow-development-methodology-v1` / `0ee3175150b2501e0062934cb6301c8435d50848` | `Tmp/worktrees/ecosystem-development-methodology-v1` | `make check` 104 tests passed |

元workspaceのEcoSystem rootにある無関係な未追跡`Get-NetFirewallRule`、`Get-NetTCPConnection`は
変更、移動、削除していない。

## Existing owner inventory

| Concern | Existing owner / source | Decision |
| --- | --- | --- |
| Atlas、Backlog、Implementation Unit、Concept／Delivery、WIP=1 | CORE backlog domain/applicationとAtlas仕様 | 拡張。再実装禁止 |
| Lease、Queue Freeze、Stage／Closure Receipt、Workstream Artifact | CORE Workstream store | typed artifact payloadを追加して再利用 |
| 7日Maturation／Revalidation | CORE backlog maturation + injected clock | 再利用 |
| Skill Registry／Skill manifest | CORE skillgovernance | development contractを後方互換で拡張 |
| Authority | CORE policy／execution role境界 | Skillから分離したpure gateを追加。Agent/modelをauthorityにしない |
| Event／Trace | CORE orchestrator Event Log | artifact確定事実を既存logへ発行 |
| Viewer | CORE `/viewer` Atlas Pipeline | owner projectionを追加 |
| CLI | RenCrow_CMD `atlas` | read facadeを追加。state machineを持たない |
| EcoSystem pin／deployment | EcoSystem catalog | source commit後にpinと横断checkを行う |

## Confirmed gaps

1. Task固有stateと中央transition table。
2. plan-scoped Specification、Plan、Implementation Authority、Ruling、Evidence、Review、Ledger。
3. Worktree／Baseline／TDD／Independent Review／Root Cause／Conflict／LIVE gate。
4. Skill Cardのcapability／tool／knowledge／authority／I/O／cost／risk／evaluation contract。
5. methodology detailのCORE API、CMD facade、Event、Viewer projection。

## Refuted additions

新しいAtlas、Backlog、Workstream、WIP lease、Delivery enum、Policy Engine、Skill Registry、Event
store、scheduler、物理DBは不要であり追加しない。Aka／Ao／Gin／Kin等を新しいAgent identityとして
固定せず、実装・reviewの割当はexecution roleとruntime capabilityとして扱う。

## Operational acceptance

`source -> CORE owner -> authenticated policy -> Workstream state -> Atlas runtime route -> CMD／Viewer
consumer -> visible task/evidence/result -> Event／receipt`を確認し、さらにbuild artifact、EcoSystem pin、
deploy、restart、process identity、readiness、production smoke、Viewerを確認した場合だけ
`LIVE_VERIFIED`とする。
