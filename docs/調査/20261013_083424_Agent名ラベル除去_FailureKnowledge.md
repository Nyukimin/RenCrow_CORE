# Agent応答の先頭ラベル除去: 全Agent共通の正規化と Failure Knowledge

## 概要

Agentの応答の頭に、`[midori] `、重なった`[midori] [midori] `のようなAgent名ラベルが付き、表示本文・stream delta・
保存される会話履歴・TTSの読み上げ文に残っていた。ラベルを取り除く処理はMioの「自分の名前だけ」にしかなく、
Midori／Kuro／Shiroには無かった。全Agent共通の1つの規則（先頭にある既知Agent名の角括弧ラベルの連続を除去）に
まとめ、非streamの応答、streamのdelta、保存する履歴、読み上げ文のすべてが同じ無ラベルの本文から作られるようにした。

## 発生条件

- 履歴のAgent発言は、`conversation.RecallPack.ToPromptMessages`が`[speaker] msg`の形でpromptへ入れる（形式は変更しない）。
  modelがこの形を真似て、応答の頭にラベルを付ける。
- 同じsessionで別Agentの後に応答するAgentは、自分以外の名前のラベルを真似る。

## 事実（本番の観測）

- Midoriの応答は`[midori] `で始まる。
- Shiroの応答は、同じsessionでMidoriの後に送ると`[midori] [midori] `で始まる（自分以外の名前、かつ重なる）。
- Mioの応答には付かない（Mioには自分の名前を除く処理がある。Shiro CHATもMioAgent.Chatで処理されるが、
  除くのは話者自身の名前だけなので、`[midori]`は残った）。
- ラベルはLLMのstream deltaに`[mid`／`ori`／`]`のように分割されて流れる。
- TTSの読み上げ文にも`midori」`のような形で残り、声で読まれていた。
- 実Agentと`ProcessMessage`を通す再現test（`speaker_label_invariant_test.go`）は、修正前のコードで
  `TTS speech text = "midori] [midori] ..."`となり、本番の観測と一致した。

## 結論と修正

不変条件: どのAgentの応答も、先頭にAgent名ラベルを持たない。

- 正本は`internal/domain/conversation/speaker_label.go`。履歴の`[speaker] msg`を作る`recall_pack.go`と同じ
  packageが、その逆変換（`StripLeadingSpeakerLabels`）とstream用（`NewSpeakerLabelStreamFilter`）を持つ。
- 除去するのは、応答の先頭にある既知Agent名（`mio`／`shiro`／`kuro`／`midori`／`aka`／`ao`／`kin`／`gin`。
  応答Agent自身以外の名前も含む）の`[name]`の連続。大文字小文字は無視する。ヘルパは応答を生成したAgent
  （speaker）を受け取り、各Agentは自分のspeakerを渡す。先頭から順に、ラベルごとに次のとおり判定する。
  - **生成したAgent自身の名前のラベル**: 区切りが無くても除去する。直後の空白・コロンも除去する
    （`[mio]こんにちは`は`こんにちは`）。旧Mio（8/11 commit 4afbd194）と同じ挙動を全Agentへ適用する。
  - **他の既知Agent名のラベル**: 直後に区切り（半角空白・全角空白・タブ・改行・コロン（半角／全角））が
    ある場合と、本文がそのラベルだけの場合に限って除去する。履歴の`[speaker] msg`は空白で区切るため、
    modelが真似た`[midori] `と`[midori] [midori] `は全て取り除ける。区切りが無いものは本文として残し、
    そこで判定を止める。`[shiro]からの報告です`は、他Agentの報告を中継する言い方で、Mioの旧testが本文として
    残すと固定していた意図した仕様（最初の実装は区切りなしで除去してこれを壊し、次のfixで区切り必須にした。
    その時点で旧Mioの「自分のラベルは区切りなしでも除去」が失われたため、次のfixで自分のラベルを戻した）。
  例: midoriの生成で`[midori][midori] 本文`は`本文`、shiroの生成で`[midori] [midori] 本文`は`本文`、
  mioの生成で`[shiro]からの報告です`はそのまま、mioの生成で`[mio]こんにちは`は`こんにちは`。
  本文の途中のラベルと、既知でない角括弧（`[注]`）も変えない。旧別名（`wild`／`heavy`）のspeakerは
  `midori`／`kuro`として扱う。
