
````markdown
# SBV2 TTSサーバ 利用仕様

## 1. 概要

このサーバは、Windows PC上で Style-Bert-VITS2（SBV2）を常時起動し、別PCや別アプリからHTTP APIで音声合成を利用するためのTTSサーバです。

利用者は、SBV2本体の起動方法やモデルファイルの場所を意識せず、HTTPリクエストを送ることで音声ファイルを取得できます。

現在、主に使う音声は次の2つです。

```text
amitaro
shi-gozaki
````

現在の環境では、以下の対応になっています。

```text
amitaro     -> model_id=0 / speaker_id=0 / style=Neutral
shi-gozaki  -> model_id=6 / speaker_id=0 / style=Neutral
```

注意：`model_id` はモデル追加や削除で変わる可能性があります。
上位アプリでは、将来的に `voice名 -> model_id` をAdapterまたは設定ファイルで解決する方針です。

---

## 2. 配置

SBV2関連ファイルは以下に置きます。

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2
```

構成は以下です。

```text
sbv2
├─ Start-SBV2-API.ps1
├─ Stop-SBV2-API.ps1
├─ HealthCheck-SBV2-API.ps1
├─ Style-Bert-VITS2
├─ logs
└─ warmup
```

各ファイルの役割は以下です。

```text
Start-SBV2-API.ps1
  SBV2 APIサーバを起動し、amitaro / shi-gozaki を起動時にウォームアップする

Stop-SBV2-API.ps1
  SBV2 APIサーバと pyopenjtalk worker を停止する

HealthCheck-SBV2-API.ps1
  SBV2 APIの状態を確認し、不調なら Stop -> Start で再起動する

logs
  起動ログ、APIログ、HealthCheckログを保存する

warmup
  起動時ウォームアップ音声と手動テスト音声を保存する
```

`Start-SBV2-API.ps1` はカレントディレクトリを変更しません。
スクリプト自身の場所を基準に、`Style-Bert-VITS2`、`logs`、`warmup` を相対的に参照します。

---

## 3. 接続確認

SBV2サーバPC自身で確認する場合は、ブラウザで以下を開きます。

```text
http://127.0.0.1:5000/docs
```

別PCから確認する場合は、SBV2サーバPCのIPアドレスを使います。

```text
http://<SBV2サーバPCのIP>:5000/docs
```

例：

```text
http://192.168.1.36:5000/docs
```

別PCから接続できない場合は、Windowsファイアウォールで5000番ポートが許可されているか確認します。

---

## 4. 利用者向けAPI

### 4.1 モデル一覧確認

SBV2サーバPC上で確認する場合：

```powershell
Invoke-RestMethod "http://127.0.0.1:5000/models/info" |
  ConvertTo-Json -Depth 10
```

別PCから確認する場合：

```powershell
Invoke-RestMethod "http://192.168.1.36:5000/models/info" |
  ConvertTo-Json -Depth 10
```

現在の代表的な指定値は以下です。

```text
amitaro:
  model_id=0
  speaker_id=0
  style=Neutral

shi-gozaki:
  model_id=6
  speaker_id=0
  style=Neutral
```

### 4.2 音声生成API

音声生成は `/voice` にPOSTします。

主なパラメータは以下です。

```text
text        読み上げる文章
model_id    使用するモデルID
speaker_id  話者ID
style       スタイル名
```

### 4.3 amitaro の音声生成例

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2

Invoke-WebRequest `
  -Uri "http://127.0.0.1:5000/voice?text=あみたろの接続テストです。&model_id=0&speaker_id=0&style=Neutral" `
  -Method Post `
  -OutFile ".\warmup\amitaro_test.wav"
```

出力先：

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2\warmup\amitaro_test.wav
```

### 4.4 shi-gozaki の音声生成例

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2

Invoke-WebRequest `
  -Uri "http://127.0.0.1:5000/voice?text=しんござきの接続テストです。&model_id=6&speaker_id=0&style=Neutral" `
  -Method Post `
  -OutFile ".\warmup\shi_gozaki_test.wav"
```

出力先：

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2\warmup\shi_gozaki_test.wav
```

### 4.5 別PCから使う場合

SBV2サーバPCのIPが `192.168.1.36` の場合：

```powershell
Invoke-WebRequest `
  -Uri "http://192.168.1.36:5000/voice?text=別PCからの接続テストです。&model_id=0&speaker_id=0&style=Neutral" `
  -Method Post `
  -OutFile ".\amitaro_from_remote.wav"
```

この場合、`-OutFile` の保存先は別PC側です。
SBV2サーバPC側に音声ファイルが保存されるわけではありません。

---

## 5. 起動

手動起動：

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2
.\Start-SBV2-API.ps1
```

起動時に行う処理：

```text
1. venv内の python.exe で server_fastapi.py を起動
2. /docs に応答するまで待機
3. /models/info からモデル一覧を取得
4. amitaro と shi-gozaki の model_id を検出
5. それぞれ1回ずつ /voice を実行してウォームアップ
```

