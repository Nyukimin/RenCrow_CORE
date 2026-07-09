# Revenue Operating Principles 仕様

## 1. 目的

本仕様は、RenCrow における収益化の基本思想、行動原則、運用フロー、AI エージェントの役割分担を定義する。

RenCrow の大きな使命のひとつは、単に作業を減らすことではなく、ユーザーが 1 人でも収益化に必要な以下の活動を継続的に進められるよう支援することである。

```text
市場調査
商品設計
投稿設計
集客導線
販売導線
顧客ヒアリング
商品改善
プロモーション
収益分析
```

本仕様では、この考え方を **Revenue Operating Principles** と呼ぶ。

RenCrow は、AI を単なる作業代行として扱うのではなく、売れる商品、集客導線、販売導線を設計し、改善し続けるための事業支援システムとして動作する。

重要なのは「AI で作業を減らす」ことだけではなく、「AI エージェントを使って、売れる商品、集客導線、販売導線を 1 人で組み上げる」ことである。

## 2. 位置づけ

本仕様は、RenCrow の低レイヤ機能仕様ではなく、上位の事業行動指針である。

```text
19_DCI_直接コーパス探索仕様
  原文を調べ直す能力

20_Tool_Harness_Contract_Mediation仕様
  ツール呼び出しを安定させる能力

21_AI_Native_Engineering_Workflow仕様
  AI が働く開発環境を整える仕様

22_Revenue_Operating_Principles仕様
  RenCrow が収益化のためにどう動くかを定義する上位指針
```

本仕様は、今後追加される Revenue Engine / Marketing Workflow / Product Workflow の親方針となる。

## 3. 基本思想

RenCrow の収益化支援では、以下を基本思想とする。

```text
作業削減ではなく、事業構築を支援する。
AI に丸投げせず、候補生成と分析を高速化する。
最終判断はユーザーが行う。
無料反応より有料顧客を重視する。
自分の好きなことだけでなく、市場でお金が動いているかを見る。
商品は机上の空想ではなく、有料顧客の声から改善する。
SNS は入口であり、関係構築はクローズド導線で行う。
売上は投稿単体ではなく、導線全体で作る。
```

RenCrow はこの思想を、れんの状況に合わせて以下のように解釈する。

```text
少額、低コストで検証する。
在庫を抱えない。
広告に依存しない。
発信と商品を同時に育てる。
AI で改善回数を増やす。
有料顧客の声を次の商品に変える。
```

## 4. 対象となる収益化領域

RenCrow が支援対象とする主な収益化領域は以下である。

```text
note
Kindle
X 発信
Substack
テンプレート販売
プロンプト集
診断ツール
ワークシート
会員サイト
オンライン教材
AI 活用パッケージ
実装サポート付き教材
コンサルティング
小規模サブスク
```

## 5. RenCrow が優先する商品タイプ

RenCrow では、初期商品を以下の順で優先する。

```text
1. 低単価デジタル商品
2. 実用テンプレート
3. ワークシート
4. 手順書
5. プロンプト集
6. 小型教材
7. 診断、チェックリスト
8. 実装支援付き教材
9. 高単価パッケージ
10. 継続サポート / 会員導線
```

初期段階では、いきなり高単価商品を作るのではなく、少額でも有料顧客を作ることを優先する。

## 6. 禁止事項

収益化を目的とする場合でも、RenCrow は以下を禁止する。

```text
成功保証
根拠のない売上断言
実績の捏造
過度な不安煽り
顧客の弱みにつけ込む販売
購入後に提供できない高額商品の販売
AI 生成物の丸投げ販売
他者コンテンツの盗用
顧客の声の無断利用
誤認を誘う限定表現
医療、金融、法律領域での無資格断定
```

特に以下の表現は禁止する。

```text
誰でも必ず稼げる
半年で必ず 1000 万円
この通りやれば確実
失敗しない
再現性 100%
```

売上目標は計算式として扱うことはできるが、成功保証として扱ってはいけない。

## 7. Revenue Workflow 全体像

RenCrow の収益化ワークフローは以下である。

```text
Market Research
  ↓
Positioning
  ↓
SNS Demand Validation
  ↓
Low-ticket Product
  ↓
Paid Customer Acquisition
  ↓
Closed Community / List Building
  ↓
Customer Voice Collection
  ↓
Product Improvement
  ↓
High-ticket Offer Design
  ↓
Promotion Sequence
  ↓
Sales / Delivery
  ↓
Retention / Upsell / Referral
```

