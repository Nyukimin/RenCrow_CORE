# Viewer Tailscale 配信仕様

作成日: 2026-05-21
ステータス: 実装前仕様
対象: RenCrow Viewer の宅内 LAN / 宅外 Tailscale アクセス分離

## 1. 目的

RenCrow Viewer は宅内では LAN IP で利用できるままにし、宅外からはブラウザだけで Tailscale 経由の Viewer にアクセスできるようにする。

この仕様は「RenCrow 全 API を宅外公開する」仕様ではない。宅外公開の対象は Viewer のブラウザ体験に必要な最小 route に限定する。STT / TTS の provider API は原則として LAN 内 service のまま維持し、必要な場合だけ Viewer origin 配下で browser-facing stream を proxy する。

Viewer の STT WebSocket は RenCrow が提供する同一 origin の `/stt` を正規経路とする。Viewer が MacBook STT Gateway などの provider / gateway URL へ直接接続する構成は正規経路ではない。

## 2. 前提

- RenCrow 本体は Ubuntu 側で稼働する。
  - LAN host: `192.168.1.204`
  - runtime: `http://127.0.0.1:18790`
  - LAN Viewer: `http://192.168.1.204:18790/viewer`
- STT / TTS は MacBook 207 側で稼働する。
  - STT: `http://192.168.1.207:8766`
  - TTS: `http://192.168.1.207:7870`
- STT / TTS は宅外へ直接公開しない。
- 宅外ユーザーは Tailscale に参加済みのブラウザ端末から Viewer へアクセスする。
- ブラウザ microphone API は secure context が必要である。宅外 Viewer は HTTPS origin で配信する。

## 3. 要件

### 3.1 宅内 LAN

- `http://192.168.1.204:18790/viewer` で Viewer に接続できる。
- RenCrow は `http://192.168.1.207:8766/v1/audio/transcriptions` を server-side STT provider として使える。
- Viewer は `ws://192.168.1.204:18790/stt` または localhost の `ws://127.0.0.1:18790/stt` へ接続し、RenCrow が STT Gateway / provider へ中継する。
- RenCrow は `http://192.168.1.207:7870` を server-side TTS provider として使える。
- LAN 内の HTTP Viewer は維持する。ただし HTTP origin ではブラウザの `navigator.mediaDevices.getUserMedia` が使えない場合があるため、マイク STT 成功を保証しない。

### 3.2 宅外 Tailscale

- 宅外からは Tailscale HTTPS origin の Viewer にブラウザでアクセスする。
- 例: `https://<ubuntu-tailnet-host>/viewer?tab=timeline`
- 宅外端末へ STT / TTS service の LAN IP を直接要求しない。
- Viewer の microphone STT を宅外で使う場合、browser-facing STT stream は Viewer と同じ HTTPS origin から `wss://` で到達できる必要がある。

### 3.3 公開しないもの

- MacBook STT Gateway の `http://192.168.1.207:8766` を public internet へ公開しない。
- MacBook TTS Gateway の `http://192.168.1.207:7870` を public internet へ公開しない。
- LLM Chat / Worker / LLM Ops を宅外ブラウザへ直接公開しない。
- RenCrow の webhook / admin / promotion / external-control API を無差別に Tailscale HTTPS へ露出しない。

## 4. 推奨構成

```text
宅内 LAN
  Browser
    -> http://192.168.1.204:18790/viewer
    -> ws://192.168.1.204:18790/stt
  RenCrow on Ubuntu
    -> http://192.168.1.207:8766/v1/audio/transcriptions
    -> ws://192.168.1.207:8766/stt
    -> http://192.168.1.207:7870/api/tts

宅外 Tailscale
  Browser
    -> https://<ubuntu-tailnet-host>/viewer
    -> https://<ubuntu-tailnet-host>/viewer/runtime-config
    -> https://<ubuntu-tailnet-host>/viewer/debug/system
    -> wss://<ubuntu-tailnet-host>/stt
  Tailscale HTTPS / reverse proxy on Ubuntu
    -> http://127.0.0.1:18790/viewer...
    -> ws://127.0.0.1:18790/stt
  RenCrow on Ubuntu
    -> ws://192.168.1.207:8766/stt
```

宅外の browser-facing endpoint は Tailscale HTTPS origin に揃える。これにより browser は secure context として Viewer を扱い、microphone API と WSS を利用できる。

## 5. URL contract

### 5.1 LAN 用 URL

LAN 内の基本 URL:

