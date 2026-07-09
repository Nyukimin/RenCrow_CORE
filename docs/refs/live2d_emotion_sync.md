# Live2D 感情同期システム

**作成日**: 2026-06-12  
**機能**: 発話の感情とLive2Dキャラクターの表情・モーションを自動同期

---

## 概要

チャット発話から検出された感情に基づいて、Live2Dキャラクター（Mio/Shiro）の表情とモーションをリアルタイムで変更します。

---

## アーキテクチャ

### 感情フロー

```
ユーザー発話
    ↓
感情検出（DetectEmotion）
    ↓
感情タイプ（EmotionType）
    ↓
Live2D State（motion + expression）
    ↓
postMessage API
    ↓
Live2D iframe
    ↓
Live2D モーション・表情変更
```

---

## 感情マッピング

### EmotionType → Live2DState

| 感情 | Motion | Expression | 説明 |
|------|--------|------------|------|
| `normal` | - | `f01` | 通常の表情 |
| `happy` | `tapBody` | `f02` | 笑顔、体をタップ |
| `sad` | - | `f03` | 悲しい表情 |
| `angry` | `shake` | `f04` | 怒り、震える |
| `surprise` | `pinchIn` | `f05` | 驚き、ピンチイン |
| `think` | - | `f06` | 考え中 |
| `speaking` | `tapBody` | `f02` | 話し中（笑顔） |

### Live2D Motion 一覧

- `tapBody` - 体をタップ（軽い動き）
- `shake` - 震える（強い動き）
- `pinchIn` - ピンチイン（縮小）
- （その他、Live2Dモデルによって異なる）

### Live2D Expression 一覧

- `f01` - 通常
- `f02` - 笑顔
- `f03` - 悲しい
- `f04` - 怒り
- `f05` - 驚き
- `f06` - 考え中

---

## 実装詳細

### 1. 感情マッピング定義

#### ファイル: `internal/adapter/viewer/live2d_emotion_controller.go`

```go
var Live2DEmotionMapping = map[EmotionType]Live2DState{
    EmotionHappy: {
        Motion:     "tapBody",
        Expression: "f02", // Smile
    },
    // ...
}
```

### 2. スクリプト生成

```go
func BuildLive2DControlScript(emotion EmotionType) string {
    // postMessageを受信してLive2Dを制御するJavaScriptを生成
}
```

### 3. postMessage API

#### Parent → iframe (Embed)

```javascript
frame.contentWindow.postMessage({
    type: 'emotion',
    emotion: 'happy',
    state: {
        motion: 'tapBody',
        expression: 'f02'
    }
}, '*');
```

#### Embed → Live2D HTML

```javascript
frame.contentWindow.postMessage({
    type: 'emotion',
    emotion: 'happy',
    state: { ... }
}, '*');
```

---

## エンドポイント

### POST /viewer/live2d/emotion

感情制御APIエンドポイント

**リクエスト**:
```json
{
  "emotion": "happy"
}
```

**レスポンス**:
```json
{
  "type": "emotion",
  "emotion": "happy",
  "state": {
    "motion": "tapBody",
    "expression": "f02"
  }
}
```

---

## 使い方

### 1. Chat UI経由（自動）

```javascript
// Chat APIから感情を取得
const response = await fetch('/viewer/api/chat', {
    method: 'POST',
    body: JSON.stringify({ message: 'ありがとう！' })
});
const data = await response.json();

// 感情が自動的にLive2Dに反映される
// data.emotion === 'happy'
```

### 2. JavaScript API経由（手動）

```javascript
// Chat UI内のsetEmotion関数を呼び出し
setEmotion('happy');
```

### 3. postMessage経由（iframe制御）

```javascript
// Live2D embedフレームにメッセージを送信
const frame = document.getElementById('live2d-frame');
frame.contentWindow.postMessage({
    type: 'emotion',
    emotion: 'surprise',
    state: {
        motion: 'pinchIn',
        expression: 'f05'
    }
}, '*');
```

---

## Live2D HTML統合

### スクリプト注入

`HandleLive2DCharacter` は以下のスクリプトをLive2D HTMLに注入：