この流れを、RenCrow では **Revenue Loop** と呼ぶ。

## 8. Market Research

### 8.1 目的

市場調査の目的は、自分がやりたいことを探すことではなく、すでにお金が動いている市場の中で、自分が勝てる切り口を探すことである。

確認する観点:

```text
今すでにお金が動いているか
悩みが強いか
高単価に繋がるか
反応が取れているか
弱い競合がいるか
自分の経験や資産と接続できるか
```

### 8.2 調査対象

```text
X
note
Brain
Tips
Instagram
Threads
YouTube
Substack
競合 LP
公式 LINE 導線
販売ページ
レビュー
購入者コメント
```

### 8.3 調査項目

```text
伸びているテーマ
売れている切り口
反応が取れているタイトル
購入されている商品
価格帯
販売導線
購入者の悩み
弱い競合の共通点
自分が入り込めるポジション
```

### 8.4 AI の使い方

AI には答えを丸投げしない。

AI には以下をさせる。

```text
候補を大量に出す
競合を分類する
伸びている投稿を構造分析する
タイトルの型を抽出する
商品導線を分解する
弱い競合を見つける
自分の強みとの接点を出す
```

最終判断はユーザーが行う。

## 9. Positioning

### 9.1 目的

Positioning は、市場の中で自分がどの切り口で入るかを決める工程である。

```text
誰に
どんな悩みに
どんな変化を
どんな手段で
どんな価格帯で
提供するか
```

### 9.2 れん向け候補領域

RenCrow では、れんの既存資産を踏まえ、以下を候補領域として扱う。

```text
AI エージェント活用 x 個人の商品化
ローカル LLM / RenCrow 開発メモ
X / note 発信の AI 活用
AI で仕事と家庭の負担を減らす
BipolarToBalance Kindle / ワークシート
家族向け、当事者向け実用教材
背徳ごはんブランド
AI-native coding workflow
Worker / Coder 設計パッケージ
```

### 9.3 Positioning Output

```markdown
# Positioning Sheet

## Target
誰に届けるか。

## Pain
どんな悩みがあるか。

## Desired Outcome
どんな未来を望んでいるか。

## Offer Angle
どの切り口で提案するか。

## Proof
なぜれんが語れるか。

## First Product
最初に出す低単価商品。

## Future Backend
上位商品候補。
```

## 10. SNS Demand Validation

### 10.1 目的

SNS は、商品を売る前に需要を確認する場所である。

SNS 投稿は日記ではなく、需要に刺さる問題解決として設計する。

### 10.2 投稿テーマ例

```text
AI で何を作れば売れるのか
初心者が最初に作るべき商品
AI で note を売る導線
X から LINE に繋げる方法
AI エージェントで LP を作る方法
高単価商品に繋げる設計
売れない人がやっているミス
RenCrow 開発から見えた AI 運用
ローカル LLM を収益化に使う方法
```

### 10.3 投稿作成フロー

```text
1. 伸びている投稿を集める
2. AI に構造分析させる
3. ネタを 100 個出させる
4. AI に下書きを作らせる
5. ユーザーが言い切りを強くする
6. 具体例を足す
7. 商品導線へ繋げる
8. 反応を記録する
9. 反応が取れた型を再利用する
```

### 10.4 AI 使用原則

```text
AI で楽をするのではなく、改善回数を増やす。
AI の平均点をそのまま出さない。
人間の違和感、怒り、焦り、欲望、悩みの生々しさを補う。
```

## 11. Low-ticket Product

### 11.1 目的

Low-ticket Product は、最初の有料顧客を作るための商品である。

初期段階では、売上額よりも「お金を払ってくれた人がいるか」を重視する。

### 11.2 価格帯

```text
500 円
980 円
1,980 円
2,980 円
5,000 円
```

### 11.3 商品例

```text
初心者が AI エージェントで note を 1 本作る手順
X 投稿を 30 日分作るプロンプト集
売れる note 構成テンプレ
AI で LP を作る完全コピペ集
副業初心者向け商品設計シート
高単価オファー作成ワーク
SNS 発信の需要確認チェックリスト
RenCrow 式 AI 作業分解テンプレ
Worker / Coder 活用チェックリスト
```