```text
Viewer: http://192.168.1.204:18790/viewer
STT HTTP provider: http://192.168.1.207:8766/v1/audio/transcriptions
STT stream for Viewer: ws://192.168.1.204:18790/stt
STT Gateway stream for RenCrow: ws://192.168.1.207:8766/stt
TTS provider: http://192.168.1.207:7870
```

LAN の Viewer は RenCrow origin の `/stt` へ接続する。`ws://192.168.1.207:8766/stt` は RenCrow から STT Gateway へ接続するための server-side / 診断用 URL であり、Viewer browser の通常接続先にしない。`http://192.168.1.204:18790` から開いた Viewer では microphone API が使えない可能性があるため、LAN HTTP Viewer のマイク STT は必須成功条件にしない。

### 5.2 宅外 Tailscale 用 URL

宅外の基本 URL:

```text
Viewer: https://<ubuntu-tailnet-host>/viewer
Runtime config: https://<ubuntu-tailnet-host>/viewer/runtime-config
Debug system: https://<ubuntu-tailnet-host>/viewer/debug/system
STT stream: wss://<ubuntu-tailnet-host>/stt
```

`<ubuntu-tailnet-host>` は Ubuntu 側 Tailscale HTTPS の hostname とする。例: `<ubuntu-host>.<tailnet>.ts.net`。

宅外ブラウザに返す `stt_stream_url` は `wss://<ubuntu-tailnet-host>/stt` でなければならない。`ws://192.168.1.207:8766/stt` を宅外ブラウザへ返してはいけない。

## 6. RenCrow config 方針

RenCrow の STT / TTS config には server-side と browser-facing が混在している。

- `stt.provider_url`: RenCrow server-side が叩く HTTP STT provider。
- `stt.stream_url`: Viewer browser に返す browser-facing STT WebSocket URL。正規値は RenCrow origin の `/stt` であり、Gateway 直 URL ではない。
- `STT_GATEWAY_URL` / `RENCROW_STT_URL`: RenCrow server-side が STT Gateway へ接続する WebSocket URL。
- `tts.http_base_url`: RenCrow server-side が叩く TTS provider。
- `tts.irodori.base_url`: RenCrow server-side が叩く Irodori provider。

### 6.1 LAN 優先 config

宅内運用だけなら以下でよい。

```yaml
stt:
  provider_url: http://192.168.1.207:8766/v1/audio/transcriptions
  stream_url: ws://192.168.1.204:18790/stt

tts:
  http_base_url: http://192.168.1.207:7870
  irodori:
    base_url: http://192.168.1.207:7870
```

RenCrow server-side から STT Gateway へ接続する URL は service env などで分けて設定する。

```bash
STT_GATEWAY_URL=ws://192.168.1.207:8766/stt
```

ただし、この config の `stream_url` は宅外 Tailscale HTTPS Viewer には不適切である。宅外では同じ RenCrow `/stt` でも `wss://<ubuntu-tailnet-host>/stt` として返す。

### 6.2 宅外 Viewer 対応 config

宅外 Viewer の microphone STT を有効にする場合は、`stt.stream_url` だけ Tailscale HTTPS origin に寄せる。

```yaml
stt:
  provider_url: http://192.168.1.207:8766/v1/audio/transcriptions
  stream_url: wss://<ubuntu-tailnet-host>/stt

tts:
  http_base_url: http://192.168.1.207:7870
  irodori:
    base_url: http://192.168.1.207:7870
```

RenCrow server-side から STT Gateway へ接続する URL は service env などで分けて設定する。

```bash
STT_GATEWAY_URL=ws://192.168.1.207:8766/stt
```

`provider_url` と TTS base URL は RenCrow server-side から MacBook 207 へ LAN 経由で到達できればよい。宅外ブラウザへ公開する必要はない。

### 6.3 将来の分離案

現行 config では `stt.stream_url` が単一値のため、LAN 用と宅外用の browser-facing URL を同時に出し分けられない。

将来は以下のどちらかで分離する。

1. request host を見て `/viewer/runtime-config` が `stt_stream_url` を動的に返す。
2. config に `stt.stream_url_lan` と `stt.stream_url_public` を追加する。

初期実装では宅外 Viewer を優先するなら `stt.stream_url=wss://<ubuntu-tailnet-host>/stt` とし、LAN HTTP Viewer の mic STT は必須成功条件から外す。

## 7. Proxy contract

宅外 Tailscale HTTPS origin は以下を proxy する。

### 7.1 Viewer routes

必須:

