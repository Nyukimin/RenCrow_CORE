# RenCrow ATLAS Backfill Index v1

Generated: 2026-08-21T22:26:00+09:00

## Agent社会

- **Mio / Chat** (`agent.mio_chat`) [repo_spec]
  - 目的: 利用者との通常会話の入口と、回答統合・通常入力の交通整理を一つの安定したAgent Identityへ持たせる。
- **Shiro / Worker** (`agent.shiro_worker`) [repo_spec]
  - 目的: 設計提案と副作用実行を分離し、実際のTool・command・test・適用を責任を持って実行する。
- **Kuro / Heavy** (`agent.kuro_heavy`) [repo_spec]
  - 目的: 深い分析・設計・高難度技術作業を、Kuroという人格を維持したままHeavy Execution Roleへ接続する。
- **Midori / Wild** (`agent.midori_wild`) [repo_spec]
  - 目的: 創作・視覚・横方向探索を担当する人格を、Wild Execution RoleとVision/Image能力へ接続する。
- **Aka / Ao / Gin / Kin** (`agent.coder_pool`) [repo_spec]
  - 目的: 設計・実装・レビュー・反証など異なる強みを持つCoderを、提案主体として複数使い分ける。
- **Agent人数無制限設計** (`agent.unbounded`) [direct_spec]
  - 目的: Agent数を4人や8人などコード上の固定数に縛らず、将来のAgent追加を構造変更なしで可能にする。
- **Agent間 delegate / ack / report** (`agent.delegate_lifecycle`) [repo_spec]
  - 目的: Agent間の仕事移譲を口頭会話だけでなく、追跡・再送・完了確認可能なlifecycleにする。
- **IdleChat / 自走会話** (`agent.idlechat`) [repo_spec]
  - 目的: ユーザーからの明示入力がない時間にも、キャラクターが話題を持ち、会話・物語・ニュースを自律的に提示できるようにする。
- **上位協議 Mio / Shiro / Kuro / Midori** (`agent.upper_council`) [direct_chat]
  - 目的: 4人格を固定上下関係なしの対等な協議主体として使い、単一Agentでは見落とす論点を補完する。
- **Shiro → Coder Coordinator関係** (`agent.shiro_coder_coordination`) [direct_chat]
  - 目的: 実装作業ではShiroを実行Coordinatorにし、Coderの提案を一箇所で統合・検証する。
- **Character Runtime / sequential turns** (`agent.character_runtime`) [repo_spec]
  - 目的: 複数キャラクターの発話を安定したmessage identityと順序で実行・表示する。
- **User Interrupt / 会話割り込み** (`agent.interrupt`) [direct_spec]
  - 目的: Agent処理中でもユーザーの新入力を会話状態へ安全に反映できるようにする。

## Atlas

- **Atlas / Backlog / Implementation Lifecycle** (`atlas.lifecycle`) [direct_spec]
  - 目的: RenCrowのアイデア、採用判断、仕様、TDD、E2E、Build、Deploy、Live Verificationを一つの開発ライフサイクルとして追跡し、採用済み項目を1件ずつ完成まで消化する。
- **ATLAS Idea Design Card / Specification Capture** (`atlas.idea_record`) [direct_spec]
  - 目的: ATLASの各Itemをワードだけでなく、何のため・何が問題・何をする・背景・期待効果・元仕様まで復元可能な設計記憶として保存する。
- **ATLAS Initial Backfill** (`atlas.backfill`) [direct_chat]
  - 目的: 現在までのATLAS全Itemを元Chat・既存仕様・GitHubへ遡り、Purpose/Problem/Idea/BackgroundとSpecification Artifactを初期datasetとして復元する。
- **ATLAS Backfill Automation** (`atlas.backfill_automation`) [direct_spec]
  - 目的: 今後RenCrow自身が新しいChat・仕様・GitHub変更から設計意図と仕様Artifactを抽出し、Atlas Candidateを継続更新できるようにする。

## EcoSystem

- **EcoSystem source-pinned catalog** (`ecosystem.catalog`) [repo_spec]
  - 目的: 独立したRenCrow各repositoryを、一つの製品として再現可能な組み合わせに固定する。