### 11.4 商品作成原則

```text
すごい商品より、今すぐ欲しい商品を作る。
完成度より、購入後に改善できる構造を作る。
情報だけでなく、使えるテンプレートや手順を含める。
販売後に顧客の声を回収する。
```

## 12. Closed Relationship Channel

### 12.1 目的

SNS だけで関係を終わらせない。

購入者や見込み顧客をクローズドな場所に集め、継続的に関係を作る。

### 12.2 候補

```text
公式 LINE
メールマガジン
Discord
note メンバーシップ
Substack
会員サイト
購入者限定ページ
Google フォーム
```

### 12.3 クローズド導線で行うこと

```text
購入後アンケート
悩みのヒアリング
追加特典の配布
無料相談の案内
次の商品予告
成功事例の回収
質問への回答
次のニーズ調査
```

## 13. Customer Voice Loop

### 13.1 目的

商品は机の上で考えるのではなく、有料顧客との会話から改善する。

### 13.2 回収する声

```text
ここがわからない
これができない
この場合はどうしたらいいか
もっと実践型が欲しい
直接見てほしい
自分用に作ってほしい
```

### 13.3 顧客の声の扱い

顧客の声は、以下に分類する。

```text
confusion:
  わからなかった点

blocker:
  実行できなかった点

desire:
  欲しい未来

request:
  追加要望

proof:
  成功事例

objection:
  購入前の不安

language:
  顧客自身の言葉
```

### 13.4 顧客の声から作るもの

```text
FAQ
追加特典
追補記事
次の商品
上位商品
販売ページの改善
投稿ネタ
プロモーション内の問題提起
```

## 14. High-ticket Offer

### 14.1 目的

高単価商品は、情報だけでなく、収益化の仕組みを提供する商品として設計する。

情報だけでは安く見られやすい。設計図、テンプレート、実装手順、チェックリスト、プロンプト、具体例、添削、導線、販売ページ、購入後の動きまで含めることで、提供価値を高める。

### 14.2 価格帯目安

```text
10 万円
20 万円
30 万円
50 万円
```

ただし、価格は価値、実績、提供範囲、サポート負荷に応じて決める。

### 14.3 高単価パッケージ候補

```text
AI エージェント活用 商品設計パッケージ
X / note 収益導線構築パッケージ
RenCrow 式 AI 開発環境構築サポート
AI-native coding workflow 導入支援
BipolarToBalance Kindle 出版支援パッケージ
AI を使った個人ブランド商品化支援
```

### 14.4 含める要素

```text
現状診断
アカウント設計
商品設計
note 作成
LP 作成
販売導線
LINE / メルマガ導線
プロンプト一式
会員サイト構成
販売企画
プロモーション設計
添削
実装サポート
```

## 15. Promotion Sequence

### 15.1 目的

プロモーションは、いきなり売り込むものではなく、関係作りの流れとして設計する。

### 15.2 標準プロモーション順序

```text
1. 問題提起する
2. 失敗あるあるを見せる
3. 理想の未来を見せる
4. なぜ今必要かを伝える
5. 具体的な解決策を出す
6. 小さな実績や事例を見せる
7. 無料または低単価で体験させる
8. 購入者の声を出す
9. 高単価商品の必要性を伝える
10. 期限を切って募集する
```

### 15.3 注意事項

```text
煽りすぎない
成功保証しない
顧客の不安を過度に利用しない
期限や人数制限を偽らない
実績を盛らない
販売後の提供範囲を明確にする
```

## 16. Revenue Metrics

### 16.1 基本 KPI

```text
market_research_count
competitor_review_count
post_count
post_engagement_rate
save_rate
profile_click_rate
lead_count
low_ticket_sales_count
paid_customer_count
customer_voice_count
repeat_purchase_count
high_ticket_inquiry_count
conversion_rate
refund_rate
support_load
monthly_revenue
```

### 16.2 重視する指標

RenCrow では、以下を重視する。

```text
有料顧客数
購入後アンケート数
顧客の具体的な悩み
低単価商品の購入率
高単価相談への移行率
継続購入率
紹介数
```

以下は参考指標であり、目的化しない。

```text
インプレッション
フォロワー数
いいね数
単発バズ
```

## 17. Agent Roles

