# RenCrow Service Rebuild Restart

## Purpose

`RenCrow_CORE` の live user service を、クリーン停止、build / install、restart、runtime verification まで一貫して扱う。

## When to Use

- ユーザーが「再ビルド」「再起動」「live service に反映」「make install」と依頼した。
- Viewer / API / runtime の変更を live `rencrow.service` へ反映する。
- service の WorkingDirectory、binary、health、Viewer 到達性を確認する必要がある。

## Required Context

- repo root: `/home/nyukimi/RenCrow/RenCrow_CORE`
- service: `rencrow.service`
- expected port: `18790`
- health endpoint: `http://127.0.0.1:18790/health`

## Procedure

1. `git status --short` で unrelated changes を確認する。
2. `systemctl --user cat rencrow.service` で WorkingDirectory と ExecStart を確認する。
3. `systemctl --user stop rencrow.service` で service を止める。
4. 残存 `rencrow` process を確認し、必要なら停止する。
5. `:18790` が listen されていないことを確認する。
6. `/health` が応答しないことを確認する。
7. `make install` を実行する。
8. `systemctl --user start rencrow.service` を実行する。
9. `systemctl --user status rencrow.service --no-pager` と `/health` を確認する。
10. 必要に応じて `/viewer/runtime-config`、`/viewer/llm-ops/status`、対象 Viewer route を確認する。

## Verification

- service status が active。
- `/health` が 200。
- WorkingDirectory が `/home/nyukimi/RenCrow/RenCrow_CORE`。
- ユーザー依頼に関係する API / Viewer route が live service で確認済み。

## Safety

- `/home/nyukimi/RenCrow` workspace root で build / test / git 操作をしない。
- unrelated dirty files を revert しない。
- service restart 前に stop / port down / health down を確認する。
- runtime verification なしに「反映済み」と報告しない。