- `/viewer`
- `/viewer/assets/`
- `/viewer/runtime-config`
- `/viewer/debug/system`
- `/viewer/events`
- `/viewer/send`
- `/viewer/stt/log`
- `/viewer/stt/wav`
- `/viewer/stt/autotest`
- `/viewer/tts/audio`
- `/audio-router/events`

必要に応じて Viewer が参照する `/viewer/...` API を追加する。

### 7.2 STT stream route

必須:

```text
wss://<ubuntu-tailnet-host>/stt
  -> ws://127.0.0.1:18790/stt
  -> RenCrow server-side
  -> ws://192.168.1.207:8766/stt
```

Proxy は WebSocket upgrade を通す。

必須 header:

- `Upgrade`
- `Connection`
- `Sec-WebSocket-Key`
- `Sec-WebSocket-Version`
- `Sec-WebSocket-Protocol` がある場合は維持

Timeout:

- idle timeout は STT session 中に切れない値にする。
- 初期値は 10 分以上を推奨する。

## 8. Tailscale Serve 方針

Tailscale Serve を使う場合、公開対象は Viewer と STT stream proxy に限定する。

概念例:

```text
https://<ubuntu-tailnet-host>/viewer* -> http://127.0.0.1:18790/viewer*
https://<ubuntu-tailnet-host>/audio-router/events -> http://127.0.0.1:18790/audio-router/events
wss://<ubuntu-tailnet-host>/stt -> ws://127.0.0.1:18790/stt
```

Tailscale Serve が WebSocket proxy の要件を満たせない場合は、Ubuntu 上に Caddy / nginx などを立て、Tailscale HTTPS からその local reverse proxy へ渡す。

実装補助スクリプト:

```bash
scripts/tailscale_viewer_serve_verify.sh
```

## 9. Watchdog 方針

Viewer Tailscale 配信の watchdog は、RenCrow health と Tailscale Serve の維持だけを担当する。

対象:

- `http://127.0.0.1:18790/health` が 200 を返すこと。
- Tailscale Serve が `https://<ubuntu-tailnet-host>/` を `http://127.0.0.1:18790` へ proxy していること。
- `https://<ubuntu-tailnet-host>/viewer?tab=timeline` が 200 を返すこと。

禁止:

- Tailscale Funnel を復旧しない。
- `tailscale funnel reset` を実行しない。
- LINE webhook port `18791` を監視しない。
- LLM / Ollama / STT / TTS provider の復旧を担当しない。
- RenCrow process の restart を自動実行しない。

Funnel と Serve は同じ Tailscale Serve/Funnel config を共有するため、旧 Funnel watchdog が `tailscale funnel reset` を実行すると Viewer 用 Serve 設定も消える。Viewer 配信では旧 `picoclaw-watchdog.timer` の Funnel 復旧処理を使わず、`scripts/ops_watchdog.sh` の Viewer Serve 専用 watchdog を使う。

運用コマンド:

```bash
make install-watchdog enable-watchdog
make watchdog-run-once
make watchdog-status
```

このスクリプトは、`picoclaw-funnel.service` が active の場合は停止条件として exit 2 で止まる。Funnel 停止後に `tailscale serve --bg --yes http://127.0.0.1:18790` を設定し、Viewer HTTPS、`/viewer/runtime-config`、非 Viewer route guard、`/stt` WebSocket handshake を確認する。

root 権限で Funnel 停止から検証まで一括で行う場合:

```bash
sudo scripts/tailscale_viewer_disable_funnel_and_verify.sh
```

この wrapper は `picoclaw-funnel.service` を `disable --now` し、停止と disable を確認した後、通常ユーザーに戻して `scripts/tailscale_viewer_serve_verify.sh` を実行する。

## 9. Caddy / nginx 方針

Reverse proxy を使う場合の要件:

- TLS 終端は Tailscale HTTPS または proxy が担う。
- `/viewer` 系は `http://127.0.0.1:18790` へ proxy。
- `/stt` は `ws://127.0.0.1:18790/stt` へ proxy。RenCrow が server-side で STT Gateway へ接続する。
- Host / X-Forwarded-* を保存する。
- WebSocket upgrade を明示する。
- STT / TTS の provider API は原則 proxy しない。

実装時は使用する proxy 製品に合わせて具体 config を別紙化する。

## 10. Security

- 宅外公開は Tailscale private network 内に限定する。
- Funnel など public internet 公開はこの仕様の対象外。
- STT / TTS provider API を宅外へ直接 expose しない。
- Viewer route 以外の admin / external-control route は原則公開しない。
- API key / token を URL や config に平文で含めない。
- `runtime-config` は secret 値を返さない。