### 17.1 Market Research Agent

```text
責務:
- 市場調査
- 競合分析
- 売れている商品調査
- 伸びている投稿分析
- ポジション候補生成
```

### 17.2 Content Strategy Agent

```text
責務:
- 投稿テーマ設計
- フック案生成
- 投稿構造分析
- 反応分析
- ネタ一覧作成
```

### 17.3 Product Design Agent

```text
責務:
- 低単価商品設計
- テンプレート作成
- ワークシート作成
- 教材構成
- 特典設計
```

### 17.4 Funnel Builder Agent

```text
責務:
- LP 構成
- 購入導線
- 公式 LINE / メルマガ導線
- ステップ配信
- 購入後案内文
```

### 17.5 Customer Voice Agent

```text
責務:
- 購入者アンケート分析
- 悩み分類
- FAQ 化
- 次の商品候補生成
- 販売文への反映
```

### 17.6 Promotion Planner Agent

```text
責務:
- プロモーション日程作成
- 教育投稿設計
- 販売前コンテンツ設計
- 募集文作成
- 締切前リマインド設計
```

### 17.7 Metrics Analyst Agent

```text
責務:
- KPI 集計
- 投稿反応分析
- 売上分析
- 導線ボトルネック発見
- 改善提案
```

## 18. Human Decision Gate

RenCrow は候補を出すが、以下は必ずユーザー判断とする。

```text
市場選定
商品価格
高単価商品の販売開始
顧客への個別案内
実績の掲載
顧客の声の利用
広告出稿
返金対応
医療、金融、法律に関わる表現
```

AI に丸投げしないことは本仕様の中心原則である。

## 19. Daily Revenue Routine

RenCrow は、日次で以下を支援する。

```text
1. 市場を見る
2. 伸びている投稿を見る
3. AI に構造分析させる
4. 投稿案を作る
5. 投稿する
6. 反応を見る
7. 商品を改善する
8. 有料顧客の声を見る
9. 導線を直す
10. 販売文を磨く
```

## 20. 30 日 MVP

初期 30 日では、以下を目標とする。

```text
1. 金になるジャンルを決める
2. 競合 50 人を見る
3. 売れている商品 30 個を見る
4. AI で市場分析する
5. 自分の切り口を決める
6. X / note / Threads のいずれかを開始または強化する
7. 毎日投稿する
8. 反応が取れた投稿を分析する
9. 需要が強いテーマを見つける
10. 500 円から 980 円の商品を作る
11. 初回有料顧客を獲得する
12. 購入者アンケートを取る
```

## 21. DB 設計

### 21.1 market_research_item

```sql
CREATE TABLE IF NOT EXISTS market_research_item (
  item_id TEXT PRIMARY KEY,
  source_platform TEXT NOT NULL,
  source_url TEXT,
  creator_name TEXT,
  theme TEXT,
  product_name TEXT,
  price INTEGER,
  observed_signal TEXT,
  notes TEXT,
  created_at TEXT NOT NULL
);
```

### 21.2 sns_post_metric

```sql
CREATE TABLE IF NOT EXISTS sns_post_metric (
  post_id TEXT PRIMARY KEY,
  platform TEXT NOT NULL,
  posted_at TEXT,
  title TEXT,
  theme TEXT,
  impressions INTEGER,
  likes INTEGER,
  reposts INTEGER,
  comments INTEGER,
  saves INTEGER,
  profile_clicks INTEGER,
  link_clicks INTEGER,
  sales_count INTEGER,
  notes TEXT
);
```

### 21.3 product_catalog

```sql
CREATE TABLE IF NOT EXISTS product_catalog (
  product_id TEXT PRIMARY KEY,
  product_name TEXT NOT NULL,
  product_type TEXT,
  price INTEGER,
  target TEXT,
  pain TEXT,
  promise TEXT,
  deliverables TEXT,
  status TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT
);
```

### 21.4 customer_voice

```sql
CREATE TABLE IF NOT EXISTS customer_voice (
  voice_id TEXT PRIMARY KEY,
  customer_id TEXT,
  product_id TEXT,
  voice_type TEXT,
  raw_text TEXT,
  summary TEXT,
  usable_for_marketing INTEGER DEFAULT 0,
  permission_status TEXT DEFAULT 'unknown',
  created_at TEXT NOT NULL
);
```