```javascript
<script>
(function() {
    var currentEmotion = 'normal';
    var live2dState = { motion: '', expression: 'f01' };

    // Wait for Live2D to load
    function waitForLive2D(callback) {
        if (window.live2d && window.live2d.model) {
            callback();
        } else {
            setTimeout(function() { waitForLive2D(callback); }, 100);
        }
    }

    // Set emotion
    function setEmotion(emotion, state) {
        console.log('Setting Live2D emotion:', emotion, state);

        if (!window.live2d || !window.live2d.model) {
            console.warn('Live2D not ready');
            return;
        }

        try {
            // Set expression
            if (state.expression && window.live2d.model.setExpression) {
                window.live2d.model.setExpression(state.expression);
            }

            // Start motion
            if (state.motion && window.live2d.model.startMotion) {
                window.live2d.model.startMotion('tapBody', state.motion, 3);
            }
        } catch (e) {
            console.error('Live2D control error:', e);
        }
    }

    // Listen for messages from parent
    window.addEventListener('message', function(event) {
        if (event.data.type === 'emotion') {
            setEmotion(event.data.emotion, event.data.state);
        }
    });

    // Initial emotion on load
    waitForLive2D(function() {
        setEmotion(currentEmotion, live2dState);
    });
})();
</script>
```

---

## デバッグ

### コンソールログ

各レイヤーでログを出力：

```
[Chat] Setting emotion: happy
[Chat] Sent emotion to Live2D: happy { motion: 'tapBody', expression: 'f02' }

[Live2D Embed] Received emotion change from parent: { type: 'emotion', emotion: 'happy', ... }
[Live2D Embed] Setting emotion: happy { motion: 'tapBody', expression: 'f02' }

[Live2D HTML] Setting Live2D emotion: happy { motion: 'tapBody', expression: 'f02' }
```

### 動作確認

1. ブラウザのコンソールを開く
2. `/viewer/live2d/chat` にアクセス
3. メッセージを送信（例: 「ありがとう！」）
4. コンソールで感情の流れを確認

---

## トラブルシューティング

### 感情が変わらない

**確認事項**:
1. ブラウザコンソールにエラーが出ていないか
2. `window.live2d.model` が存在するか
3. Live2Dモデルが読み込まれているか（1秒待機）

**対策**:
- 待機時間を延長: `setTimeout(..., 1000)` → `setTimeout(..., 2000)`
- Live2D APIを確認: `console.log(window.live2d)`

### expressionが設定できない

**原因**: Live2Dモデルに該当expressionが存在しない

**対策**:
- `window.live2d.model._expressions` を確認
- マッピングを修正: `f02` → 実際のexpression ID

### motionが再生されない

**原因**: Live2Dモデルに該当motionが存在しない

**対策**:
- `window.live2d.model._motionManager` を確認
- マッピングを修正: `tapBody` → 実際のmotion名

---

## カスタマイズ

### 新しい感情を追加

1. **EmotionType に追加**
```go
// emotion_detector.go
const EmotionExcited EmotionType = "excited"
```

2. **キーワード定義**
```go
emotionKeywords[EmotionExcited] = []string{
    "興奮", "最高", "やった",
    "excited", "awesome",
    "🎉", "🔥",
}
```

3. **Live2D マッピング**
```go
// live2d_emotion_controller.go
Live2DEmotionMapping[EmotionExcited] = Live2DState{
    Motion:     "jump",
    Expression: "f07",
}
```

4. **Chat UI マッピング**
```javascript
// live2d_chat.html
emotionStateMapping['excited'] = { motion: 'jump', expression: 'f07' };
```

---

## パフォーマンス

### 最適化

- **iframe再利用**: 感情変更時にiframeを再読み込みしない
- **postMessage**: 軽量な通信（JSON）
- **待機時間**: Live2D初期化を待つ（1秒）

### メモリ使用量

- Live2D HTML: 1.2MB (Mio), 893KB (Shiro)
- スクリプト注入: +2KB程度

---

**作成者**: Claude Sonnet 4.5  
**最終更新**: 2026-06-12
