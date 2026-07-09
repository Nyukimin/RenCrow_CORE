# Worker（作業船）仕様書 v1.0

## 1. 目的

Workerは「調査・抽出・整形・差分作成」を担当し、機械処理可能なJSONを返す。
会話・判断・最終決定（DB反映の承認）はMio（Chat）が担う。

## 2. 非目的

-   自然文での説明、背景解説、提案、感想
-   DBへの直接書き込み（更新案の生成まで）
-   長文コンテキスト前提の一括処理

## 3. I/O契約

### 入力

-   OpenClaw Gatewayが生成した作業依頼プロンプト
-   必ずスキーマ宣言を先頭に含む

### 出力

-   JSONオブジェクト1つのみ
-   UTF-8
-   スキーマ準拠をGateway側で検証

## 4. 標準出力スキーマ

``` json
{
  "goal": "string",
  "tasks": [
    { "id": "string", "desc": "string", "acceptance": "string" }
  ],
  "risks": [
    { "id": "string", "desc": "string", "mitigation": "string" }
  ],
  "next": ["string"]
}
```

## 5. API呼び出し仕様

-   format: "json"
-   stream: false
-   Content-Type: application/json; charset=utf-8
-   keep_alive: -1

## 6. プロンプトPrefix（必須）

次のJSONスキーマのオブジェクト1つだけを返せ。他は一切出力するな。 {
"goal": string, "tasks": \[ { "id": string, "desc": string,
"acceptance": string } \], "risks": \[ { "id": string, "desc": string,
"mitigation": string } \], "next": \[ string \] }

## 7. レスポンス処理

正常系: 1. HTTP 200確認 2. message.content取得 3. JSON.parse 4.
スキーマ検証

異常系: - parse失敗またはキー不足 → 再試行1回 - 再試行でも失敗 →
Mio（Chat）へエラー返却

## 8. EventId運用

-   Gateway入口で採番
-   Workerプロンプトに埋め込む
-   ログと紐付ける

## 9. タイムアウト

-   推論タイムアウト: 60〜120秒
-   スキーマ不正時のみ再試行1回

## 10. Definition of Done

1.  JSON-only出力が保証される
2.  スキーマ100%準拠
3.  parse/検証/再試行がGateway内で完結
4.  EventIdで追跡可能