## 11. Verification

### 11.1 LAN

- [ ] `http://192.168.1.204:18790/viewer` が開く。
- [ ] `http://192.168.1.204:18790/viewer/runtime-config` が 200。
- [ ] `stt.provider_url` が `http://192.168.1.207:8766/v1/audio/transcriptions`。
- [ ] `tts.http_base_url` が `http://192.168.1.207:7870`。
- [ ] `/viewer/debug/system` が STT / TTS ready を返す。

### 11.2 宅外 Tailscale Viewer

- [ ] `https://<ubuntu-tailnet-host>/viewer?tab=timeline` が開く。
- [ ] browser console で `window.isSecureContext === true`。
- [ ] browser console で `navigator.mediaDevices.getUserMedia` が存在する。
- [ ] `/viewer/runtime-config` が `stt_stream_url=wss://<ubuntu-tailnet-host>/stt` を返す。
- [ ] mic button が通常チャット timeline で disabled ではない。
- [ ] mic click 後に `Mic: on` になる。
- [ ] STT WebSocket が connected になる。
- [ ] 実音声から non-empty transcript が返る。

### 11.3 失敗時の判定

- `isSecureContext=false`: Viewer HTTPS 配信が成立していない。
- `navigator.mediaDevices` がない: browser microphone API が使えない origin。
- `Mic: off` かつ `STT microphone start unavailable`: microphone permission / secure context / device 問題。
- `Mic: on` かつ `STT: reconnecting`: WSS proxy または STT stream 問題。
- `/viewer/debug/system` は ready だが mic が動かない: server-side STT/TTS は正常、browser-facing Viewer / WSS の問題。

## 12. 実装開始条件

実装前に以下を確定する。

1. Ubuntu 側 Tailscale HTTPS hostname。
2. Tailscale Serve 単体で `/stt` WebSocket proxy が可能か。
3. 可能でない場合に使う reverse proxy。候補: Caddy / nginx。
4. 宅外 Viewer を優先して `stt.stream_url` を `wss://<ubuntu-tailnet-host>/stt` にするか、runtime-config の動的出し分けを先に実装するか。

初期の推奨は、Tailscale HTTPS hostname を確定し、`/stt` WSS proxy を用意したうえで `stt.stream_url=wss://<ubuntu-tailnet-host>/stt` に寄せること。

## 13. Goal 実行用作業ルール

この仕様を Goal に設定して実装する場合、未完了項目を小さな検証済み commit 単位で順に処理する。

各単位では、実装前に以下を短く定義する。

- 対象
- 変更範囲
- 検証コマンド
- runtime / Viewer 確認の方法
- 完了条件

実装後は該当テストと必要な runtime / Viewer 確認を行う。確認済みの関連ファイルだけを選択的に stage し、日本語 commit message で commit する。commit 後は push する。push できたら、停止条件に当たらない限り、ユーザー確認を待たず次の未完了項目へ進む。

禁止:

- worktree 全体を一括 stage / commit しない。
- `vault/`、live E2E 生成物、一時 screenshot、tmp artifact を明示指示なく commit しない。
- 失敗したテストを無視して push しない。
- blocked / skipped / fallback / mock / health-only を成功扱いしない。
- Viewer / proxy / runtime-config / STT Gateway / TTS Gateway / docs を、責務が曖昧なまま 1 commit に混ぜない。
- LAN HTTP Viewer の mic failure を、Tailscale HTTPS Viewer の失敗として混同しない。

報告:

- 各 push 後に、commit hash、検証コマンド、runtime / Viewer 確認結果、次に進む対象を短く報告する。
- 停止条件に当たらない限り、ユーザー確認は不要とする。

## 14. Goal 実行時の推奨分割

### 14.1 調査・現状確認

対象:

- Tailscale HTTPS hostname
- 現在の `tailscale serve status`
- RenCrow runtime `http://127.0.0.1:18790`
- MacBook STT `ws://192.168.1.207:8766/stt`
- `/viewer/runtime-config`
- `/viewer/debug/system`

検証:

- `tailscale status`
- `tailscale serve status`
- `curl -sS http://127.0.0.1:18790/viewer/runtime-config`
- `curl -sS http://127.0.0.1:18790/viewer/debug/system`
- STT stream の WebSocket 到達確認

完了条件:

- Tailscale HTTPS hostname が特定される。
- Viewer 本体と STT stream の proxy 先が確定する。
- できない項目は blocked として理由を記録する。