### 21.5 revenue_event

```sql
CREATE TABLE IF NOT EXISTS revenue_event (
  event_id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,
  product_id TEXT,
  amount INTEGER,
  channel TEXT,
  customer_id TEXT,
  notes TEXT,
  created_at TEXT NOT NULL
);
```

### 21.6 channel_draft

```sql
CREATE TABLE IF NOT EXISTS channel_draft (
  draft_id TEXT PRIMARY KEY,
  workstream_id TEXT,
  channel TEXT NOT NULL,
  subject TEXT,
  body TEXT NOT NULL,
  source_report_id TEXT,
  approval_status TEXT DEFAULT 'pending',
  external_send_applied INTEGER DEFAULT 0,
  created_at TEXT NOT NULL
);
```

## 22. 設定ファイル案

### 22.1 configs/revenue_operating.yaml

```yaml
revenue:
  mission:
    enabled: true
    principle: "AIで作業を減らすのではなく、商品・集客導線・販売導線を構築する"

  ethics:
    prohibit_success_guarantee: true
    prohibit_fake_results: true
    prohibit_excessive_fear_marketing: true
    require_customer_permission_for_voice: true

  market_research:
    competitor_target_count_30d: 50
    product_target_count_30d: 30

  sns:
    daily_post_required: true
    focus_on_problem_solution: true
    avoid_diary_content: true

  low_ticket:
    enabled: true
    initial_price_min: 500
    initial_price_max: 5000
    priority: "first_paid_customer"

  closed_channel:
    enabled: true
    options:
      - official_line
      - email
      - discord
      - membership
      - customer_page

  human_gate:
    require_user_approval:
      - market_selection
      - product_price
      - high_ticket_offer
      - customer_voice_publication
      - paid_ads
      - refund_policy
```

## 23. EventId

収益化関連の主なイベント種別は以下である。

```text
market_research_started
market_research_completed
positioning_created
sns_post_drafted
sns_post_published
sns_post_analyzed
product_idea_created
product_created
product_published
purchase_recorded
customer_voice_recorded
funnel_created
promotion_started
promotion_completed
high_ticket_offer_created
human_gate_required
```

## 24. MVP 実装順

### Phase 1: Revenue Principle

- 本仕様を RenCrow の上位方針に登録する。
- 禁止事項を Rules に反映する。
- Human Decision Gate を定義する。

### Phase 2: Market Research

- 競合調査テンプレートを作成する。
- 売れている商品調査テンプレートを作成する。
- `market_research_item` DB を作成する。

### Phase 3: SNS Demand Validation

- 投稿ネタを生成する。
- 投稿構造を分析する。
- `sns_post_metric` DB を作成する。
- 反応分析テンプレートを作成する。

### Phase 4: Low-ticket Product

- `product_catalog` DB を作成する。
- 商品設計シートを作成する。
- 販売ページテンプレートを作成する。
- 購入後アンケートを作成する。

### Phase 5: Customer Voice Loop

- `customer_voice` DB を作成する。
- 悩みを分類する。
- FAQ 化する。
- 商品改善を提案する。

### Phase 6: Funnel / Promotion

- 導線を設計する。
- ステップ配信を設計する。
- プロモーション日程を作成する。
- 高単価商品を設計する。

## 24.1 実装状況

2026-05 時点で、Revenue Operating Workflow は最小の記録基盤まで部分実装済みである。

実装済み。

