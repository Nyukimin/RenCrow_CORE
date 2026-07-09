# MCP Chrome セットアップ進捗状況

**最終更新**: 2026-02-25 17:40
**スレッド**: Win11 側の MCP Chrome 環境構築 ✅ **完了**

---

## Win11側の環境構築 ✅ 完了

### マシン情報
- **ホスト名**: BMAX2ND
- **IP アドレス**: 100.83.235.65
- **MCP接続ポート**: 12306
- **MCP Client URL**: `http://127.0.0.1:12306/mcp`

### インストール完了項目
- ✅ PowerShell 5.1
- ✅ Node.js v22.17.0
- ✅ pnpm 10.30.2
- ✅ Python 3.13.5
- ✅ Chrome インストール済み
- ✅ mcp-chrome-bridge v1.0.31 (`npm install -g mcp-chrome-bridge`)
- ✅ Chrome拡張機能ビルド完了
- ✅ Native Messaging Host 登録完了
- ✅ Chrome拡張機能ロード完了
- ✅ ファイアウォール設定完了（ポート 12306）

### 接続状態
- ✅ **Native Messaging接続**: 成功
- ✅ **接続ポート**: 12306
- ⏳ **MCPサービス**: 未起動（正常、クライアント接続待ち）

### ビルド成果物の場所
- Chrome拡張機能: `C:\Users\nyuki\Downloads\mcp-chrome\app\chrome-extension\.output\chrome-mv3`

---

## RenCrow側の実装（Phase 5-B） ⏳ 次のステップ

### アーキテクチャ
```
RenCrow (Linux) → HTTP API → Win11 (mcp-chrome-bridge) → Chrome拡張機能 → Chromeブラウザ
```

### 実装が必要な項目

#### 1. pkg/mcp/ パッケージの作成
- `pkg/mcp/client.go` - MCPクライアント実装
- `pkg/mcp/types.go` - MCP型定義
- `pkg/mcp/chrome.go` - Chrome操作ラッパー

#### 2. MCP クライアントの実装
- HTTP API経由でWin11側に接続
- リトライ・タイムアウト処理
- エラーハンドリング

#### 3. 設定ファイルの更新 (`config/config.json`)
```json
{
  "mcp": {
    "chrome": {
      "enabled": true,
      "base_url": "http://100.83.235.65:12306/mcp",
      "timeout": 30,
      "retry_count": 3
    }
  }
}
```

#### 4. Coder3 統合
- `pkg/provider/anthropic.go` に Chrome 操作の plan 生成を追加
- plan に `uses_browser: true` フラグを追加

#### 5. Worker に Chrome 操作実行を追加
- MCP Chrome API の呼び出し
- 実行結果のログ記録


---

## 参考資料

### 実装プラン
- **MCP Chrome 統合手順**: `docs/06_実装ガイド進行管理/20260225_MCP_Chrome統合手順.md`
- **Coder3統合仕様**: `docs/06_実装ガイド進行管理/20260224_Coder3統合仕様反映.md`

### LLM運用仕様
- **Coder3 仕様**: `docs/05_LLM運用プロンプト設計/Coder3_Claude_API仕様.md`
- **Worker 仕様**: `docs/05_LLM運用プロンプト設計/LLM_Worker_Spec_v1_0.md`

### 正本仕様
- **実装仕様**: `docs/01_正本仕様/実装仕様.md`

### GitHub
- **mcp-chrome**: https://github.com/hangwin/mcp-chrome

---

## テストプラン

### 1. 接続テスト
```bash
# Linux側から Win11 の MCP Chrome に接続
curl http://100.83.235.65:12306/mcp
```

### 2. ブラウザ操作テスト（実装後）
```go
// pkg/mcp/client_test.go
func TestChromeNavigate(t *testing.T) {
    client := mcp.NewClient("http://100.83.235.65:12306/mcp")
    err := client.Navigate("https://www.google.com")
    assert.NoError(t, err)
}
```

- Coder3 が Chrome 操作の plan を生成
- Worker が Chrome 操作を実行

---

## トラブルシューティング

### Win11側のログ確認
```powershell
# mcp-chrome-bridgeのログ
cat C:\Users\nyuki\AppData\Local\mcp-chrome-bridge\logs\*.log | Select-Object -Last 50

# Chrome拡張機能のログ
# Chrome DevTools → Console で確認
```

### 接続テスト
```powershell
# Win11側でポート確認
netstat -an | findstr 12306

# ファイアウォールルール確認
Get-NetFirewallRule -DisplayName "MCP Chrome Bridge"
```

### 診断コマンド
```powershell
mcp-chrome-bridge doctor
```

---

**次回の続行ポイント**: RenCrow側で `pkg/mcp/` パッケージを作成し、MCP Chrome クライアントを実装