- 判定でラベルが残るケース: 他のAgent名のラベルの直後が区切りでない（mioの生成で`[midori]本文`、
  `[shiro]からの報告です`、shiroの生成で`[mio]は…`）、他のAgent名のラベル同士が区切りなしで続く
  （mioの生成で`[midori][midori] 本文`は先頭の`[midori]`の直後が`[`のため全体が残る）、先頭が他のAgent名の
  ラベルで次に自分のラベルが区切りなしで続く（`[shiro][mio]本文`）、本文の途中、既知でない角括弧、ラベルの先頭部分で
  終わる応答（`[mid`）。`[midori] [midori]本文`をmioが生成した場合は先頭だけ除かれ`[midori]本文`になる。
  自分のラベルの直後が区切りでない空白類（NBSPなど）は、前後の空白除去で取り除かれる。
- stream: 非streamと同じ判定にする。先頭が`[`で、かつ既知ラベル（または自分のラベル）の先頭部分である間と、
  自分以外の既知ラベルが閉じた直後の1文字（区切りかどうかが決まるまで）だけ保留する。自分のラベルは閉じた時点で
  捨て、他のラベルは区切りならラベルを捨てて続きを判定し、区切りでなければラベルごと流す。既知でないと分かった
  時点でも保留分をまとめて流す。確定後のdeltaは保留しない。ラベルで終わったstreamはラベルだけの応答として
  捨てる。`Generate`の完了直後に`flush`を呼び、判定の途中で終わったstreamの保留分を処理する（生成が失敗した
  ときは流さない）。Mioのattribution retryは生成ごとに新しいfilterを使う。
- 適用先: `MioAgent.Chat`（Mio／Shiro CHAT／PLAN）のstreamと本文、`WildAgent.Generate`（Midori）のstreamと本文、
  `HeavyAgent.Generate`（Kuro、streamなし）の本文、`ShiroAgent.Execute`のpromptに履歴を入れる生成経路の本文。
  各Agentは返す値とcommitする値を同じ無ラベルの本文にする（`commitConversationTurn`は純粋なまま）。
  ctxにstream callbackが無いときは従来どおり非streamのまま（Wildは従来どおり常にstream要求）。
- TTS: 読み上げ文はAgentが返した本文とstreamから作られるため、上記で無ラベルになる。`FilterSpeakableText`は
  chunk単位で呼ばれ、本文中のラベルを誤って消すため、ここでは除去しない。
- Mioの既存の自己ラベル除去（8/11 commit 4afbd194）は共通ヘルパに置き換えた。旧testの全ケース
  （`[mio] こんにちは`、`[MIO]：`、`[shiro]からの報告です`を本文として残す、本文中の`[mio]`を残す、分割delta）は
  `mio_test.go`の`TestStripLeadingAgentSelfLabel`／`TestFilterLeadingAgentSelfLabel`として共通ヘルパに対して
  保っている。変更点は1つで、区切りのある他Agent名のラベル（本番で観測した`[midori] [midori] `）も除去する。
- 対象外（意図的）: Shiroのcodex／subagent／native coding経路（promptに履歴の`[speaker]`形式が入らず、
  出力もmodelのchat本文ではない）、旧別名`heavy`／`wild`（履歴のラベルはcanonical名になる）、IdleChat
  （別の`[from]: content`形式を持つ）。

## Failure Knowledge

- Failure / Problem: Agentの応答の頭に`[midori] `や`[midori] [midori] `が付き、表示・履歴・読み上げに残る。
- Cause: 履歴を`[speaker] msg`でpromptへ入れる入力側の形式をmodelが真似る。除去は応答Agentごとに別々で、
  Mioだけが自分の名前の分を持っていた（Semantic Duplication、かつ他Agent名と重なりを想定していなかった）。
