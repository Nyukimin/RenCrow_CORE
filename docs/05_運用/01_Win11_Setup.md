# Win11 MCP Chrome セットアップ - クイックスタート

**対象マシン**: BMAX2ND (100.83.235.65)
**所要時間**: 約 15-20 分

---

## 概要

RenCrow の Coder3 に Chrome 操作機能を追加するため、Win11 マシンに MCP Chrome ブリッジをセットアップします。

```
RenCrow (Linux) → HTTP → Win11 (mcp-chrome-bridge) → Chrome 拡張機能 → Chrome
```

---

## セットアップ方法（2 つの選択肢）

### 方法 1: 自動スクリプト（推奨）

**Win11 で PowerShell を管理者権限で開いて実行**:

```powershell
# スクリプトをダウンロード（または手動でコピー）
cd $env:USERPROFILE\Downloads

# 実行
.\win11_setup_script.ps1
```

スクリプトの場所: `docs/06_実装ガイド進行管理/win11_setup_script.ps1`

### 方法 2: 手動セットアップ

詳細手順: `docs/06_実装ガイド進行管理/win11_manual_setup.md`

**概要**:
1. pnpm のインストール
2. mcp-chrome-bridge のインストール確認
3. Chrome 拡張機能のビルド
4. Chrome 拡張機能のインストール
5. Native Messaging Host の登録
6. ファイアウォール設定
7. mcp-chrome-bridge の起動

---

## セットアップ後の確認

### Win11 側でのテスト

```powershell
# テストスクリプトを実行
.\win11_test_mcp.ps1
```

または手動で:

```powershell
# ヘルスチェック
curl http://localhost:12306/health

# ツール一覧取得
$body = @{method="tools/list";params=@{}} | ConvertTo-Json
Invoke-WebRequest -Uri http://localhost:12306/mcp -Method POST -ContentType "application/json" -Body $body
```

### Linux 側からの接続テスト

**前提**: Win11 で mcp-chrome-bridge が起動していること

```bash
# 接続テストスクリプトを実行
cd /home/nyukimi/RenCrow_CORE
./scripts/test_mcp_connection.sh
```

または手動で:

```bash
# ヘルスチェック
curl http://100.83.235.65:12306/health

# ツール一覧取得
curl -X POST http://100.83.235.65:12306/mcp \
  -H "Content-Type: application/json" \
  -d '{"method":"tools/list","params":{}}'
```

---

## mcp-chrome-bridge の起動方法

### 一時起動（テスト用）

```powershell
mcp-chrome-bridge --port 12306
```

### 永続化（自動起動）

#### オプション A: nssm でサービス化（推奨）

```powershell
# nssm をダウンロード: https://nssm.cc/download
# サービスをインストール
nssm install mcp-chrome-bridge "C:\Program Files\nodejs\node.exe" ...

# サービスを開始
nssm start mcp-chrome-bridge
```

詳細: `win11_manual_setup.md` の「ステップ 7」を参照

#### オプション B: タスクスケジューラ

1. タスクスケジューラを開く（`taskschd.msc`）
2. 「基本タスクの作成」
3. トリガー: コンピューターの起動時
4. 操作: `mcp-chrome-bridge --port 12306`

---

## トラブルシューティング

### ❌ Win11 への接続ができない（Linux 側）

```bash
# Tailscale 接続を確認
tailscale status | grep 100.83.235.65

# Win11 が起動しているか確認
ping 100.83.235.65
```

### ❌ mcp-chrome-bridge が起動しない

```powershell
# ログを確認
cat ~\.mcp-chrome\mcp-chrome.log

# ポートが使われていないか確認
netstat -ano | findstr :12306

# 診断実行
mcp-chrome-bridge doctor
```

### ❌ Chrome 拡張機能が動作しない

1. `chrome://extensions/` を開く
2. 拡張機能が有効か確認
3. 「詳細」→「エラー」でログを確認
4. Chrome を再起動

### ❌ Native Messaging Host が認識されない

```powershell
# レジストリを確認
Get-ItemProperty -Path "HKCU:\Software\Google\Chrome\NativeMessagingHosts\com.mcp.chrome"

# 再登録
mcp-chrome-bridge register
mcp-chrome-bridge fix-permissions
```

---

## 完了後のチェックリスト

- [ ] pnpm がインストールされている
- [ ] mcp-chrome-bridge がインストールされている
- [ ] Chrome 拡張機能がビルドされている
- [ ] Chrome 拡張機能が Chrome にインストールされている
- [ ] Native Messaging Host が登録されている
- [ ] ファイアウォールでポート 12306 が開放されている
- [ ] Win11 ローカルで接続テストが成功する
- [ ] Linux から Win11 への接続テストが成功する

---

## 次のステップ

セットアップが完了したら、RenCrow 側の統合を完了させます：

1. **Phase 5-C2**: Coder3 のシステムプロンプトに MCP ツールを追加
2. **Phase 5-C4**: Worker による Chrome 操作実行
3. **Phase 5-C5**: End-to-End テスト

詳細: `docs/06_実装ガイド進行管理/20260225_MCP_Chrome統合手順.md`

---

## 関連ファイル

- **自動セットアップ**: `win11_setup_script.ps1`
- **手動セットアップ**: `win11_manual_setup.md`
- **Win11 テスト**: `win11_test_mcp.ps1`
- **Linux テスト**: `/scripts/test_mcp_connection.sh`
- **統合手順書**: `20260225_MCP_Chrome統合手順.md`

---

**最終更新**: 2026-02-25
**作成者**: Claude Sonnet 4.5