- **Deployment drift / redeploy verification** (`ecosystem.deployment_drift`) [repo_spec]
  - 目的: source pinと実際に配置されたbinaryが一致しているか検証し、古いbinaryを長期間動かし続ける事故を防ぐ。
- **GAMES** (`ecosystem.games`) [repo_spec]
  - 目的: Agentが会話以外の世界で観察・判断・行動・失敗を経験し、その結果を経験としてRenCrowへ戻す実験環境を提供する。
- **TRADE** (`ecosystem.trade`) [repo_spec]
  - 目的: 金融知識・戦略を安全に研究・Replay・Simulator・Shadow評価し、将来の金融操作境界を独立moduleとして育てる。
- **ASSISTANT** (`ecosystem.assistant`) [repo_spec]
  - 目的: 予定・天気・交通・ニュース・目覚まし等の生活Routineを常時監視し、個人/家族へPUSHするproactive serviceを提供する。

## LLM基盤

- **LLM Gateway / Runtime** (`llm.gateway_runtime`) [repo_spec]
  - 目的: COREから物理LLM backend/model差を切り離し、Execution Role単位で交換可能な推論基盤を提供する。
- **Agent / Role / Model分離** (`llm.role_model_separation`) [direct_spec]
  - 目的: 人格・責務・実行方法・物理modelを別概念として扱い、modelを交換してもAgent Identityと経験を維持する。
- **Backend交換** (`llm.backend_swap`) [repo_spec]
  - 目的: 同じExecution Roleから、llama.cpp、MLX、Ollama、provider runtime等を環境に応じて交換できるようにする。
- **KV Cache最適化** (`llm.kv_cache`) [direct_chat]
  - 目的: 長いSystemPrompt/Stable Contextを毎回再計算せず、安定prefixを再利用して推論コストと待ち時間を減らす。
- **Model / Inference Provider Router** (`llm.model_router`) [repo_spec]
  - 目的: task、価格、latency、privacy、local/cloud、capabilityに応じて推論先を交換可能にする。
- **SystemPrompt Static Prefix / Dynamic Area** (`llm.prompt_structure`) [direct_chat]
  - 目的: 人格・Policyなど安定情報と、Memory/route/timeなど毎回変わる情報を分離し、責務とcache効率を高める。
- **Knowledge/PersonaをLoRA・FT正本にしない** (`llm.no_ft_baseline`) [direct_chat]
  - 目的: 知識・人格・経験を交換可能な外部データとして保持し、LoRA/FTを正本にしない。
- **Memory Substrate Router** (`llm.memory_substrate_router`) [project_summary]
  - 目的: taskや履歴長に応じて、Legacy L0、Candidate L0、vector、lexical、raw corpus等のMemory方式を切り替えられるようにする。

## UI・身体

- **PORTAL** (`ui.portal`) [repo_spec]
  - 目的: RenCrowのChat/IdleChat/Gamesを外部利用者向けWeb画面として提供し、Debug/Ops UIとは分離する。
- **PuruPuru Avatar / 表情** (`ui.avatar`) [repo_spec]
  - 目的: Mio/Shiro/Kuro/Midoriを静止テキストではなく、表情・呼吸・瞬き・髪揺れを持つキャラクターとして表示する。
- **TTS + 実音声口パク** (`ui.tts_lipsync`) [repo_spec]
  - 目的: Agentの文章をキャラクター音声として再生し、その実音声に同期して口を動かす。
- **STT** (`ui.stt`) [repo_spec]
  - 目的: 利用者の音声入力をCOREの通常Chat入力へ変換する。
- **Vision** (`ui.vision`) [repo_spec]
  - 目的: 画像・動画をAgentが直接raw処理せず、共通Vision Gatewayで検証・正規化してから解釈できるようにする。
- **Image Generation** (`ui.image_generation`) [repo_spec]
  - 目的: Agentの描画要求をbackend固有設定から切り離し、共通HTTP contractで画像生成できるようにする。
- **Debug Viewer / Observability Console** (`ui.viewer_debug`) [repo_spec]
  - 目的: RenCrow内部のAgent、Job、Memory、Tool、Prompt、イベントを一箇所で観測・診断できるようにする。