ウォームアップ音声は以下に出ます。

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2\warmup\amitaro_warmup.wav
C:\GenerativeAI\Audio_Tools\TTS\sbv2\warmup\shi-gozaki_warmup.wav
```

---

## 6. 停止

手動停止：

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2
.\Stop-SBV2-API.ps1
```

停止対象：

```text
server_fastapi.py
style_bert_vits2.nlp.japanese.pyopenjtalk_worker
```

SBV2以外のPythonプロセスを巻き込まないよう、コマンドラインに上記文字列を含むプロセスだけを対象にします。

---

## 7. HealthCheckと自動復旧

Windowsのタスクスケジューラで、10分ごとに `HealthCheck-SBV2-API.ps1` を実行します。

HealthCheckで確認する内容：

```text
1. http://127.0.0.1:5000/docs が応答する
2. http://127.0.0.1:5000/models/info が応答する
3. /models/info 内に amitaro が存在する
4. /models/info 内に shi-gozaki が存在する
```

正常なら何もしません。

不調なら、以下の順で自動復旧します。

```text
1. Stop-SBV2-API.ps1
2. 数秒待機
3. Start-SBV2-API.ps1
4. 再度HealthCheck
```

HealthCheckの状態確認：

```powershell
Get-ScheduledTaskInfo -TaskName "SBV2 Health Check"
```

正常例：

```text
LastTaskResult : 0
NextRunTime    : 2026/04/29 12:10:00
```

`LastTaskResult : 0` は正常終了です。

---

## 8. ログ

ログは以下に出ます。

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2\logs
```

主なログ：

```text
sbv2_startup.log
  Start-SBV2-API.ps1 の処理ログ

sbv2_api_stdout.log
  server_fastapi.py の標準出力

sbv2_api_stderr.log
  server_fastapi.py のエラー出力

sbv2_healthcheck.log
  HealthCheck-SBV2-API.ps1 の実行ログ
```

確認例：

```powershell
Get-Content .\logs\sbv2_startup.log -Tail 80
Get-Content .\logs\sbv2_healthcheck.log -Tail 80
Get-Content .\logs\sbv2_api_stderr.log -Tail 80
```

---

## 9. モデル配置

モデルは以下に置きます。

```text
C:\GenerativeAI\Audio_Tools\TTS\sbv2\Style-Bert-VITS2\model_assets
```

現在利用する主なモデル：

```text
model_assets\amitaro
model_assets\shi-gozaki
```

各モデルフォルダには、原則として以下が必要です。

```text
config.json
*.safetensors
style_vectors.npy
```

---

## 10. 運用上の注意

### model_id は固定前提にしすぎない

現在は以下です。

```text
amitaro     -> model_id=0
shi-gozaki  -> model_id=6
```

ただし、モデル追加や削除で `model_id` が変わる可能性があります。
アプリ連携では、`/models/info` を読んで `config_path` からモデル名を判定するか、Adapter側で `voice名 -> model_id` を解決してください。

### 上位アプリは voice 名で考える

理想形は、上位アプリが以下のように扱うことです。

```json
{
  "text": "こんにちは。",
  "voice": "amitaro"
}
```

SBV2固有の以下は、Adapter側に閉じ込めます。

```text
model_id
speaker_id
style
endpoint
port
```

### HealthCheckは音声生成までは行わない

10分ごとのHealthCheckでは、音声生成までは行いません。
毎回 `/voice` を実行すると、GPU負荷や不要なwav生成が増えるためです。

音声生成まで含む確認は、起動時のウォームアップまたは手動テストで行います。

---

## 11. よく使うコマンド

手動起動：

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2
.\Start-SBV2-API.ps1
```

手動停止：

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2
.\Stop-SBV2-API.ps1
```

HealthCheck手動実行：

```powershell
cd C:\GenerativeAI\Audio_Tools\TTS\sbv2
.\HealthCheck-SBV2-API.ps1
```

タスク状態確認：

```powershell
Get-ScheduledTask -TaskName "SBV2 Health Check"
Get-ScheduledTaskInfo -TaskName "SBV2 Health Check"
```

HealthCheckログ確認：

```powershell
Get-Content C:\GenerativeAI\Audio_Tools\TTS\sbv2\logs\sbv2_healthcheck.log -Tail 80
```

API仕様確認：

```text
http://127.0.0.1:5000/docs
```

別PCから：

```text
http://<SBV2サーバPCのIP>:5000/docs
```

---

## 12. 完成状態

現在の実装では、以下が完了しています。

```text
SBV2 APIサーバ起動
amitaro / shi-gozaki の起動時ウォームアップ
StopスクリプトによるSBV2関連プロセス停止
HealthCheckスクリプトによる状態確認
不調時の Stop -> Start 自動復旧
Windowsタスクスケジューラによる10分ごとの監視
```

利用者が最低限知っておけばよいことは以下です。

```text
接続先URL
使用したい model_id
speaker_id
style
出力ファイルの保存先
```

代表値：

```text
amitaro:
  model_id=0
  speaker_id=0
  style=Neutral

shi-gozaki:
  model_id=6
  speaker_id=0
  style=Neutral
```

```