### 14.2 `/viewer` HTTPS 配信

対象:

- Ubuntu 側 Tailscale Serve または reverse proxy
- `/viewer`
- `/viewer/assets/`
- `/viewer/runtime-config`
- `/viewer/debug/system`
- `/viewer/events`
- `/viewer/send`

検証:

- 宅外相当の HTTPS origin で `/viewer?tab=timeline` が開く。
- browser console で `window.isSecureContext === true`。
- browser console で `navigator.mediaDevices.getUserMedia` が存在する。

完了条件:

- Tailscale HTTPS origin で Viewer が表示される。
- Viewer route の 404 / 502 / stale asset がない。
- microphone API が secure context として利用可能である。

### 14.3 `/stt` WSS proxy

対象:

- `wss://<ubuntu-tailnet-host>/stt`
- `ws://127.0.0.1:18790/stt`
- RenCrow server-side から `ws://192.168.1.207:8766/stt`
- WebSocket upgrade

検証:

- WSS 接続が open になる。
- `Mic: on` 後に `STT: connected` が表示される。
- `final` が RenCrow `/stt` 経由で返る。
- proxy timeout で session が即切断されない。

完了条件:

- 宅外 HTTPS Viewer から `/stt` WSS が connected になる。
- 宅外 HTTPS Viewer から `/stt` WSS 経由で final transcript が返る。
- `ws://192.168.1.207:8766/stt` を宅外ブラウザへ直接返していない。

### 14.4 runtime-config の `stt_stream_url` 調整

対象:

- `~/.picoclaw/config.yaml`
- または `/viewer/runtime-config` の host-aware 出し分け実装

検証:

- 宅外 Viewer で `stt_stream_url=wss://<ubuntu-tailnet-host>/stt`。
- server-side STT provider は `http://192.168.1.207:8766/v1/audio/transcriptions` のまま。
- TTS provider は `http://192.168.1.207:7870` のまま。

完了条件:

- browser-facing URL と server-side provider URL が分離される。
- TTS / STT provider API を宅外へ直接公開していない。

### 14.5 Viewer microphone E2E

対象:

- `https://<ubuntu-tailnet-host>/viewer?tab=timeline`
- microphone permission
- STT WebSocket
- STT transcript

検証:

- `window.isSecureContext === true`
- `navigator.mediaDevices.getUserMedia` exists
- mic button disabled ではない。
- click 後 `Mic: on`。
- `STT: connected`。
- 実音声から non-empty transcript。
- transcript が通常チャット入力または送信 flow に入る。

完了条件:

- health OK だけでなく、実 browser mic から STT transcript が確認される。
- fallback / mock / skip を使っていない。

## 15. Goal 実行時の停止条件

次の場合だけ停止して報告または質問する。

- Tailscale HTTPS hostname が不明、または作業者側で Tailscale Serve / DNS / ACL を変更できない。
- Tailscale Serve が WebSocket proxy に対応できず、Caddy / nginx などの追加導入が必要。
- 依存追加、CI/deploy 設定変更、destructive operation、ファイル削除、セーフガード変更が必要。
- service restart が必要だが、RenCrow のクリーン停止条件を満たせない。
- Viewer HTTPS は開くが `isSecureContext=false` のまま。
- `navigator.mediaDevices.getUserMedia` が存在しない原因が browser / OS permission / 実機設定にあり、作業者側で解消できない。
- STT / TTS MacBook service が down、または 207 への LAN 到達がない。
- テスト失敗や runtime / Viewer 確認失敗が、現在の作業範囲内で短時間に解消できない。
- push 対象に未確認差分、別責務の差分、live E2E 生成物が混ざりそうになった。

停止時は、失敗した command、観測した URL、browser console の主要 error、次に必要なユーザー側作業を短く報告する。

## 16. 再起動ルール

RenCrow / PicoClaw を再起動する場合は、必ず以下を満たしてから起動する。

1. `systemctl --user stop picoclaw.service`
2. `pgrep -a picoclaw` が空であること
3. `ss -ltnp '( sport = :18790 )'` で listen がないこと
4. `curl http://127.0.0.1:18790/health` が connection refused になること
5. 必要な build / install を実行すること
6. `systemctl --user start picoclaw.service`

再起動後は最低限以下を確認する。

- `systemctl --user is-active picoclaw.service`
- `/health`
- `/viewer/runtime-config`
- `/viewer/debug/system`
- Tailscale HTTPS Viewer の表示
- microphone / STT WSS の状態