- **Memory / Recall Inspector** (`ui.memory_inspector`) [project_summary]
  - 目的: 現在の応答でL0-L3、UserProfile、Knowledgeの何が選ばれ、なぜ選ばれたかViewerで見えるようにする。
- **Shared Artifact / Discussion Board** (`ui.artifact_board`) [direct_chat]
  - 目的: 協議中の論点、Evidence、決定、未解決事項を会話ログとは別にViewerで一覧できるようにする。

## 協議・判断

- **Router** (`decision.router`) [repo_spec]
  - 目的: 入力内容に応じて適切なAgent/Execution Roleへ処理を振り分ける。
- **Registry駆動 routing** (`agent.registry_routing`) [direct_chat]
  - 目的: Agent追加時にRouterコードを書き換えず、Contract/Capability/Restrictionから候補を選べるようにする。
- **固定 Facilitator** (`decision.facilitator`) [direct_chat]
  - 目的: 上位協議の進行手続きを安定させ、論点整理・発言制御・記録・停滞解消を誰かの人格権力にしない。
- **協議ごとの Chair / 主導役** (`decision.chair`) [direct_chat]
  - 目的: 議題ごとに適切なAgentが内容面を主導しつつ、恒久的な上下関係を作らない。
- **Blind Forecast / Calibration** (`decision.blind_calibration`) [direct_chat]
  - 目的: Agent同士が互いの回答に引っ張られる前に独立予測を取り、過去精度で確率を校正して集合判断へ使う。
- **Prediction Market / LMSR** (`decision.prediction_market`) [direct_chat]
  - 目的: 複数Agentの確率判断を市場型集約でも評価し、単純な投票より高品質なDecision Supportが可能か検証する。
- **HASSUM / 意味的不確実性** (`decision.hassum`) [project_summary]
  - 目的: Multi-Agent協議の中間回答について、意味的不確実性を測り、再質問や再検討を必要な箇所だけ起動できるか評価する。
- **Decision Runtime** (`decision.runtime`) [direct_chat]
  - 目的: 複数の協議方式を一つの固定会話フローにせず、taskに応じて選べるDecision Support層として扱う。
- **Shared Artifact / 共有ファイル協調** (`decision.shared_artifact`) [direct_chat]
  - 目的: Agent間で会話全文を繰り返す代わりに、論点・証拠・暫定結論・未解決事項を共通artifactとして共有する。
- **Committee / Sharding** (`decision.committee`) [project_summary]
  - 目的: Agent数が増えても全員を毎回発言させず、taskに必要な小委員会へ分割して協議を収束させる。

## 安全・統治

- **Policy Decision / Safety Gate** (`safety.policy_gate`) [repo_spec]
  - 目的: Agentの提案と実際のside effectの間に、モデル非依存の同期Policy判断を置く。
- **Fail Closed** (`safety.fail_closed`) [repo_spec]
  - 目的: 必要なsource、mount、scope、provider、validationが欠けた時に、別経路で成功したふりをせず明示失敗する。
- **Prompt/Memory汚染対策** (`safety.prompt_memory_contamination`) [repo_spec]
  - 目的: 貼付資料や外部コンテンツをユーザー本人の事実・命令として誤解しないようにする。
- **Durable Store Gate** (`storage.durable_gate`) [direct_spec]
  - 目的: 新しい永続DBや保存方式を無秩序に増やさず、既存store再利用・owner・backup・recoveryを検証してから追加する。
- **No-Human-Gate** (`safety.no_human_gate`) [repo_spec]
  - 目的: RenCrow runtime内部で人の判断待ち状態を作らず、確定済みPolicyによりexecute/rejected/blockedを同期確定する。
- **Trust Boundary Tagging** (`safety.trust_boundary`) [project_summary]
  - 目的: 外部Web、Tool result、Memory、Agent発話など入力sourceごとの信頼境界を明示する。
- **Prompt Injection Guard** (`safety.prompt_injection`) [project_summary]
  - 目的: 外部コンテンツに含まれる命令文をAgentやMemoryへそのまま通さず、risk分類とsanitizationを行う。
- **Action Gate / Sandbox** (`safety.action_sandbox`) [repo_spec]
  - 目的: LLMが生成したactionをそのまま実行せず、権限・scope・sandboxをモデル外で制御する。