- Lesson: 入力の形式を作るownerが、その逆変換も持つ。除去規則は1か所に置き、非stream・stream・保存・読み上げで
  同じ規則を使う。streamは先頭だけを短く保留して判定する。除去の条件は、直すべき症状（履歴の形式を真似た
  `[name] `）に必要な範囲に絞る。他Agentの区切りの無い`[name]本文`は、報告を中継する言い方と区別できず、
  除くと本文を壊す。一方、動作確認済みの旧挙動（自分のラベルは区切りなしでも除去）は、規則を一般化する
  ときに落とさない。既存testが固定していた意図を、新しい要件で上書きする前に確認する。判定は「誰が生成したか」
  に依存するため、ヘルパは生成したAgentを受け取る。
- Invariant: どのAgentの応答（本文・stream delta・保存する履歴・読み上げ文）も、先頭にAgent名ラベルを持たない。
  除去は先頭の既知Agent名のラベルの連続を先頭から順に判定し、生成したAgent自身のラベルは区切りなしでも、
  他のAgent名のラベルは直後に区切りがある場合とラベルだけの本文に限って行う。区切りが無い他Agentのラベル
  （mioの生成で`[shiro]からの報告です`）、本文中のラベル、既知でない角括弧は変えない。
- Enforcement: `conversation.AgentLabelNames`が除去対象の唯一の一覧で、`agentprofile.Catalog`のAgent IDと
  一致することをtestが双方向に強制する（Agentを追加するとtestが失敗する）。履歴の`[speaker] msg`形式との
  逆変換関係もtestで固定する。
- Tests: `internal/domain/conversation/speaker_label_test.go`（非stream、分割delta、重なり、他Agent名、途中、
  未知の角括弧、ラベルのみ（区切りなしで終わる場合を含む）、空、全角空白、区切りの種類、自分のラベルと他のラベルの
  順次判定（判定の4例を含む）、区切りなしで残るケース、`]`と区切りが別deltaのケース、区切りがrune境界で割れるケース、
  保留の上限、flush、生成したAgentごとの非streamとstreamの一致、履歴形式の逆変換）、
  `internal/domain/agent/mio_test.go`（Mioの旧回帰testの全ケース）、
  `internal/domain/agent/speaker_label_test.go`（Mio／Shiro CHAT／Midori／Kuro／Shiro OPSの本文・保存・stream、
  Mio retry、flush、stream callback無しの非stream維持）、
  `internal/application/orchestrator/speaker_label_invariant_test.go`（`ProcessMessage`経由で`agent.thinking`、
  `agent.response`、TTS pushに無ラベル）、`internal/application/agentprofile/label_names_test.go`。

## 関連ファイル

- `internal/domain/conversation/speaker_label.go`、`internal/domain/conversation/message.go`（`SpeakerKin`を追加）
- `internal/domain/agent/mio.go`、`wild.go`、`heavy.go`、`shiro.go`
- `docs/03_キャラクター・エージェント仕様.md`（Persona と共有状態）、`docs/06_Public_API仕様.md`（`agent.thinking`の節）

## 未確認

- 本番での確認は配備後（Midori／Shiro／Mio／Kuroの宛先別E2E、新しいsessionで`agent.thinking`のdeltaと
  `agent.response`と読み上げ文にラベルが無いこと）。
- 修正前に保存済みの会話履歴には、ラベル付きのAgent発言が残る。履歴は次のpromptで`[speaker] [midori] ...`の形に
  なり得るが、応答側で除去されるため機能上は問題ない。回帰確認は新しいsessionで行う。
- `infrastructure/persistence/conversation/llm_summarizer.go`は`[speaker] msg`形式を要約LLMへ渡している。要約本文への
  ラベル混入は今回の対象外で、別件として扱う。
