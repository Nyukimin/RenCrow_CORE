# Live2D Chat Integration

**作成日**: 2026-06-12  
**機能**: Chat/IdleChat に Live2D キャラクター表示と感情表現を統合

---

## 概要

RenCrowのチャット機能にLive2Dキャラクター（MioとShiro）を統合し、会話内容に応じた感情表現を自動的に表示します。

### 主な機能

1. **Live2D キャラクター表示**: MioとShiroの透過版Live2Dモデル
2. **感情検出**: メッセージ内容から自動的に感情を検出
3. **レスポンシブ対応**: デスクトップ・モバイル両対応
4. **ライブモード**: 大画面表示モード（mode=live）
5. **リアルタイム更新**: チャット中にキャラクターの感情が変化

---

## エンドポイント

### 1. Live2D Chat UI
```
GET /viewer/live2d/chat
```

チャット画面を表示（Live2D統合済み）

**機能**:
- MioとShiroの切り替え
- 通常モード/ライブモード切り替え
- リアルタイムチャット
- 感情表示

### 2. Live2D Character
```
GET /viewer/live2d/character?character_id={mio|shiro}&mode={normal|live}
```

Live2D HTMLを直接表示

**パラメータ**:
- `character_id`: `mio` または `shiro` (デフォルト: `mio`)
- `mode`: `normal` または `live` (デフォルト: `normal`)

### 3. Live2D Embed
```
GET /viewer/live2d/embed?character_id={mio|shiro}&emotion={emotion}&mode={normal|live}
```

埋め込み用Live2Dフレーム

**パラメータ**:
- `character_id`: キャラクターID
- `emotion`: 感情タイプ（normal, happy, sad, angry, surprise, think, speaking）
- `mode`: 表示モード

### 4. Chat API
```
POST /viewer/api/chat
Content-Type: application/json

{
  "message": "メッセージ",
  "character_id": "mio",
  "mode": "normal"
}
```

チャットメッセージを送信し、感情付きレスポンスを取得

**レスポンス**:
```json
{
  "message": "応答メッセージ",
  "character_id": "mio",
  "emotion": "happy",
  "live2d_url": "/viewer/live2d/character?character_id=mio",
  "live2d_embed_url": "/viewer/live2d/embed?character_id=mio&emotion=happy"
}
```

---

## 感情タイプ

### EmotionType

| 感情 | 説明 | キーワード例 |
|------|------|-------------|
| `normal` | 通常 | デフォルト |
| `happy` | 嬉しい | 嬉しい、楽しい、ありがとう、😊 |
| `sad` | 悲しい | 悲しい、残念、申し訳、😢 |
| `angry` | 怒り | 怒、腹、イライラ、😠 |
| `surprise` | 驚き | 驚、まさか、すごい、😲 |
| `think` | 考え中 | 考え、確認、うーん、？ |
| `speaking` | 話し中 | （内部使用） |

### 感情検出ロジック

メッセージ内容から自動的に感情を検出：

1. **キーワードマッチング**: 日本語・英語・絵文字
2. **優先順位**: angry > surprise > sad > happy > think
3. **疑問符検出**: `？` または `?` → think
4. **デフォルト**: normal

---

## 使用例

### 基本的な使い方

```html
<!-- Chat UIを開く -->
<a href="/viewer/live2d/chat">Live2D Chatを開く</a>
```

### iframe埋め込み

```html
<!-- Live2Dキャラクターを埋め込み -->
<iframe 
  src="/viewer/live2d/embed?character_id=mio&emotion=happy&mode=normal"
  width="300" 
  height="400"
  style="border: none;"
></iframe>
```

### JavaScript API呼び出し

```javascript
async function sendMessage(message) {
  const response = await fetch('/viewer/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message: message,
      character_id: 'mio',
      mode: 'normal'
    })
  });
  
  const data = await response.json();
  console.log('Emotion:', data.emotion);
  console.log('Response:', data.message);
}
```

---

## モバイル対応

### レスポンシブデザイン

- **デスクトップ**: 横並び（キャラクター左、チャット右）
- **モバイル**: 縦並び（キャラクター上、チャット下）

### 表示サイズ

| デバイス | 通常モード | ライブモード |
|---------|-----------|-------------|
| デスクトップ | 400px幅 | 600px幅 |
| モバイル | 250px高さ | 350px高さ |

---

## ファイル構成

### 新規作成ファイル

```
internal/adapter/viewer/
├── live2d_handler.go           # Live2Dハンドラー
├── live2d_handler_test.go      # テスト
├── emotion_detector.go         # 感情検出
└── assets/
    └── live2d_chat.html        # Chat UI

internal/adapter/viewer/assets/images/
├── mio/
│   └── Mio_透過版.html        # Mio Live2Dモデル
└── shiro/
    └── Shiro_透過版.html      # Shiro Live2Dモデル
```

### ルーティング

```go
// cmd/rencrow/routes.go
mux.HandleFunc("/viewer/live2d/character", viewer.HandleLive2DCharacter)
mux.HandleFunc("/viewer/live2d/embed", viewer.HandleLive2DCharacterEmbed)
mux.HandleFunc("/viewer/live2d/asset", viewer.HandleLive2DAsset)
mux.HandleFunc("/viewer/live2d/chat", viewer.HandleLive2DChat)
mux.HandleFunc("/viewer/api/chat", viewer.HandleLive2DChatAPI)
```

---

## 今後の拡張

### Phase 2: LLM統合
- [ ] 実際のLLMサービスと統合
- [ ] IdleChatとの連携
- [ ] 会話履歴の保存

### Phase 3: 高度な感情表現
- [ ] Live2Dモーション制御
- [ ] 音声連動（リップシンク）
- [ ] カスタム感情パターン

### Phase 4: パーソナライゼーション
- [ ] ユーザー別設定
- [ ] カスタムキャラクター
- [ ] 感情学習

---

## トラブルシューティング

### Live2Dが表示されない

**確認事項**:
1. HTMLファイルのパスが正しいか
2. ブラウザのコンソールにエラーが出ていないか
3. `/viewer/live2d/character` に直接アクセスできるか

### 感情が検出されない

**対策**:
- キーワードを追加: `internal/adapter/viewer/emotion_detector.go` の `emotionKeywords` を編集
- 優先順位を調整: `DetectEmotion()` 関数の `priorities` を変更

### モバイルで表示が崩れる

**確認事項**:
- viewport メタタグが設定されているか
- CSS の media query が適用されているか

---

**作成者**: Claude Sonnet 4.5  
**最終更新**: 2026-06-12