- **Fake Consensus Prevention** (`safety.fake_consensus`) [direct_chat]
  - 目的: 同一modelや同一推論経路を複数Agent名で繰り返しただけの票を、独立した集合知として水増ししない。
- **Conceptual Integrity / そらあかんやろ Guardrail** (`safety.conceptual_integrity`) [project_summary]
  - 目的: Coding Agentが高速に機能を増やしても、RenCrow全体の責務・命名・owner・重複排除を一貫させる。

## 実行・Tool

- **ToolLoop / ToolHarness** (`tool.harness`) [repo_spec]
  - 目的: AgentがToolを呼ぶときのschema validation、repair、policy、実行、結果正規化を共通境界へ集約する。
- **SQLite Tool Registry** (`tool.registry`) [repo_spec]
  - 目的: 現在利用可能なToolを明示的に登録・一覧化し、任意file探索なしでWorkerへ提供する。
- **Runtime Capability Snapshot** (`tool.capability_snapshot`) [repo_spec]
  - 目的: 全Agentが現在使えるTool/Skill/MCPを同じStable RuntimeContextから認識できるようにする。
- **Capability Apply / self-restart** (`tool.capability_apply`) [repo_spec]
  - 目的: 新しいTool/Skill/MCP追加を、手作業再起動ではなく検証付きrevisionとして全AgentのStable Contextへ反映する。
- **Scheduler / Heartbeat** (`tool.scheduler`) [repo_spec]
  - 目的: 時間経過や定期条件で、RenCrowの継続作業・補充・監視を自律的に進める。
- **Repair / Resilience** (`tool.repair_resilience`) [repo_spec]
  - 目的: COREのpanic・異常終了・ハングを証拠付きで回収し、診断・修正・再配置・再発監視まで閉じる。
- **Worker / Coder Separation** (`tool.worker_coder_boundary`) [direct_chat]
  - 目的: Coderは考えて提案し、Workerだけが副作用を実行するという不変条件を全実装で守る。
- **Repair Job** (`tool.repair_job`) [repo_spec]
  - 目的: 障害修復を一時的な手作業ではなく、proposal→apply→verifyの追跡可能なJobにする。
- **Read-only Research Router** (`tool.web_research`) [direct_spec]
  - 目的: GitHub/Web/RSS/YouTube等の外部調査を、side effectを持たない証拠取得経路として統一する。
- **Skill / MCP Governance** (`tool.skill_mcp`) [repo_spec]
  - 目的: Tool以外のSkillやMCPも、version・permission・availabilityを把握して安全にAgentへ提供する。

## 成長・自律

- **経験→Reflection→次の行動** (`growth.experience_loop`) [direct_chat]
  - 目的: RenCrowの『賢さ』を知識量ではなく、経験した結果によって次の判断・行動が変わる能力として実装する。
- **人工的モチベーション / Drive** (`growth.drive`) [direct_chat]
  - 目的: 過去経験から形成された持続的な内部状態が、ユーザーが毎回同じ指示を出さなくても将来のGoal/Action Selectionを変えるようにする。
- **経験による人格分岐** (`growth.persona_divergence`) [direct_chat]
  - 目的: 同じ初期Character Promptから始まっても、各Agentが経験した出来事の違いによって判断傾向・関心・行動が分岐するようにする。

## 知識獲得

- **Knowledge Relation** (`knowledge.relation`) [repo_spec]
  - 目的: 単一item検索だけでなく、関連人物・作品・概念を限定的に辿れるKnowledge関係を提供する。
- **Knowledge Memory / Common Raw** (`knowledge.memory`) [direct_chat]
  - 目的: RenCrow自身が利用する知識をLLMパラメータではなく、provenance付き外部Knowledgeとして保持する。
- **映画カタログ** (`knowledge.movie_catalog`) [repo_spec]
  - 目的: RenCrowが映画について会話・評価・関連作品想起を行うための構造化domain knowledgeを持つ。
- **人物関連カタログ** (`knowledge.person_catalog`) [repo_spec]
  - 目的: 人物を起点に出演作・受賞・書誌など関連作品を検証可能な形で検索できるようにする。