```text
Domain:
  internal/domain/revenue
  market research / SNS post metric / product / customer voice / revenue event / human decision gate record / daily routine report / channel draft

Validation:
  成功保証表現の拒否
  顧客の声をmarketing usableにする場合のpermission確認
  Human Decision Gate の required / needs_review / blocked 判定
  Daily Routine Report は draft_report のみ許可し、外部送信済み状態を拒否
  Channel Draft は external_send_applied=false の下書きのみ許可し、外部送信済み状態を拒否

Persistence:
  internal/infrastructure/persistence/revenue
  JSONL store
  SQLite store
  human_decision_gate 台帳
  daily_routine_report 台帳
  channel_draft 台帳

Config:
  revenue.enabled
  revenue.storage
  revenue.log_path
  revenue.sqlite_path
  revenue.prohibit_success_guarantee
  revenue.require_customer_voice_permission
  revenue.external_publish_requires_approval
  revenue.high_ticket_offer_requires_approval

Viewer / API:
  GET  /viewer/revenue
    includes dashboard summary:
      total_revenue_amount
      paid_customer_count
      paid_event_count
      purchase_count
      usable_voice_count
      pending_decision_count
      latest_daily_report_id
      channel_draft_count
      latest_channel_draft_id
      kpi_trend
      product_sales
      customer_voice_types
  POST /viewer/revenue/market-research
  POST /viewer/revenue/sns-posts
  POST /viewer/revenue/products
  POST /viewer/revenue/customer-voices
  POST /viewer/revenue/events
  POST /viewer/revenue/daily-routine
  POST /viewer/revenue/channel-drafts
  POST /viewer/revenue/human-decision-gate
  POST /viewer/revenue/human-decision-gate/review
  Ops summary card
  total revenue / paid customers
  daily report count
  pending decision count
  channel draft count
  trend days / latest revenue
  top product / top voice type
  Channel Drafts 専用 panel
  Human Decision Gate approve / reject controls

External Client:
  pkg/rencrowclient.EvaluateRevenueHumanDecision
  pkg/rencrowclient.ReviewRevenueHumanDecision
  pkg/rencrowclient.CreateRevenueDailyRoutineReport
```

残作業。

```text
Workstream / RevenueAgent:
  daily routine draft report API はある。
  RevenueAgent 専用 DailyRoutineService はある。
  Revenue系 Workstream Heartbeat から RevenueAgent の Daily Routine Report を draft_report として自動保存する経路はある。
  残る作業は、market research、SNS analysis、customer voice loop の高度化と、Human Decision Gate 承認後の外部送信適用。

Human Decision Gate:
  外部公開、高単価 offer、顧客の声公開、広告、返金などを
  pending / approved / rejected として保存する台帳。
  現状は判定・保存 API、approved / rejected review API、
  Viewer Ops pending count 表示、承認 / 却下 UI までで、
  外部送信や投稿実行には接続していない。

Viewer:
  dashboard summary はある。
  summary 内に KPI推移、customer voice分類、商品別売上、channel draft countを含む。
  Ops summary card は trend days / latest revenue / top product / top voice type / channel drafts を表示する。
  Channel Drafts 専用 panel は draft only / external_send_applied 状態を表示し、外部送信ボタンは持たない。
  Revenue Drilldown panel として KPI推移、商品別売上、customer voice分類、Human Decision Gate のテキストグラフを表示する。

Closed Channel:
  LINE / email / Discord などへの外部送信は下書きまでに制限し、
  Human approval なしに送信しない接続。`/viewer/revenue/channel-drafts` は下書き保存のみで、送信は行わない。
```

## 25. 成功指標

```text
first_paid_customer_acquired
low_ticket_sales_count
customer_voice_count
product_iteration_count
lead_count
closed_channel_join_count
high_ticket_inquiry_count
monthly_revenue
repeat_purchase_count
```

初期段階の最重要指標は以下である。

```text
初回有料顧客
購入者の声
商品改善回数
低単価商品の販売数
```

## 26. 設計上の結論

RenCrow における収益化支援は、単なる作業効率化ではない。

目的は、ユーザーが 1 人でも以下を回せるようにすることである。

```text
市場を見る
需要を確認する
商品を作る
投稿する
販売導線を作る
有料顧客の声を集める
商品を改善する
上位商品を作る
プロモーションする
```

AI エージェントは、ユーザーの代わりに判断する存在ではない。

AI エージェントは、候補生成、分析、構成、下書き、改善、記録を高速化する存在である。最終判断はユーザーが行う。

この仕様により、RenCrow は「便利な AI」ではなく、「収益化を支える AI 事業運用システム」として動作する。

## 27. まとめ

本仕様は、RenCrow の収益化行動原則を定義する。

カテゴリは以下である。

```text
Revenue
Business Operations
Market Research
Product Design
SNS Strategy
Customer Voice
Promotion
Human Decision Gate
Ethics
```

RenCrow では、お金を稼ぐことを重要な使命として扱う。

ただし、短期的な煽りや誇大広告ではなく、以下を重視する。

```text
市場
需要
有料顧客
顧客の声
改善回数
導線
信頼
継続性
```

この方針を、今後の Revenue Engine 実装仕様の上位原則とする。
