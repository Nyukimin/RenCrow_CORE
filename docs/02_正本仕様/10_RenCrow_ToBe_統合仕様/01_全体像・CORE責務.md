# To-Be: 全体像・CORE責務

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: 目的、前提、全体像、CORE責務、機能台帳

## 1. 目的

本仕様は、RenCrow が到達すべき全体像と、現行 `RenCrow_CORE` が満たすべき責務、契約、不変条件を定義する。

対象は次の4領域である。

```text
1. RenCrow CORE 全体責務
2. 稼ぐための Economic Objective / Revenue Loop
3. Codex などの外部エージェントを Advisor として扱う設計
4. Knowledge / Memory / Relation / Recall の軽量横断想起
```

本仕様は、実装判断に必要な責務境界、domain model、DB schema、API 契約を定義する。実装 Phase、テスト、移行、受入条件は `11_RenCrow_ToBe_統合実装仕様.md` を正とする。

## 2. 前提

### 2.1 既存正本

本仕様は以下を前提にする。

- `docs/refs/10_新仕様/04_Chat_Worker_Coder仕様.md`
- `docs/refs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- `docs/refs/10_新仕様/13_実装項目インベントリ.md`
- `docs/refs/10_新仕様/18_知識記憶システム構想.md`
- `docs/refs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md`
- `docs/refs/10_新仕様/22_Revenue_Operating_Principles仕様.md`
- `docs/refs/10_新仕様/23_Workstream_Operating_Loop仕様.md`
- `docs/refs/10_新仕様/24_Agent_Skill_Governance仕様.md`
- `docs/refs/10_新仕様/29_Sandbox_Promotion_Gate仕様.md`
- `docs/refs/10_新仕様/90_Runtime_Topology_Config仕様.md`

### 2.2 実装上の前提

- CORE は制御面と状態管理面を持つ runtime である。
- LLM / TTS / STT / Vision / Game 本体は CORE に抱え込まない。
- Coder は `plan` / `patch` / `proposal` を生成する。
- Worker は実行、適用、安全確認、ログ、検証を担当する。
- 外部検索結果、Advisor 出力、Revenue 候補は無審査で確定知識にしない。
- 公開、送信、契約、請求、価格決定、個人情報利用は Human approval 必須とする。

## 3. To-Be 全体像

RenCrow は次の三層で整理する。

```text
RenCrow
├─ Agents
│  ├─ Mio
│  ├─ Shiro
│  ├─ Aka
│  ├─ Ao
│  ├─ Gin
│  ├─ Kin
│  ├─ Kuro
│  └─ Midori
│
├─ Advisors
│  ├─ Codex
│  ├─ Claude Code
│  ├─ Gemini CLI
│  ├─ Cursor Agent
│  └─ Local Specialist
│
└─ Tools
   ├─ Git
   ├─ Shell
   ├─ Browser
   ├─ Web Gather
   ├─ File
   ├─ Image
   └─ Runtime module servers
```

### 3.1 Agent

Agent は RenCrow 内部の主体である。

Agent は次を持つ。

```text
Agent
├─ Role
├─ Capability
├─ Goal
├─ Motivation
├─ Autonomy
├─ Economic Objective
├─ Utility
├─ Trust
├─ Reputation
├─ Memory
└─ Knowledge Affinity
```

Agent は意思決定に関与できる。ただし権限は `Autonomy Envelope` により制限される。

### 3.2 Advisor

Advisor は外部専門家である。

Advisor ができること:

- 調査
- 設計案
- patch 案
- テスト案
- risk 指摘
- alternative 比較

Advisor がしてはいけないこと:

- 最終判断
- 直接実行
- Memory 直接更新
- Revenue 確定
- 外部公開
- git push
- production DB write
- approval / sandbox bypass

### 3.3 Tool

Tool は手段である。

Tool は意思を持たない。Tool 呼び出しは Worker、Tool Harness、Command Gate、Sandbox Guard の管理下に置く。

## 4. CORE の責務

### 4.1 CORE に含めるもの

```text
Chat runtime
Agent selection / delegation
Worker execution control
Coder plan / patch proposal control
Advisor request / result control
LLM / TTS / STT / Vision module server connection
Viewer / Channel API
Memory lifecycle
Knowledge registration / recall
Source Registry / staging / validation
Revenue candidate / approval / delivery state
Scheduler / Heartbeat / Ops
Tool Harness / Sandbox / Governance
Logs / Health / Runtime topology
```

### 4.2 CORE に含めないもの

```text
LLM 推論本体
TTS 生成本体
STT 推論本体
Vision 推論本体
Game 世界本体
大量 Knowledge data 本体
X Bookmark 収集データ本体
映画 / 音楽 / 小説などの大規模外部DB本体
```

CORE はデータを抱える製品ではなく、データを管理し、探し、Agent へ渡す runtime とする。

## 5. 機能台帳

### 5.1 状態区分

各機能は次の状態で管理する。

| 状態 | 意味 |
| --- | --- |
| `canonical` | 責務、境界、不変条件が確定済み |
| `contracted` | `modules/*` などで公開契約化済み |
| `implemented` | production code に実装済み |
| `facade_only` | feature 入口や facade はあるが本体は legacy-body 側 |
| `legacy_body` | 現役実装だが新しい module 境界へ未整理 |
| `concept` | 構想または仕様のみ |
| `out_of_scope` | 現時点ではCORE本線対象外 |

### 5.2 追加する台帳単位

最初に次の単位で台帳化する。

| Feature | 初期状態 | 備考 |
| --- | --- | --- |
| `core` | `contracted` | 既存 modules/core |
| `chat` | `contracted` | 既存 modules/chat |
| `worker` | `contracted` | 既存 modules/worker |
| `llm` | `contracted` | module server 接続 |
| `tts` | `contracted` | module server 接続 |
| `stt` | `contracted` | module server 接続 |
| `browseractor` | `contracted` | browser 操作契約 |
| `webgather` | `contracted` | discovery/fetch/extract |
| `memory` | `implemented` | contract 化途中 |
| `knowledge` | `implemented` | relation layer は未実装 |
| `source` | `implemented` | Source Registry |
| `revenue` | `implemented` | Economic Objective は未整理 |
| `workstream` | `implemented` | revenue/economic と接続予定 |
| `advisor` | `concept` | Codex.run 既存 tool を上位化 |
| `agent_profile` | `concept` | Goal / Utility / Autonomy |
| `knowledge_relation` | `concept` | item_relations 追加 |
| `economic_objective` | `concept` | Opportunity loop |

### 5.3 責務の配置先

最小構成は Markdown 台帳から開始する。

```text
docs/02_正本仕様/12_CORE_機能台帳.md
```

runtime から参照する段階になったら次を追加する。

```text
internal/domain/featureledger
internal/application/featureledger
internal/infrastructure/persistence/featureledger
internal/features/core
```

DB 化は Phase 2 以降でよい。最初から DB を増やさない。