- **X Bookmark intake** (`knowledge.x_bookmark`) [repo_spec]
  - 目的: ユーザーが蓄積したX Bookmarkを、消えるブラウザ履歴ではなくRenCrowの検討可能なKnowledge候補へ移す。
- **News DB / 日次ニュース** (`knowledge.news_db`) [repo_spec]
  - 目的: 最新情報を会話・IdleChat・朝刊へ安定供給し、同じ記事を何度も取り直さずprovenance付きで保持する。
- **DailyNewsBrief** (`knowledge.daily_brief`) [repo_spec]
  - 目的: 朝に必要なニュースを準備済みの形で提示し、DB miss時のみlive検索へ降りる。
- **News collect / analyze分離** (`knowledge.news_split`) [repo_spec]
  - 目的: ニュース取得の成否と、LLMによる考察の成否を別artifact・別失敗境界にする。
- **Registry-first / Search-last** (`knowledge.registry_search`) [direct_spec]
  - 目的: 既知source・ローカルKnowledgeを優先し、Web検索を本当に必要な場合だけ使う。
- **自律的な興味探索** (`knowledge.autonomous_research`) [direct_chat]
  - 目的: ユーザーとの会話で与えられた知識だけに依存せず、RenCrowが興味領域・知識gapを自分で掘り、Knowledgeを増やす。
- **Multi-domain Knowledge Base** (`knowledge.multidomain`) [direct_spec]
  - 目的: 映画だけでなく小説、TV、漫画、音楽、舞台、ボードゲーム等を横断して会話できる知識基盤を作る。
- **Co-occurrence / PMI** (`knowledge.cooccurrence`) [direct_spec]
  - 目的: ユーザーの会話や作品集合から、意味embeddingとは別の『一緒に語られやすい』関連性を学ぶ。
- **Source Registry** (`knowledge.source_registry`) [repo_spec]
  - 目的: 外部情報源の種類、信頼度、取得方法、freshness、利用可否を一箇所で管理する。
- **Staging → Validator → Promotion** (`knowledge.staging_validator`) [direct_spec]
  - 目的: 収集した外部情報を、取得しただけで正規Knowledgeにせず、検証・review後に昇格させる。
- **Citation / Provenance Ledger** (`knowledge.citation_provenance`) [direct_spec]
  - 目的: RenCrowが使った事実について、どこから取得し、いつ検証し、どのartifactへ変換したか追えるようにする。

## 記憶・想起

- **L0 現行会話** (`memory.l0_legacy`) [repo_spec]
  - 目的: 現在のthread内で必要な直近文脈・状態を保持し、自然な連続会話を成立させる。
- **L0v2 Shadow Recall** (`memory.l0v2`) [direct_spec]
  - 目的: 現行L0を壊さず、長くなった会話から関連Turnを検索し、その前後文脈を必要な時だけ復元する新方式を比較する。
- **L1 / UserMemory candidate** (`memory.l1_user`) [repo_spec]
  - 目的: 会話からユーザー固有の長期的に有用な事実・嗜好・履歴を候補化し、検証済み状態として次回以降へ利用する。
- **L2 / Digest** (`memory.l2_digest`) [repo_spec]
  - 目的: 大量の会話・episodeをそのまま毎回読むのではなく、検索や想起に使える中間圧縮・digestを保持する。
- **L3 / Long-term** (`memory.l3_longterm`) [repo_spec]
  - 目的: threadを越えた長期の会話経験を、モデル交換や再起動後も検索・想起できる形で保持する。
- **Vector Memory** (`memory.vector`) [repo_spec]
  - 目的: 語句一致だけでは拾えない過去会話を意味的類似性で検索する。
- **RecallPack** (`memory.recallpack`) [repo_spec]
  - 目的: 複数Memory/Knowledge sourceから、現在のtaskに必要な情報だけをモデルへ渡す共通入力形式を作る。
- **Memory Verification Tool** (`memory.verification`) [direct_spec]
  - 目的: Memory方式の改善・悪化を同一条件で再現可能に比較し、失敗箇所をRetrieval/RecallPack/Answerに分解する。
