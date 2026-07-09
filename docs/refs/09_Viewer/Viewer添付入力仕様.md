# Viewer添付入力仕様

## 目的

Viewer から音声入力に加えて、画像、PDF、テキスト系ファイル、カメラ画像を RenCrow の通常メッセージ経路へ渡せるようにする。

## 対象経路

- `Viewer UI -> /viewer/send -> MessageOrchestrator`
- Chat / Worker / Heavy / Wild / Coder の既存ルーティング経路
- Viewer のルート選択エイリアス:
  - Worker
  - Heavy
  - Wild

## 添付種別

対応する添付は次の通り。

- 画像: `image/*`
- PDF: `application/pdf`
- テキスト: `text/*`
- テキスト相当: `.txt`, `.md`, `.json`, `.csv`, `.yaml`, `.yml`

アプリケーション層では MIME type を基準に `image` または `document` へ分類する。

## 保存先

Viewer から送信された添付は Workspace 配下へ保存する。

```text
<workspace>/viewer_uploads/<YYYYMMDD>/viewer/<attachment_id>/<safe_filename>
```

保存時に以下を付与する。

- `id`
- `kind`
- `filename`
- `content_type`
- `size_bytes`
- `path`
- `sha256`

## LLM への渡し方

画像は、対応プロバイダーに対して LLM メッセージのマルチパートとして渡す。

- OpenAI 互換: `image_url` data URL
- Claude: `image` base64 source
- Gemini: `inlineData`

PDF とテキスト系書類は、この段階では本文抽出を行わず、ファイル参照情報だけをユーザーメッセージ末尾へ追記する。

マルチモーダル非対応プロバイダーでは、画像も含めてファイル参照情報がテキストフォールバックとして渡る。

## UI

Viewer の入力欄に以下を追加する。

- 添付ボタン
- カメラ画像ボタン
- 添付トレイ
- 添付削除ボタン

添付がある場合は `multipart/form-data` で `/viewer/send` へ送信する。添付がない通常メッセージは従来通り JSON で送信する。

## サイズ制限

初期値は次の通り。

- 1ファイル最大: 10 MiB
- 合計最大: 30 MiB

## 責務境界

- Viewer adapter: multipart 受信、UI 入力、保存サービスへの橋渡し
- application/attachment: サイズ検証、分類、保存、ハッシュ算出
- domain/attachment: 添付の値オブジェクト、分類、ファイル名正規化
- domain/task: 添付を Task に保持
- domain/agent: LLM メッセージへの添付反映
- infrastructure/llm/providers: 各 API 形式へのマルチパート変換

## 現在の制限

- PDF / テキスト本文の抽出は未実装。
- LINE / Slack 等の外部チャネル添付取り込みは未実装。
- Tool Calling 用 `ChatRequest` はテキストのみのまま維持する。