- **Memory Candidate Gate** (`memory.candidate_gate`) [repo_spec]
  - 目的: 会話や外部資料から抽出した情報を、そのまま長期記憶にせず、証拠・scope・validationを通して昇格させる。
- **忘却 / pin / supersede / lifecycle** (`memory.lifecycle`) [repo_spec]
  - 目的: 長期Memoryを追加し続けるだけでなく、固定・忘却・置換・archiveを明示的に管理する。
- **Every-turn Recall** (`memory.every_turn_recall`) [direct_spec]
  - 目的: 各ターンで必要な過去経験・UserMemory・Knowledgeを必ず検索候補にし、記憶を『保存しただけ』にしない。
- **Associative Recall / 連想想起** (`memory.associative_recall`) [direct_spec]
  - 目的: ユーザーが作品名や話題を出したとき、意味類似だけでなく関連作品・過去会話・共起から自然な連想を返す。
- **Character Memory** (`memory.character`) [direct_spec]
  - 目的: ユーザー共通知識とは別に、各Agent自身が経験したこと・関係・学習を保持する。
- **Local-first Memory OS** (`memory.local_first_os`) [direct_spec]
  - 目的: LLMを交換可能な推論器として扱い、Memory/Knowledgeの正本をRenCrow側へ置く。
- **SQLite Hot Store** (`memory.sqlite_hot`) [repo_spec]
  - 目的: 会話・workflow・registryなど低遅延な永続状態を、標準Go runtimeで扱いやすいembedded storeへ置く。
- **DuckDB / Parquet Cold Projection** (`memory.duckdb_parquet`) [direct_spec]
  - 目的: 大量・長期データを分析・archive・持ち運びしやすい列指向形式へ派生保存する。
- **FTS / BM25 Retrieval** (`memory.fts_bm25`) [direct_spec]
  - 目的: 固有名詞・コード・短い語句などembeddingよりlexical searchが強い検索を補完する。
- **DCI / Direct Corpus Interaction** (`memory.dci`) [direct_spec]
  - 目的: 高精度が必要な時に、要約・embeddingだけでなく許可済みraw corpusを直接検索してEvidenceを取る。

## 評価・観測

- **message_id / trace_id** (`eval.trace_ids`) [direct_spec]
  - 目的: 会話、Agent処理、Tool、TTS、Jobを同じ処理系として後から追跡できる相関IDを持たせる。
- **Typed partial outcome** (`eval.partial_outcome`) [repo_spec]
  - 目的: 複数follower/side effectの一部だけ失敗した状態を、成功にも全体失敗にも丸めず保持する。
- **Shadow / Replay** (`eval.shadow_replay`) [direct_spec]
  - 目的: 新しい判断方式・Memory方式をproduction正系へ影響させず、同じ入力で並走比較する。
- **Memory Regression Benchmark** (`eval.memory_benchmark`) [direct_spec]
  - 目的: Memoryの検索精度・長距離理解・競合解決などを継続的に測り、parameter変更による回帰を検知する。
- **Live Trace / Session Replay UI** (`eval.trace_ui`) [project_summary]
  - 目的: 1ユーザー入力からAgent、Tool、Memory、LLM、TTSまでの処理をtimelineとして後追いする。
- **EventId / Operation Correlation** (`eval.event_id`) [direct_spec]
  - 目的: 会話以外のKnowledge、Workstream、Backlog、Tool、deployment等も共通相関IDで追跡する。
- **OpenTelemetry Export** (`eval.otel`) [project_summary]
  - 目的: RenCrow内部traceを標準telemetry形式へexportし、外部観測基盤でも分析できるようにする。
- **Outcome KPI** (`eval.outcome_kpi`) [project_summary]
  - 目的: RenCrowの性能をtoken/secだけでなく、仕事がどれだけ正しく早く完了したかで評価する。
- **Staleness / Quarantine / Audit** (`eval.staleness_quarantine`) [repo_spec]
  - 目的: Knowledge、Memory、Source、Backlog artifactについて古さ・未検証・隔離状態を明示する。
- **Terminal Outcome Contract** (`eval.terminal_outcome`) [repo_spec]
  - 目的: Worker、Coder、Repair、Backlog等のJobをok/failed/blocked/cancelled等の明示終端で閉じる。
